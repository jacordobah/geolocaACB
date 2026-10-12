"""Opt-in isolated tests. Creates only tracking_sessions; retains disabled test sessions.

The temporary HTTP subprocess exits at teardown; permanent services are untouched.
"""
import asyncio
from concurrent.futures import ThreadPoolExecutor
from datetime import UTC, datetime, timedelta
import hashlib
import json
import os
import subprocess
import sys
import threading
import time
from types import SimpleNamespace
import unittest
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

from bson import ObjectId, json_util
from pymongo import MongoClient

from acb_path_lo.application.use_cases.disable_tracking import DisableTracking
from acb_path_lo.application.use_cases.enable_tracking import EnableTracking
from acb_path_lo.application.use_cases.get_first_meeting_point import GetFirstMeetingPoint
from acb_path_lo.application.use_cases.get_tracking_session import GetTrackingSession
from acb_path_lo.application.use_cases.receive_gps_observation import ReceiveGpsObservation
from acb_path_lo.domain.model.coordinates import Coordinates
from acb_path_lo.domain.model.gps_observation import GpsObservation, utc_milliseconds
from acb_path_lo.domain.tracking_errors import TrackingError, TrackingStoreUnavailable
from acb_path_lo.infrastructure.persistence.mongo_tracking_repository import MongoTrackingRepository
from acb_path_lo.infrastructure.persistence.mongo_map_repository import MongoMapRepository
from acb_path_lo.infrastructure.config.tracking import load_tracking_policy
from acb_path_lo.infrastructure.security.tracking_credentials import create_tracking_credentials
from acb_path_lo.infrastructure.security.tracking_credentials import verify_credential


@unittest.skipUnless(os.getenv('ACB_PHASE3_INTEGRATION') == '1', 'Requires explicitly isolated MongoDB')
class TrackingIntegrationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if (os.environ.get('ACB_TEST_PROJECT') != 'acb-kmz-test-20261012'
                or os.environ.get('MONGO_URI') != 'mongodb://acb_mongo_db:27017'
                or os.environ.get('MONGO_DATABASE') != 'geolocaacb'):
            raise RuntimeError('Unexpected database: refusing integration writes')
        cls.client = MongoClient(os.environ['MONGO_URI'], serverSelectionTimeoutMS=5000)
        cls.addClassCleanup(cls.client.close)
        cls.database = cls.client[os.environ['MONGO_DATABASE']]
        cls.repository = MongoTrackingRepository(cls.database)
        cls.route_id = os.environ['ACB_INTEGRATION_ROUTE_ID']
        route = cls.database.routes.find_one({'_id': ObjectId(cls.route_id)})
        if not route or not route.get('first_meeting_point_id'):
            raise RuntimeError('Configured test route required')
        cls.point = cls.database.meeting_points.find_one({'_id': route['first_meeting_point_id']})
        cls.sessions = []
        cls.before = cls.snapshot()
        cls.addClassCleanup(cls.verify_integrity)
        cls.addClassCleanup(cls.disable_created_sessions)
        cls.base = 'http://127.0.0.1:18003'
        # No access logs: credentials and request bodies must never be logged.
        cls.api = subprocess.Popen([sys.executable, '-B', '-m', 'uvicorn',
            'acb_path_lo.main:app', '--host', '127.0.0.1', '--port', '18003',
            '--no-access-log'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        cls.addClassCleanup(cls.stop_api)
        for _ in range(100):
            if cls.api.poll() is not None:
                raise RuntimeError('Temporary API exited before readiness')
            try:
                with urlopen(cls.base + '/openapi.json', timeout=1) as response:
                    if response.status == 200:
                        return
            except (URLError, TimeoutError):
                time.sleep(.1)
        raise RuntimeError('Temporary API did not become ready')

    @classmethod
    def stop_api(cls):
        cls.api.terminate()
        try:
            cls.api.wait(timeout=10)
        except subprocess.TimeoutExpired:
            cls.api.kill()
            cls.api.wait(timeout=5)

    @classmethod
    def snapshot(cls):
        return {name: hashlib.sha256(json_util.dumps({
            'documents': list(cls.database[name].find().sort('_id', 1)),
            'indexes': list(cls.database[name].list_indexes())}, sort_keys=True).encode()).hexdigest()
            for name in ('routes', 'meeting_points', 'route_status')}

    @classmethod
    def verify_integrity(cls):
        after = cls.snapshot()
        if cls.before != after:
            raise AssertionError('Original collection documents or indexes changed')
        print('PHASE3_INTEGRITY', json.dumps(after))
        print('PHASE3_COUNTS', json.dumps({name: cls.database[name].count_documents({})
              for name in ('routes', 'meeting_points', 'route_status', 'tracking_sessions')}))

    @classmethod
    def disable_created_sessions(cls):
        async def disable_all():
            for identifier in cls.sessions:
                await DisableTracking(cls.repository, lambda: datetime.now(UTC)).execute(identifier)
        asyncio.run(disable_all())
        print('PHASE3_SESSIONS_RETAINED_DISABLED', json.dumps(cls.sessions))

    def create_session(self):
        identifier, credential, fingerprint = create_tracking_credentials()
        use_case = EnableTracking(self.repository,
            GetFirstMeetingPoint(MongoMapRepository(self.database, ensure_indexes=False)),
            load_tracking_policy(), lambda: datetime.now(UTC))
        asyncio.run(use_case.execute(identifier, 'phase3-synthetic-guide',
                                     self.route_id, fingerprint))
        self.sessions.append(identifier)
        return identifier, credential

    def payload(self, sequence=1, offset=0, accuracy=5):
        longitude, latitude = self.point['location']['coordinates']
        return {'sequence': sequence, 'latitude': latitude + offset, 'longitude': longitude,
                'accuracy_m': accuracy, 'captured_at': datetime.now(UTC).isoformat()}

    def http(self, identifier, credential, payload=None):
        path = '/api/v1/tracking-sessions/' + identifier + ('/observations' if payload is not None else '')
        headers = {'Content-Type': 'application/json'}
        if credential:
            headers['Authorization'] = 'Bearer ' + credential
        request = Request(self.base + path, headers=headers,
                          data=json.dumps(payload).encode() if payload is not None else None)
        try:
            response = urlopen(request, timeout=15)
        except HTTPError as error:
            response = error
        with response:
            return response.status, json.load(response)

    def test_http_flow_retries_and_existing_endpoints(self):
        identifier, credential = self.create_session()
        for seq, offset, accuracy, expected in [(1, .01, 5, 'outside'),
                (2, 0, None, 'indeterminate'), (3, 0, 20, 'indeterminate'),
                (4, .00025, 5, 'indeterminate'), (5, 0, 5, 'inside')]:
            payload = self.payload(seq, offset, accuracy)
            status, body = self.http(identifier, credential, payload)
            print('PHASE3_HTTP', json.dumps({'scenario': expected, 'http': status, 'response': body}))
            self.assertEqual((status, body['status']), (200, expected))
            self.assertEqual(body['first_arrival'] is not None, seq == 5)
        original = body
        before = self.database.tracking_sessions.find_one({'_id': identifier})
        self.assertEqual(self.http(identifier, credential, payload), (200, original))
        self.assertEqual(before, self.database.tracking_sessions.find_one({'_id': identifier}))
        self.assertEqual(self.http(identifier, credential, {**payload, 'latitude': 0})[0], 409)
        self.assertEqual(self.http(identifier, credential, {**payload, 'sequence': 4})[0], 409)
        asyncio.run(DisableTracking(self.repository, lambda: datetime.now(UTC)).execute(identifier))
        before = self.database.tracking_sessions.find_one({'_id': identifier})
        self.assertEqual(self.http(identifier, credential, payload), (200, original))
        self.assertEqual(before, self.database.tracking_sessions.find_one({'_id': identifier}))
        self.assertEqual(self.http(identifier, credential, self.payload(6))[0], 409)
        self.assertEqual(self.http(identifier, None)[0], 401)
        self.assertEqual(self.http(identifier, 'invalid')[0], 404)
        status, view = self.http(identifier, credential)
        self.assertEqual((status, view['state']), (200, 'disabled'))
        self.assertNotIn('credential_hash', view)
        for path in ['/api/v1/routes', '/api/v1/meeting-points',
                     f'/api/v1/routes/{self.route_id}/first-meeting-point']:
            with urlopen(self.base + path, timeout=10) as response:
                self.assertEqual(response.status, 200)
        request = Request(self.base + f'/api/v1/routes/{self.route_id}/first-meeting-point/validate',
            data=json.dumps({key: payload[key] for key in ('latitude', 'longitude', 'accuracy_m')}).encode(),
            headers={'Content-Type': 'application/json'})
        with urlopen(request, timeout=10) as response:
            self.assertEqual(json.load(response)['status'], 'inside')

    def test_http_temporal_errors_and_concurrent_identical_retries(self):
        identifier, credential = self.create_session()
        for offset, code in [(-31, 'observation_too_old'), (6, 'observation_from_future'),
                             (-6, 'observation_before_tracking')]:
            payload = {**self.payload(), 'captured_at': (datetime.now(UTC) + timedelta(seconds=offset)).isoformat()}
            status, body = self.http(identifier, credential, payload)
            self.assertEqual((status, body['detail']['code']), (409, code))
        payload = self.payload()
        barrier = threading.Barrier(4)
        def send():
            barrier.wait(timeout=10)
            return self.http(identifier, credential, payload)
        with ThreadPoolExecutor(max_workers=4) as pool:
            results = list(pool.map(lambda _: send(), range(4)))
        self.assertTrue(all(result == results[0] for result in results))
        self.assertEqual(results[0][0], 200)
        stored = self.database.tracking_sessions.find_one({'_id': identifier})
        self.assertEqual(stored['version'], 1)
        self.assertEqual(stored['first_arrival']['sequence'], 1)
        print('PHASE3_CONCURRENT_HTTP', json.dumps({'responses': [r[0] for r in results], 'version': 1}))

    def test_real_cas_two_inside_and_disable_race(self):
        for race in ('two_inside', 'disable'):
            identifier, credential = self.create_session()
            barrier = threading.Barrier(2)
            class BarrierRepository(MongoTrackingRepository):
                async def synchronize(repo, session):
                    if session.version == 0:
                        await asyncio.to_thread(barrier.wait, 10)
                async def accept(repo, session, observation, arrival):
                    await repo.synchronize(session)
                    return await super().accept(session, observation, arrival)
                async def disable(repo, session, when):
                    await repo.synchronize(session)
                    return await super().disable(session, when)
            repository = BarrierRepository(self.database)
            now = utc_milliseconds(datetime.now(UTC))
            longitude, latitude = self.point['location']['coordinates']
            receive = ReceiveGpsObservation(GetTrackingSession(repository, verify_credential), lambda: now)
            async def execute():
                first = receive.execute(identifier, credential, GpsObservation(1, Coordinates(latitude, longitude), 5, now))
                second = (receive.execute(identifier, credential, GpsObservation(2, Coordinates(latitude, longitude), 5, now))
                          if race == 'two_inside' else DisableTracking(repository, lambda: now).execute(identifier))
                return await asyncio.gather(first, second, return_exceptions=True)
            results = asyncio.run(execute())
            for result in results:
                if isinstance(result, Exception):
                    self.assertIsInstance(result, TrackingError)
                    self.assertIn(result.code, ('tracking_disabled', 'observation_out_of_order'))
            stored = self.database.tracking_sessions.find_one({'_id': identifier})
            if race == 'two_inside':
                self.assertIn(stored['first_arrival']['sequence'], (1, 2))
                arrival = stored['first_arrival']
                self.assertEqual(self.http(identifier, credential, self.payload(3))[0], 200)
                self.assertEqual(self.database.tracking_sessions.find_one({'_id': identifier})['first_arrival'], arrival)
            else:
                self.assertEqual(stored['state'], 'disabled')
                self.assertEqual(self.http(identifier, credential, self.payload(3))[0], 409)
            print('PHASE3_CAS', json.dumps({'race': race, 'state': stored['state'],
                'first_sequence': stored['first_arrival']['sequence'] if stored['first_arrival'] else None,
                'version': stored['version']}))

    def test_uncertain_real_write_and_expired_replay(self):
        identifier, credential = self.create_session()
        class UncertainRepository(MongoTrackingRepository):
            async def accept(repo, *args):
                accepted = await super().accept(*args)
                if accepted:
                    raise TrackingStoreUnavailable()
                return accepted
        now = utc_milliseconds(datetime.now(UTC))
        longitude, latitude = self.point['location']['coordinates']
        observation = GpsObservation(1, Coordinates(latitude, longitude), 5, now)
        receive = ReceiveGpsObservation(GetTrackingSession(UncertainRepository(self.database), verify_credential), lambda: now)
        with self.assertRaises(TrackingStoreUnavailable):
            asyncio.run(receive.execute(identifier, credential, observation))
        before = self.database.tracking_sessions.find_one({'_id': identifier})
        later = ReceiveGpsObservation(GetTrackingSession(self.repository, verify_credential), lambda: now + timedelta(hours=1))
        result = asyncio.run(later.execute(identifier, credential, observation))
        self.assertIsNotNone(result.first_arrival)
        self.assertEqual(before, self.database.tracking_sessions.find_one({'_id': identifier}))
        print('PHASE3_UNCERTAIN_WRITE', json.dumps({'recovered': True, 'writes_after_retry': 0}))

    def test_disable_wins_while_observation_is_pending(self):
        identifier, credential = self.create_session()
        arrived = threading.Event()
        disabled = threading.Event()
        class PendingRepository(MongoTrackingRepository):
            async def accept(repo, *args):
                arrived.set()
                if not await asyncio.to_thread(disabled.wait, 10):
                    raise RuntimeError('Concurrent disable did not complete')
                return await super().accept(*args)
        now = utc_milliseconds(datetime.now(UTC))
        longitude, latitude = self.point['location']['coordinates']
        receive = ReceiveGpsObservation(GetTrackingSession(PendingRepository(self.database), verify_credential), lambda: now)
        async def execute():
            async def disable():
                if not await asyncio.to_thread(arrived.wait, 10):
                    raise RuntimeError('Observation did not reach conditional write')
                result = await DisableTracking(self.repository, lambda: now).execute(identifier)
                disabled.set()
                return result
            return await asyncio.gather(receive.execute(identifier, credential,
                GpsObservation(1, Coordinates(latitude, longitude), 5, now)), disable(), return_exceptions=True)
        received, control_result = asyncio.run(execute())
        self.assertIsInstance(received, TrackingError)
        self.assertEqual(received.code, 'tracking_disabled')
        self.assertEqual(control_result.state, 'disabled')
        stored = self.database.tracking_sessions.find_one({'_id': identifier})
        self.assertEqual(stored['version'], 1)
        self.assertIsNone(stored['first_arrival'])
        self.assertIsNone(stored['last_observation'])
        print('PHASE3_DISABLE_WINS', json.dumps({'version': 1, 'first_arrival': None, 'observation': 'rejected'}))

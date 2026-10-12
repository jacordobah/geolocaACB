"""Opt-in Fase 4 checks. Writes only synthetic sessions and gps_positions."""
import asyncio
from concurrent.futures import ThreadPoolExecutor
from datetime import UTC, datetime, timedelta
import hashlib
import json
import os
import subprocess
import sys
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
from acb_path_lo.application.use_cases.repair_pending_position import RepairPendingPosition
from acb_path_lo.domain.model.coordinates import Coordinates
from acb_path_lo.domain.model.gps_position import GpsPosition
from acb_path_lo.domain.model.gps_observation import GpsObservation, utc_milliseconds
from acb_path_lo.domain.tracking_errors import TrackingStoreUnavailable
from acb_path_lo.infrastructure.persistence.mongo_position_repository import MongoPositionRepository
from acb_path_lo.infrastructure.persistence.mongo_map_repository import MongoMapRepository
from acb_path_lo.infrastructure.persistence.mongo_tracking_repository import MongoTrackingRepository
from acb_path_lo.infrastructure.config.tracking import load_tracking_policy
from acb_path_lo.infrastructure.security.tracking_credentials import create_tracking_credentials
from acb_path_lo.infrastructure.security.tracking_credentials import verify_credential
from acb_path_lo.infrastructure.tools.configure_tracking import run as control


@unittest.skipUnless(os.getenv('ACB_PHASE4_INTEGRATION') == '1', 'Requires isolated Fase 4 MongoDB')
class HistoryIntegrationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if (os.getenv('ACB_TEST_PROJECT') != 'acb-kmz-test-20261012'
                or os.getenv('MONGO_URI') != 'mongodb://acb_mongo_db:27017'
                or os.getenv('MONGO_DATABASE') != 'geolocaacb'
                or os.getenv('ACB_GPS_HISTORY_CUTOFF_BOGOTA') != '08:00'):
            raise RuntimeError('Integration writes require explicit isolated test configuration')
        cls.client = MongoClient(os.environ['MONGO_URI'], serverSelectionTimeoutMS=5000)
        cls.addClassCleanup(cls.client.close)
        cls.database = cls.client[os.environ['MONGO_DATABASE']]
        cls.before = cls.snapshot()
        cls.addClassCleanup(cls.verify_integrity)
        cls.sessions = []
        cls.tracking = MongoTrackingRepository(cls.database)
        cls.positions = MongoPositionRepository(cls.database)
        # Complete only unfinished synthetic sessions left by an interrupted test run.
        for old in cls.database.tracking_sessions.find({
                'guide_id': 'phase4-synthetic-guide',
                'pending_position': {'$ne': None}}, {'_id': 1}):
            asyncio.run(RepairPendingPosition(cls.tracking, cls.positions,
                                              lambda: datetime.now(UTC)).execute(old['_id']))
        cls.addClassCleanup(cls.disable_created)
        route = cls.database.routes.find_one({'_id': ObjectId(os.environ['ACB_INTEGRATION_ROUTE_ID'])})
        if not route or not route.get('first_meeting_point_id'):
            raise RuntimeError('Configured geographic test route required')
        cls.route_id = str(route['_id'])
        cls.point = cls.database.meeting_points.find_one({'_id': route['first_meeting_point_id']})
        cls.base = 'http://127.0.0.1:18004'
        cls.api = subprocess.Popen([sys.executable, '-B', '-m', 'uvicorn',
            'acb_path_lo.main:app', '--host', '127.0.0.1', '--port', '18004',
            '--no-access-log'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        cls.addClassCleanup(cls.stop_api)
        for _ in range(100):
            if cls.api.poll() is not None:
                raise RuntimeError('Temporary API exited')
            try:
                with urlopen(cls.base + '/openapi.json', timeout=1) as result:
                    if result.status == 200:
                        return
            except (URLError, TimeoutError):
                time.sleep(.1)
        raise RuntimeError('Temporary API did not become ready')

    @classmethod
    def snapshot(cls):
        return {name: hashlib.sha256(json_util.dumps({
            'docs': list(cls.database[name].find().sort('_id', 1)),
            'indexes': list(cls.database[name].list_indexes())}, sort_keys=True).encode()).hexdigest()
            for name in ('routes', 'meeting_points', 'route_status')}

    @classmethod
    def verify_integrity(cls):
        after = cls.snapshot()
        if cls.before != after:
            raise AssertionError('Original documents or indexes changed')
        print('PHASE4_INTEGRITY', json.dumps(after))
        print('PHASE4_COUNTS', json.dumps({name: cls.database[name].count_documents({})
              for name in ('routes', 'meeting_points', 'route_status', 'tracking_sessions', 'gps_positions')}))

    @classmethod
    def stop_api(cls):
        cls.api.terminate()
        try:
            cls.api.wait(timeout=10)
        except subprocess.TimeoutExpired:
            cls.api.kill()
            cls.api.wait(timeout=5)

    @classmethod
    def disable_created(cls):
        for identifier in cls.sessions:
            asyncio.run(DisableTracking(cls.tracking, lambda: datetime.now(UTC)).execute(identifier))
        print('PHASE4_SESSIONS_DISABLED', len(cls.sessions))

    def create_session(self):
        result = asyncio.run(control(SimpleNamespace(action='enable', guide_id='phase4-synthetic-guide',
                                   route_id=self.route_id), self.database))
        self.sessions.append(result['tracking_id'])
        return result['tracking_id'], result['credential']

    def payload(self, sequence=1, offset=0, accuracy=5):
        longitude, latitude = self.point['location']['coordinates']
        return {'sequence': sequence, 'latitude': latitude + offset, 'longitude': longitude,
                'accuracy_m': accuracy, 'captured_at': datetime.now(UTC).isoformat()}

    def http(self, identifier, credential, payload=None, suffix=''):
        path = '/api/v1/tracking-sessions/' + identifier + ('/observations' if payload is not None else suffix)
        headers = {'Content-Type': 'application/json'}
        if credential is not None:
            headers['Authorization'] = 'Bearer ' + credential
        request = Request(self.base + path, headers=headers,
                          data=json.dumps(payload).encode() if payload is not None else None)
        try:
            response = urlopen(request, timeout=15)
        except HTTPError as error:
            response = error
        with response:
            return response.status, json.load(response)

    def test_http_history_pagination_and_indices(self):
        identifier, credential = self.create_session()
        indexes = list(self.database.gps_positions.list_indexes())
        unique = next(index for index in indexes if list(index['key']) == ['tracking_id', 'sequence'])
        ttl = next(index for index in indexes if list(index['key']) == ['expires_at'])
        self.assertTrue(unique['unique'])
        self.assertEqual(ttl['expireAfterSeconds'], 0)
        self.assertEqual(len(ttl['key']), 1)
        print('PHASE4_INDEXES', json_util.dumps(indexes))
        results = []
        for sequence, offset, accuracy, expected in [(1, .01, 5, 'outside'),
                (2, 0, None, 'indeterminate'), (3, 0, 5, 'inside'), (5, .01, 5, 'outside')]:
            payload = self.payload(sequence, offset, accuracy)
            status, body = self.http(identifier, credential, payload)
            self.assertEqual((status, body['status']), (200, expected))
            results.append((payload, body))
            print('PHASE4_HTTP', json.dumps({'scenario': expected, 'http': status,
                'sequence': sequence, 'arrival': body['first_arrival'] is not None}))
        arrival = results[2][1]['first_arrival']
        self.assertEqual(results[3][1]['first_arrival'], arrival)
        docs = list(self.database.gps_positions.find({'tracking_id': identifier}).sort('sequence', 1))
        self.assertEqual([doc['sequence'] for doc in docs], [1, 2, 3, 5])
        self.assertEqual([doc['status'] for doc in docs], ['outside', 'indeterminate', 'inside', 'outside'])
        self.assertTrue(all(isinstance(doc['_id'], ObjectId) for doc in docs))
        self.assertTrue(all(isinstance(doc['expires_at'], datetime) for doc in docs))
        self.assertEqual(docs[0]['guide_id'], 'phase4-synthetic-guide')
        self.assertEqual(docs[0]['route_id'], self.route_id)
        self.assertEqual(docs[0]['expires_at'].astimezone(UTC).hour, 13)
        decoded = self.positions.collection.find_one({'tracking_id': identifier, 'sequence': 1})
        exact = GpsPosition(**{key: value for key, value in decoded.items() if key != '_id'})
        asyncio.run(self.positions.ensure(exact))
        with self.assertRaises(TrackingStoreUnavailable):
            from dataclasses import replace
            asyncio.run(self.positions.ensure(replace(exact, request_hash='different')))
        self.assertEqual(self.database.gps_positions.count_documents({'tracking_id': identifier}), 4)
        page1 = self.http(identifier, credential, suffix='/positions?limit=2')
        page2 = self.http(identifier, credential, suffix='/positions?limit=2&after_sequence=2')
        self.assertEqual([p['sequence'] for p in page1[1]['positions']], [1, 2])
        self.assertEqual(page1[1]['next_after_sequence'], 2)
        self.assertEqual([p['sequence'] for p in page2[1]['positions']], [3, 5])
        self.assertIsNone(page2[1]['next_after_sequence'])
        self.assertNotIn('request_hash', page1[1]['positions'][0])
        before = self.database.tracking_sessions.find_one({'_id': identifier})
        self.assertEqual(self.http(identifier, credential, results[-1][0]), (200, results[-1][1]))
        self.assertEqual(self.database.tracking_sessions.find_one({'_id': identifier}), before)
        self.assertEqual(self.database.gps_positions.count_documents({'tracking_id': identifier}), 4)
        for payload in [{**results[-1][0], 'latitude': 0},
                        {**results[-1][0], 'sequence': 4}]:
            self.assertEqual(self.http(identifier, credential, payload)[0], 409)
        old = {**self.payload(6), 'captured_at':
               (datetime.now(UTC) - timedelta(seconds=31)).isoformat()}
        self.assertEqual(self.http(identifier, credential, old)[0], 409)
        self.assertEqual(self.database.gps_positions.count_documents({'tracking_id': identifier}), 4)
        self.assertEqual(self.http(identifier, credential, suffix='/positions')[0], 200)
        self.assertEqual(self.http(identifier, None, suffix='/positions')[0], 401)
        self.assertEqual(self.http(identifier, 'wrong', suffix='/positions')[0], 404)
        self.assertEqual(self.http(identifier, credential, suffix='/positions?limit=501')[0], 422)
        asyncio.run(DisableTracking(self.tracking, lambda: datetime.now(UTC)).execute(identifier))
        self.assertEqual(self.http(identifier, credential, suffix='/positions')[0], 200)
        self.assertEqual(self.http(identifier, credential, self.payload(6))[0], 409)
        self.assertEqual(self.database.tracking_sessions.find_one({'_id': identifier})['first_arrival']['sequence'], 3)
        schema = self.http_openapi()
        self.assertIn('/api/v1/tracking-sessions/{tracking_id}/positions', schema['paths'])
        print('PHASE4_PAGES', json.dumps({'first': [1, 2], 'second': [3, 5]}))

    def http_openapi(self):
        with urlopen(self.base + '/openapi.json', timeout=10) as response:
            return json.load(response)

    def test_concurrent_retries_one_position_and_arrival(self):
        identifier, credential = self.create_session()
        payload = self.payload()
        with ThreadPoolExecutor(max_workers=4) as pool:
            responses = list(pool.map(lambda _: self.http(identifier, credential, payload), range(4)))
        self.assertTrue(all(result == responses[0] for result in responses))
        self.assertEqual(responses[0][0], 200)
        self.assertEqual(self.database.gps_positions.count_documents({'tracking_id': identifier}), 1)
        session = self.database.tracking_sessions.find_one({'_id': identifier})
        self.assertEqual(session['first_arrival']['sequence'], 1)
        self.assertIsNone(session['pending_position'])
        print('PHASE4_CONCURRENCY', json.dumps({'statuses': [r[0] for r in responses],
            'positions': 1, 'first_arrival': 1}))

    def test_partial_failures_and_disabled_repair(self):
        identifier, credential = self.create_session()
        now = utc_milliseconds(datetime.now(UTC))
        longitude, latitude = self.point['location']['coordinates']
        observation = GpsObservation(1, Coordinates(latitude, longitude), 5, now)
        class FailBeforePosition(MongoPositionRepository):
            async def ensure(repo, position):
                raise TrackingStoreUnavailable()
        receiver = ReceiveGpsObservation(GetTrackingSession(self.tracking, verify_credential),
                                         lambda: now, FailBeforePosition(self.database))
        with self.assertRaises(TrackingStoreUnavailable):
            asyncio.run(receiver.execute(identifier, credential, observation))
        session = self.database.tracking_sessions.find_one({'_id': identifier})
        first_arrival = session['first_arrival']
        self.assertIsNotNone(session['pending_position'])
        self.assertEqual(self.database.gps_positions.count_documents({'tracking_id': identifier}), 0)
        self.assertEqual(self.http(identifier, credential, suffix='/positions')[1]['detail']['code'],
                         'position_history_pending')
        asyncio.run(DisableTracking(self.tracking, lambda: now).execute(identifier))
        asyncio.run(RepairPendingPosition(self.tracking, self.positions, lambda: now).execute(identifier))
        self.assertEqual(self.database.gps_positions.count_documents({'tracking_id': identifier}), 1)
        self.assertIsNone(self.database.tracking_sessions.find_one({'_id': identifier})['pending_position'])
        self.assertEqual(self.database.tracking_sessions.find_one({'_id': identifier})['first_arrival'], first_arrival)
        self.assertEqual(self.http(identifier, credential, suffix='/positions')[0], 200)
        print('PHASE4_REPAIR', json.dumps({'after_accept_failure': 503,
            'pending_get': 503, 'repaired_disabled': True}))

    def test_uncertain_insert_and_failure_before_clear(self):
        identifier, credential = self.create_session()
        now = utc_milliseconds(datetime.now(UTC))
        longitude, latitude = self.point['location']['coordinates']
        observation = GpsObservation(1, Coordinates(latitude, longitude), 5, now)
        class UncertainInsert(MongoPositionRepository):
            async def ensure(repo, position):
                await super().ensure(position)
                raise TrackingStoreUnavailable()
        receiver = ReceiveGpsObservation(GetTrackingSession(self.tracking, verify_credential),
                                         lambda: now, UncertainInsert(self.database))
        with self.assertRaises(TrackingStoreUnavailable):
            asyncio.run(receiver.execute(identifier, credential, observation))
        self.assertEqual(self.database.gps_positions.count_documents({'tracking_id': identifier}), 1)
        self.assertIsNotNone(self.database.tracking_sessions.find_one({'_id': identifier})['pending_position'])
        payload = {'sequence': 1, 'latitude': latitude, 'longitude': longitude,
                   'accuracy_m': 5, 'captured_at': now.isoformat()}
        self.assertEqual(self.http(identifier, credential, payload)[0], 200)
        self.assertEqual(self.database.gps_positions.count_documents({'tracking_id': identifier}), 1)
        self.assertIsNone(self.database.tracking_sessions.find_one({'_id': identifier})['pending_position'])
        identifier2, credential2 = self.create_session()
        now2 = utc_milliseconds(datetime.now(UTC))
        observation2 = GpsObservation(1, Coordinates(latitude, longitude), 5, now2)
        class FailBeforeClear(MongoTrackingRepository):
            async def clear_pending(repo, session):
                raise TrackingStoreUnavailable()
        receiver2 = ReceiveGpsObservation(GetTrackingSession(FailBeforeClear(self.database), verify_credential),
                                          lambda: now2, self.positions)
        with self.assertRaises(TrackingStoreUnavailable):
            asyncio.run(receiver2.execute(identifier2, credential2, observation2))
        self.assertEqual(self.database.gps_positions.count_documents({'tracking_id': identifier2}), 1)
        self.assertIsNotNone(self.database.tracking_sessions.find_one({'_id': identifier2})['pending_position'])
        self.assertEqual(self.http(identifier2, credential2,
            {**payload, 'captured_at': now2.isoformat()})[0], 200)
        self.assertEqual(self.database.gps_positions.count_documents({'tracking_id': identifier2}), 1)
        print('PHASE4_UNCERTAIN', json.dumps({'insert_result_uncertain': 'recovered',
            'clear_failed': 'recovered', 'duplicates': 0}))

    def test_expired_position_filtered_and_pending_not_reinserted(self):
        identifier, credential = self.create_session()
        now = utc_milliseconds(datetime.now(UTC))
        longitude, latitude = self.point['location']['coordinates']
        observation = GpsObservation(1, Coordinates(latitude, longitude), 5, now)
        class FailBeforePosition(MongoPositionRepository):
            async def ensure(repo, position):
                raise TrackingStoreUnavailable()
        receiver = ReceiveGpsObservation(GetTrackingSession(self.tracking, verify_credential),
                                         lambda: now, FailBeforePosition(self.database))
        with self.assertRaises(TrackingStoreUnavailable):
            asyncio.run(receiver.execute(identifier, credential, observation))
        pending = self.database.tracking_sessions.find_one({'_id': identifier})['pending_position']
        asyncio.run(RepairPendingPosition(self.tracking, self.positions,
                                         lambda: pending['expires_at'].replace(tzinfo=UTC)).execute(identifier))
        self.assertEqual(self.database.gps_positions.count_documents({'tracking_id': identifier}), 0)
        self.assertIsNone(self.database.tracking_sessions.find_one({'_id': identifier})['pending_position'])
        # A synthetic expired record verifies the query filter independently of TTL timing.
        expired = dict(pending)
        expired['sequence'] = 999
        expired['expires_at'] = datetime.now(UTC) - timedelta(minutes=1)
        self.database.gps_positions.insert_one(expired)
        status, body = self.http(identifier, credential, suffix='/positions')
        self.assertEqual(status, 200)
        self.assertEqual(body['positions'], [])
        self.assertTrue(any(index.get('expireAfterSeconds') == 0
                            for index in self.database.gps_positions.list_indexes()))
        print('PHASE4_EXPIRY', json.dumps({'expired_pending_reinserted': False,
            'expired_record_visible_http': False}))

    def test_legacy_session_http_has_no_retroactive_history(self):
        identifier, credential, fingerprint = create_tracking_credentials()
        reader = GetFirstMeetingPoint(MongoMapRepository(self.database, ensure_indexes=False))
        legacy = EnableTracking(self.tracking, reader, load_tracking_policy(),
                                lambda: datetime.now(UTC))
        asyncio.run(legacy.execute(identifier, 'phase4-legacy-synthetic-guide',
                                   self.route_id, fingerprint))
        self.sessions.append(identifier)
        status, body = self.http(identifier, credential, suffix='/positions')
        self.assertEqual((status, body['detail']['code']), (409, 'history_not_configured'))
        self.assertEqual(self.http(identifier, credential, self.payload())[0], 200)
        self.assertEqual(self.database.gps_positions.count_documents({'tracking_id': identifier}), 0)

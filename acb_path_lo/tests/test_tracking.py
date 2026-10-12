from dataclasses import replace
from datetime import UTC, datetime, timedelta, timezone
import json
import unittest
from unittest.mock import patch
from uuid import uuid4

from acb_path_lo.application.use_cases.disable_tracking import DisableTracking
from acb_path_lo.application.use_cases.enable_tracking import EnableTracking
from acb_path_lo.application.use_cases.get_tracking_session import GetTrackingSession
from acb_path_lo.application.use_cases.receive_gps_observation import ReceiveGpsObservation
from acb_path_lo.domain.model.coordinates import Coordinates
from acb_path_lo.domain.model.first_meeting_point import FirstMeetingPoint
from acb_path_lo.domain.model.gps_observation import GpsObservation, MAX_SEQUENCE, utc_milliseconds
from acb_path_lo.domain.model.tracking_session import TrackingPolicy, TrackingSession
from acb_path_lo.domain.tracking_errors import TrackingError, TrackingStoreUnavailable
from acb_path_lo.infrastructure.config.tracking import load_tracking_policy
from acb_path_lo.infrastructure.security.tracking_credentials import create_tracking_credentials, verify_credential
from acb_path_lo.main import app


class MemoryTrackingRepository:
    def __init__(self):
        self.sessions = {}
        self.writes = 0
        self.uncertain = False

    async def create(self, session):
        if session.tracking_id in self.sessions:
            raise TrackingError('tracking_session_conflict')
        self.sessions[session.tracking_id] = session
        self.writes += 1

    async def get(self, identifier):
        return self.sessions.get(identifier)

    async def accept(self, session, observation, arrival):
        current = self.sessions.get(session.tracking_id)
        if current.version != session.version or current.state != 'enabled':
            return False
        if arrival is not None and current.first_arrival is not None:
            return False
        self.sessions[session.tracking_id] = replace(current, version=current.version + 1,
            last_observation=observation, first_arrival=arrival or current.first_arrival)
        self.writes += 1
        if self.uncertain:
            self.uncertain = False
            raise TrackingStoreUnavailable()
        return True

    async def disable(self, session, when):
        current = self.sessions[session.tracking_id]
        if current.version != session.version or current.state != 'enabled':
            return False
        self.sessions[session.tracking_id] = replace(current, state='disabled',
            disabled_at=when, version=current.version + 1)
        self.writes += 1
        return True


class TrackingFixture(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.now = utc_milliseconds(datetime.now(UTC))
        self.repository = MemoryTrackingRepository()
        self.identifier, self.credential, fingerprint = create_tracking_credentials()
        self.session = TrackingSession(self.identifier, 'guide-test', 'route-test',
            FirstMeetingPoint('route-test', 'point-test', 'PE01', -74, 4),
            TrackingPolicy(30, 10, 30, 5), fingerprint, self.now)
        await self.repository.create(self.session)
        self.get = GetTrackingSession(self.repository, verify_credential)
        self.receive = ReceiveGpsObservation(self.get, lambda: self.now)
        self.disable = DisableTracking(self.repository, lambda: self.now)

    def observation(self, sequence=1, latitude=4, accuracy=5, captured=None):
        return GpsObservation(sequence, Coordinates(latitude, -74), accuracy, captured or self.now)

    async def send(self, observation=None):
        return await self.receive.execute(self.identifier, self.credential, observation or self.observation())

    async def assert_code(self, code, awaitable):
        with self.assertRaises(TrackingError) as context:
            await awaitable
        self.assertEqual(context.exception.code, code)

class TrackingTests(TrackingFixture):
    async def test_first_inside_is_immutable_and_contains_evidence(self):
        first = await self.send()
        self.assertEqual(first.status, 'inside')
        self.assertEqual(first.first_arrival.meeting_point_id, 'point-test')
        self.assertEqual(first.first_arrival.meeting_point_latitude, 4)
        self.assertEqual(first.first_arrival.latitude, 4)
        later = await self.send(self.observation(8, latitude=4.00001))
        self.assertEqual(later.first_arrival, first.first_arrival)

    async def test_outside_and_indeterminate_do_not_arrive(self):
        for sequence, latitude, accuracy, status in [(1, 4.01, 5, 'outside'),
                (2, 4, None, 'indeterminate'), (3, 4, 20, 'indeterminate'),
                (4, 4.00025, 5, 'indeterminate')]:
            result = await self.send(self.observation(sequence, latitude, accuracy))
            self.assertEqual(result.status, status)
            self.assertIsNone(result.first_arrival)

    async def test_replay_after_disable_and_age_preserves_response_without_write(self):
        observation = self.observation()
        original = await self.send(observation)
        await self.disable.execute(self.identifier)
        writes = self.repository.writes
        self.now += timedelta(hours=1)
        self.assertEqual(await self.send(observation), original)
        self.assertEqual(self.repository.writes, writes)
        await self.assert_code('tracking_disabled', self.send(self.observation(2)))

    async def test_replay_after_age_while_enabled(self):
        observation = self.observation()
        original = await self.send(observation)
        self.now += timedelta(hours=1)
        self.assertEqual(await self.send(observation), original)
        self.assertEqual(self.repository.writes, 2)

    async def test_sequence_conflict_stale_and_capture_order(self):
        await self.send(self.observation(4))
        writes = self.repository.writes
        await self.assert_code('observation_sequence_conflict', self.send(self.observation(4, 4.01)))
        await self.assert_code('observation_out_of_order', self.send(self.observation(3)))
        await self.assert_code('observation_out_of_order', self.send(self.observation(5,
            captured=self.now - timedelta(milliseconds=1))))
        self.assertEqual(self.repository.writes, writes)

    async def test_temporal_rejections_and_inclusive_edges(self):
        for offset, code in [(-31, 'observation_too_old'), (6, 'observation_from_future'),
                             (-6, 'observation_before_tracking')]:
            await self.assert_code(code, self.send(self.observation(captured=self.now + timedelta(seconds=offset))))
        await self.send(self.observation(captured=self.now - timedelta(seconds=5)))
        await self.send(self.observation(2, captured=self.now + timedelta(seconds=5)))
        self.repository.sessions[self.identifier] = replace(self.session, enabled_at=self.now - timedelta(minutes=1))
        await self.send(self.observation(captured=self.now - timedelta(seconds=30)))

    async def test_credentials_required_for_replay_and_reads(self):
        await self.send()
        for identifier, credential in [(self.identifier, 'bad'), (str(uuid4()), self.credential)]:
            await self.assert_code('tracking_session_unavailable', self.get.execute(identifier, credential))
        await self.assert_code('tracking_session_unavailable', self.receive.execute(self.identifier, 'bad', self.observation()))

    async def test_uncertain_write_recovers_on_retry(self):
        self.repository.uncertain = True
        observation = self.observation()
        await self.assert_code('tracking_store_unavailable', self.send(observation))
        result = await self.send(observation)
        self.assertIsNotNone(result.first_arrival)
        self.assertEqual(self.repository.writes, 2)

    async def test_disable_idempotent_and_cas_exhaustion(self):
        await self.disable.execute(self.identifier)
        await self.disable.execute(self.identifier)
        self.assertEqual(self.repository.writes, 2)
        self.repository.sessions[self.identifier] = self.session
        async def never(*args):
            return False
        with patch.object(self.repository, 'accept', never):
            await self.assert_code('tracking_busy', self.send())
        with patch.object(self.repository, 'disable', never):
            await self.assert_code('tracking_busy', self.disable.execute(self.identifier))

    async def test_enable_checks_configuration_and_resolves_target_once(self):
        class PointReader:
            calls = 0
            async def execute(reader, route):
                reader.calls += 1
                return self.session.target
        reader = PointReader()
        identifier = str(uuid4())
        enable = EnableTracking(self.repository, reader, None, lambda: self.now)
        await self.assert_code('tracking_configuration_unavailable', enable.execute(identifier, 'g', 'r', 'hash'))
        self.assertEqual(reader.calls, 0)
        enable.policy = self.session.policy
        created = await enable.execute(identifier, 'simulated:external-id', 'r', 'hash')
        self.assertEqual(created.target, self.session.target)
        self.assertEqual(reader.calls, 1)

    async def test_snapshot_policy_and_duplicate_creation_preserve_disabled_state(self):
        await self.disable.execute(self.identifier)
        await self.assert_code('tracking_session_conflict', self.repository.create(self.session))
        self.assertEqual((await self.repository.get(self.identifier)).state, 'disabled')
        # Separate enabled fixture; changing process configuration cannot alter its policy.
        self.repository.sessions[self.identifier] = self.session
        with patch.dict('os.environ', {'ACB_FIRST_PE_RADIUS_M': '1', 'ACB_GPS_MAX_AGE_S': '1'}):
            self.now += timedelta(seconds=2)
            result = await self.send(self.observation(captured=self.session.enabled_at))
        self.assertEqual(result.radius_m, 30)
        self.assertEqual(result.status, 'inside')

    async def test_disable_winning_against_pending_receive(self):
        actual_accept = self.repository.accept
        async def disable_before_accept(session, observation, arrival):
            await self.disable.execute(self.identifier)
            return await actual_accept(session, observation, arrival)
        with patch.object(self.repository, 'accept', disable_before_accept):
            await self.assert_code('tracking_disabled', self.send())
        self.assertIsNone((await self.repository.get(self.identifier)).first_arrival)

    def test_normalization_and_technical_ranges(self):
        first = self.observation(captured=self.now.replace(microsecond=123001))
        second = GpsObservation(1, Coordinates(4.0, -74.0), 5.0,
                                self.now.replace(microsecond=123999).astimezone(
                                    timezone(timedelta(hours=-5))))
        self.assertEqual(first.fingerprint(), second.fingerprint())
        self.assertEqual(GpsObservation(1, Coordinates(-0.0, 0), None, self.now).fingerprint(),
                         GpsObservation(1, Coordinates(0, -0.0), None, self.now).fingerprint())
        for sequence in [True, 0, -1, 1.5, MAX_SEQUENCE + 1]:
            with self.assertRaises(ValueError):
                self.observation(sequence)
        with self.assertRaises(ValueError):
            self.observation(captured=datetime(2026, 1, 1))

    def test_configuration_has_no_defaults(self):
        values = {'ACB_FIRST_PE_RADIUS_M': '30', 'ACB_FIRST_PE_MAX_ACCURACY_M': '10',
                  'ACB_GPS_MAX_AGE_S': '30', 'ACB_GPS_CLOCK_SKEW_S': '5'}
        self.assertEqual(load_tracking_policy(values), self.session.policy)
        for key in values:
            missing = dict(values)
            del missing[key]
            self.assertIsNone(load_tracking_policy(missing))
            for value in ['-1', 'nan', 'inf', 'x', '']:
                self.assertIsNone(load_tracking_policy({**values, key: value}))
        self.assertIsNone(load_tracking_policy({**values, 'ACB_GPS_MAX_AGE_S': '0'}))
        self.assertIsNotNone(load_tracking_policy({**values, 'ACB_GPS_CLOCK_SKEW_S': '0'}))


async def tracking_http(path, payload=None, credential=None):
    messages = []
    data = json.dumps(payload).encode() if payload is not None else b''
    async def receive():
        return {'type': 'http.request', 'body': data, 'more_body': False}
    async def send(message):
        messages.append(message)
    headers = [(b'content-type', b'application/json')]
    if credential:
        headers.append((b'authorization', ('Bearer ' + credential).encode()))
    await app({'type': 'http', 'asgi': {'version': '3.0'}, 'http_version': '1.1',
               'method': 'POST' if payload is not None else 'GET', 'scheme': 'http',
               'path': path, 'root_path': '', 'query_string': b'', 'headers': headers,
               'server': ('test', 80), 'client': ('test', 123)}, receive, send)
    status = next(item['status'] for item in messages if item['type'] == 'http.response.start')
    body = b''.join(item.get('body', b'') for item in messages if item['type'] == 'http.response.body')
    return status, json.loads(body)


class TrackingHttpTests(TrackingFixture):
    async def request(self, payload=None, credential=True, identifier=None):
        path = '/api/v1/tracking-sessions/' + (identifier or self.identifier)
        if payload is not None:
            path += '/observations'
        with patch.object(app.state, 'tracking_repository', self.repository, create=True):
            return await tracking_http(path, payload, self.credential if credential is True else credential)

    def payload(self):
        return {'sequence': 1, 'latitude': 4, 'longitude': -74, 'accuracy_m': 5,
                'captured_at': self.now.isoformat()}

    async def test_http_success_read_replay_and_security(self):
        status, original = await self.request(self.payload())
        self.assertEqual(status, 200)
        self.assertEqual(original['status'], 'inside')
        await self.disable.execute(self.identifier)
        self.assertEqual(await self.request(self.payload()), (200, original))
        status, view = await self.request()
        self.assertEqual(status, 200)
        self.assertNotIn('credential_hash', view)
        self.assertEqual(view['state'], 'disabled')
        for credential, code in [(None, 401), ('bad', 404)]:
            self.assertEqual((await self.request(credential=credential))[0], code)
        self.assertEqual((await self.request(identifier=str(uuid4())))[0], 404)
        self.assertEqual((await self.request(identifier='bad'))[0], 422)

    async def test_http_strict_inputs(self):
        cases = [{'sequence': value} for value in [True, '1', 1.1, 0, MAX_SEQUENCE + 1]]
        cases += [{name: value} for name, value in [('latitude', True), ('latitude', '4'),
            ('longitude', float('nan')), ('latitude', 91), ('longitude', -181),
            ('accuracy_m', 0), ('accuracy_m', -1), ('accuracy_m', float('inf')),
            ('accuracy_m', '5'), ('captured_at', None), ('captured_at', 123),
            ('captured_at', '2026-10-12T10:00:00'), ('guide_id', 'other'),
            ('route_id', 'other'), ('radius_m', 30), ('meeting_point_id', 'other')]]
        for changes in cases:
            with self.subTest(changes=changes):
                self.assertEqual((await self.request({**self.payload(), **changes}))[0], 422)
        missing = self.payload()
        del missing['captured_at']
        self.assertEqual((await self.request(missing))[0], 422)
        self.assertEqual(self.repository.writes, 1)

    async def test_http_conflict_and_uncertain_store(self):
        self.repository.uncertain = True
        self.assertEqual((await self.request(self.payload()))[0], 503)
        self.assertEqual((await self.request(self.payload()))[0], 200)
        self.assertEqual((await self.request({**self.payload(), 'latitude': 4.01}))[0], 409)

    async def test_http_read_store_failure_does_not_write(self):
        async def unavailable(identifier):
            raise TrackingStoreUnavailable()
        with patch.object(self.repository, 'get', unavailable):
            status, body = await self.request()
        self.assertEqual((status, body['detail']['code']), (503, 'tracking_store_unavailable'))
        self.assertEqual(self.repository.writes, 1)

    def test_openapi(self):
        schema = app.openapi()
        operation = schema['paths']['/api/v1/tracking-sessions/{tracking_id}/observations']['post']
        self.assertEqual(operation['security'], [{'TrackingSessionCredential': []}])
        model = schema['components']['schemas']['ObservationRequest']
        self.assertIn('captured_at', model['required'])
        self.assertIn('sequence', model['required'])
        self.assertFalse(model['additionalProperties'])
        self.assertNotIn('credential_hash', schema['components']['schemas']['TrackingView']['properties'])

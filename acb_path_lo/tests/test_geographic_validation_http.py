import json
import os
import unittest
from unittest.mock import patch

from pymongo.errors import ConnectionFailure

from acb_path_lo.domain.errors import (
    AssociationConflict, FirstMeetingPointNotConfigured, InvalidGeographicData,
    InvalidIdentifier, MeetingPointNotFound, RouteNotFound,
)
from acb_path_lo.domain.model.first_meeting_point import FirstMeetingPoint
from acb_path_lo.domain.model.geographic_validation import GeofencePolicy
from acb_path_lo.infrastructure.config.geofence import load_geofence_policy
from acb_path_lo.main import app
from acb_path_lo.tests.test_first_meeting_point import asgi_get


async def asgi_post(path, payload):
    data = json.dumps(payload).encode()
    messages = []
    async def receive():
        return {'type': 'http.request', 'body': data, 'more_body': False}
    async def send(message):
        messages.append(message)
    await app({'type': 'http', 'asgi': {'version': '3.0'}, 'http_version': '1.1',
               'method': 'POST', 'scheme': 'http', 'path': path, 'root_path': '',
               'query_string': b'', 'headers': [(b'content-type', b'application/json')],
               'server': ('test', 80), 'client': ('test', 123)}, receive, send)
    status = next(m['status'] for m in messages if m['type'] == 'http.response.start')
    body = b''.join(m.get('body', b'') for m in messages if m['type'] == 'http.response.body')
    return status, json.loads(body)


class ReadOnlyRepository:
    async def get_first_meeting_point(self, route_id):
        return FirstMeetingPoint(route_id, 'point', 'PE01', -74, 4)
    async def list_routes(self):
        return []
    async def list_meeting_points(self):
        return []


class ConfigurationTests(unittest.TestCase):
    def test_missing_invalid_and_valid_configuration(self):
        self.assertIsNone(load_geofence_policy({}))
        for key in ('ACB_FIRST_PE_RADIUS_M', 'ACB_FIRST_PE_MAX_ACCURACY_M'):
            for value in ['', '0', '-1', 'nan', 'inf', 'true', 'abc']:
                values = {'ACB_FIRST_PE_RADIUS_M': '30', 'ACB_FIRST_PE_MAX_ACCURACY_M': '10', key: value}
                with self.subTest(key=key, value=value):
                    self.assertIsNone(load_geofence_policy(values))
        self.assertEqual(load_geofence_policy({'ACB_FIRST_PE_RADIUS_M': '30', 'ACB_FIRST_PE_MAX_ACCURACY_M': '10'}), GeofencePolicy(30, 10))


class ValidationHttpTests(unittest.IsolatedAsyncioTestCase):
    path = '/api/v1/routes/route/first-meeting-point/validate'

    async def request(self, payload, repository=None, policy=GeofencePolicy(30, 10)):
        with patch.object(app.state, 'map_repository', repository or ReadOnlyRepository(), create=True), patch.object(app.state, 'geofence_policy', policy, create=True):
            return await asgi_post(self.path, payload)

    async def test_success_and_timestamp_normalization(self):
        status, body = await self.request({'latitude': 4, 'longitude': -74, 'accuracy_m': 5,
                                          'captured_at': '2026-10-12T10:00:00-05:00'})
        self.assertEqual(status, 200)
        self.assertEqual(body['status'], 'inside')
        self.assertEqual(body['distance_m'], 0)
        self.assertEqual(body['captured_at'], '2026-10-12T15:00:00Z')
        self.assertTrue(body['validated_at'].endswith('Z'))

    async def test_missing_accuracy_is_indeterminate(self):
        status, body = await self.request({'latitude': 4, 'longitude': -74})
        self.assertEqual(status, 200)
        self.assertEqual(body['status'], 'indeterminate')
        self.assertEqual(body['quality']['reason'], 'accuracy_missing')

    async def test_strict_input_rejection(self):
        invalid = [
            {'latitude': True}, {'longitude': False}, {'latitude': '4'},
            {'longitude': '-74'}, {'latitude': 91}, {'longitude': -181},
            {'latitude': float('nan')}, {'longitude': float('inf')},
            {'accuracy_m': 0}, {'accuracy_m': -1}, {'accuracy_m': True},
            {'accuracy_m': '5'}, {'accuracy_m': float('-inf')},
            {'radius_m': 30}, {'unknown': 'field'},
            {'captured_at': '2026-10-12T10:00:00'}, {'captured_at': 123},
            {'captured_at': 'invalid'},
        ]
        for fields in invalid:
            payload = {'latitude': 4, 'longitude': -74, **fields}
            with self.subTest(fields=fields):
                status, _ = await self.request(payload)
                self.assertEqual(status, 422)

    async def test_existing_error_contracts(self):
        for error, status, code in [
            (InvalidIdentifier, 422, 'invalid_identifier'),
            (RouteNotFound, 404, 'route_not_found'),
            (FirstMeetingPointNotConfigured, 409, 'first_meeting_point_not_configured'),
            (MeetingPointNotFound, 409, 'referenced_meeting_point_not_found'),
            (InvalidGeographicData, 409, 'invalid_geographic_association'),
            (AssociationConflict, 409, 'invalid_geographic_association'),
            (ConnectionFailure, 503, 'geographic_store_unavailable'),
        ]:
            class FailingRepository:
                async def get_first_meeting_point(self, route_id):
                    raise error('test')
            with self.subTest(error=error):
                actual, body = await self.request({'latitude': 4, 'longitude': -74}, FailingRepository())
                self.assertEqual(actual, status)
                self.assertEqual(body['detail']['code'], code)

    async def test_unavailable_configuration_only_disables_validation(self):
        for values in [{}, {'ACB_FIRST_PE_RADIUS_M': '-1', 'ACB_FIRST_PE_MAX_ACCURACY_M': '10'}]:
            policy = load_geofence_policy(values)
            with patch.object(app.state, 'map_repository', ReadOnlyRepository(), create=True), patch.object(app.state, 'geofence_policy', policy, create=True):
                status, body = await asgi_post(self.path, {'latitude': 4, 'longitude': -74})
                self.assertEqual((status, body['detail']['code']), (503, 'geofence_configuration_unavailable'))
                for path in ['/api/v1/routes/route/first-meeting-point', '/api/v1/routes', '/api/v1/meeting-points', '/openapi.json']:
                    self.assertEqual((await asgi_get(path))[0], 200)

    async def test_policy_does_not_read_environment_per_request(self):
        with patch.dict(os.environ, {'ACB_FIRST_PE_RADIUS_M': '1', 'ACB_FIRST_PE_MAX_ACCURACY_M': '1'}):
            _, body = await self.request({'latitude': 4, 'longitude': -74})
        self.assertEqual(body['radius_m'], 30)

    def test_openapi_contract_and_existing_endpoints(self):
        schema = app.openapi()
        path = '/api/v1/routes/{route_id}/first-meeting-point/validate'
        operation = schema['paths'][path]['post']
        reference = operation['requestBody']['content']['application/json']['schema']['$ref'].split('/')[-1]
        request = schema['components']['schemas'][reference]
        self.assertFalse(request['additionalProperties'])
        self.assertEqual(set(request['required']), {'latitude', 'longitude'})
        self.assertNotIn('radius_m', request['properties'])
        response = schema['components']['schemas']['GeographicValidation']
        self.assertEqual(set(response['properties']['status']['enum']), {'inside', 'outside', 'indeterminate'})
        for path in ['/api/v1/routes', '/api/v1/meeting-points', '/api/v1/routes/{route_id}/first-meeting-point', '/api/v1/imports/kmz', '/api/v1/status/notify']:
            self.assertIn(path, schema['paths'])

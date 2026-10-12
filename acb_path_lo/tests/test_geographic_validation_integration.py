"""Explicit opt-in HTTP/MongoDB checks; never insert, update or delete data."""
import json
import math
import os
import unittest
from urllib.error import HTTPError
from urllib.request import Request, urlopen

from bson import ObjectId, json_util
from pymongo import MongoClient

from acb_path_lo.domain.services.geographic_validation import EARTH_RADIUS_M


def request_json(base, path, payload=None):
    data = None if payload is None else json.dumps(payload).encode()
    request = Request(base.rstrip('/') + path, data=data,
                      headers={'Content-Type': 'application/json'} if data else {})
    try:
        response = urlopen(request, timeout=10)
    except HTTPError as error:
        response = error
    with response:
        return response.status, json.load(response)


@unittest.skipUnless(os.getenv('ACB_PHASE2_URL'), 'Requires explicit phase 2 live URLs and policy')
class GeographicValidationIntegrationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.base = os.environ['ACB_PHASE2_URL']
        cls.unconfigured_base = os.environ['ACB_PHASE2_UNCONFIGURED_URL']
        cls.invalid_base = os.environ['ACB_PHASE2_INVALID_URL']
        cls.route_id = os.environ['ACB_INTEGRATION_ROUTE_ID']
        cls.radius = float(os.environ['ACB_FIRST_PE_RADIUS_M'])
        cls.maximum_accuracy = float(os.environ['ACB_FIRST_PE_MAX_ACCURACY_M'])
        cls.client = MongoClient(os.environ['MONGO_URI'], serverSelectionTimeoutMS=5000)
        cls.addClassCleanup(cls.client.close)
        cls.database = cls.client[os.environ['MONGO_DATABASE']]
        cls.before = cls.snapshot()
        route = cls.database.routes.find_one({'_id': ObjectId(cls.route_id)})
        cls.point = cls.database.meeting_points.find_one({'_id': route['first_meeting_point_id']})

    @classmethod
    def snapshot(cls):
        return {name: json_util.dumps({
            'documents': list(cls.database[name].find({}).sort('_id', 1)),
            'indexes': list(cls.database[name].list_indexes()),
        }, sort_keys=True) for name in ('routes', 'meeting_points', 'route_status')}

    @classmethod
    def tearDownClass(cls):
        if cls.snapshot() != cls.before:
            raise AssertionError('MongoDB documents or indexes changed during read-only HTTP verification')
        print('INTEGRITY: all documents and indexes unchanged')

    def test_real_geographic_scenarios(self):
        longitude, latitude = self.point['location']['coordinates']
        accuracy = min(self.maximum_accuracy, self.radius / 6)
        scenarios = [
            ('zero', 0, accuracy, 'inside', 'uncertainty_within_radius'),
            ('inside', self.radius / 2, accuracy, 'inside', 'uncertainty_within_radius'),
            ('outside', self.radius * 2, accuracy, 'outside', 'uncertainty_outside_radius'),
            ('boundary', self.radius - accuracy / 2, accuracy, 'indeterminate', 'boundary_uncertainty'),
            ('insufficient', 0, self.maximum_accuracy * 2, 'indeterminate', 'accuracy_exceeds_limit'),
            ('missing', 0, None, 'indeterminate', 'accuracy_missing'),
        ]
        path = f'/api/v1/routes/{self.route_id}/first-meeting-point/validate'
        for name, distance, reported_accuracy, expected_status, reason in scenarios:
            payload = {'latitude': latitude + math.degrees(distance / EARTH_RADIUS_M), 'longitude': longitude}
            if reported_accuracy is not None:
                payload['accuracy_m'] = reported_accuracy
            if name == 'zero':
                payload['captured_at'] = '2026-10-12T10:00:00-05:00'
            with self.subTest(scenario=name):
                status, body = request_json(self.base, path, payload)
                print('HTTP_SCENARIO', json.dumps({'scenario': name, 'http': status, 'request': payload, 'response': body}))
                self.assertEqual(status, 200)
                self.assertEqual(body['meeting_point_id'], str(self.point['_id']))
                self.assertAlmostEqual(body['distance_m'], distance, delta=0.001)
                self.assertEqual(body['radius_m'], self.radius)
                self.assertEqual(body['status'], expected_status)
                self.assertEqual(body['quality']['reason'], reason)
                self.assertTrue(body['validated_at'].endswith('Z'))

    def test_missing_and_invalid_configuration_keep_reads_available(self):
        longitude, latitude = self.point['location']['coordinates']
        for name, base in [('missing_config', self.unconfigured_base), ('invalid_config', self.invalid_base)]:
            status, body = request_json(base, f'/api/v1/routes/{self.route_id}/first-meeting-point/validate', {'latitude': latitude, 'longitude': longitude})
            print('HTTP_SCENARIO', json.dumps({'scenario': name, 'http': status, 'response': body}))
            self.assertEqual((status, body['detail']['code']), (503, 'geofence_configuration_unavailable'))
            for path in [f'/api/v1/routes/{self.route_id}/first-meeting-point', '/api/v1/routes', '/api/v1/meeting-points', '/openapi.json']:
                status, _ = request_json(base, path)
                print('HTTP_COMPATIBILITY', json.dumps({'configuration': name, 'path': path, 'http': status}))
                self.assertEqual(status, 200)

    def test_real_http_geographic_errors(self):
        longitude, latitude = self.point['location']['coordinates']
        payload = {'latitude': latitude, 'longitude': longitude}
        unconfigured = self.database.routes.find_one({'first_meeting_point_id': {'$exists': False}})
        cases = [('bad', 422, 'invalid_identifier'),
                 (str(ObjectId()), 404, 'route_not_found'),
                 (str(unconfigured['_id']), 409, 'first_meeting_point_not_configured')]
        for route_id, expected, code in cases:
            status, body = request_json(self.base, f'/api/v1/routes/{route_id}/first-meeting-point/validate', payload)
            print('HTTP_SCENARIO', json.dumps({'scenario': code, 'http': status, 'response': body}))
            self.assertEqual((status, body['detail']['code']), (expected, code))

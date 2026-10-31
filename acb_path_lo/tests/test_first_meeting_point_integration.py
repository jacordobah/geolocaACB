"""Opt-in read-only test of the configured prototype through real HTTP/MongoDB."""
import json
import os
import unittest
from urllib.request import urlopen

from bson import ObjectId
from pymongo import MongoClient


@unittest.skipUnless(os.getenv('ACB_INTEGRATION_URL'), 'Requires explicit live test URL')
class FirstMeetingPointIntegrationTests(unittest.TestCase):
    def test_http_matches_configured_mongo_point(self):
        route_id = os.environ['ACB_INTEGRATION_ROUTE_ID']
        point_id = os.environ['ACB_INTEGRATION_POINT_ID']
        url = os.environ['ACB_INTEGRATION_URL'].rstrip('/')
        with urlopen(f'{url}/api/v1/routes/{route_id}/first-meeting-point', timeout=10) as response:
            self.assertEqual(response.status, 200)
            body = json.load(response)
        with MongoClient(os.environ['MONGO_URI'], serverSelectionTimeoutMS=5000) as client:
            database = client[os.environ['MONGO_DATABASE']]
            route = database.routes.find_one({'_id': ObjectId(route_id)})
            point = database.meeting_points.find_one({'_id': ObjectId(point_id)})
            self.assertEqual(route['first_meeting_point_id'], point['_id'])
            self.assertEqual(body['route_id'], route_id)
            self.assertEqual(body['meeting_point_id'], point_id)
            self.assertEqual([body['longitude'], body['latitude']], point['location']['coordinates'])

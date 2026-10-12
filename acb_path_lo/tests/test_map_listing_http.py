"""Regression coverage for listing BSON references without changing stored data."""
import copy
import unittest
from unittest.mock import patch

from bson import ObjectId

from acb_path_lo.infrastructure.persistence.mongo_map_repository import MongoMapRepository
from acb_path_lo.main import app
from acb_path_lo.tests.test_first_meeting_point import Collection, asgi_get


class ReadableCollection(Collection):
    def find(self, query):
        return [document for document in self.documents.values()
                if all(document.get(key) == value for key, value in query.items())]


class Database:
    def __init__(self):
        self.collections = {}

    def __getitem__(self, name):
        return self.collections.setdefault(name, ReadableCollection())


class MapListingHttpTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.repository = MongoMapRepository(Database())
        self.route_id, self.point_id = ObjectId(), ObjectId()
        self.route = {
            '_id': self.route_id, 'import_id': 'configured-import', 'name': 'Route',
            'first_meeting_point_id': self.point_id,
            'geometry': {'type': 'LineString', 'coordinates': [[-74.1, 4.6], [-74.2, 4.7]]},
        }
        self.point = {
            '_id': self.point_id, 'import_id': 'configured-import', 'name': 'PE01',
            'location': {'type': 'Point', 'coordinates': [-74.1, 4.6]},
        }
        self.repository.routes.documents[self.route_id] = self.route
        self.repository.meeting_points.documents[self.point_id] = self.point

    async def test_configured_route_returns_json_and_preserves_bson(self):
        before = copy.deepcopy(self.route)
        with patch.object(app.state, 'map_repository', self.repository, create=True):
            status, body = await asgi_get('/api/v1/routes')
        self.assertEqual(status, 200)
        self.assertEqual(body[0]['_id'], str(self.route_id))
        self.assertEqual(body[0]['first_meeting_point_id'], str(self.point_id))
        self.assertEqual(self.route, before)
        self.assertIsInstance(self.route['first_meeting_point_id'], ObjectId)

    async def test_point_listing_and_rf09_coexist_without_changing_data(self):
        before = copy.deepcopy((self.route, self.point))
        with patch.object(app.state, 'map_repository', self.repository, create=True):
            status, points = await asgi_get('/api/v1/meeting-points')
            pe_status, pe = await asgi_get(
                f'/api/v1/routes/{self.route_id}/first-meeting-point')
        self.assertEqual(status, 200)
        self.assertEqual(points[0]['_id'], str(self.point_id))
        self.assertEqual(points[0]['location']['coordinates'], [-74.1, 4.6])
        self.assertEqual(pe_status, 200)
        self.assertEqual(pe['meeting_point_id'], str(self.point_id))
        self.assertEqual([pe['longitude'], pe['latitude']], [-74.1, 4.6])
        self.assertEqual((self.route, self.point), before)

    async def test_filters_keep_imports_separate(self):
        other_id = ObjectId()
        self.repository.routes.documents[other_id] = {'_id': other_id, 'import_id': 'other'}
        routes = await self.repository.list_routes('configured-import')
        points = await self.repository.list_meeting_points('configured-import')
        self.assertEqual([route['_id'] for route in routes], [str(self.route_id)])
        self.assertEqual([point['_id'] for point in points], [str(self.point_id)])
        self.assertEqual(await self.repository.list_routes('missing'), [])
        self.assertEqual(await self.repository.list_meeting_points('missing'), [])

    def test_openapi_contains_all_endpoint_contracts(self):
        paths = app.openapi()['paths']
        for path, method in [
            ('/api/v1/imports/kmz', 'post'),
            ('/api/v1/status/notify', 'post'),
            ('/api/v1/routes', 'get'),
            ('/api/v1/meeting-points', 'get'),
            ('/api/v1/routes/{route_id}/first-meeting-point', 'get'),
        ]:
            self.assertIn(method, paths[path])

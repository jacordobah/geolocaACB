import copy
import json
import unittest
from types import SimpleNamespace
from unittest.mock import patch

from bson import ObjectId
from pymongo.errors import ConnectionFailure

from acb_path_lo.application.use_cases.get_first_meeting_point import GetFirstMeetingPoint
from acb_path_lo.application.use_cases.set_first_meeting_point import SetFirstMeetingPoint
from acb_path_lo.domain.errors import (
    AssociationConflict, FirstMeetingPointNotConfigured, InvalidGeographicData,
    InvalidIdentifier, MeetingPointNotFound, RouteNotFound,
)
from acb_path_lo.infrastructure.persistence.mongo_map_repository import MongoMapRepository
from acb_path_lo.main import app


class Collection:
    def __init__(self):
        self.documents = {}
        self.updates = 0
        self.before_update = None

    def create_index(self, keys):
        pass

    def find_one(self, query):
        return copy.deepcopy(self.documents.get(query['_id']))

    def update_one(self, query, update, upsert=False):
        assert not upsert
        if self.before_update:
            self.before_update()
        document = self.documents.get(query['_id'])
        eligible = document is not None and (
            'first_meeting_point_id' not in document
            or document['first_meeting_point_id'] == query['$or'][1]['first_meeting_point_id']
        )
        if not eligible:
            return SimpleNamespace(matched_count=0, modified_count=0)
        changed = document.get('first_meeting_point_id') != update['$set']['first_meeting_point_id']
        document.update(update['$set'])
        self.updates += int(changed)
        return SimpleNamespace(matched_count=1, modified_count=int(changed))


class Database:
    def __init__(self):
        self.collections = {}

    def __getitem__(self, name):
        return self.collections.setdefault(name, Collection())


class FirstMeetingPointTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.database = Database()
        self.repository = MongoMapRepository(self.database)
        self.route_id, self.point_id = ObjectId(), ObjectId()
        self.route = {'_id': self.route_id}
        self.point = {
            '_id': self.point_id, 'name': 'PE01',
            'location': {'type': 'Point', 'coordinates': [-74.1, 4.6]},
        }
        self.repository.routes.documents[self.route_id] = self.route
        self.repository.meeting_points.documents[self.point_id] = self.point
        self.get = GetFirstMeetingPoint(self.repository)
        self.configure = SetFirstMeetingPoint(self.repository)

    async def test_configure_and_resolve_without_bson_in_result(self):
        await self.configure.execute(str(self.route_id), str(self.point_id))
        result = await self.get.execute(str(self.route_id))
        self.assertEqual(result.route_id, str(self.route_id))
        self.assertEqual(result.meeting_point_id, str(self.point_id))
        self.assertEqual((result.longitude, result.latitude), (-74.1, 4.6))

    async def test_same_association_is_idempotent(self):
        for _ in range(2):
            await self.configure.execute(str(self.route_id), str(self.point_id))
        self.assertEqual(self.repository.routes.updates, 1)

    async def test_different_association_is_preserved(self):
        original = ObjectId()
        self.route['first_meeting_point_id'] = original
        with self.assertRaises(AssociationConflict):
            await self.configure.execute(str(self.route_id), str(self.point_id))
        self.assertEqual(self.route['first_meeting_point_id'], original)

    async def test_concurrent_association_change_is_rejected(self):
        original = ObjectId()
        def concurrent_change():
            self.route['first_meeting_point_id'] = original
        self.repository.routes.before_update = concurrent_change
        with self.assertRaises(AssociationConflict):
            await self.configure.execute(str(self.route_id), str(self.point_id))
        self.assertEqual(self.route['first_meeting_point_id'], original)

    async def test_missing_route(self):
        missing = str(ObjectId())
        with self.assertRaises(RouteNotFound):
            await self.get.execute(missing)
        with self.assertRaises(RouteNotFound):
            await self.configure.execute(missing, str(self.point_id))

    async def test_missing_reference(self):
        with self.assertRaises(FirstMeetingPointNotConfigured):
            await self.get.execute(str(self.route_id))

    async def test_missing_point(self):
        self.repository.meeting_points.documents.clear()
        with self.assertRaises(MeetingPointNotFound):
            await self.configure.execute(str(self.route_id), str(self.point_id))
        self.route['first_meeting_point_id'] = self.point_id
        with self.assertRaises(MeetingPointNotFound):
            await self.get.execute(str(self.route_id))

    async def test_invalid_identifiers(self):
        for value in ['not-an-id', '', None]:
            with self.subTest(value=value), self.assertRaises(InvalidIdentifier):
                await self.get.execute(value)
        with self.assertRaises(InvalidIdentifier):
            await self.configure.execute(str(self.route_id), 'bad')

    async def test_invalid_geometry_is_not_configured(self):
        for coords in [[4.6, -740], [-74.1], [float('nan'), 4.6], [True, 4.6]]:
            self.point['location']['coordinates'] = coords
            with self.subTest(coords=coords), self.assertRaises(InvalidGeographicData):
                await self.configure.execute(str(self.route_id), str(self.point_id))
        self.assertEqual(self.repository.routes.updates, 0)

    async def test_malformed_reference(self):
        self.route['first_meeting_point_id'] = 'not-a-bson-id'
        with self.assertRaises(InvalidGeographicData):
            await self.get.execute(str(self.route_id))


async def asgi_get(path):
    messages = []
    async def receive():
        return {'type': 'http.request', 'body': b'', 'more_body': False}
    async def send(message):
        messages.append(message)
    scope = {'type': 'http', 'asgi': {'version': '3.0'}, 'http_version': '1.1',
             'method': 'GET', 'scheme': 'http', 'path': path, 'root_path': '',
             'query_string': b'', 'headers': [], 'server': ('test', 80),
             'client': ('test', 123)}
    await app(scope, receive, send)
    status = next(m['status'] for m in messages if m['type'] == 'http.response.start')
    body = b''.join(m.get('body', b'') for m in messages if m['type'] == 'http.response.body')
    return status, json.loads(body)


class HttpTests(unittest.IsolatedAsyncioTestCase):
    setUp = FirstMeetingPointTests.setUp
    async def test_http_success(self):
        await self.configure.execute(str(self.route_id), str(self.point_id))
        with patch.object(app.state, 'map_repository', self.repository, create=True):
            status, body = await asgi_get(f'/api/v1/routes/{self.route_id}/first-meeting-point')
        self.assertEqual(status, 200)
        self.assertEqual(body['meeting_point_id'], str(self.point_id))
        self.assertEqual(body['longitude'], -74.1)

    async def test_http_error_mapping(self):
        cases = [
            (InvalidIdentifier, 422, 'invalid_identifier'),
            (RouteNotFound, 404, 'route_not_found'),
            (FirstMeetingPointNotConfigured, 409, 'first_meeting_point_not_configured'),
            (MeetingPointNotFound, 409, 'referenced_meeting_point_not_found'),
            (AssociationConflict, 409, 'invalid_geographic_association'),
            (InvalidGeographicData, 409, 'invalid_geographic_association'),
            (ConnectionFailure, 503, 'geographic_store_unavailable'),
        ]
        for error, expected, code in cases:
            class FailingRepository:
                async def get_first_meeting_point(self, route_id):
                    raise error('test failure')
            with self.subTest(error=error.__name__), patch.object(
                app.state, 'map_repository', FailingRepository(), create=True
            ):
                status, body = await asgi_get(f'/api/v1/routes/{self.route_id}/first-meeting-point')
                self.assertEqual(status, expected)
                self.assertEqual(body['detail']['code'], code)

    def test_existing_endpoints_remain_in_openapi(self):
        paths = app.openapi()['paths']
        self.assertIn('post', paths['/api/v1/imports/kmz'])
        self.assertIn('post', paths['/api/v1/status/notify'])
        self.assertEqual(set(paths['/api/v1/routes/{route_id}/first-meeting-point']), {'get'})

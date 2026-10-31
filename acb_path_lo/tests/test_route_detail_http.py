import copy
import unittest
from unittest.mock import patch

from bson import ObjectId

from acb_path_lo.infrastructure.persistence.mongo_map_repository import (
    MongoMapRepository,
)
from acb_path_lo.main import app
from acb_path_lo.tests.test_first_meeting_point import asgi_get


class Collection:
    def __init__(self):
        self.documents = {}

    def create_index(self, keys):
        pass

    def find_one(self, query):
        return copy.deepcopy(self.documents.get(query["_id"]))

    def find(self, query):
        reference = query.get("_id")
        if isinstance(reference, dict) and "$in" in reference:
            return [
                copy.deepcopy(document)
                for identifier, document in self.documents.items()
                if identifier in reference["$in"]
            ]
        return [
            copy.deepcopy(document)
            for document in self.documents.values()
            if all(document.get(key) == value for key, value in query.items())
        ]


class Database:
    def __init__(self):
        self.collections = {}

    def __getitem__(self, name):
        return self.collections.setdefault(name, Collection())


class RouteDetailHttpTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.database = Database()
        self.repository = MongoMapRepository(self.database)
        self.route_id = ObjectId()
        self.first_id, self.second_id, self.school_id = ObjectId(), ObjectId(), ObjectId()
        self.route = {
            "_id": self.route_id,
            "name": "541 RUTA DEMO",
            "route_code": "MM01",
            "code": "541",
            "day_shift": "JORNADA MAÑANA",
            "geometry": {"type": "LineString", "coordinates": [[-74.1, 4.6], [-74.2, 4.7]]},
            "endpoint_status": "resolved",
            "start_meeting_point_id": self.first_id,
            "end_meeting_point_id": self.school_id,
            "meeting_point_ids": [self.second_id, self.first_id],
        }
        self.points = {
            self.first_id: {
                "_id": self.first_id, "name": "PE01",
                "kind": "meeting_point",
                "description": None,
                "location": {"type": "Point", "coordinates": [-74.1, 4.6]},
            },
            self.second_id: {
                "_id": self.second_id, "name": "PE02",
                "kind": "meeting_point",
                "description": None,
                "location": {"type": "Point", "coordinates": [-74.15, 4.65]},
            },
            self.school_id: {
                "_id": self.school_id, "name": "COLEGIO",
                "kind": "school",
                "location": {"type": "Point", "coordinates": [-74.2, 4.7]},
            },
        }
        self.repository.routes.documents[self.route_id] = self.route
        self.repository.meeting_points.documents.update(self.points)

    async def get(self, path):
        with patch.object(app.state, "map_repository", self.repository, create=True):
            return await asgi_get(path)

    async def test_route_detail_expands_points_in_stored_order(self):
        status, body = await self.get(f"/api/v1/routes/{self.route_id}")

        self.assertEqual(status, 200)
        self.assertNotIn("meeting_point_ids", body)
        self.assertEqual(
            [point["name"] for point in body["meeting_points"]],
            ["PE02", "PE01"],
        )
        self.assertEqual(
            [point["_id"] for point in body["meeting_points"]],
            [str(self.second_id), str(self.first_id)],
        )
        self.assertEqual(body["start_meeting_point_id"], str(self.first_id))
        self.assertEqual(body["end_meeting_point_id"], str(self.school_id))
        self.assertEqual(body["route_code"], "MM01")
        self.assertEqual(body["day_shift"], "JORNADA MAÑANA")
        self.assertNotIn("import_id", body)
        self.assertNotIn("folder_path", body)

    async def test_route_without_relation_returns_empty_points(self):
        self.route.pop("meeting_point_ids")

        status, body = await self.get(f"/api/v1/routes/{self.route_id}")

        self.assertEqual(status, 200)
        self.assertEqual(body["meeting_points"], [])

    async def test_route_not_found_and_invalid_identifier(self):
        missing = ObjectId()

        status, body = await self.get(f"/api/v1/routes/{missing}")
        self.assertEqual(status, 404)
        self.assertEqual(body["detail"]["code"], "route_not_found")

        status, body = await self.get("/api/v1/routes/not-an-id")
        self.assertEqual(status, 422)
        self.assertEqual(body["detail"]["code"], "invalid_identifier")

    async def test_missing_reference_is_rejected(self):
        self.repository.meeting_points.documents.pop(self.second_id)

        status, body = await self.get(f"/api/v1/routes/{self.route_id}")
        self.assertEqual(status, 409)
        self.assertEqual(body["detail"]["code"], "referenced_meeting_point_not_found")

    async def test_malformed_geometry_and_reference_are_rejected(self):
        self.repository.meeting_points.documents[self.second_id]["location"] = None

        status, body = await self.get(f"/api/v1/routes/{self.route_id}")
        self.assertEqual(status, 409)
        self.assertEqual(body["detail"]["code"], "invalid_geographic_association")

        self.route["meeting_point_ids"] = ["not-an-object-id"]
        status, body = await self.get(f"/api/v1/routes/{self.route_id}")
        self.assertEqual(status, 409)
        self.assertEqual(body["detail"]["code"], "invalid_geographic_association")

    def test_route_detail_is_registered_in_openapi(self):
        paths = app.openapi()["paths"]
        self.assertIn("get", paths["/api/v1/routes/{route_id}"])


if __name__ == "__main__":
    unittest.main()

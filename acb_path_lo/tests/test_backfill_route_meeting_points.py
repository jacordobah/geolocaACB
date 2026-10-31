import copy
import unittest
from types import SimpleNamespace

from bson import ObjectId

from acb_path_lo.application.use_cases.import_kmz import ImportKmz
from acb_path_lo.infrastructure.kmz.kmz_reader_impl import KmzReaderImpl
from acb_path_lo.infrastructure.persistence.mongo_map_repository import (
    MongoMapRepository,
)
from acb_path_lo.infrastructure.tools.backfill_route_meeting_points import backfill
from acb_path_lo.tests.test_import_pipeline import make_kmz


class Collection:
    def __init__(self):
        self.documents = []
        self.updates = 0

    def create_index(self, keys):
        pass

    def insert_many(self, documents):
        for document in documents:
            stored = copy.deepcopy(document)
            stored.setdefault("_id", ObjectId())
            self.documents.append(stored)

    def delete_many(self, query):
        self.documents = [
            document for document in self.documents
            if document.get("import_id") != query["import_id"]
        ]

    def find(self, query):
        return [
            copy.deepcopy(document)
            for document in self.documents
            if all(
                key not in query or document.get(key) == value
                for key, value in query.items()
            )
        ]

    def find_one(self, query):
        matches = self.find(query)
        return matches[0] if matches else None

    def update_one(self, query, update, upsert=False):
        assert not upsert
        for document in self.documents:
            if document.get("_id") != query.get("_id"):
                continue
            if "meeting_point_ids" in query and "$exists" in query["meeting_point_ids"]:
                if query["meeting_point_ids"]["$exists"] != ("meeting_point_ids" in document):
                    return SimpleNamespace(matched_count=0)
            document.update(copy.deepcopy(update["$set"]))
            self.updates += 1
            return SimpleNamespace(matched_count=1)
        return SimpleNamespace(matched_count=0)


class Database:
    def __init__(self):
        self.collections = {}

    def __getitem__(self, name):
        return self.collections.setdefault(name, Collection())


class BackfillRouteMeetingPointsTests(unittest.IsolatedAsyncioTestCase):
    async def test_backfill_adds_missing_relations_without_touching_existing_ones(self):
        database = Database()
        repository = MongoMapRepository(database)
        await ImportKmz(KmzReaderImpl(), repository).execute(make_kmz())

        routes = database["routes"].documents
        existing = routes[0]["meeting_point_ids"]
        for route in routes:
            route.pop("meeting_point_ids", None)
        routes.append({
            "_id": ObjectId(),
            "import_id": routes[0]["import_id"],
            "name": "541 RUTA SIN VARIANTE",
            "code": "541",
            "folder_path": ["RUTAS AL COLEGIO EN BICI", "MM02", "RUTAS"],
            "geometry": {
                "type": "LineString",
                "coordinates": [[-74.1, 4.6], [-74.2, 4.7]],
            },
            "endpoint_status": "unresolved",
        })

        counts = backfill(database)

        self.assertEqual(counts["updated"], 2)
        self.assertEqual(counts["skipped_no_variant"], 1)
        self.assertEqual(len(routes[0]["meeting_point_ids"]), 1)
        self.assertEqual(len(routes[1]["meeting_point_ids"]), 1)
        self.assertNotIn("meeting_point_ids", routes[2])
        self.assertEqual(routes[0]["meeting_point_ids"], existing)
        self.assertEqual(database["routes"].updates, 2)

    async def test_dry_run_does_not_write(self):
        database = Database()
        repository = MongoMapRepository(database)
        await ImportKmz(KmzReaderImpl(), repository).execute(make_kmz())
        routes = database["routes"].documents
        for route in routes:
            route.pop("meeting_point_ids", None)

        counts = backfill(database, dry_run=True)

        self.assertEqual(counts["updated"], 2)
        self.assertEqual(database["routes"].updates, 0)
        self.assertNotIn("meeting_point_ids", routes[0])


if __name__ == "__main__":
    unittest.main()

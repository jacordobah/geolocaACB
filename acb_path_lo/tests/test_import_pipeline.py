from io import BytesIO
import unittest
from zipfile import ZipFile

from pymongo.errors import PyMongoError

from acb_path_lo.application.use_cases.import_kmz import ImportKmz
from acb_path_lo.domain.model.route import Route
from acb_path_lo.domain.model.route_endpoints import EndpointMatch, RouteEndpoints
from acb_path_lo.infrastructure.kmz.kmz_reader_impl import KmzReaderImpl
from acb_path_lo.infrastructure.persistence.mongo_map_repository import (
    MongoMapRepository,
)
from acb_path_lo.main import app


class FakeCollection:
    def __init__(self):
        self.documents = []
        self.indexes = []
        self.fail_insert = False

    def create_index(self, keys):
        self.indexes.append(keys)

    def insert_many(self, documents):
        if self.fail_insert:
            raise PyMongoError("insert failed")
        self.documents.extend(documents)

    def delete_many(self, query):
        self.documents = [
            document
            for document in self.documents
            if document["import_id"] != query["import_id"]
        ]

    def find(self, query):
        if not query:
            return list(self.documents)
        return [
            document
            for document in self.documents
            if all(
                document.get(key) == value
                for key, value in query.items()
            )
        ]


class FakeDatabase:
    def __init__(self):
        self.collections = {}

    def __getitem__(self, name):
        return self.collections.setdefault(name, FakeCollection())


def make_kmz() -> bytes:
    archive = BytesIO()
    with ZipFile(archive, "w") as kmz:
        kmz.writestr(
            "routes.kml",
            """<?xml version="1.0" encoding="UTF-8"?>
            <kml xmlns="http://www.opengis.net/kml/2.2">
              <Document>
                <Folder>
                  <name>RUTAS AL COLEGIO EN BICI</name>
                  <Folder>
                    <name>MM01 - COLEGIO DEMO</name>
                    <Folder>
                      <name>RUTAS</name>
                      <Folder>
                        <name>MM01 - COLEGIO DEMO</name>
                        <Placemark>
                          <name>541 RUTA DEMO</name>
                          <LineString>
                            <coordinates>-74.1,4.6,0 -74.2,4.7,0</coordinates>
                          </LineString>
                        </Placemark>
                        <Placemark>
                          <name>542 RUTA DEMO</name>
                          <LineString>
                            <coordinates>-74.2,4.7,0 -74.1,4.6,0</coordinates>
                          </LineString>
                        </Placemark>
                        <Folder>
                          <name>PUNTOS DE ENCUENTRO</name>
                          <Folder>
                            <name>PUNTOS DE ENCUENTRO 541</name>
                            <Placemark>
                              <name>PE01</name>
                              <Point><coordinates>-74.1,4.6,0</coordinates></Point>
                            </Placemark>
                          </Folder>
                          <Folder>
                            <name>PUNTOS DE ENCUENTRO 542</name>
                            <Placemark>
                              <name>PE01</name>
                              <Point><coordinates>-74.1002,4.5998,0</coordinates></Point>
                            </Placemark>
                          </Folder>
                        </Folder>
                        <Placemark>
                          <name>SAN FRANCISCO DE ASIS</name>
                          <description>Entrada principal</description>
                          <Point><coordinates>-74.2,4.7,0</coordinates></Point>
                        </Placemark>
                        <Placemark>
                          <name>TC01</name>
                          <Point><coordinates>-74.15,4.65,0</coordinates></Point>
                        </Placemark>
                        <Placemark>
                          <name>PR01</name>
                          <Point><coordinates>-74.15,4.65,0</coordinates></Point>
                        </Placemark>
                        <Placemark>
                          <name>12345678</name>
                          <Point><coordinates>-74.15,4.65,0</coordinates></Point>
                        </Placemark>
                      </Folder>
                    </Folder>
                  </Folder>
                </Folder>
              </Document>
            </kml>""",
        )
        kmz.writestr(
            "extra.kml",
            """<kml xmlns="http://www.opengis.net/kml/2.2">
              <Placemark>
                <name>Ruta sur</name>
                <LineString>
                  <coordinates>-75.1,5.6 -75.2,5.7</coordinates>
                </LineString>
              </Placemark>
            </kml>""",
        )
    return archive.getvalue()


class ImportPipelineTests(unittest.IsolatedAsyncioTestCase):
    def test_kmz_upload_route_is_registered(self):
        paths = app.openapi()["paths"]
        self.assertIn("/api/v1/imports/kmz", paths)
        self.assertIn("post", paths["/api/v1/imports/kmz"])

    async def test_import_saves_routes_points_and_resolved_endpoints(self):
        database = FakeDatabase()
        repository = MongoMapRepository(database)
        use_case = ImportKmz(KmzReaderImpl(), repository)

        result = await use_case.execute(make_kmz())

        routes = database["routes"].documents
        points = database["meeting_points"].documents
        self.assertEqual(result["saved_routes"], 3)
        self.assertEqual(result["saved_points"], 3)
        self.assertEqual(
            result["endpoints"],
            {"resolved": 2, "unresolved": 0, "not_applicable": 1},
        )
        self.assertEqual(len(routes), 3)
        self.assertEqual(len(points), 3)
        self.assertEqual(
            routes[0]["geometry"],
            {
                "type": "LineString",
                "coordinates": [[-74.1, 4.6], [-74.2, 4.7]],
            },
        )
        self.assertEqual(
            routes[1]["geometry"]["coordinates"],
            [[-74.2, 4.7], [-74.1, 4.6]],
        )
        self.assertEqual(
            points[2]["location"],
            {"type": "Point", "coordinates": [-74.2, 4.7]},
        )
        self.assertEqual(points[2]["description"], "Entrada principal")
        self.assertEqual(points[2]["kind"], "school")
        self.assertEqual(points[0]["kind"], "meeting_point")
        self.assertEqual(
            points[0]["folder_path"][-2:],
            ["PUNTOS DE ENCUENTRO", "PUNTOS DE ENCUENTRO 541"],
        )
        self.assertEqual(routes[0]["code"], "541")
        self.assertEqual(routes[0]["endpoint_status"], "resolved")
        self.assertEqual(routes[0]["start_meeting_point_id"], points[0]["_id"])
        self.assertEqual(routes[0]["end_meeting_point_id"], points[2]["_id"])
        self.assertEqual(routes[0]["first_meeting_point_id"], points[0]["_id"])
        self.assertEqual(routes[0]["meeting_point_ids"], [points[0]["_id"]])
        self.assertEqual(routes[1]["start_meeting_point_id"], points[2]["_id"])
        self.assertEqual(routes[1]["end_meeting_point_id"], points[1]["_id"])
        self.assertEqual(routes[1]["meeting_point_ids"], [points[1]["_id"]])
        self.assertNotIn("first_meeting_point_id", routes[1])
        self.assertEqual(routes[2]["endpoint_status"], "not_applicable")
        self.assertNotIn("start_meeting_point_id", routes[2])
        self.assertEqual(routes[2]["meeting_point_ids"], [])
        self.assertTrue(
            all(
                document["import_id"] == result["import_id"]
                for document in routes + points
            )
        )

    async def test_import_rolls_back_points_when_route_insert_fails(self):
        database = FakeDatabase()
        repository = MongoMapRepository(database)
        database["routes"].fail_insert = True

        with self.assertRaises(PyMongoError):
            await ImportKmz(KmzReaderImpl(), repository).execute(make_kmz())

        self.assertEqual(database["meeting_points"].documents, [])
        self.assertEqual(database["routes"].documents, [])

    async def test_save_import_rejects_endpoint_resolution_order_mismatch(self):
        database = FakeDatabase()
        repository = MongoMapRepository(database)
        route = Route(
            "541 RUTA DEMO", [(-74.1, 4.6), (-74.2, 4.7)],
            code="541", folder_path=(),
        )
        endpoints = [
            RouteEndpoints(route_index=1, start=None, end=None, status="unresolved"),
        ]

        with self.assertRaises(ValueError):
            await repository.save_import("test-import", [route], [], endpoints, [])

        self.assertEqual(database["meeting_points"].documents, [])
        self.assertEqual(database["routes"].documents, [])

    async def test_save_import_rejects_out_of_range_endpoint_index(self):
        database = FakeDatabase()
        repository = MongoMapRepository(database)
        route = Route(
            "541 RUTA DEMO", [(-74.1, 4.6), (-74.2, 4.7)],
            code="541", folder_path=(),
        )
        match = EndpointMatch(
            point_index=5, distance_m=1.0, source="folder",
            confidence="high", matched_vertex_index=0,
        )
        endpoints = [
            RouteEndpoints(route_index=0, start=match, end=None, status="resolved"),
        ]

        with self.assertRaises(ValueError):
            await repository.save_import("test-import", [route], [], endpoints, [])

        self.assertEqual(database["meeting_points"].documents, [])
        self.assertEqual(database["routes"].documents, [])

    async def test_list_routes_and_meeting_points(self):
        database = FakeDatabase()
        repository = MongoMapRepository(database)
        await ImportKmz(KmzReaderImpl(), repository).execute(make_kmz())

        routes = await repository.list_routes()
        points = await repository.list_meeting_points()

        self.assertEqual(len(routes), 3)
        self.assertEqual(len(points), 3)
        self.assertEqual(routes[0]["name"], "541 RUTA DEMO")
        self.assertEqual(points[0]["name"], "PE01")
        self.assertIsInstance(routes[0]["start_meeting_point_id"], str)
        self.assertIsInstance(routes[0]["first_meeting_point_id"], str)
        self.assertIsInstance(routes[0]["meeting_point_ids"], list)
        self.assertTrue(all(
            isinstance(reference, str)
            for reference in routes[0]["meeting_point_ids"]
        ))

        import_id = routes[0]["import_id"]
        self.assertEqual(len(await repository.list_routes(import_id)), 3)
        self.assertEqual(len(await repository.list_meeting_points(import_id)), 3)
        self.assertEqual(len(await repository.list_routes("missing")), 0)


if __name__ == "__main__":
    unittest.main()

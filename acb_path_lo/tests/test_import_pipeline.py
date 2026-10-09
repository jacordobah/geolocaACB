from io import BytesIO
import unittest
from zipfile import ZipFile

from acb_path_lo.application.use_cases.import_kmz import ImportKmz
from acb_path_lo.infrastructure.kmz.kmz_reader_impl import KmzReaderImpl
from acb_path_lo.infrastructure.persistence.mongo_map_repository import (
    MongoMapRepository,
)
from acb_path_lo.main import app


class FakeCollection:
    def __init__(self):
        self.documents = []
        self.indexes = []

    def create_index(self, keys):
        self.indexes.append(keys)

    def insert_many(self, documents):
        self.documents.extend(documents)

    def delete_many(self, query):
        self.documents = [
            document
            for document in self.documents
            if document["import_id"] != query["import_id"]
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
                <Placemark>
                  <name>Ruta norte</name>
                  <LineString>
                    <coordinates>-74.1,4.6,0 -74.2,4.7,0</coordinates>
                  </LineString>
                </Placemark>
                <Placemark>
                  <name>Punto de encuentro</name>
                  <description>Entrada principal</description>
                  <Point><coordinates>-74.3,4.8,0</coordinates></Point>
                </Placemark>
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
        route_paths = {route.path for route in app.routes}
        self.assertIn("/api/v1/imports/kmz", route_paths)

    async def test_import_saves_every_route_and_meeting_point(self):
        database = FakeDatabase()
        repository = MongoMapRepository(database)
        use_case = ImportKmz(KmzReaderImpl(), repository)

        result = await use_case.execute(make_kmz())

        routes = database["routes"].documents
        points = database["meeting_points"].documents
        self.assertEqual(result["saved_routes"], 2)
        self.assertEqual(result["saved_points"], 1)
        self.assertEqual(len(routes), 2)
        self.assertEqual(len(points), 1)
        self.assertEqual(
            routes[0]["geometry"],
            {
                "type": "LineString",
                "coordinates": [[-74.1, 4.6], [-74.2, 4.7]],
            },
        )
        self.assertEqual(
            routes[1]["geometry"]["coordinates"],
            [[-75.1, 5.6], [-75.2, 5.7]],
        )
        self.assertEqual(
            points[0]["location"],
            {"type": "Point", "coordinates": [-74.3, 4.8]},
        )
        self.assertEqual(points[0]["description"], "Entrada principal")
        self.assertTrue(
            all(
                document["import_id"] == result["import_id"]
                for document in routes + points
            )
        )


if __name__ == "__main__":
    unittest.main()

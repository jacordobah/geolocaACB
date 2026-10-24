import math
import unittest
from unittest.mock import patch

from bson import ObjectId
from pymongo.errors import ConnectionFailure

from acb_path_lo.domain.errors import (
    AssociationConflict, InvalidGeographicData, InvalidIdentifier,
    MeetingPointNotFound, RouteEndpointsNotConfigured, RouteNotFound,
)
from acb_path_lo.domain.model.endpoint_policy import EndpointPolicy
from acb_path_lo.domain.model.meeting_point import MeetingPoint
from acb_path_lo.domain.model.point_kind import PointKind
from acb_path_lo.domain.model.route import Route
from acb_path_lo.domain.services.route_endpoints import resolve_route_endpoints
from acb_path_lo.infrastructure.config.endpoints import (
    DEFAULT_PE_TOLERANCE_M, DEFAULT_SCHOOL_TOLERANCE_M, load_endpoint_policy,
)
from acb_path_lo.infrastructure.persistence.mongo_map_repository import (
    MongoMapRepository,
)
from acb_path_lo.main import app
from acb_path_lo.tests.test_first_meeting_point import Database, asgi_get

BRANCH = ("ZONA 5", "JORNADA MAÑANA", "RUTAS AL COLEGIO EN BICI")
SCHOOL = BRANCH + ("MM01 - COLEGIO DEMO",)
ROUTE_FOLDER = SCHOOL + ("RUTAS", "MM01 - COLEGIO DEMO")
PE_FOLDER_541 = ROUTE_FOLDER + ("PUNTOS DE ENCUENTRO", "PUNTOS DE ENCUENTRO 541")
PE_FOLDER_542 = ROUTE_FOLDER + ("PUNTOS DE ENCUENTRO", "PUNTOS DE ENCUENTRO 542")
PE_FOLDER_542_B = ROUTE_FOLDER + ("PUNTOS DE ENCUENTRO", "PUNTOS DE ENCUENTRO 542 B")
CE_FOLDER = SCHOOL + ("CE RUTA DE CONFIANZA", "OPCION 1")


def point(name, longitude, latitude, kind, folder_path):
    return MeetingPoint(
        name=name, longitude=longitude, latitude=latitude,
        kind=kind, folder_path=folder_path,
    )


class EndpointResolutionTests(unittest.TestCase):
    def test_541_starts_at_scoped_meeting_point_and_ends_at_school(self):
        route = Route(
            "541 RUTA DEMO", [(-74.1, 4.6), (-74.15, 4.65), (-74.2, 4.7)],
            code="541", folder_path=ROUTE_FOLDER,
        )
        points = [
            point("PE01", -74.1001, 4.6001, PointKind.MEETING_POINT, PE_FOLDER_541),
            point("PE01", -74.1, 4.6, PointKind.MEETING_POINT, PE_FOLDER_542),
            point("COLEGIO DEMO SEDE A", -74.2, 4.7, PointKind.SCHOOL, ROUTE_FOLDER),
        ]

        result = resolve_route_endpoints([route], points)[0]

        self.assertEqual(result.status, "resolved")
        self.assertEqual(points[result.start.point_index].folder_path, PE_FOLDER_541)
        self.assertEqual(result.start.source, "folder")
        self.assertEqual(result.start.confidence, "high")
        self.assertEqual(points[result.end.point_index].name, "COLEGIO DEMO SEDE A")

    def test_542_starts_at_school_and_ends_at_scoped_meeting_point(self):
        route = Route(
            "542 RUTA DEMO", [(-74.2, 4.7), (-74.1, 4.6)],
            code="542", folder_path=ROUTE_FOLDER,
        )
        points = [
            point("PE01", -74.1, 4.6, PointKind.MEETING_POINT, PE_FOLDER_541),
            point("PE01", -74.1001, 4.6001, PointKind.MEETING_POINT, PE_FOLDER_542),
            point("COLEGIO DEMO SEDE A", -74.2, 4.7, PointKind.SCHOOL, ROUTE_FOLDER),
        ]

        result = resolve_route_endpoints([route], points)[0]

        self.assertEqual(result.status, "resolved")
        self.assertEqual(points[result.start.point_index].name, "COLEGIO DEMO SEDE A")
        self.assertEqual(points[result.end.point_index].folder_path, PE_FOLDER_542)

    def test_541_uses_the_lowest_numbered_meeting_point(self):
        route = Route(
            "541 RUTA DEMO", [(-74.1, 4.6), (-74.2, 4.7)],
            code="541", folder_path=ROUTE_FOLDER,
        )
        points = [
            point("PE02", -74.1, 4.6, PointKind.MEETING_POINT, PE_FOLDER_541),
            point("PE01", -74.1001, 4.6001, PointKind.MEETING_POINT, PE_FOLDER_541),
            point("COLEGIO DEMO SEDE A", -74.2, 4.7, PointKind.SCHOOL, ROUTE_FOLDER),
        ]

        result = resolve_route_endpoints([route], points)[0]

        self.assertEqual(points[result.start.point_index].name, "PE01")
        self.assertEqual(result.start.confidence, "high")

    def test_541_prefers_pe00_when_present(self):
        route = Route(
            "541 RUTA DEMO", [(-74.1, 4.6), (-74.2, 4.7)],
            code="541", folder_path=ROUTE_FOLDER,
        )
        points = [
            point("PE01", -74.1, 4.6, PointKind.MEETING_POINT, PE_FOLDER_541),
            point("PE00", -74.1001, 4.6001, PointKind.MEETING_POINT, PE_FOLDER_541),
            point("COLEGIO DEMO SEDE A", -74.2, 4.7, PointKind.SCHOOL, ROUTE_FOLDER),
        ]

        result = resolve_route_endpoints([route], points)[0]

        self.assertEqual(points[result.start.point_index].name, "PE00")

    def test_542_uses_the_highest_numbered_meeting_point(self):
        route = Route(
            "542 RUTA DEMO", [(-74.2, 4.7), (-74.1, 4.6)],
            code="542", folder_path=ROUTE_FOLDER,
        )
        points = [
            point("PE12", -74.1, 4.6, PointKind.MEETING_POINT, PE_FOLDER_542),
            point("PE13", -74.1001, 4.6001, PointKind.MEETING_POINT, PE_FOLDER_542),
            point("COLEGIO DEMO SEDE A", -74.2, 4.7, PointKind.SCHOOL, ROUTE_FOLDER),
        ]

        result = resolve_route_endpoints([route], points)[0]

        self.assertEqual(points[result.end.point_index].name, "PE13")
        self.assertEqual(result.end.confidence, "high")

    def test_542_variant_with_the_nearest_highest_numbered_meeting_point_wins(self):
        route = Route(
            "542 RUTA DEMO", [(-74.2, 4.7), (-74.1, 4.6)],
            code="542", folder_path=ROUTE_FOLDER,
        )
        points = [
            point("PE07", -74.1001, 4.6001, PointKind.MEETING_POINT, PE_FOLDER_542),
            point("PE04", -74.1, 4.6, PointKind.MEETING_POINT, PE_FOLDER_542_B),
            point("COLEGIO DEMO SEDE A", -74.2, 4.7, PointKind.SCHOOL, ROUTE_FOLDER),
        ]

        result = resolve_route_endpoints([route], points)[0]

        self.assertEqual(points[result.end.point_index].name, "PE04")
        self.assertEqual(points[result.end.point_index].folder_path, PE_FOLDER_542_B)

    def test_unnumbered_meeting_point_falls_back_to_geometry(self):
        route = Route(
            "541 RUTA DEMO", [(-74.1, 4.6), (-74.2, 4.7)],
            code="541", folder_path=ROUTE_FOLDER,
        )
        points = [
            point("PUNTO DE ENCUENTRO", -74.1, 4.6, PointKind.MEETING_POINT, PE_FOLDER_541),
            point("COLEGIO DEMO SEDE A", -74.2, 4.7, PointKind.SCHOOL, ROUTE_FOLDER),
        ]

        result = resolve_route_endpoints([route], points)[0]

        self.assertEqual(result.status, "resolved")
        self.assertEqual(points[result.start.point_index].name, "PUNTO DE ENCUENTRO")

    def test_roles_follow_route_code_when_drawing_direction_is_reversed(self):
        routes = [
            Route(
                "541 RUTA DEMO", [(-74.2, 4.7), (-74.1, 4.6)],
                code="541", folder_path=ROUTE_FOLDER,
            ),
            Route(
                "542 RUTA DEMO", [(-74.1, 4.6), (-74.2, 4.7)],
                code="542", folder_path=ROUTE_FOLDER,
            ),
        ]
        points = [
            point("PE01", -74.1001, 4.6001, PointKind.MEETING_POINT, PE_FOLDER_541),
            point("PE01", -74.1, 4.6, PointKind.MEETING_POINT, PE_FOLDER_542),
            point("COLEGIO DEMO SEDE A", -74.2, 4.7, PointKind.SCHOOL, ROUTE_FOLDER),
        ]

        results = resolve_route_endpoints(routes, points)

        self.assertEqual(results[0].status, "resolved")
        self.assertEqual(points[results[0].start.point_index].folder_path, PE_FOLDER_541)
        self.assertEqual(points[results[0].end.point_index].name, "COLEGIO DEMO SEDE A")
        self.assertEqual(results[1].status, "resolved")
        self.assertEqual(points[results[1].start.point_index].name, "COLEGIO DEMO SEDE A")
        self.assertEqual(points[results[1].end.point_index].folder_path, PE_FOLDER_542)

    def test_ruta_singular_folder_is_a_school_route(self):
        folder = SCHOOL + ("RUTA", "MM03 - COLEGIO DEMO")
        route = Route(
            "541 RUTA DEMO", [(-74.1, 4.6), (-74.2, 4.7)],
            code="541", folder_path=folder,
        )
        points = [
            point("PE01", -74.1, 4.6, PointKind.MEETING_POINT, PE_FOLDER_541),
            point("COLEGIO DEMO SEDE A", -74.2, 4.7, PointKind.SCHOOL, folder),
        ]

        result = resolve_route_endpoints([route], points)[0]

        self.assertEqual(result.status, "resolved")
        self.assertEqual(points[result.start.point_index].name, "PE01")
        self.assertEqual(points[result.end.point_index].name, "COLEGIO DEMO SEDE A")

    def test_explicit_points_win_inside_the_route_folder(self):
        route = Route(
            "541 RUTA DEMO", [(-74.1, 4.6), (-74.2, 4.7)],
            code="541", folder_path=ROUTE_FOLDER,
        )
        points = [
            point("PUNTO DE INICIO", -74.1001, 4.6001, PointKind.EXPLICIT_START, ROUTE_FOLDER),
            point("PUNTO DE FIN", -74.2001, 4.7001, PointKind.EXPLICIT_END, ROUTE_FOLDER),
            point("PE02", -74.1, 4.6, PointKind.MEETING_POINT, PE_FOLDER_541),
            point("PE01", -74.1, 4.6, PointKind.MEETING_POINT, PE_FOLDER_541),
            point("COLEGIO DEMO SEDE A", -74.2, 4.7, PointKind.SCHOOL, ROUTE_FOLDER),
        ]

        result = resolve_route_endpoints([route], points)[0]

        self.assertEqual(result.status, "resolved")
        self.assertEqual(result.start.source, "explicit")
        self.assertEqual(result.end.source, "explicit")
        self.assertEqual((result.start.confidence, result.end.confidence), ("high", "high"))

    def test_routes_outside_the_school_branch_are_not_applicable(self):
        routes = [
            Route("541", [(-74.1, 4.6), (-74.2, 4.7)], code="541", folder_path=CE_FOLDER),
            Route("RUTA CENTRO", [(-74.1, 4.6), (-74.2, 4.7)], folder_path=ROUTE_FOLDER),
        ]

        results = resolve_route_endpoints(routes, [])

        self.assertEqual([result.status for result in results],
                         ["not_applicable", "not_applicable"])
        self.assertTrue(all(result.start is None for result in results))

    def test_missing_candidates_are_reported_as_unresolved(self):
        route = Route(
            "541 RUTA DEMO", [(-74.1, 4.6), (-74.2, 4.7)],
            code="541", folder_path=ROUTE_FOLDER,
        )
        points = [
            point("COLEGIO DEMO SEDE A", -74.2, 4.7, PointKind.SCHOOL, ROUTE_FOLDER),
        ]

        result = resolve_route_endpoints([route], points)[0]

        self.assertEqual(result.status, "unresolved")
        self.assertEqual(result.reason, "start_not_found")
        self.assertIsNone(result.start)
        self.assertIsNotNone(result.end)

    def test_numbered_meeting_point_beyond_tolerance_is_selected_with_low_confidence(self):
        route = Route(
            "541 RUTA DEMO", [(-74.1, 4.6), (-74.2, 4.7)],
            code="541", folder_path=ROUTE_FOLDER,
        )
        points = [
            point("PE01", -74.11, 4.61, PointKind.MEETING_POINT, PE_FOLDER_541),
            point("COLEGIO DEMO SEDE A", -74.2, 4.7, PointKind.SCHOOL, ROUTE_FOLDER),
        ]

        result = resolve_route_endpoints([route], points)[0]

        self.assertEqual(result.status, "resolved")
        self.assertEqual(result.start.confidence, "low")
        self.assertGreater(result.start.distance_m, DEFAULT_PE_TOLERANCE_M)

    def test_school_beyond_tolerance_is_not_matched(self):
        route = Route(
            "541 RUTA DEMO", [(-74.1, 4.6), (-74.2, 4.7)],
            code="541", folder_path=ROUTE_FOLDER,
        )
        points = [
            point("PE01", -74.1, 4.6, PointKind.MEETING_POINT, PE_FOLDER_541),
            point("COLEGIO DEMO SEDE A", -74.21, 4.71, PointKind.SCHOOL, ROUTE_FOLDER),
        ]

        result = resolve_route_endpoints([route], points)[0]

        self.assertEqual(result.status, "unresolved")
        self.assertEqual(result.reason, "end_not_found")

    def test_542_uses_highest_numbered_meeting_point_even_if_lower_is_closer(self):
        route = Route(
            "542 RUTA DEMO", [(-74.2, 4.7), (-74.1, 4.6)],
            code="542", folder_path=ROUTE_FOLDER,
        )
        points = [
            point("PE12", -74.1, 4.6, PointKind.MEETING_POINT, PE_FOLDER_542),
            point("PE13", -74.1001, 4.6001, PointKind.MEETING_POINT, PE_FOLDER_542),
            point("COLEGIO DEMO SEDE A", -74.2, 4.7, PointKind.SCHOOL, ROUTE_FOLDER),
        ]

        result = resolve_route_endpoints([route], points)[0]

        self.assertEqual(points[result.end.point_index].name, "PE13")
        self.assertGreater(result.end.distance_m, 0)

    def test_541_keeps_school_as_end_when_a_numbered_pe_is_within_tolerance(self):
        route = Route(
            "541 RUTA DEMO", [(-74.1, 4.6), (-74.2, 4.7)],
            code="541", folder_path=ROUTE_FOLDER,
        )
        points = [
            point("PE01", -74.1, 4.6, PointKind.MEETING_POINT, PE_FOLDER_541),
            point("PE10", -74.1995, 4.6995, PointKind.MEETING_POINT, PE_FOLDER_541),
            point("COLEGIO DEMO SEDE A", -74.2, 4.7, PointKind.SCHOOL, ROUTE_FOLDER),
        ]

        result = resolve_route_endpoints([route], points)[0]

        self.assertEqual(points[result.start.point_index].name, "PE01")
        self.assertEqual(points[result.end.point_index].name, "COLEGIO DEMO SEDE A")

    def test_542_keeps_school_as_start_when_pe01_is_closer(self):
        route = Route(
            "542 RUTA DEMO", [(-74.1, 4.6), (-74.2, 4.7)],
            code="542", folder_path=ROUTE_FOLDER,
        )
        points = [
            point("PE01", -74.1001, 4.6001, PointKind.MEETING_POINT, PE_FOLDER_542),
            point("PE04", -74.2, 4.7, PointKind.MEETING_POINT, PE_FOLDER_542),
            point("COLEGIO DEMO SEDE A", -74.10036, 4.60036, PointKind.SCHOOL, ROUTE_FOLDER),
        ]

        result = resolve_route_endpoints([route], points)[0]

        self.assertEqual(points[result.start.point_index].name, "COLEGIO DEMO SEDE A")
        self.assertEqual(points[result.end.point_index].name, "PE04")

    def test_542_high_numbered_pe_beyond_tolerance_is_low_confidence(self):
        route = Route(
            "542 RUTA DEMO", [(-74.2, 4.7), (-74.1, 4.6)],
            code="542", folder_path=ROUTE_FOLDER,
        )
        points = [
            point("PE13", -74.12, 4.62, PointKind.MEETING_POINT, PE_FOLDER_542),
            point("COLEGIO DEMO SEDE A", -74.2, 4.7, PointKind.SCHOOL, ROUTE_FOLDER),
        ]

        result = resolve_route_endpoints([route], points)[0]

        self.assertEqual(result.status, "resolved")
        self.assertEqual(points[result.end.point_index].name, "PE13")
        self.assertEqual(result.end.confidence, "low")
        self.assertGreater(result.end.distance_m, DEFAULT_PE_TOLERANCE_M)

    def test_invalid_tolerances_are_rejected(self):
        for value in (0, -1, math.nan, math.inf):
            with self.subTest(value=value), self.assertRaises(ValueError):
                resolve_route_endpoints([], [], pe_tolerance_m=value)


class EndpointPolicyTests(unittest.TestCase):
    def test_defaults_and_overrides(self):
        defaults = load_endpoint_policy({})
        self.assertEqual(
            (defaults.pe_tolerance_m, defaults.school_tolerance_m),
            (DEFAULT_PE_TOLERANCE_M, DEFAULT_SCHOOL_TOLERANCE_M),
        )
        configured = load_endpoint_policy({
            "ACB_ENDPOINT_PE_TOLERANCE_M": "25",
            "ACB_ENDPOINT_SCHOOL_TOLERANCE_M": "80",
        })
        self.assertEqual((configured.pe_tolerance_m, configured.school_tolerance_m), (25.0, 80.0))

    def test_invalid_values_fall_back_to_defaults(self):
        for value in ("0", "-5", "nan", "inf", "abc"):
            with self.subTest(value=value):
                policy = load_endpoint_policy({"ACB_ENDPOINT_PE_TOLERANCE_M": value})
                self.assertEqual(policy.pe_tolerance_m, DEFAULT_PE_TOLERANCE_M)

    def test_endpoint_policy_rejects_invalid_values(self):
        with self.assertRaises(ValueError):
            EndpointPolicy(pe_tolerance_m=0)


class EndpointRepositoryTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.database = Database()
        self.repository = MongoMapRepository(self.database)
        self.route_id, self.start_id, self.end_id = ObjectId(), ObjectId(), ObjectId()
        self.route = {
            "_id": self.route_id, "import_id": "test-import",
            "endpoint_status": "resolved",
            "start_meeting_point_id": self.start_id,
            "end_meeting_point_id": self.end_id,
            "endpoints": {
                "start": {"source": "folder", "distance_m": 1.5},
                "end": {"source": "folder", "distance_m": 8.25},
            },
        }
        self.start = {
            "_id": self.start_id, "import_id": "test-import", "name": "PE01",
            "location": {"type": "Point", "coordinates": [-74.1, 4.6]},
        }
        self.end = {
            "_id": self.end_id, "import_id": "test-import", "name": "COLEGIO DEMO",
            "location": {"type": "Point", "coordinates": [-74.2, 4.7]},
        }
        self.repository.routes.documents[self.route_id] = self.route
        self.repository.meeting_points.documents[self.start_id] = self.start
        self.repository.meeting_points.documents[self.end_id] = self.end

    async def test_get_route_endpoints_resolves_both_roles(self):
        result = await self.repository.get_route_endpoints(str(self.route_id))

        self.assertEqual(result.route_id, str(self.route_id))
        self.assertEqual(result.status, "resolved")
        self.assertEqual(result.start.name, "PE01")
        self.assertEqual((result.start.longitude, result.start.latitude), (-74.1, 4.6))
        self.assertEqual(result.start.distance_m, 1.5)
        self.assertEqual(result.end.name, "COLEGIO DEMO")

    async def test_missing_configuration(self):
        self.route.pop("start_meeting_point_id")
        self.route.pop("end_meeting_point_id")
        with self.assertRaises(RouteEndpointsNotConfigured):
            await self.repository.get_route_endpoints(str(self.route_id))

    async def test_cross_import_reference_is_rejected(self):
        self.start["import_id"] = "other-import"
        with self.assertRaises(AssociationConflict):
            await self.repository.get_route_endpoints(str(self.route_id))

    async def test_missing_point(self):
        self.repository.meeting_points.documents.pop(self.end_id)
        with self.assertRaises(MeetingPointNotFound):
            await self.repository.get_route_endpoints(str(self.route_id))

    async def test_malformed_location_is_rejected(self):
        self.start["location"] = None
        with self.assertRaises(InvalidGeographicData):
            await self.repository.get_route_endpoints(str(self.route_id))

    async def test_malformed_reference(self):
        self.route["end_meeting_point_id"] = "not-a-bson-id"
        with self.assertRaises(InvalidGeographicData):
            await self.repository.get_route_endpoints(str(self.route_id))

    async def test_missing_route_and_invalid_identifier(self):
        with self.assertRaises(RouteNotFound):
            await self.repository.get_route_endpoints(str(ObjectId()))
        with self.assertRaises(InvalidIdentifier):
            await self.repository.get_route_endpoints("bad")


class EndpointHttpTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.database = Database()
        self.repository = MongoMapRepository(self.database)
        self.route_id, self.start_id, self.end_id = ObjectId(), ObjectId(), ObjectId()
        self.route = {
            "_id": self.route_id, "import_id": "test-import",
            "endpoint_status": "resolved",
            "start_meeting_point_id": self.start_id,
            "end_meeting_point_id": self.end_id,
            "endpoints": {"start": {"source": "folder", "distance_m": 1.5}},
        }
        self.repository.routes.documents[self.route_id] = self.route
        self.repository.meeting_points.documents[self.start_id] = {
            "_id": self.start_id, "import_id": "test-import", "name": "PE01",
            "location": {"type": "Point", "coordinates": [-74.1, 4.6]},
        }
        self.repository.meeting_points.documents[self.end_id] = {
            "_id": self.end_id, "import_id": "test-import", "name": "COLEGIO DEMO",
            "location": {"type": "Point", "coordinates": [-74.2, 4.7]},
        }

    async def test_http_success(self):
        with patch.object(app.state, "map_repository", self.repository, create=True):
            status, body = await asgi_get(f"/api/v1/routes/{self.route_id}/endpoints")
        self.assertEqual(status, 200)
        self.assertEqual(body["status"], "resolved")
        self.assertEqual(body["start"]["meeting_point_id"], str(self.start_id))
        self.assertEqual(body["start"]["name"], "PE01")
        self.assertEqual(body["end"]["name"], "COLEGIO DEMO")

    async def test_http_error_mapping(self):
        cases = [
            (InvalidIdentifier, 422, "invalid_identifier"),
            (RouteNotFound, 404, "route_not_found"),
            (RouteEndpointsNotConfigured, 409, "endpoints_not_configured"),
            (MeetingPointNotFound, 409, "referenced_meeting_point_not_found"),
            (AssociationConflict, 409, "invalid_geographic_association"),
            (InvalidGeographicData, 409, "invalid_geographic_association"),
            (ConnectionFailure, 503, "geographic_store_unavailable"),
        ]
        for error, expected, code in cases:
            class FailingRepository:
                async def get_route_endpoints(self, route_id):
                    raise error("test failure")
            with self.subTest(error=error.__name__), patch.object(
                app.state, "map_repository", FailingRepository(), create=True
            ):
                status, body = await asgi_get(
                    f"/api/v1/routes/{self.route_id}/endpoints")
                self.assertEqual(status, expected)
                self.assertEqual(body["detail"]["code"], code)

    def test_openapi_contains_endpoints_route(self):
        paths = app.openapi()["paths"]
        self.assertEqual(
            set(paths["/api/v1/routes/{route_id}/endpoints"]), {"get"})


if __name__ == "__main__":
    unittest.main()

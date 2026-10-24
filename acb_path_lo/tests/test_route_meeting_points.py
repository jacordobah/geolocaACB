import unittest

from acb_path_lo.domain.model.meeting_point import MeetingPoint
from acb_path_lo.domain.model.point_kind import PointKind
from acb_path_lo.domain.model.route import Route
from acb_path_lo.domain.services.route_endpoints import resolve_route_endpoints
from acb_path_lo.domain.services.route_meeting_points import (
    resolve_route_meeting_points,
)
from acb_path_lo.tests.test_route_endpoints import (
    CE_FOLDER,
    PE_FOLDER_541,
    PE_FOLDER_542,
    PE_FOLDER_542_B,
    ROUTE_FOLDER,
    point,
)


class RouteMeetingPointsResolutionTests(unittest.TestCase):
    def test_541_returns_all_meeting_points_ordered_by_number(self):
        route = Route(
            "541 RUTA DEMO", [(-74.1, 4.6), (-74.2, 4.7)],
            code="541", folder_path=ROUTE_FOLDER,
        )
        points = [
            point("PE03", -74.1, 4.6, PointKind.MEETING_POINT, PE_FOLDER_541),
            point("PE01", -74.1, 4.6, PointKind.MEETING_POINT, PE_FOLDER_541),
            point("PE02", -74.1, 4.6, PointKind.MEETING_POINT, PE_FOLDER_541),
            point("COLEGIO DEMO", -74.2, 4.7, PointKind.SCHOOL, ROUTE_FOLDER),
        ]
        endpoints = resolve_route_endpoints([route], points)

        result = resolve_route_meeting_points([route], points, endpoints)[0]

        self.assertEqual(result.variant, "541")
        self.assertEqual(
            [points[index].name for index in result.point_indices],
            ["PE01", "PE02", "PE03"],
        )

    def test_542_sede_a_excludes_the_542_b_variant(self):
        route = Route(
            "542 RUTA SEDE A", [(-74.2, 4.7), (-74.1, 4.6)],
            code="542", folder_path=ROUTE_FOLDER,
        )
        points = [
            point("PE01", -74.1, 4.6, PointKind.MEETING_POINT, PE_FOLDER_542),
            point("PE02", -74.1, 4.6, PointKind.MEETING_POINT, PE_FOLDER_542),
            point("PE04", -74.12, 4.62, PointKind.MEETING_POINT, PE_FOLDER_542_B),
            point("COLEGIO DEMO", -74.2, 4.7, PointKind.SCHOOL, ROUTE_FOLDER),
        ]
        endpoints = resolve_route_endpoints([route], points)

        result = resolve_route_meeting_points([route], points, endpoints)[0]

        self.assertEqual(result.variant, "542")
        self.assertEqual(
            [points[index].name for index in result.point_indices],
            ["PE01", "PE02"],
        )

    def test_542_sede_b_uses_the_542_b_variant(self):
        route = Route(
            "542 RUTA SEDE B", [(-74.2, 4.7), (-74.1, 4.6)],
            code="542", folder_path=ROUTE_FOLDER,
        )
        points = [
            point("PE01", -74.12, 4.62, PointKind.MEETING_POINT, PE_FOLDER_542),
            point("PE04", -74.1, 4.6, PointKind.MEETING_POINT, PE_FOLDER_542_B),
            point("COLEGIO DEMO", -74.2, 4.7, PointKind.SCHOOL, ROUTE_FOLDER),
        ]
        endpoints = resolve_route_endpoints([route], points)

        result = resolve_route_meeting_points([route], points, endpoints)[0]

        self.assertEqual(result.variant, "542 B")
        self.assertEqual(
            [points[index].name for index in result.point_indices],
            ["PE04"],
        )

    def test_explicit_points_and_other_kinds_are_excluded(self):
        route = Route(
            "542 RUTA DEMO", [(-74.2, 4.7), (-74.1, 4.6)],
            code="542", folder_path=ROUTE_FOLDER,
        )
        points = [
            point("PE01", -74.1, 4.6, PointKind.MEETING_POINT, PE_FOLDER_542),
            point("INICIO", -74.2, 4.7, PointKind.EXPLICIT_START, ROUTE_FOLDER),
            point("COLEGIO DEMO", -74.2, 4.7, PointKind.SCHOOL, ROUTE_FOLDER),
        ]
        endpoints = resolve_route_endpoints([route], points)

        result = resolve_route_meeting_points([route], points, endpoints)[0]

        self.assertEqual(endpoints[0].end.source, "folder")
        self.assertEqual(
            [points[index].name for index in result.point_indices],
            ["PE01"],
        )

    def test_routes_without_a_pe_endpoint_or_variant_return_empty(self):
        routes = [
            Route(
                "541 RUTA DEMO", [(-74.1, 4.6), (-74.2, 4.7)],
                code="541", folder_path=ROUTE_FOLDER,
            ),
            Route(
                "541", [(-74.1, 4.6), (-74.2, 4.7)],
                code="541", folder_path=CE_FOLDER,
            ),
        ]
        points = [
            point("COLEGIO DEMO", -74.2, 4.7, PointKind.SCHOOL, ROUTE_FOLDER),
        ]
        endpoints = resolve_route_endpoints(routes, points)

        results = resolve_route_meeting_points(routes, points, endpoints)

        self.assertEqual([result.point_indices for result in results], [(), ()])
        self.assertEqual([result.variant for result in results], [None, None])

    def test_resolution_rejects_misaligned_endpoints(self):
        route = Route(
            "541 RUTA DEMO", [(-74.1, 4.6), (-74.2, 4.7)],
            code="541", folder_path=ROUTE_FOLDER,
        )
        endpoints = resolve_route_endpoints([route], [])

        with self.assertRaises(ValueError):
            resolve_route_meeting_points([route], [], [])
        with self.assertRaises(ValueError):
            resolve_route_meeting_points([route], [], endpoints + endpoints)


if __name__ == "__main__":
    unittest.main()

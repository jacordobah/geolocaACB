from ..ports.kmz_reader import KmzReader
from ...domain.model.endpoint_policy import EndpointPolicy
from ...domain.model.point_kind import STORED_POINT_KINDS
from ...domain.repositories.map_repository import MapRepository
from ...domain.services.import_scope import select_importable_school_data
from ...domain.services.route_endpoints import resolve_route_endpoints
from ...domain.services.route_meeting_points import resolve_route_meeting_points


class ImportKmz:

    def __init__(
        self,
        reader: KmzReader,
        repository: MapRepository,
        endpoint_policy: EndpointPolicy | None = None,
    ):
        self.reader = reader
        self.repository = repository
        self.endpoint_policy = endpoint_policy or EndpointPolicy()

    async def execute(self, content: bytes) -> dict:
        routes, points = self.reader.read(content)
        routes, points = select_importable_school_data(routes, points)
        points = [point for point in points if point.kind in STORED_POINT_KINDS]

        if not routes and not points:
            raise ValueError(
                "The file does not contain any valid routes or meeting points"
            )

        endpoints = resolve_route_endpoints(
            routes,
            points,
            pe_tolerance_m=self.endpoint_policy.pe_tolerance_m,
            school_tolerance_m=self.endpoint_policy.school_tolerance_m,
        )
        route_meeting_points = resolve_route_meeting_points(routes, points, endpoints)

        await self.repository.save_import(
            routes,
            points,
            endpoints,
            route_meeting_points,
        )

        return {
            "saved_routes": len(routes),
            "saved_points": len(points),
            "endpoints": {
                "resolved": sum(item.status == "resolved" for item in endpoints),
                "unresolved": sum(item.status == "unresolved" for item in endpoints),
                "not_applicable": sum(item.status == "not_applicable" for item in endpoints),
            },
        }

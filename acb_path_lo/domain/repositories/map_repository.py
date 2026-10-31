from typing import Protocol
from ..model.route import Route
from ..model.meeting_point import MeetingPoint
from ..model.first_meeting_point import FirstMeetingPoint
from ..model.route_endpoints import RouteEndpoints, RouteEndpointsView
from ..model.route_meeting_points import RouteMeetingPoints

class MapRepository(Protocol):
    async def get_first_meeting_point(self, route_id: str) -> FirstMeetingPoint:
        ...

    async def get_route(self, route_id: str) -> dict:
        ...

    async def get_route_endpoints(self, route_id: str) -> RouteEndpointsView:
        ...

    async def set_first_meeting_point(self, route_id: str, point_id: str) -> None:
        ...

    async def save_import(
        self,
        routes: list[Route],
        points: list[MeetingPoint],
        endpoints: list[RouteEndpoints],
        route_meeting_points: list[RouteMeetingPoints],
    ) -> None:
        ...

    async def list_routes(self) -> list[dict]:
        ...

    async def find_routes_by_codes(self, code: str, route_code: str) -> list[dict]:
        ...

    async def list_meeting_points(self) -> list[dict]:
        ...

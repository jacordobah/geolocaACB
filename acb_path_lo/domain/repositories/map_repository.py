from typing import Protocol
from ..model.route import Route
from ..model.meeting_point import MeetingPoint
from ..model.first_meeting_point import FirstMeetingPoint

class MapRepository(Protocol):
    async def get_first_meeting_point(self, route_id: str) -> FirstMeetingPoint:
        ...

    async def set_first_meeting_point(self, route_id: str, point_id: str) -> None:
        ...

    async def save_import(
        self,
        import_id: str,
        routes: list[Route],
        points: list[MeetingPoint],
    ) -> None:
        ...

from typing import Protocol
from ..model.route import Route
from ..model.meeting_point import MeetingPoint


class MapRepository(Protocol):
    async def save_import(
        self,
        import_id: str,
        routes: list[Route],
        points: list[MeetingPoint],
    ) -> None:
        ...
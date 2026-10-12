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

    async def list_routes(
        self,
        import_id: str | None = None,
    ) -> list[dict]:
        ...

    async def list_meeting_points(
        self,
        import_id: str | None = None,
    ) -> list[dict]:
        ...
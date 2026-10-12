from ...domain.repositories.map_repository import MapRepository


class SetFirstMeetingPoint:
    """Configure a previously verified association through local tooling."""

    def __init__(self, repository: MapRepository):
        self.repository = repository

    async def execute(self, route_id: str, point_id: str) -> None:
        await self.repository.set_first_meeting_point(route_id, point_id)

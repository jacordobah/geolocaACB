from ...domain.model.first_meeting_point import FirstMeetingPoint
from ...domain.repositories.map_repository import MapRepository


class GetFirstMeetingPoint:
    def __init__(self, repository: MapRepository):
        self.repository = repository

    async def execute(self, route_id: str) -> FirstMeetingPoint:
        return await self.repository.get_first_meeting_point(route_id)

from ...domain.repositories.map_repository import MapRepository


class ListMeetingPoints:
    def __init__(self, repository: MapRepository):
        self.repository = repository

    async def execute(self, import_id: str | None = None) -> list[dict]:
        return await self.repository.list_meeting_points(import_id)

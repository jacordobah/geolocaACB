from ...domain.repositories.map_repository import MapRepository


class GetRoute:
    def __init__(self, repository: MapRepository):
        self.repository = repository

    async def execute(self, route_id: str) -> dict:
        return await self.repository.get_route(route_id)

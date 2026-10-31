from ...domain.repositories.map_repository import MapRepository


class FindRoutes:
    def __init__(self, repository: MapRepository):
        self.repository = repository

    async def execute(self, code: str, route_code: str) -> list[dict]:
        return await self.repository.find_routes_by_codes(code, route_code)

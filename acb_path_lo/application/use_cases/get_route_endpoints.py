from ...domain.model.route_endpoints import RouteEndpointsView
from ...domain.repositories.map_repository import MapRepository


class GetRouteEndpoints:

    def __init__(self, repository: MapRepository):
        self.repository = repository

    async def execute(self, route_id: str) -> RouteEndpointsView:
        return await self.repository.get_route_endpoints(route_id)

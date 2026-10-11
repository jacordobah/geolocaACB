from ...domain.model.route_status import RouteStatus
from ...domain.repositories.route_status_repository import (
    RouteStatusRepository,
)

class SaveRouteStatus:
    def __init__(self, repository: RouteStatusRepository):
        self.repository = repository

    async def execute(self, route_status: RouteStatus) -> None:
        await self.repository.save_route_status(route_status)

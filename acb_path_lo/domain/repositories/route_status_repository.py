from typing import Protocol

from ..model.route_status import RouteStatus

class RouteStatusRepository(Protocol):
    async def save_route_status(
        self,
        route_status: RouteStatus,
    ) -> None:
        ...
import asyncio
import logging

from pymongo.database import Database
from pymongo.errors import PyMongoError

from ...domain.model.route_status import RouteStatus

logger = logging.getLogger(__name__)

class MongoRouteStatusRepository:
    def __init__(self, database: Database):
        self.route_status = database["route_status"]

        self.route_status.create_index([("route", 1), ("user_id", 1)])

    async def save_route_status(
        self,
        route_status: RouteStatus,
    ) -> None:
        route_status_documents = {
            "name": route_status.name,
            "route": route_status.route,
            "user_id": route_status.user_id,
            "status": route_status.status.value,
            "notified_at": route_status.notified_at,
        }

        await asyncio.to_thread(
            self._save_route_status_documents,
            route_status_documents,
        )

    def _save_route_status_documents(
        self,
        route_status_documents: dict,
    ) -> None:
        try:
            if route_status_documents:
                self.route_status.insert_one(route_status_documents)
        except PyMongoError:
            logger.exception("Could not save route statuses")
            raise
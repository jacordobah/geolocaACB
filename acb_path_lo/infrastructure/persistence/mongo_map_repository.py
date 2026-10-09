import asyncio
import logging

from pymongo.database import Database
from pymongo.errors import PyMongoError

from ...domain.model.meeting_point import MeetingPoint
from ...domain.model.route import Route

logger = logging.getLogger(__name__)


class MongoMapRepository:
    def __init__(self, database: Database):
        self.routes = database["routes"]
        self.meeting_points = database["meeting_points"]

        self.routes.create_index("import_id")
        self.routes.create_index([("geometry", "2dsphere")])
        self.meeting_points.create_index("import_id")
        self.meeting_points.create_index([("location", "2dsphere")])

    async def save_import(
        self,
        import_id: str,
        routes: list[Route],
        points: list[MeetingPoint],
    ) -> None:
        route_documents = [
            {
                "import_id": import_id,
                "name": route.name,
                "geometry": {
                    "type": "LineString",
                    "coordinates": [
                        [longitude, latitude]
                        for longitude, latitude in route.coordinates
                    ],
                },
            }
            for route in routes
        ]
        point_documents = [
            {
                "import_id": import_id,
                "name": point.name,
                "description": point.description,
                "location": {
                    "type": "Point",
                    "coordinates": [point.longitude, point.latitude],
                },
            }
            for point in points
        ]

        await asyncio.to_thread(
            self._save_documents,
            import_id,
            route_documents,
            point_documents,
        )

    def _save_documents(
        self,
        import_id: str,
        route_documents: list[dict],
        point_documents: list[dict],
    ) -> None:
        try:
            if route_documents:
                self.routes.insert_many(route_documents)
            if point_documents:
                self.meeting_points.insert_many(point_documents)
        except PyMongoError:
            for collection in (self.routes, self.meeting_points):
                try:
                    collection.delete_many({"import_id": import_id})
                except PyMongoError:
                    logger.exception(
                        "Could not roll back partial KMZ import %s",
                        import_id,
                    )
            raise

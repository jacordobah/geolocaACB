import asyncio
import logging
import math

from bson import ObjectId
from bson.errors import InvalidId

from pymongo.database import Database
from pymongo.errors import PyMongoError

from ...domain.model.meeting_point import MeetingPoint
from ...domain.model.route import Route
from ...domain.model.first_meeting_point import FirstMeetingPoint
from ...domain.errors import (
    AssociationConflict, FirstMeetingPointNotConfigured, InvalidGeographicData,
    InvalidIdentifier, MeetingPointNotFound, RouteNotFound,
)

logger = logging.getLogger(__name__)

class MongoMapRepository:
    @staticmethod
    def _object_id(value: str) -> ObjectId:
        try:
            if not isinstance(value, str):
                raise TypeError("Identifier must be a string")
            return ObjectId(value)
        except (InvalidId, TypeError) as error:
            raise InvalidIdentifier("Invalid geographic identifier") from error

    @staticmethod
    def _point_result(route_id: ObjectId, point: dict) -> FirstMeetingPoint:
        location = point.get("location", {})
        coordinates = location.get("coordinates")
        if (
            location.get("type") != "Point"
            or not isinstance(coordinates, (list, tuple))
            or len(coordinates) != 2
            or any(isinstance(v, bool) or not isinstance(v, (int, float))
                   or not math.isfinite(v) for v in coordinates)
            or not -180 <= coordinates[0] <= 180
            or not -90 <= coordinates[1] <= 90
            or not isinstance(point.get("name"), str)
        ):
            raise InvalidGeographicData("Invalid meeting point geometry")
        return FirstMeetingPoint(
            route_id=str(route_id), meeting_point_id=str(point["_id"]),
            name=point["name"], longitude=coordinates[0], latitude=coordinates[1],
        )

    async def get_first_meeting_point(self, route_id: str) -> FirstMeetingPoint:
        return await asyncio.to_thread(self._get_first_meeting_point, route_id)

    def _get_first_meeting_point(self, route_id: str) -> FirstMeetingPoint:
        identifier = self._object_id(route_id)
        route = self.routes.find_one({"_id": identifier})
        if route is None:
            raise RouteNotFound("Geographic route not found")
        reference = route.get("first_meeting_point_id")
        if reference is None:
            raise FirstMeetingPointNotConfigured("First meeting point is not configured")
        if not isinstance(reference, ObjectId):
            raise InvalidGeographicData("Invalid meeting point reference")
        point = self.meeting_points.find_one({"_id": reference})
        if point is None:
            raise MeetingPointNotFound("Referenced meeting point not found")
        if not route.get("import_id") or point.get("import_id") != route["import_id"]:
            raise AssociationConflict("Route and point belong to different imports")
        return self._point_result(identifier, point)

    async def set_first_meeting_point(self, route_id: str, point_id: str) -> None:
        await asyncio.to_thread(self._set_first_meeting_point, route_id, point_id)

    def _set_first_meeting_point(self, route_id: str, point_id: str) -> None:
        identifier, reference = self._object_id(route_id), self._object_id(point_id)
        route = self.routes.find_one({"_id": identifier})
        if route is None:
            raise RouteNotFound("Geographic route not found")
        point = self.meeting_points.find_one({"_id": reference})
        if point is None:
            raise MeetingPointNotFound("Meeting point not found")
        if not route.get("import_id") or point.get("import_id") != route["import_id"]:
            raise AssociationConflict("Route and point belong to different imports")
        self._point_result(identifier, point)
        # Standalone MongoDB: this CAS is atomic for the route document only.
        # Point existence/import membership was checked above, not transactionally.
        result = self.routes.update_one(
            {"_id": identifier, "import_id": route["import_id"], "$or": [
                {"first_meeting_point_id": {"$exists": False}},
                {"first_meeting_point_id": reference},
            ]},
            {"$set": {"first_meeting_point_id": reference}},
            upsert=False,
        )
        if result.matched_count != 1:
            raise AssociationConflict("Route changed or already has a different association")

    def __init__(self, database: Database, *, ensure_indexes: bool = True):
        self.routes = database["routes"]
        self.meeting_points = database["meeting_points"]

        if not ensure_indexes:
            return
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

    async def list_routes(self, import_id: str | None = None) -> list[dict]:
        return await asyncio.to_thread(
            self._list_documents,
            self.routes,
            import_id,
        )

    async def list_meeting_points(
        self, import_id: str | None = None
    ) -> list[dict]:
        return await asyncio.to_thread(
            self._list_documents,
            self.meeting_points,
            import_id,
        )

    @classmethod
    def _list_documents(
        cls,
        collection,
        import_id: str | None,
    ) -> list[dict]:
        query = {"import_id": import_id} if import_id else {}
        return [cls._serialize(document) for document in collection.find(query)]

    @staticmethod
    def _serialize(document: dict) -> dict:
        serialized = dict(document)
        if "_id" in serialized:
            serialized["_id"] = str(serialized["_id"])
        if isinstance(serialized.get("first_meeting_point_id"), ObjectId):
            serialized["first_meeting_point_id"] = str(serialized["first_meeting_point_id"])
        return serialized

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

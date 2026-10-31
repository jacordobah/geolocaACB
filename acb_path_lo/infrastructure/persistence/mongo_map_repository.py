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
from ...domain.model.route_endpoints import (
    EndpointMatch, RouteEndpoints, RouteEndpointPoint, RouteEndpointsView,
)
from ...domain.model.route_meeting_points import RouteMeetingPoints
from ...domain.errors import (
    AssociationConflict, FirstMeetingPointNotConfigured, InvalidGeographicData,
    InvalidIdentifier, MeetingPointNotFound, RouteEndpointsNotConfigured,
    RouteNotFound,
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
    def _point_data(point: dict) -> tuple[str, float, float]:
        location = point.get("location")
        if not isinstance(location, dict):
            raise InvalidGeographicData("Invalid meeting point geometry")
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
        return point["name"], coordinates[0], coordinates[1]

    @classmethod
    def _point_result(cls, route_id: ObjectId, point: dict) -> FirstMeetingPoint:
        name, longitude, latitude = cls._point_data(point)
        return FirstMeetingPoint(
            route_id=str(route_id), meeting_point_id=str(point["_id"]),
            name=name, longitude=longitude, latitude=latitude,
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
        return self._point_result(identifier, point)

    async def get_route_endpoints(self, route_id: str) -> RouteEndpointsView:
        return await asyncio.to_thread(self._get_route_endpoints, route_id)

    def _get_route_endpoints(self, route_id: str) -> RouteEndpointsView:
        identifier = self._object_id(route_id)
        route = self.routes.find_one({"_id": identifier})
        if route is None:
            raise RouteNotFound("Geographic route not found")
        references = {
            "start": route.get("start_meeting_point_id"),
            "end": route.get("end_meeting_point_id"),
        }
        if references["start"] is None and references["end"] is None:
            raise RouteEndpointsNotConfigured("Route endpoints are not configured")
        metadata = route.get("endpoints")
        if not isinstance(metadata, dict):
            metadata = {}
        resolved = {}
        for role, reference in references.items():
            if reference is None:
                continue
            if not isinstance(reference, ObjectId):
                raise InvalidGeographicData("Invalid route endpoint reference")
            point = self.meeting_points.find_one({"_id": reference})
            if point is None:
                raise MeetingPointNotFound("Referenced meeting point not found")
            name, longitude, latitude = self._point_data(point)
            role_metadata = metadata.get(role)
            if not isinstance(role_metadata, dict):
                role_metadata = {}
            resolved[role] = RouteEndpointPoint(
                meeting_point_id=str(point["_id"]), name=name,
                longitude=longitude, latitude=latitude,
                source=role_metadata.get("source"),
                distance_m=role_metadata.get("distance_m"),
            )
        return RouteEndpointsView(
            route_id=str(identifier),
            status=route.get("endpoint_status", "resolved"),
            start=resolved.get("start"),
            end=resolved.get("end"),
        )

    async def get_route(self, route_id: str) -> dict:
        return await asyncio.to_thread(self._get_route, route_id)

    def _get_route(self, route_id: str) -> dict:
        identifier = self._object_id(route_id)
        route = self.routes.find_one({"_id": identifier})
        if route is None:
            raise RouteNotFound("Geographic route not found")
        references = route.get("meeting_point_ids", [])
        if references is None:
            references = []
        if not isinstance(references, (list, tuple)):
            raise InvalidGeographicData("Invalid meeting point references")
        if any(not isinstance(reference, ObjectId) for reference in references):
            raise InvalidGeographicData("Invalid meeting point references")

        points = []
        if references:
            documents = self.meeting_points.find({"_id": {"$in": list(references)}})
            by_id = {document.get("_id"): document for document in documents}
            for reference in references:
                point = by_id.get(reference)
                if point is None:
                    raise MeetingPointNotFound("Referenced meeting point not found")
                self._point_data(point)
                points.append(self._serialize(point))

        result = self._serialize(route)
        result.pop("meeting_point_ids", None)
        result["meeting_points"] = points
        return result

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
        self._point_data(point)
        # Standalone MongoDB: this CAS is atomic for the route document only.
        # Point existence was checked above, not transactionally.
        result = self.routes.update_one(
            {"_id": identifier, "$or": [
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
        self.routes.create_index("meeting_point_ids")
        self.routes.create_index([("geometry", "2dsphere")])
        self.meeting_points.create_index([("location", "2dsphere")])

    async def save_import(
        self,
        routes: list[Route],
        points: list[MeetingPoint],
        endpoints: list[RouteEndpoints],
        route_meeting_points: list[RouteMeetingPoints],
    ) -> None:
        if len(endpoints) != len(routes):
            raise ValueError("Endpoint resolution must match the number of routes")
        if any(
            resolved.route_index != index
            for index, resolved in enumerate(endpoints)
        ):
            raise ValueError(
                "Endpoint resolution must follow the order of the routes"
            )
        if len(route_meeting_points) != len(routes):
            raise ValueError("Meeting point resolution must match the number of routes")
        if any(
            resolved.route_index != index
            for index, resolved in enumerate(route_meeting_points)
        ):
            raise ValueError(
                "Meeting point resolution must follow the order of the routes"
            )
        point_documents = [
            {
                "_id": ObjectId(),
                "name": point.name,
                "kind": point.kind.value,
                "description": point.description,
                "location": {
                    "type": "Point",
                    "coordinates": [point.longitude, point.latitude],
                },
            }
            for point in points
        ]
        route_documents = []
        for route, resolved, resolved_points in zip(
            routes, endpoints, route_meeting_points
        ):
            document = {
                "_id": ObjectId(),
                "name": route.name,
                "route_code": route.route_code,
                "code": route.code,
                "day_shift": route.day_shift,
                "endpoint_status": resolved.status,
                "geometry": {
                    "type": "LineString",
                    "coordinates": [
                        [longitude, latitude]
                        for longitude, latitude in route.coordinates
                    ],
                },
                "meeting_point_ids": self._meeting_point_documents(
                    resolved_points, point_documents,
                ),
            }
            if resolved.reason is not None:
                document["endpoint_reason"] = resolved.reason
            start_reference, start_metadata = self._endpoint_document(
                resolved.start, point_documents,
            )
            end_reference, end_metadata = self._endpoint_document(
                resolved.end, point_documents,
            )
            if start_reference is not None:
                document["start_meeting_point_id"] = start_reference
                if route.code == "541" and resolved.status == "resolved":
                    document["first_meeting_point_id"] = start_reference
            if end_reference is not None:
                document["end_meeting_point_id"] = end_reference
            if start_metadata is not None or end_metadata is not None:
                document["endpoints"] = {
                    role: metadata
                    for role, metadata in (
                        ("start", start_metadata), ("end", end_metadata),
                    )
                    if metadata is not None
                }
            route_documents.append(document)

        await asyncio.to_thread(
            self._save_documents,
            route_documents,
            point_documents,
        )

    @staticmethod
    def _meeting_point_documents(
        resolved: RouteMeetingPoints, point_documents: list[dict],
    ) -> list[ObjectId]:
        references = []
        for index in resolved.point_indices:
            if not 0 <= index < len(point_documents):
                raise ValueError("Meeting point index is out of range")
            document = point_documents[index]
            if document["kind"] != "meeting_point":
                raise ValueError("Resolved point is not a meeting point")
            references.append(document["_id"])
        return references

    @staticmethod
    def _endpoint_document(
        match: EndpointMatch | None, point_documents: list[dict],
    ) -> tuple[ObjectId | None, dict | None]:
        if match is None:
            return None, None
        if not 0 <= match.point_index < len(point_documents):
            raise ValueError("Endpoint point index is out of range")
        reference = point_documents[match.point_index]["_id"]
        metadata = {
            "source": match.source,
            "distance_m": match.distance_m,
            "confidence": match.confidence,
            "matched_vertex_index": match.matched_vertex_index,
        }
        return reference, metadata

    async def list_routes(self) -> list[dict]:
        return await asyncio.to_thread(self._list_documents, self.routes)

    async def find_routes_by_codes(self, code: str, route_code: str) -> list[dict]:
        return await asyncio.to_thread(
            self._find_routes_by_codes, code, route_code,
        )

    def _find_routes_by_codes(self, code: str, route_code: str) -> list[dict]:
        return [
            self._serialize(document)
            for document in self.routes.find({
                "code": code,
                "route_code": route_code,
            })
        ]

    async def list_meeting_points(self) -> list[dict]:
        return await asyncio.to_thread(self._list_documents, self.meeting_points)

    @classmethod
    def _list_documents(cls, collection) -> list[dict]:
        return [cls._serialize(document) for document in collection.find({})]

    @staticmethod
    def _serialize(document: dict) -> dict:
        serialized = dict(document)
        if "_id" in serialized:
            serialized["_id"] = str(serialized["_id"])
        for key in (
            "first_meeting_point_id",
            "start_meeting_point_id",
            "end_meeting_point_id",
        ):
            if isinstance(serialized.get(key), ObjectId):
                serialized[key] = str(serialized[key])
        references = serialized.get("meeting_point_ids")
        if isinstance(references, (list, tuple)):
            serialized["meeting_point_ids"] = [
                str(reference) if isinstance(reference, ObjectId) else reference
                for reference in references
            ]
        return serialized

    def _save_documents(
        self,
        route_documents: list[dict],
        point_documents: list[dict],
    ) -> None:
        try:
            if point_documents:
                self.meeting_points.insert_many(point_documents)
            if route_documents:
                self.routes.insert_many(route_documents)
        except PyMongoError:
            route_ids = [document["_id"] for document in route_documents]
            point_ids = [document["_id"] for document in point_documents]
            for collection, identifiers in (
                (self.routes, route_ids),
                (self.meeting_points, point_ids),
            ):
                try:
                    collection.delete_many({"_id": {"$in": identifiers}})
                except PyMongoError:
                    logger.exception(
                        "Could not roll back partial KMZ import",
                    )
            raise

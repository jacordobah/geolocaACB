"""Backfill meeting point associations for routes imported before this field."""
import argparse
import os

from bson import ObjectId
from pymongo import MongoClient

from ...domain.model.meeting_point import MeetingPoint
from ...domain.model.point_kind import PointKind
from ...domain.model.route import Route
from ...domain.model.route_endpoints import EndpointMatch, RouteEndpoints
from ...domain.services.route_meeting_points import resolve_route_meeting_points


def _route_from_document(document: dict) -> Route:
    geometry = document.get("geometry")
    coordinates = geometry.get("coordinates") if isinstance(geometry, dict) else None
    if not isinstance(coordinates, list) or not coordinates:
        raise ValueError("route_without_geometry")
    return Route(
        name=document.get("name", ""),
        coordinates=[(float(pair[0]), float(pair[1])) for pair in coordinates],
        code=document.get("code"),
        folder_path=tuple(document.get("folder_path", ())),
    )


def _point_from_document(document: dict) -> MeetingPoint:
    location = document.get("location")
    coordinates = location.get("coordinates") if isinstance(location, dict) else None
    if not isinstance(coordinates, list) or len(coordinates) != 2:
        raise ValueError("point_without_geometry")
    return MeetingPoint(
        name=document.get("name", ""),
        longitude=float(coordinates[0]),
        latitude=float(coordinates[1]),
        description=document.get("description"),
        kind=PointKind(document.get("kind", PointKind.OTHER.value)),
        folder_path=tuple(document.get("folder_path", ())),
    )


def _endpoint_match(
    reference,
    point_indices: dict[ObjectId, int],
    metadata,
) -> EndpointMatch | None:
    index = point_indices.get(reference) if isinstance(reference, ObjectId) else None
    if index is None:
        return None
    if not isinstance(metadata, dict):
        metadata = {}
    return EndpointMatch(
        point_index=index,
        distance_m=float(metadata.get("distance_m", 0.0)),
        source=str(metadata.get("source", "backfill")),
        confidence=str(metadata.get("confidence", "unknown")),
        matched_vertex_index=int(metadata.get("matched_vertex_index", 0)),
    )


def _resolve_references(document: dict, points: list[MeetingPoint], point_docs: list[dict]):
    point_indices = {point["_id"]: index for index, point in enumerate(point_docs)}
    code = document.get("code")
    endpoints = document.get("endpoints")
    endpoints = endpoints if isinstance(endpoints, dict) else {}
    start_metadata = endpoints.get("start")
    end_metadata = endpoints.get("end")

    if code == "541":
        for reference, metadata in (
            (document.get("start_meeting_point_id"), start_metadata),
            (document.get("first_meeting_point_id"), {}),
        ):
            match = _endpoint_match(reference, point_indices, metadata)
            if match is not None and points[match.point_index].kind is PointKind.MEETING_POINT:
                return match, None
        return None, None

    if code == "542":
        match = _endpoint_match(
            document.get("end_meeting_point_id"), point_indices, end_metadata,
        )
        if match is not None and points[match.point_index].kind is PointKind.MEETING_POINT:
            return None, match
    return None, None


def backfill(database, import_id: str | None = None, dry_run: bool = False) -> dict:
    route_query = {"import_id": import_id} if import_id else {}
    counts = {
        "updated": 0,
        "skipped_existing": 0,
        "skipped_no_variant": 0,
        "skipped_invalid": 0,
    }
    for document in database["routes"].find(route_query):
        if document.get("meeting_point_ids") is not None:
            counts["skipped_existing"] += 1
            continue
        if not document.get("import_id"):
            counts["skipped_invalid"] += 1
            continue
        try:
            route = _route_from_document(document)
            point_docs = list(database["meeting_points"].find({
                "import_id": document.get("import_id"),
            }))
            points = [_point_from_document(point) for point in point_docs]
            start, end = _resolve_references(document, points, point_docs)
            if start is None and end is None:
                counts["skipped_no_variant"] += 1
                continue
            resolved = resolve_route_meeting_points(
                [route],
                points,
                [RouteEndpoints(
                    route_index=0, start=start, end=end, status="resolved",
                )],
            )[0]
            if not resolved.point_indices:
                counts["skipped_no_variant"] += 1
                continue
            references = [point_docs[index]["_id"] for index in resolved.point_indices]
            if not dry_run:
                result = database["routes"].update_one(
                    {
                        "_id": document["_id"],
                        "meeting_point_ids": {"$exists": False},
                    },
                    {"$set": {"meeting_point_ids": references}},
                )
                if result.matched_count != 1:
                    counts["skipped_existing"] += 1
                    continue
            counts["updated"] += 1
        except (IndexError, KeyError, TypeError, ValueError):
            counts["skipped_invalid"] += 1
    return counts


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--import-id")
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--confirm", action="store_true")
    args = parser.parse_args()
    if not args.dry_run and not args.confirm:
        parser.error("Use --confirm or --dry-run")
    if os.getenv("ACB_TEST_PROJECT") != "acb-kmz-test-20261012":
        parser.error("Only the explicitly verified isolated test project is allowed")
    if (os.getenv("MONGO_URI") != "mongodb://acb_mongo_db:27017"
            or os.getenv("MONGO_DATABASE") != "geolocaacb"):
        parser.error("Unexpected local test database configuration")
    with MongoClient(os.environ["MONGO_URI"], serverSelectionTimeoutMS=5000) as client:
        client.admin.command("ping")
        counts = backfill(
            client[os.environ["MONGO_DATABASE"]],
            args.import_id,
            args.dry_run,
        )
    print(counts)


if __name__ == "__main__":
    main()

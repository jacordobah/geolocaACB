from ..model.meeting_point import MeetingPoint
from ..model.point_kind import PointKind
from ..model.route import Route
from .route_endpoints import (
    SCHOOL_ROUTE_CODES,
    _is_school_route,
    _meeting_point_variant,
    _school_scope,
)


def select_importable_school_data(
    routes: list[Route],
    points: list[MeetingPoint],
) -> tuple[list[Route], list[MeetingPoint]]:
    selected_routes = [
        route for route in routes
        if _is_importable_route(route, points)
    ]
    scopes = {_school_scope(route.folder_path) for route in selected_routes}
    folders = {route.folder_path for route in selected_routes}
    selected_points = [
        point for point in points
        if _is_relevant_point(point, selected_routes, scopes, folders)
    ]
    return selected_routes, selected_points


def _is_importable_route(
    route: Route,
    points: list[MeetingPoint],
) -> bool:
    if route.code not in SCHOOL_ROUTE_CODES or not _is_school_route(route):
        return False
    scope = _school_scope(route.folder_path)
    if scope is None:
        return False
    return any(
        point.kind is PointKind.MEETING_POINT
        and _school_scope(point.folder_path) == scope
        and _meeting_point_variant(point.folder_path, route.code) is not None
        for point in points
    )


def _is_relevant_point(
    point: MeetingPoint,
    routes: list[Route],
    scopes: set[tuple[str, ...] | None],
    folders: set[tuple[str, ...]],
) -> bool:
    if point.kind is PointKind.MEETING_POINT:
        scope = _school_scope(point.folder_path)
        return scope is not None and any(
            _school_scope(route.folder_path) == scope
            and _meeting_point_variant(point.folder_path, route.code) is not None
            for route in routes
        )
    if point.kind is PointKind.SCHOOL:
        return _school_scope(point.folder_path) in scopes
    if point.kind in (PointKind.EXPLICIT_START, PointKind.EXPLICIT_END):
        return point.folder_path in folders
    return False

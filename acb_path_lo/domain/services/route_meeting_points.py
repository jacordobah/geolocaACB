from ..model.meeting_point import MeetingPoint
from ..model.point_kind import PointKind
from ..model.route import Route
from ..model.route_endpoints import EndpointMatch, RouteEndpoints
from ..model.route_meeting_points import RouteMeetingPoints
from .route_endpoints import (
    SCHOOL_ROUTE_CODES,
    _is_school_route,
    _meeting_point_variant,
    _pe_number,
    _school_scope,
    _segments,
)
from .text_matching import normalize_label


def resolve_route_meeting_points(
    routes: list[Route],
    points: list[MeetingPoint],
    endpoints: list[RouteEndpoints],
) -> list[RouteMeetingPoints]:
    if len(endpoints) != len(routes):
        raise ValueError("Endpoint resolution must match the number of routes")
    if any(item.route_index != index for index, item in enumerate(endpoints)):
        raise ValueError("Endpoint resolution must follow the order of the routes")
    return [
        _resolve_route(index, route, points, resolved)
        for index, (route, resolved) in enumerate(zip(routes, endpoints))
    ]


def _resolve_route(
    route_index: int,
    route: Route,
    points: list[MeetingPoint],
    endpoints: RouteEndpoints,
) -> RouteMeetingPoints:
    if route.code not in SCHOOL_ROUTE_CODES or not _is_school_route(route):
        return RouteMeetingPoints(route_index)

    scope = _school_scope(route.folder_path)
    if scope is None:
        return RouteMeetingPoints(route_index)

    endpoint = endpoints.start if route.code == "541" else endpoints.end
    variant = _endpoint_variant(endpoint, points, route.code)
    if variant is None:
        return RouteMeetingPoints(route_index)

    indices = [
        index
        for index, point in enumerate(points)
        if point.kind is PointKind.MEETING_POINT
        and _segments(point.folder_path)[:len(scope)] == scope
        and _meeting_point_variant(point.folder_path, route.code) == variant
    ]
    indices.sort(key=lambda index: _point_order(points[index], index))
    return RouteMeetingPoints(
        route_index=route_index,
        point_indices=tuple(indices),
        variant=variant,
    )


def _endpoint_variant(
    endpoint: EndpointMatch | None,
    points: list[MeetingPoint],
    route_code: str,
) -> str | None:
    if endpoint is None:
        return None
    if not 0 <= endpoint.point_index < len(points):
        raise ValueError("Meeting point index is out of range")
    point = points[endpoint.point_index]
    if point.kind is not PointKind.MEETING_POINT:
        return None
    return _meeting_point_variant(point.folder_path, route_code)


def _point_order(point: MeetingPoint, index: int) -> tuple[bool, int, str, int]:
    number = _pe_number(point)
    return (
        number is None,
        number if number is not None else 0,
        normalize_label(point.name),
        index,
    )

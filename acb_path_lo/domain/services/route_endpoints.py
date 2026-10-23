import re

from ..model.coordinates import Coordinates, finite_number
from ..model.endpoint_policy import (
    DEFAULT_PE_TOLERANCE_M, DEFAULT_SCHOOL_TOLERANCE_M,
)
from ..model.meeting_point import MeetingPoint
from ..model.point_kind import PointKind
from ..model.route import Route
from ..model.route_endpoints import EndpointMatch, RouteEndpoints
from .geographic_validation import haversine_distance_m
from .text_matching import normalize_label

SCHOOL_BRANCH = "RUTAS AL COLEGIO EN BICI"
SCHOOL_ROUTES_FOLDERS = ("RUTA", "RUTAS")
SCHOOL_ROUTE_CODES = frozenset({"541", "542"})
MEETING_POINT_FOLDER_PREFIXES = ("PUNTOS DE ENCUENTRO", "PUNTOS DE ENCUENTO")
PE_NAME_PATTERN = re.compile(r"PE\s*(\d+)")
ENDPOINT_WINDOW_RATIO = 0.1
ENDPOINT_WINDOW_MIN = 1
ENDPOINT_WINDOW_MAX = 25


def resolve_route_endpoints(
    routes: list[Route],
    points: list[MeetingPoint],
    *,
    pe_tolerance_m: float = DEFAULT_PE_TOLERANCE_M,
    school_tolerance_m: float = DEFAULT_SCHOOL_TOLERANCE_M,
) -> list[RouteEndpoints]:
    finite_number(pe_tolerance_m, "pe_tolerance_m")
    finite_number(school_tolerance_m, "school_tolerance_m")
    if pe_tolerance_m <= 0 or school_tolerance_m <= 0:
        raise ValueError("Endpoint tolerances must be positive")
    return [
        _resolve_route(index, route, points, pe_tolerance_m, school_tolerance_m)
        for index, route in enumerate(routes)
    ]


def _resolve_route(
    route_index: int,
    route: Route,
    points: list[MeetingPoint],
    pe_tolerance_m: float,
    school_tolerance_m: float,
) -> RouteEndpoints:
    if route.code not in SCHOOL_ROUTE_CODES or not _is_school_route(route):
        return RouteEndpoints(
            route_index=route_index, start=None, end=None,
            status="not_applicable", reason="outside_school_routes",
        )

    scope = _school_scope(route.folder_path)
    if scope is None:
        return RouteEndpoints(
            route_index=route_index, start=None, end=None,
            status="unresolved", reason="school_scope_not_found",
        )

    scoped = [
        index for index, point in enumerate(points)
        if _segments(point.folder_path)[:len(scope)] == scope
    ]
    explicit_start = [
        index for index in scoped
        if points[index].kind is PointKind.EXPLICIT_START
        and points[index].folder_path == route.folder_path
    ]
    explicit_end = [
        index for index in scoped
        if points[index].kind is PointKind.EXPLICIT_END
        and points[index].folder_path == route.folder_path
    ]
    meeting_points = [
        index for index in scoped
        if points[index].kind is PointKind.MEETING_POINT
        and _meeting_point_code_matches(points[index].folder_path, route.code)
    ]
    schools = [
        index for index in scoped
        if points[index].kind is PointKind.SCHOOL
    ]

    coordinates = route.coordinates
    window = max(
        ENDPOINT_WINDOW_MIN,
        min(ENDPOINT_WINDOW_MAX, round(len(coordinates) * ENDPOINT_WINDOW_RATIO)),
    )
    first = _vertices(coordinates, 0, window)
    last = _vertices(coordinates, len(coordinates) - window, len(coordinates))

    start = _nearest(points, explicit_start, first, school_tolerance_m, "explicit")
    end = _nearest(points, explicit_end, last, school_tolerance_m, "explicit")
    if start is None or end is None:
        inferred_start, inferred_end = _resolve_inferred_endpoints(
            points, meeting_points, schools, first, last,
            pe_tolerance_m, school_tolerance_m, route.code,
        )
        if start is None:
            start = inferred_start
        if end is None:
            end = inferred_end

    if start is not None and end is not None:
        return RouteEndpoints(route_index, start, end, "resolved")
    reason = "start_not_found" if start is None else "end_not_found"
    if start is None and end is None:
        reason = "endpoints_not_found"
    return RouteEndpoints(route_index, start, end, "unresolved", reason)


def _is_school_route(route: Route) -> bool:
    segments = _segments(route.folder_path)
    return SCHOOL_BRANCH in segments and any(
        folder in segments for folder in SCHOOL_ROUTES_FOLDERS
    )


def _segments(folder_path: tuple[str, ...]) -> tuple[str, ...]:
    return tuple(normalize_label(segment) for segment in folder_path)


def _school_scope(folder_path: tuple[str, ...]) -> tuple[str, ...] | None:
    segments = _segments(folder_path)
    try:
        branch_index = segments.index(SCHOOL_BRANCH)
    except ValueError:
        return None
    if branch_index + 1 >= len(segments):
        return None
    return segments[:branch_index + 2]


def _meeting_point_variant(
    folder_path: tuple[str, ...], code: str,
) -> str | None:
    for segment in _segments(folder_path):
        for prefix in MEETING_POINT_FOLDER_PREFIXES:
            if segment.startswith(f"{prefix} "):
                token = segment[len(prefix):].strip()
                if token == code or token.startswith(f"{code} "):
                    return token
    return None


def _meeting_point_code_matches(
    folder_path: tuple[str, ...], code: str,
) -> bool:
    return _meeting_point_variant(folder_path, code) is not None


def _pe_number(point: MeetingPoint) -> int | None:
    match = PE_NAME_PATTERN.fullmatch(normalize_label(point.name))
    return int(match.group(1)) if match else None


def _vertices(
    coordinates: list[tuple[float, float]], start: int, stop: int,
) -> list[tuple[int, float, float]]:
    return [
        (index, coordinates[index][0], coordinates[index][1])
        for index in range(start, stop)
    ]


def _resolve_inferred_endpoints(
    points: list[MeetingPoint],
    meeting_points: list[int],
    schools: list[int],
    first: list[tuple[int, float, float]],
    last: list[tuple[int, float, float]],
    pe_tolerance_m: float,
    school_tolerance_m: float,
    route_code: str,
) -> tuple[EndpointMatch | None, EndpointMatch | None]:
    school_match, school_side = _school_match(
        points, schools, first, last, school_tolerance_m,
    )
    pe_vertices = last if school_side == "first" else first
    pe_match = _numbered_meeting_point(
        points, meeting_points, pe_vertices, pe_tolerance_m, route_code,
    )
    if pe_match is None:
        pe_match = _nearest(
            points, meeting_points, pe_vertices, pe_tolerance_m, "folder",
        )

    if route_code == "541":
        return pe_match, school_match
    return school_match, pe_match


def _school_match(
    points: list[MeetingPoint],
    schools: list[int],
    first: list[tuple[int, float, float]],
    last: list[tuple[int, float, float]],
    tolerance_m: float,
) -> tuple[EndpointMatch | None, str]:
    first_best = _closest(points, schools, first)
    last_best = _closest(points, schools, last)
    if first_best is None and last_best is None:
        return None, "last"
    if first_best is None:
        best, side = last_best, "last"
    elif last_best is None:
        best, side = first_best, "first"
    elif first_best[0] <= last_best[0]:
        best, side = first_best, "first"
    else:
        best, side = last_best, "last"
    return _match(best, tolerance_m, "folder"), side


def _numbered_meeting_point(
    points: list[MeetingPoint],
    candidates: list[int],
    vertices: list[tuple[int, float, float]],
    tolerance_m: float,
    route_code: str,
) -> EndpointMatch | None:
    groups: dict[str | None, list[int]] = {}
    for index in candidates:
        variant = _meeting_point_variant(points[index].folder_path, route_code)
        groups.setdefault(variant, []).append(index)

    matches = [
        match
        for indices in groups.values()
        if (match := _numbered_endpoint(
            points, indices, vertices, tolerance_m, route_code == "541",
        )) is not None
    ]
    if not matches:
        return None
    return min(matches, key=lambda match: match.distance_m)


def _numbered_endpoint(
    points: list[MeetingPoint],
    candidates: list[int],
    vertices: list[tuple[int, float, float]],
    tolerance_m: float,
    choose_lowest: bool,
) -> EndpointMatch | None:
    numbered = [
        (number, index)
        for index in candidates
        if (number := _pe_number(points[index])) is not None
    ]
    if not numbered:
        return None
    target = (
        min(number for number, _ in numbered)
        if choose_lowest
        else max(number for number, _ in numbered)
    )
    indices = [index for number, index in numbered if number == target]
    return _nearest(
        points, indices, vertices, tolerance_m, "folder", allow_beyond=True,
    )


def _closest(
    points: list[MeetingPoint],
    candidates: list[int],
    vertices: list[tuple[int, float, float]],
) -> tuple[float, int, int] | None:
    best: tuple[float, int, int] | None = None
    for candidate in candidates:
        point = points[candidate]
        target = Coordinates(latitude=point.latitude, longitude=point.longitude)
        for vertex_index, longitude, latitude in vertices:
            distance = haversine_distance_m(
                target, Coordinates(latitude=latitude, longitude=longitude),
            )
            if best is None or distance < best[0]:
                best = (distance, candidate, vertex_index)
    return best


def _match(
    best: tuple[float, int, int] | None,
    tolerance_m: float,
    source: str,
    *,
    allow_beyond: bool = False,
) -> EndpointMatch | None:
    if best is None:
        return None
    distance, candidate, vertex_index = best
    if distance > tolerance_m and not allow_beyond:
        return None
    return EndpointMatch(
        point_index=candidate,
        distance_m=distance,
        source=source,
        confidence=_confidence(distance, tolerance_m, source),
        matched_vertex_index=vertex_index,
    )


def _nearest(
    points: list[MeetingPoint],
    candidates: list[int],
    vertices: list[tuple[int, float, float]],
    tolerance_m: float,
    source: str,
    *,
    allow_beyond: bool = False,
) -> EndpointMatch | None:
    return _match(
        _closest(points, candidates, vertices),
        tolerance_m, source, allow_beyond=allow_beyond,
    )


def _confidence(distance_m: float, tolerance_m: float, source: str) -> str:
    if source == "explicit":
        return "high"
    if distance_m > tolerance_m:
        return "low"
    if distance_m <= tolerance_m / 4:
        return "high"
    return "medium"

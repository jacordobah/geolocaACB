from dataclasses import dataclass


@dataclass(frozen=True)
class EndpointMatch:
    point_index: int
    distance_m: float
    source: str
    confidence: str
    matched_vertex_index: int


@dataclass(frozen=True)
class RouteEndpoints:
    route_index: int
    start: EndpointMatch | None
    end: EndpointMatch | None
    status: str
    reason: str | None = None


@dataclass(frozen=True)
class RouteEndpointPoint:
    meeting_point_id: str
    name: str
    longitude: float
    latitude: float
    source: str | None = None
    distance_m: float | None = None


@dataclass(frozen=True)
class RouteEndpointsView:
    route_id: str
    status: str
    start: RouteEndpointPoint | None = None
    end: RouteEndpointPoint | None = None

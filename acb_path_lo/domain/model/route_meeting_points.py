from dataclasses import dataclass


@dataclass(frozen=True)
class RouteMeetingPoints:
    route_index: int
    point_indices: tuple[int, ...] = ()
    variant: str | None = None

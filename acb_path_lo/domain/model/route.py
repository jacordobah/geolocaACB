from dataclasses import dataclass

@dataclass
class Route:
    name: str
    coordinates: list[tuple[float, float]]
    code: str | None = None
    folder_path: tuple[str, ...] = ()
    route_code: str | None = None
    day_shift: str | None = None

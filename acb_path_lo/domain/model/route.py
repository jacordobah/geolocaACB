from dataclasses import dataclass

@dataclass
class Route:
    name: str
    coordinates: list[tuple[float, float]]
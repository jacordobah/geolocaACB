from dataclasses import dataclass

from .point_kind import PointKind


@dataclass
class MeetingPoint:
    name: str
    longitude: float
    latitude: float
    description: str | None = None
    kind: PointKind = PointKind.OTHER
    folder_path: tuple[str, ...] = ()

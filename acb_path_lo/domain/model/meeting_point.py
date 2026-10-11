from dataclasses import dataclass

@dataclass
class MeetingPoint:
    name: str
    longitude: float
    latitude: float
    description: str | None = None
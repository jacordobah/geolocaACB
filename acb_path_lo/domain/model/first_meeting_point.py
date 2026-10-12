from dataclasses import dataclass


@dataclass(frozen=True)
class FirstMeetingPoint:
    route_id: str
    meeting_point_id: str
    name: str
    longitude: float
    latitude: float

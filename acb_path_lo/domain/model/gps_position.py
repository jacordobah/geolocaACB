from dataclasses import dataclass
from datetime import UTC, datetime, time, timedelta
from zoneinfo import ZoneInfo

from .gps_observation import utc_milliseconds


BOGOTA = ZoneInfo("America/Bogota")


@dataclass(frozen=True)
class HistoryPolicy:
    cutoff_hour: int
    cutoff_minute: int

    def __post_init__(self):
        if (type(self.cutoff_hour) is not int or type(self.cutoff_minute) is not int
                or not 0 <= self.cutoff_hour <= 23 or not 0 <= self.cutoff_minute <= 59):
            raise ValueError("Invalid Bogotá history cutoff")

    def expires_at(self, received_at: datetime) -> datetime:
        local_day = received_at.astimezone(BOGOTA).date() + timedelta(days=1)
        cutoff = datetime.combine(local_day, time(self.cutoff_hour, self.cutoff_minute), BOGOTA)
        return utc_milliseconds(cutoff.astimezone(UTC))


@dataclass(frozen=True)
class GpsPosition:
    tracking_id: str
    guide_id: str
    route_id: str
    sequence: int
    latitude: float
    longitude: float
    accuracy_m: float | None
    captured_at: datetime
    received_at: datetime
    expires_at: datetime
    status: str
    request_hash: str


@dataclass(frozen=True)
class PositionView:
    sequence: int
    latitude: float
    longitude: float
    accuracy_m: float | None
    captured_at: datetime
    received_at: datetime
    expires_at: datetime
    status: str

    @classmethod
    def from_position(cls, position: GpsPosition):
        return cls(position.sequence, position.latitude, position.longitude,
                   position.accuracy_m, position.captured_at, position.received_at,
                   position.expires_at, position.status)


@dataclass(frozen=True)
class PositionPage:
    tracking_id: str
    positions: list[PositionView]
    next_after_sequence: int | None

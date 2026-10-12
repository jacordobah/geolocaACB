from dataclasses import dataclass, field
from datetime import datetime
from typing import Literal

from .coordinates import finite_number
from .first_meeting_point import FirstMeetingPoint
from .geographic_validation import GeofencePolicy, MeasurementQuality
from .gps_position import GpsPosition, HistoryPolicy


@dataclass(frozen=True)
class TrackingPolicy:
    radius_m: float
    max_accuracy_m: float
    max_age_s: float
    clock_skew_s: float

    def __post_init__(self):
        GeofencePolicy(self.radius_m, self.max_accuracy_m)
        for name in ("max_age_s", "clock_skew_s"):
            finite_number(getattr(self, name), name)
        if self.max_age_s <= 0 or self.clock_skew_s < 0:
            raise ValueError("Invalid temporal policy")


@dataclass(frozen=True)
class FirstArrival:
    sequence: int
    guide_id: str
    route_id: str
    meeting_point_id: str
    meeting_point_latitude: float
    meeting_point_longitude: float
    latitude: float
    longitude: float
    accuracy_m: float
    distance_m: float
    captured_at: datetime
    received_at: datetime
    validated_at: datetime


@dataclass(frozen=True)
class ObservationResult:
    tracking_id: str
    sequence: int
    guide_id: str
    route_id: str
    meeting_point_id: str
    distance_m: float
    radius_m: float
    point_inside: bool
    status: Literal["inside", "outside", "indeterminate"]
    quality: MeasurementQuality
    captured_at: datetime
    received_at: datetime
    validated_at: datetime
    first_arrival: FirstArrival | None


@dataclass(frozen=True)
class LastObservation:
    sequence: int
    captured_at: datetime
    request_hash: str
    response: ObservationResult


@dataclass(frozen=True)
class SessionIssuance:
    key_hash: str
    request_hash: str
    nonce: str


@dataclass(frozen=True)
class TrackingSession:
    tracking_id: str
    guide_id: str
    route_id: str
    target: FirstMeetingPoint
    policy: TrackingPolicy
    credential_hash: str = field(repr=False)
    enabled_at: datetime
    state: Literal["enabled", "disabled"] = "enabled"
    disabled_at: datetime | None = None
    version: int = 0
    last_observation: LastObservation | None = None
    first_arrival: FirstArrival | None = None
    history_policy: HistoryPolicy | None = None
    pending_position: GpsPosition | None = None
    issuance: SessionIssuance | None = None


@dataclass(frozen=True)
class TrackingView:
    tracking_id: str
    guide_id: str
    route_id: str
    meeting_point_id: str
    state: Literal["enabled", "disabled"]
    enabled_at: datetime
    disabled_at: datetime | None
    last_sequence: int | None
    first_arrival: FirstArrival | None

    @classmethod
    def from_session(cls, session: TrackingSession):
        return cls(session.tracking_id, session.guide_id, session.route_id,
                   session.target.meeting_point_id, session.state, session.enabled_at,
                   session.disabled_at,
                   session.last_observation.sequence if session.last_observation else None,
                   session.first_arrival)

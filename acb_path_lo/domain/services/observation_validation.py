from datetime import datetime

from ..model.gps_observation import GpsObservation
from ..model.tracking_session import TrackingSession
from ..tracking_errors import TrackingError


def validate_observation_time(session: TrackingSession, observation: GpsObservation,
                              received_at: datetime) -> None:
    capture = observation.captured_at
    # Differences avoid overflow from adding arbitrarily large finite policies.
    if (received_at - capture).total_seconds() > session.policy.max_age_s:
        raise TrackingError("observation_too_old")
    if (capture - received_at).total_seconds() > session.policy.clock_skew_s:
        raise TrackingError("observation_from_future")
    if (session.enabled_at - capture).total_seconds() > session.policy.clock_skew_s:
        raise TrackingError("observation_before_tracking")
    if session.last_observation and capture < session.last_observation.captured_at:
        raise TrackingError("observation_out_of_order")

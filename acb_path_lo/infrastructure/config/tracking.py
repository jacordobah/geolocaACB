import os

from .geofence import load_geofence_policy
from ...domain.model.tracking_session import TrackingPolicy


def load_tracking_policy(environment=None):
    values = os.environ if environment is None else environment
    geographic = load_geofence_policy(values)
    if geographic is None:
        return None
    try:
        return TrackingPolicy(geographic.radius_m, geographic.max_accuracy_m,
                              float(values["ACB_GPS_MAX_AGE_S"]),
                              float(values["ACB_GPS_CLOCK_SKEW_S"]))
    except (KeyError, TypeError, ValueError, OverflowError):
        return None

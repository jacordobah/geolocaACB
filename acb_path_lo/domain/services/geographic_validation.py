import math

from ..model.coordinates import Coordinates, finite_number
from ..model.geographic_validation import (
    GeofenceEvaluation, GeofencePolicy, MeasurementQuality,
)

# Spherical approximation in metres; this is not a functional geofence radius.
EARTH_RADIUS_M = 6_371_000.0


def haversine_distance_m(origin: Coordinates, destination: Coordinates) -> float:
    latitude1, latitude2 = math.radians(origin.latitude), math.radians(destination.latitude)
    delta_latitude = latitude2 - latitude1
    # Equivalent longitudes (+180/-180) must also have zero separation.
    delta_longitude = math.radians(
        (destination.longitude - origin.longitude + 180) % 360 - 180
    )
    cosine1 = 0.0 if abs(origin.latitude) == 90 else math.cos(latitude1)
    cosine2 = 0.0 if abs(destination.latitude) == 90 else math.cos(latitude2)
    a = (math.sin(delta_latitude / 2) ** 2
         + cosine1 * cosine2 * math.sin(delta_longitude / 2) ** 2)
    a = min(1.0, max(0.0, a))
    return EARTH_RADIUS_M * 2 * math.atan2(math.sqrt(a), math.sqrt(1 - a))


def evaluate_geofence(
    distance_m: float, policy: GeofencePolicy, accuracy_m: float | None,
) -> GeofenceEvaluation:
    """Reported uncertainty intervals are approximations, not presence guarantees."""
    finite_number(distance_m, "distance_m")
    if distance_m < 0:
        raise ValueError("distance_m must not be negative")
    minimum = maximum = None
    status = "indeterminate"
    if accuracy_m is None:
        reason = "accuracy_missing"
    else:
        finite_number(accuracy_m, "accuracy_m")
        if accuracy_m <= 0:
            raise ValueError("accuracy_m must be positive")
        minimum, maximum = max(0.0, distance_m - accuracy_m), distance_m + accuracy_m
        finite_number(maximum, "estimated_max_distance_m")
        if accuracy_m > policy.max_accuracy_m:
            reason = "accuracy_exceeds_limit"
        elif maximum <= policy.radius_m:
            status, reason = "inside", "uncertainty_within_radius"
        elif minimum > policy.radius_m:
            status, reason = "outside", "uncertainty_outside_radius"
        else:
            reason = "boundary_uncertainty"
    return GeofenceEvaluation(
        point_inside=distance_m <= policy.radius_m,
        status=status,
        quality=MeasurementQuality(accuracy_m, policy.max_accuracy_m, minimum, maximum, reason),
    )

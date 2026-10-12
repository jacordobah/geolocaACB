import os
from collections.abc import Mapping

from ...domain.model.geographic_validation import GeofencePolicy


def load_geofence_policy(environment: Mapping[str, str] | None = None) -> GeofencePolicy | None:
    """Load once per service lifespan. Missing/invalid config disables validation only."""
    values = os.environ if environment is None else environment
    try:
        return GeofencePolicy(
            radius_m=float(values["ACB_FIRST_PE_RADIUS_M"]),
            max_accuracy_m=float(values["ACB_FIRST_PE_MAX_ACCURACY_M"]),
        )
    except (KeyError, TypeError, ValueError, OverflowError):
        return None

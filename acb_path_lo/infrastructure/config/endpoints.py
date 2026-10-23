import logging
import math
import os
from collections.abc import Mapping

from ...domain.model.endpoint_policy import (
    DEFAULT_PE_TOLERANCE_M, DEFAULT_SCHOOL_TOLERANCE_M, EndpointPolicy,
)

logger = logging.getLogger(__name__)


def load_endpoint_policy(
    environment: Mapping[str, str] | None = None,
) -> EndpointPolicy:
    """Load once per service lifespan. Missing values keep the documented defaults."""
    values = os.environ if environment is None else environment
    return EndpointPolicy(
        pe_tolerance_m=_tolerance(
            values, "ACB_ENDPOINT_PE_TOLERANCE_M", DEFAULT_PE_TOLERANCE_M,
        ),
        school_tolerance_m=_tolerance(
            values, "ACB_ENDPOINT_SCHOOL_TOLERANCE_M", DEFAULT_SCHOOL_TOLERANCE_M,
        ),
    )


def _tolerance(
    values: Mapping[str, str], name: str, default: float,
) -> float:
    raw = values.get(name)
    if raw is None:
        return default
    try:
        value = float(raw)
    except (TypeError, ValueError):
        value = math.nan
    if not math.isfinite(value) or value <= 0:
        logger.warning("Invalid %s, using default %s metres", name, default)
        return default
    return value

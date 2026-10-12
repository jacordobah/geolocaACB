from dataclasses import dataclass
from datetime import datetime
from typing import Literal

from .coordinates import finite_number


@dataclass(frozen=True)
class GeofencePolicy:
    radius_m: float
    max_accuracy_m: float

    def __post_init__(self) -> None:
        for name in ("radius_m", "max_accuracy_m"):
            value = getattr(self, name)
            finite_number(value, name)
            if value <= 0:
                raise ValueError(f"{name} must be positive")


@dataclass(frozen=True)
class MeasurementQuality:
    accuracy_m: float | None
    max_accuracy_m: float
    estimated_min_distance_m: float | None
    estimated_max_distance_m: float | None
    reason: Literal[
        "accuracy_missing", "accuracy_exceeds_limit", "uncertainty_within_radius",
        "uncertainty_outside_radius", "boundary_uncertainty",
    ]


@dataclass(frozen=True)
class GeofenceEvaluation:
    point_inside: bool
    status: Literal["inside", "outside", "indeterminate"]
    quality: MeasurementQuality


@dataclass(frozen=True)
class GeographicValidation:
    route_id: str
    meeting_point_id: str
    distance_m: float
    radius_m: float
    point_inside: bool
    status: Literal["inside", "outside", "indeterminate"]
    quality: MeasurementQuality
    captured_at: datetime | None
    validated_at: datetime

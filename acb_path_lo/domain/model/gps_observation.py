from dataclasses import dataclass
from datetime import UTC, datetime
import hashlib
import json

from .coordinates import Coordinates, finite_number

# Exact in JavaScript numbers and within BSON int64.
MAX_SEQUENCE = 2**53 - 1


def utc_milliseconds(value: datetime) -> datetime:
    if value.utcoffset() is None:
        raise ValueError("Timestamp requires a timezone")
    value = value.astimezone(UTC)
    return value.replace(microsecond=value.microsecond // 1000 * 1000)


@dataclass(frozen=True)
class GpsObservation:
    sequence: int
    coordinates: Coordinates
    accuracy_m: float | None
    captured_at: datetime

    def __post_init__(self):
        if type(self.sequence) is not int or not 1 <= self.sequence <= MAX_SEQUENCE:
            raise ValueError("Invalid observation sequence")
        if self.accuracy_m is not None:
            finite_number(self.accuracy_m, "accuracy_m")
            if self.accuracy_m <= 0:
                raise ValueError("accuracy_m must be positive")
        object.__setattr__(self, "captured_at", utc_milliseconds(self.captured_at))

    def fingerprint(self) -> str:
        # Normalize integer/float spelling, signed zero, timezone and milliseconds.
        def number(value):
            return None if value is None else (0.0 if value == 0 else float(value))
        payload = [self.sequence, number(self.coordinates.latitude),
                   number(self.coordinates.longitude), number(self.accuracy_m),
                   self.captured_at.isoformat(timespec="milliseconds")]
        return hashlib.sha256(json.dumps(payload, separators=(",", ":"),
                                         allow_nan=False).encode()).hexdigest()

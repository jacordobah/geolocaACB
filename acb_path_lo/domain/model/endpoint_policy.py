from dataclasses import dataclass

from .coordinates import finite_number

DEFAULT_PE_TOLERANCE_M = 150.0
DEFAULT_SCHOOL_TOLERANCE_M = 120.0


@dataclass(frozen=True)
class EndpointPolicy:
    pe_tolerance_m: float = DEFAULT_PE_TOLERANCE_M
    school_tolerance_m: float = DEFAULT_SCHOOL_TOLERANCE_M

    def __post_init__(self) -> None:
        for name in ("pe_tolerance_m", "school_tolerance_m"):
            value = getattr(self, name)
            finite_number(value, name)
            if value <= 0:
                raise ValueError(f"{name} must be positive")

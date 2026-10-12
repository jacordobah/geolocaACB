from datetime import datetime
from typing import Annotated

from pydantic import BaseModel, ConfigDict, Field, field_validator


class ValidateLocationRequest(BaseModel):
    model_config = ConfigDict(strict=True, extra="forbid")

    latitude: Annotated[float, Field(ge=-90, le=90, allow_inf_nan=False)]
    longitude: Annotated[float, Field(ge=-180, le=180, allow_inf_nan=False)]
    accuracy_m: Annotated[float, Field(gt=0, allow_inf_nan=False)] | None = None
    captured_at: datetime | None = None

    @field_validator("captured_at", mode="before")
    @classmethod
    def parse_capture_time(cls, value):
        if value is None:
            return None
        # Only ISO timestamp strings at the HTTP boundary; reject numeric epochs.
        if not isinstance(value, str):
            raise ValueError("captured_at must be an ISO timestamp with timezone")
        try:
            timestamp = datetime.fromisoformat(value)
        except ValueError as error:
            raise ValueError("Invalid captured_at timestamp") from error
        if timestamp.utcoffset() is None:
            raise ValueError("captured_at requires a timezone")
        return timestamp

from datetime import datetime
from typing import Annotated

from pydantic import Field

from .geographic_validation import ValidateLocationRequest
from ....domain.model.gps_observation import MAX_SEQUENCE


class ObservationRequest(ValidateLocationRequest):
    sequence: Annotated[int, Field(ge=1, le=MAX_SEQUENCE)]
    captured_at: datetime

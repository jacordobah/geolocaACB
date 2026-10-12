import re
from datetime import datetime
from typing import Annotated

from pydantic import BaseModel, ConfigDict, Field, field_validator


class CreateTrackingRequest(BaseModel):
    model_config = ConfigDict(strict=True, extra="forbid")

    guide_id: Annotated[str, Field(min_length=1, max_length=256)]
    route_id: Annotated[str, Field(min_length=24, max_length=24)]

    @field_validator("guide_id")
    @classmethod
    def nonblank_guide(cls, value: str) -> str:
        if not value.strip():
            raise ValueError("guide_id must not be blank")
        return value

    @field_validator("route_id")
    @classmethod
    def hex_route(cls, value: str) -> str:
        if re.fullmatch(r"[0-9a-fA-F]{24}", value) is None:
            raise ValueError("route_id must be a 24-character hex identifier")
        return value


class CreatedTrackingResponse(BaseModel):
    tracking_id: str
    guide_id: str
    route_id: str
    meeting_point_id: str
    enabled_at: datetime
    session_credential: str

from datetime import datetime
from typing import Protocol

from ..model.gps_position import GpsPosition


class PositionRepository(Protocol):
    async def ensure(self, position: GpsPosition) -> None: ...
    async def list_page(self, tracking_id: str, after_sequence: int, max_sequence: int,
                        now: datetime, limit: int) -> tuple[list[GpsPosition], bool]: ...

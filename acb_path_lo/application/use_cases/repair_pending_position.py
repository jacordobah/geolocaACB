from collections.abc import Callable
from datetime import datetime

from ...domain.model.gps_observation import utc_milliseconds
from ...domain.repositories.position_repository import PositionRepository
from ...domain.repositories.tracking_repository import TrackingRepository
from ...domain.tracking_errors import TrackingError


class RepairPendingPosition:
    def __init__(self, tracking: TrackingRepository, positions: PositionRepository,
                 clock: Callable[[], datetime]):
        self.tracking, self.positions, self.clock = tracking, positions, clock

    async def execute(self, tracking_id: str) -> None:
        for _ in range(8):
            session = await self.tracking.get(tracking_id)
            if session is None:
                raise TrackingError("tracking_session_unavailable")
            pending = session.pending_position
            if pending is None:
                return
            # A delayed repair must not recreate data beyond its retention window.
            if pending.expires_at > utc_milliseconds(self.clock()):
                await self.positions.ensure(pending)
            if await self.tracking.clear_pending(session):
                return
        raise TrackingError("tracking_busy")

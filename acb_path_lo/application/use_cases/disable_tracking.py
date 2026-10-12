from collections.abc import Callable
from datetime import datetime

from ...domain.model.gps_observation import utc_milliseconds
from ...domain.model.tracking_session import TrackingView
from ...domain.repositories.tracking_repository import TrackingRepository
from ...domain.tracking_errors import TrackingError


class DisableTracking:
    def __init__(self, repository: TrackingRepository, clock: Callable[[], datetime]):
        self.repository, self.clock = repository, clock

    async def execute(self, tracking_id: str) -> TrackingView:
        for _ in range(5):
            session = await self.repository.get(tracking_id)
            if session is None:
                raise TrackingError("tracking_session_unavailable")
            if session.state == "disabled":
                return TrackingView.from_session(session)
            if await self.repository.disable(session, utc_milliseconds(self.clock())):
                updated = await self.repository.get(tracking_id)
                if updated is None:
                    raise TrackingError("tracking_session_unavailable")
                return TrackingView.from_session(updated)
        raise TrackingError("tracking_busy")

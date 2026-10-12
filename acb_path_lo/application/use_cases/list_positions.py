from collections.abc import Callable
from datetime import datetime

from .get_tracking_session import GetTrackingSession
from ...domain.model.gps_observation import utc_milliseconds
from ...domain.model.gps_position import PositionPage, PositionView
from ...domain.repositories.position_repository import PositionRepository
from ...domain.tracking_errors import TrackingError


class ListPositions:
    def __init__(self, get_session: GetTrackingSession, positions: PositionRepository,
                 clock: Callable[[], datetime]):
        self.get_session, self.positions, self.clock = get_session, positions, clock

    async def execute(self, tracking_id: str, credential: str,
                      after_sequence: int, limit: int) -> PositionPage:
        session = await self.get_session.authenticate(tracking_id, credential)
        if session.history_policy is None:
            raise TrackingError("history_not_configured")
        if session.pending_position is not None:
            raise TrackingError("position_history_pending")
        maximum = session.last_observation.sequence if session.last_observation else 0
        records, has_more = await self.positions.list_page(
            tracking_id, after_sequence, maximum, utc_milliseconds(self.clock()), limit)
        return PositionPage(tracking_id, [PositionView.from_position(r) for r in records],
                            records[-1].sequence if has_more else None)

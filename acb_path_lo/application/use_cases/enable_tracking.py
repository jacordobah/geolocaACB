from collections.abc import Callable
from datetime import datetime
from uuid import UUID

from .get_first_meeting_point import GetFirstMeetingPoint
from ...domain.model.gps_observation import utc_milliseconds
from ...domain.model.tracking_session import TrackingPolicy, TrackingSession
from ...domain.repositories.tracking_repository import TrackingRepository
from ...domain.tracking_errors import TrackingError


class EnableTracking:
    def __init__(self, repository: TrackingRepository, get_point: GetFirstMeetingPoint,
                 policy: TrackingPolicy | None, clock: Callable[[], datetime]):
        self.repository, self.get_point, self.policy, self.clock = repository, get_point, policy, clock

    async def execute(self, tracking_id: str, guide_id: str, route_id: str,
                      credential_hash: str) -> TrackingSession:
        if self.policy is None:
            raise TrackingError("tracking_configuration_unavailable")
        if not isinstance(guide_id, str) or not guide_id.strip() or len(guide_id) > 256:
            raise ValueError("guide_id must contain 1 to 256 characters")
        if str(UUID(tracking_id)) != tracking_id:
            raise ValueError("tracking_id must be a canonical UUID")
        point = await self.get_point.execute(route_id)
        session = TrackingSession(tracking_id, guide_id, point.route_id, point, self.policy,
                                  credential_hash, utc_milliseconds(self.clock()))
        await self.repository.create(session)
        return session

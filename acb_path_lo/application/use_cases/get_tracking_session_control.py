from ...domain.model.tracking_session import TrackingView
from ...domain.repositories.tracking_repository import TrackingRepository
from ...domain.tracking_errors import TrackingError


class GetTrackingSessionControl:
    def __init__(self, repository: TrackingRepository):
        self.repository = repository

    async def execute(self, tracking_id: str) -> TrackingView:
        session = await self.repository.get(tracking_id)
        if session is None:
            raise TrackingError("tracking_session_unavailable")
        return TrackingView.from_session(session)

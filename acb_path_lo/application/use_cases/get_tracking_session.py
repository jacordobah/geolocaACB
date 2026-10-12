from collections.abc import Callable

from ...domain.model.tracking_session import TrackingSession, TrackingView
from ...domain.repositories.tracking_repository import TrackingRepository
from ...domain.tracking_errors import TrackingError


class GetTrackingSession:
    def __init__(self, repository: TrackingRepository,
                 verify_credential: Callable[[str, str], bool]):
        self.repository, self.verify_credential = repository, verify_credential

    async def authenticate(self, tracking_id: str, credential: str) -> TrackingSession:
        session = await self.repository.get(tracking_id)
        # Perform a comparison even when the identifier is unknown.
        valid = self.verify_credential(credential, session.credential_hash if session else "0" * 64)
        if session is None or not valid:
            raise TrackingError("tracking_session_unavailable")
        return session

    async def execute(self, tracking_id: str, credential: str) -> TrackingView:
        return TrackingView.from_session(await self.authenticate(tracking_id, credential))

"""Idempotent technical session creation; framework and database independent."""
import hashlib
import hmac
import json
from collections.abc import Callable
from dataclasses import dataclass
from uuid import uuid4

from .enable_tracking import EnableTracking
from ...domain.model.tracking_session import SessionIssuance, TrackingSession
from ...domain.repositories.tracking_repository import TrackingRepository
from ...domain.tracking_errors import TrackingError, TrackingStoreUnavailable


@dataclass(frozen=True)
class CreatedTrackingSession:
    session: TrackingSession
    credential: str
    replayed: bool


class CreateTrackingSession:
    def __init__(self, enable: EnableTracking, repository: TrackingRepository,
                 derive_credential: Callable[[str, str], str],
                 nonce_factory: Callable[[], str], creation_ready: bool = True):
        self.enable, self.repository = enable, repository
        self.derive_credential, self.nonce_factory = derive_credential, nonce_factory
        self.creation_ready = creation_ready

    async def execute(self, idempotency_key: str, guide_id: str,
                      route_id: str) -> CreatedTrackingSession:
        key_hash = hashlib.sha256(idempotency_key.encode("ascii")).hexdigest()
        request_hash = hashlib.sha256(json.dumps(
            [guide_id, route_id.lower()], ensure_ascii=False,
            separators=(",", ":")).encode("utf-8")).hexdigest()
        existing = await self.repository.get_by_issuance_key(key_hash)
        if existing is not None:
            return self._replay(existing, request_hash)
        if not self.creation_ready:
            raise TrackingError("tracking_configuration_unavailable")

        tracking_id = str(uuid4())
        nonce = self.nonce_factory()
        credential = self.derive_credential(tracking_id, nonce)
        issuance = SessionIssuance(key_hash, request_hash, nonce)
        try:
            session = await self.enable.execute(
                tracking_id, guide_id, route_id,
                hashlib.sha256(credential.encode("utf-8")).hexdigest(), issuance)
        except (TrackingStoreUnavailable, TrackingError) as error:
            # A timeout or duplicate may mean that this insert already succeeded.
            if (isinstance(error, TrackingError)
                    and error.code not in {"tracking_session_conflict", "tracking_store_unavailable"}):
                raise
            try:
                existing = await self.repository.get_by_issuance_key(key_hash)
            except TrackingStoreUnavailable:
                raise TrackingStoreUnavailable() from error
            if existing is None:
                if isinstance(error, TrackingStoreUnavailable):
                    raise TrackingStoreUnavailable() from error
                raise
            return self._replay(existing, request_hash)
        return CreatedTrackingSession(session, credential, False)

    def _replay(self, session: TrackingSession, request_hash: str) -> CreatedTrackingSession:
        issuance = session.issuance
        if issuance is None or issuance.request_hash != request_hash:
            raise TrackingError("idempotency_key_conflict")
        try:
            credential = self.derive_credential(session.tracking_id, issuance.nonce)
        except ValueError as error:
            raise TrackingStoreUnavailable() from error
        if not hmac.compare_digest(hashlib.sha256(credential.encode("utf-8")).hexdigest(),
                                   session.credential_hash):
            raise TrackingError("tracking_issuance_unavailable")
        return CreatedTrackingSession(session, credential, True)

from datetime import UTC, datetime
from uuid import UUID

from fastapi import APIRouter, Depends, Header, HTTPException, Request, Response
from fastapi.security import HTTPAuthorizationCredentials, HTTPBearer
from pymongo.errors import PyMongoError

from ....application.use_cases.create_tracking_session import CreateTrackingSession
from ....application.use_cases.disable_tracking import DisableTracking
from ....application.use_cases.enable_tracking import EnableTracking
from ....application.use_cases.get_tracking_session_control import GetTrackingSessionControl
from ....domain.errors import (
    AssociationConflict, FirstMeetingPointNotConfigured, InvalidGeographicData,
    InvalidIdentifier, MeetingPointNotFound, RouteNotFound,
)
from ....domain.model.tracking_session import TrackingView
from ....domain.tracking_errors import TrackingError
from ...config.history import load_history_policy
from ...config.tracking import load_tracking_policy
from ...security.tracking_control import (
    derive_gps_credential, new_nonce, verify_service_credential,
)
from ..dependencies import get_first_meeting_point
from ..validation import JsonSafeValidationRoute
from .routes import geographic_http_error
from .tracking import tracking_http_error
from ..schemas.tracking_control import CreateTrackingRequest, CreatedTrackingResponse


router = APIRouter(prefix="/tracking-sessions", tags=["Technical tracking control"],
                   route_class=JsonSafeValidationRoute)
service_bearer = HTTPBearer(auto_error=False, scheme_name="TrackingServiceCredential")


def require_service(request: Request,
                    value: HTTPAuthorizationCredentials | None = Depends(service_bearer)):
    expected = getattr(request.app.state, "tracking_service_hash", None)
    if expected is None:
        raise HTTPException(503, detail={"code": "tracking_control_unavailable"})
    if value is None or not verify_service_credential(value.credentials, expected):
        raise HTTPException(401, detail={"code": "invalid_service_credential"},
                            headers={"WWW-Authenticate": "Bearer"})


def control_error(error: TrackingError) -> HTTPException:
    if error.code in {"tracking_issuance_unavailable", "tracking_configuration_unavailable"}:
        return HTTPException(503, detail={"code": error.code})
    return tracking_http_error(error)


@router.post("", response_model=CreatedTrackingResponse, status_code=201,
             dependencies=[Depends(require_service)])
async def create_session(request: Request, response: Response, payload: CreateTrackingRequest,
                         idempotency_key: str | None = Header(None, alias="Idempotency-Key")):
    try:
        key = UUID(idempotency_key) if idempotency_key is not None else None
    except (ValueError, TypeError, AttributeError):
        key = None
    if key is None or str(key) != idempotency_key:
        raise HTTPException(422, detail={"code": "invalid_idempotency_key"})
    issuance_key = getattr(request.app.state, "tracking_issuance_key", None)
    if issuance_key is None or not getattr(request.app.state, "tracking_control_index_ready", False):
        raise HTTPException(503, detail={"code": "tracking_control_unavailable"})
    policy = getattr(request.app.state, "tracking_policy", None)
    history = getattr(request.app.state, "tracking_history_policy", None)
    repo = request.app.state.tracking_repository
    enable = EnableTracking(repo, get_first_meeting_point(request), policy,
                            lambda: datetime.now(UTC), history)
    creator = CreateTrackingSession(enable, repo,
        lambda tracking_id, nonce: derive_gps_credential(issuance_key, tracking_id, nonce),
        new_nonce, creation_ready=policy is not None and history is not None)
    try:
        created = await creator.execute(str(key), payload.guide_id, payload.route_id)
    except (InvalidIdentifier, RouteNotFound, FirstMeetingPointNotConfigured,
            MeetingPointNotFound, AssociationConflict, InvalidGeographicData,
            PyMongoError) as error:
        raise geographic_http_error(error) from error
    except TrackingError as error:
        raise control_error(error) from error
    session = created.session
    response.status_code = 200 if created.replayed else 201
    response.headers["Location"] = (
        f"/api/v1/tracking-sessions/{session.tracking_id}/control")
    response.headers["Cache-Control"] = "no-store"
    return CreatedTrackingResponse(
        tracking_id=session.tracking_id, guide_id=session.guide_id,
        route_id=session.route_id, meeting_point_id=session.target.meeting_point_id,
        enabled_at=session.enabled_at, session_credential=created.credential)


@router.get("/{tracking_id}/control", response_model=TrackingView,
            dependencies=[Depends(require_service)])
async def read_control_session(tracking_id: UUID, request: Request, response: Response):
    try:
        response.headers["Cache-Control"] = "no-store"
        return await GetTrackingSessionControl(
            request.app.state.tracking_repository).execute(str(tracking_id))
    except TrackingError as error:
        raise control_error(error) from error


@router.post("/{tracking_id}/disable", response_model=TrackingView,
             dependencies=[Depends(require_service)])
async def disable_session(tracking_id: UUID, request: Request, response: Response):
    try:
        response.headers["Cache-Control"] = "no-store"
        return await DisableTracking(request.app.state.tracking_repository,
                                     lambda: datetime.now(UTC)).execute(str(tracking_id))
    except TrackingError as error:
        raise control_error(error) from error

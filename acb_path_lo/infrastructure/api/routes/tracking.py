from uuid import UUID

from fastapi import APIRouter, Depends, HTTPException
from fastapi.security import HTTPAuthorizationCredentials, HTTPBearer

from ..dependencies import get_tracking_session, get_receive_gps_observation
from ..schemas.tracking import ObservationRequest
from ..validation import JsonSafeValidationRoute
from ....application.use_cases.get_tracking_session import GetTrackingSession
from ....application.use_cases.receive_gps_observation import ReceiveGpsObservation
from ....domain.model.coordinates import Coordinates
from ....domain.model.gps_observation import GpsObservation
from ....domain.model.tracking_session import ObservationResult, TrackingView
from ....domain.tracking_errors import TrackingError

router = APIRouter(prefix="/tracking-sessions", tags=["Technical tracking"],
                   route_class=JsonSafeValidationRoute)
bearer = HTTPBearer(auto_error=False, scheme_name="TrackingSessionCredential")


def session_credential(value: HTTPAuthorizationCredentials | None = Depends(bearer)) -> str:
    if value is None:
        raise HTTPException(401, detail={"code": "tracking_credentials_required"},
                            headers={"WWW-Authenticate": "Bearer"})
    return value.credentials


def tracking_http_error(error: TrackingError) -> HTTPException:
    code = error.code
    status = 409
    if code == "tracking_session_unavailable":
        status = 404
    elif code in {"tracking_store_unavailable", "tracking_configuration_unavailable", "tracking_busy"}:
        status = 503
    return HTTPException(status, detail={"code": code})


ERROR_RESPONSES = {401: {"description": "Session credential required"},
                   404: {"description": "Session unavailable or credential invalid"},
                   409: {"description": "State, sequence or temporal conflict"},
                   503: {"description": "Tracking store unavailable or contention"}}


@router.get("/{tracking_id}", response_model=TrackingView, responses=ERROR_RESPONSES)
async def read_session(tracking_id: UUID, credential: str = Depends(session_credential),
                       use_case: GetTrackingSession = Depends(get_tracking_session)):
    try:
        return await use_case.execute(str(tracking_id), credential)
    except TrackingError as error:
        raise tracking_http_error(error) from error


@router.post("/{tracking_id}/observations", response_model=ObservationResult,
             responses=ERROR_RESPONSES)
async def receive_observation(tracking_id: UUID, location: ObservationRequest,
                              credential: str = Depends(session_credential),
                              use_case: ReceiveGpsObservation = Depends(get_receive_gps_observation)):
    try:
        return await use_case.execute(str(tracking_id), credential, GpsObservation(
            location.sequence, Coordinates(location.latitude, location.longitude),
            location.accuracy_m, location.captured_at))
    except TrackingError as error:
        raise tracking_http_error(error) from error
    except ValueError as error:
        raise HTTPException(422, detail={"code": "invalid_observation"}) from error

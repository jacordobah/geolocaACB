import logging

from fastapi import APIRouter, Depends, HTTPException
from pymongo.errors import PyMongoError

from ....application.use_cases.get_first_meeting_point import GetFirstMeetingPoint
from ....application.use_cases.validate_first_meeting_point import ValidateFirstMeetingPoint
from ....domain.errors import (
    AssociationConflict, FirstMeetingPointNotConfigured, InvalidGeographicData,
    InvalidIdentifier, MeetingPointNotFound, RouteNotFound,
)
from ....domain.model.first_meeting_point import FirstMeetingPoint
from ....domain.model.coordinates import Coordinates
from ....domain.model.geographic_validation import GeographicValidation
from ..schemas.geographic_validation import ValidateLocationRequest
from ..validation import JsonSafeValidationRoute
from ..dependencies import get_first_meeting_point, get_validate_first_meeting_point

logger = logging.getLogger(__name__)
router = APIRouter(prefix="/routes", tags=["Routes"])


def geographic_http_error(error: Exception) -> HTTPException:
    if isinstance(error, InvalidIdentifier):
        return HTTPException(422, detail={"code": "invalid_identifier"})
    if isinstance(error, RouteNotFound):
        return HTTPException(404, detail={"code": "route_not_found"})
    if isinstance(error, FirstMeetingPointNotConfigured):
        return HTTPException(409, detail={"code": "first_meeting_point_not_configured"})
    if isinstance(error, MeetingPointNotFound):
        return HTTPException(409, detail={"code": "referenced_meeting_point_not_found"})
    if isinstance(error, (AssociationConflict, InvalidGeographicData)):
        return HTTPException(409, detail={"code": "invalid_geographic_association"})
    logger.error("Could not retrieve first meeting point", exc_info=True)
    return HTTPException(503, detail={"code": "geographic_store_unavailable"})


@router.get("/{route_id}/first-meeting-point", response_model=FirstMeetingPoint)
async def first_meeting_point(
    route_id: str,
    use_case: GetFirstMeetingPoint = Depends(get_first_meeting_point),
):
    try:
        return await use_case.execute(route_id)
    except (InvalidIdentifier, RouteNotFound, FirstMeetingPointNotConfigured,
            MeetingPointNotFound, AssociationConflict, InvalidGeographicData, PyMongoError) as error:
        raise geographic_http_error(error) from error


router.route_class = JsonSafeValidationRoute


@router.post("/{route_id}/first-meeting-point/validate", response_model=GeographicValidation)
async def validate_first_meeting_point(
    route_id: str, location: ValidateLocationRequest,
    use_case: ValidateFirstMeetingPoint = Depends(get_validate_first_meeting_point),
):
    try:
        return await use_case.execute(
            route_id, Coordinates(latitude=location.latitude, longitude=location.longitude),
            location.accuracy_m, location.captured_at,
        )
    except (InvalidIdentifier, RouteNotFound, FirstMeetingPointNotConfigured,
            MeetingPointNotFound, AssociationConflict, InvalidGeographicData, PyMongoError) as error:
        raise geographic_http_error(error) from error

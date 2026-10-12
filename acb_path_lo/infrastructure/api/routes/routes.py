import logging

from fastapi import APIRouter, Depends, HTTPException
from pymongo.errors import PyMongoError

from ....application.use_cases.get_first_meeting_point import GetFirstMeetingPoint
from ....domain.errors import (
    AssociationConflict, FirstMeetingPointNotConfigured, InvalidGeographicData,
    InvalidIdentifier, MeetingPointNotFound, RouteNotFound,
)
from ....domain.model.first_meeting_point import FirstMeetingPoint
from ..dependencies import get_first_meeting_point

logger = logging.getLogger(__name__)
router = APIRouter(prefix="/routes", tags=["Routes"])


@router.get("/{route_id}/first-meeting-point", response_model=FirstMeetingPoint)
async def first_meeting_point(
    route_id: str,
    use_case: GetFirstMeetingPoint = Depends(get_first_meeting_point),
):
    try:
        return await use_case.execute(route_id)
    except InvalidIdentifier as error:
        raise HTTPException(422, detail={"code": "invalid_identifier"}) from error
    except RouteNotFound as error:
        raise HTTPException(404, detail={"code": "route_not_found"}) from error
    except FirstMeetingPointNotConfigured as error:
        raise HTTPException(409, detail={"code": "first_meeting_point_not_configured"}) from error
    except MeetingPointNotFound as error:
        raise HTTPException(409, detail={"code": "referenced_meeting_point_not_found"}) from error
    except (AssociationConflict, InvalidGeographicData) as error:
        raise HTTPException(409, detail={"code": "invalid_geographic_association"}) from error
    except PyMongoError as error:
        logger.exception("Could not retrieve first meeting point")
        raise HTTPException(503, detail={"code": "geographic_store_unavailable"}) from error

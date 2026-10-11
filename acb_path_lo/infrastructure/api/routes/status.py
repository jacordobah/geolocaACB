import logging

from fastapi import APIRouter, Depends, HTTPException
from pymongo.errors import PyMongoError

from ....application.use_cases.save_route_status import SaveRouteStatus
from ....domain.model.route_status import RouteStatus
from ..dependencies import get_save_route_status

logger = logging.getLogger(__name__)

router = APIRouter(prefix="/status", tags=["status"])


@router.post("/notify", status_code=201)
async def notify_status(
    route_status: RouteStatus,
    use_case: SaveRouteStatus = Depends(get_save_route_status),
):
    try:
        await use_case.execute(route_status)
    except PyMongoError as error:
        logger.exception("Could not save route status to MongoDB")
        raise HTTPException(
            status_code=503,
            detail="The route status could not be saved to MongoDB",
        ) from error

    return {"message": "Route status saved successfully"}

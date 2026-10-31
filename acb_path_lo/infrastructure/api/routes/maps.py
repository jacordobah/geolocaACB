from fastapi import APIRouter, Depends

from ....application.use_cases.list_meeting_points import ListMeetingPoints
from ....application.use_cases.list_routes import ListRoutes
from ..dependencies import get_list_meeting_points, get_list_routes

router = APIRouter()


@router.get("/routes", tags=["Routes"])
async def list_routes(
    use_case: ListRoutes = Depends(get_list_routes),
):
    return await use_case.execute()


@router.get("/meeting-points", tags=["Meeting points"])
async def list_meeting_points(
    use_case: ListMeetingPoints = Depends(get_list_meeting_points),
):
    return await use_case.execute()
from fastapi import Request

from ...application.use_cases.import_kmz import ImportKmz
from ...application.use_cases.list_meeting_points import ListMeetingPoints
from ...application.use_cases.list_routes import ListRoutes
from ...application.use_cases.save_route_status import SaveRouteStatus
from ..kmz.kmz_reader_impl import KmzReaderImpl

def get_import_kmz(request: Request) -> ImportKmz:
    return ImportKmz(
        KmzReaderImpl(),
        request.app.state.map_repository,
    )

def get_save_route_status(request: Request) -> SaveRouteStatus:
    return SaveRouteStatus(request.app.state.route_status_repository)

def get_list_routes(request: Request) -> ListRoutes:
    return ListRoutes(request.app.state.map_repository)

def get_list_meeting_points(request: Request) -> ListMeetingPoints:
    return ListMeetingPoints(request.app.state.map_repository)
from datetime import UTC, datetime

from fastapi import HTTPException, Request

from ...application.use_cases.import_kmz import ImportKmz
from ...application.use_cases.get_first_meeting_point import GetFirstMeetingPoint
from ...application.use_cases.get_route_endpoints import GetRouteEndpoints
from ...application.use_cases.get_route import GetRoute
from ...application.use_cases.validate_first_meeting_point import ValidateFirstMeetingPoint
from ...application.use_cases.list_meeting_points import ListMeetingPoints
from ...application.use_cases.list_routes import ListRoutes
from ...application.use_cases.save_route_status import SaveRouteStatus
from ..kmz.kmz_reader_impl import KmzReaderImpl
from ...application.use_cases.get_tracking_session import GetTrackingSession
from ...application.use_cases.receive_gps_observation import ReceiveGpsObservation
from ...application.use_cases.list_positions import ListPositions
from ..security.tracking_credentials import verify_credential


def get_tracking_session(request: Request) -> GetTrackingSession:
    return GetTrackingSession(request.app.state.tracking_repository, verify_credential)


def get_receive_gps_observation(request: Request) -> ReceiveGpsObservation:
    return ReceiveGpsObservation(get_tracking_session(request), lambda: datetime.now(UTC),
                                 getattr(request.app.state, "position_repository", None))


def get_list_positions(request: Request) -> ListPositions:
    return ListPositions(get_tracking_session(request), request.app.state.position_repository,
                         lambda: datetime.now(UTC))


def get_first_meeting_point(request: Request) -> GetFirstMeetingPoint:
    return GetFirstMeetingPoint(request.app.state.map_repository)


def get_route_endpoints(request: Request) -> GetRouteEndpoints:
    return GetRouteEndpoints(request.app.state.map_repository)


def get_route(request: Request) -> GetRoute:
    return GetRoute(request.app.state.map_repository)


def get_validate_first_meeting_point(request: Request) -> ValidateFirstMeetingPoint:
    policy = getattr(request.app.state, "geofence_policy", None)
    if policy is None:
        raise HTTPException(503, detail={"code": "geofence_configuration_unavailable"})
    return ValidateFirstMeetingPoint(
        get_first_meeting_point(request), policy, lambda: datetime.now(UTC),
    )

def get_import_kmz(request: Request) -> ImportKmz:
    return ImportKmz(
        KmzReaderImpl(),
        request.app.state.map_repository,
        getattr(request.app.state, "endpoint_policy", None),
    )

def get_save_route_status(request: Request) -> SaveRouteStatus:
    return SaveRouteStatus(request.app.state.route_status_repository)

def get_list_routes(request: Request) -> ListRoutes:
    return ListRoutes(request.app.state.map_repository)

def get_list_meeting_points(request: Request) -> ListMeetingPoints:
    return ListMeetingPoints(request.app.state.map_repository)

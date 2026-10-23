class GeographicDataError(Exception):
    """Expected failure when resolving or configuring geographic data."""


class InvalidIdentifier(GeographicDataError):
    pass


class RouteNotFound(GeographicDataError):
    pass


class FirstMeetingPointNotConfigured(GeographicDataError):
    pass


class RouteEndpointsNotConfigured(GeographicDataError):
    pass


class MeetingPointNotFound(GeographicDataError):
    pass


class AssociationConflict(GeographicDataError):
    pass


class InvalidGeographicData(GeographicDataError):
    pass

from collections.abc import Callable
from datetime import UTC, datetime

from .get_first_meeting_point import GetFirstMeetingPoint
from ...domain.model.coordinates import Coordinates
from ...domain.model.geographic_validation import GeographicValidation, GeofencePolicy
from ...domain.services.geographic_validation import evaluate_geofence, haversine_distance_m


class ValidateFirstMeetingPoint:
    def __init__(
        self, get_first_meeting_point: GetFirstMeetingPoint,
        policy: GeofencePolicy, clock: Callable[[], datetime],
    ):
        self.get_first_meeting_point = get_first_meeting_point
        self.policy = policy
        self.clock = clock

    async def execute(
        self, route_id: str, coordinates: Coordinates,
        accuracy_m: float | None = None, captured_at: datetime | None = None,
    ) -> GeographicValidation:
        if captured_at is not None and captured_at.utcoffset() is None:
            raise ValueError("captured_at requires a timezone")
        point = await self.get_first_meeting_point.execute(route_id)
        distance = haversine_distance_m(
            coordinates, Coordinates(latitude=point.latitude, longitude=point.longitude),
        )
        evaluation = evaluate_geofence(distance, self.policy, accuracy_m)
        validated_at = self.clock()
        if validated_at.utcoffset() is None:
            raise ValueError("Server clock requires a timezone")
        return GeographicValidation(
            route_id=point.route_id, meeting_point_id=point.meeting_point_id,
            distance_m=distance, radius_m=self.policy.radius_m,
            point_inside=evaluation.point_inside, status=evaluation.status,
            quality=evaluation.quality,
            captured_at=captured_at.astimezone(UTC) if captured_at is not None else None,
            validated_at=validated_at.astimezone(UTC),
        )

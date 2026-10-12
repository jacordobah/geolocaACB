from collections.abc import Callable
from datetime import datetime

from .get_tracking_session import GetTrackingSession
from ...domain.model.coordinates import Coordinates
from ...domain.model.gps_position import GpsPosition
from ...domain.model.geographic_validation import GeofencePolicy
from ...domain.model.gps_observation import GpsObservation, utc_milliseconds
from ...domain.model.tracking_session import FirstArrival, LastObservation, ObservationResult
from ...domain.services.geographic_validation import haversine_distance_m, evaluate_geofence
from ...domain.services.observation_validation import validate_observation_time
from ...domain.tracking_errors import TrackingError
from ...domain.repositories.position_repository import PositionRepository
from .repair_pending_position import RepairPendingPosition


class ReceiveGpsObservation:
    def __init__(self, get_session: GetTrackingSession, clock: Callable[[], datetime],
                 positions: PositionRepository | None = None):
        self.get_session, self.clock = get_session, clock
        self.positions = positions

    async def execute(self, tracking_id: str, credential: str,
                      observation: GpsObservation) -> ObservationResult:
        received_at = utc_milliseconds(self.clock())
        fingerprint = observation.fingerprint()
        for _ in range(5):
            session = await self.get_session.authenticate(tracking_id, credential)
            if session.pending_position is not None:
                if self.positions is None:
                    raise TrackingError("position_history_pending")
                await RepairPendingPosition(self.get_session.repository, self.positions,
                                            self.clock).execute(tracking_id)
                continue
            last = session.last_observation
            # Recover a lost response even after disablement or expiry of its capture.
            if last and observation.sequence == last.sequence:
                if fingerprint == last.request_hash:
                    return last.response
                raise TrackingError("observation_sequence_conflict")
            if last and observation.sequence < last.sequence:
                raise TrackingError("observation_out_of_order")
            if session.state != "enabled":
                raise TrackingError("tracking_disabled")
            validate_observation_time(session, observation, received_at)
            target = session.target
            distance = haversine_distance_m(observation.coordinates,
                                            Coordinates(target.latitude, target.longitude))
            evaluation = evaluate_geofence(distance, GeofencePolicy(
                session.policy.radius_m, session.policy.max_accuracy_m), observation.accuracy_m)
            validated_at = utc_milliseconds(self.clock())
            arrival = session.first_arrival
            new_arrival = None
            if arrival is None and evaluation.status == "inside":
                new_arrival = FirstArrival(
                    observation.sequence, session.guide_id, session.route_id,
                    target.meeting_point_id, target.latitude, target.longitude,
                    observation.coordinates.latitude, observation.coordinates.longitude,
                    observation.accuracy_m, distance, observation.captured_at,
                    received_at, validated_at)
                arrival = new_arrival
            result = ObservationResult(
                tracking_id, observation.sequence, session.guide_id, session.route_id,
                target.meeting_point_id, distance, session.policy.radius_m,
                evaluation.point_inside, evaluation.status, evaluation.quality,
                observation.captured_at, received_at, validated_at, arrival)
            receipt = LastObservation(observation.sequence, observation.captured_at,
                                      fingerprint, result)
            if session.history_policy is None:
                if await self.get_session.repository.accept(session, receipt, new_arrival):
                    return result
            else:
                if self.positions is None:
                    raise TrackingError("position_history_unavailable")
                position = GpsPosition(
                    tracking_id, session.guide_id, session.route_id, observation.sequence,
                    observation.coordinates.latitude, observation.coordinates.longitude,
                    observation.accuracy_m, observation.captured_at, received_at,
                    session.history_policy.expires_at(received_at), evaluation.status,
                    fingerprint)
                if await self.get_session.repository.accept_with_pending(
                        session, receipt, new_arrival, position):
                    await RepairPendingPosition(self.get_session.repository, self.positions,
                                                self.clock).execute(tracking_id)
                    return result
        raise TrackingError("tracking_busy")

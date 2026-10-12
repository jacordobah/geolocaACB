import asyncio
from dataclasses import asdict
from datetime import UTC

from bson.codec_options import CodecOptions
from pymongo.errors import DuplicateKeyError, PyMongoError
from pymongo.write_concern import WriteConcern

from ...domain.model.first_meeting_point import FirstMeetingPoint
from ...domain.model.gps_position import GpsPosition, HistoryPolicy
from ...domain.model.geographic_validation import MeasurementQuality
from ...domain.model.tracking_session import (
    FirstArrival, LastObservation, ObservationResult, SessionIssuance, TrackingPolicy, TrackingSession,
)
from ...domain.tracking_errors import TrackingError, TrackingStoreUnavailable


def decode_session(document):
    values = dict(document)
    values["tracking_id"] = values.pop("_id")
    values["target"] = FirstMeetingPoint(**values["target"])
    values["policy"] = TrackingPolicy(**values["policy"])
    if values.get("history_policy"):
        values["history_policy"] = HistoryPolicy(**values["history_policy"])
    if values.get("pending_position"):
        values["pending_position"] = GpsPosition(**values["pending_position"])
    if values.get("issuance"):
        values["issuance"] = SessionIssuance(**values["issuance"])
    if values["first_arrival"]:
        values["first_arrival"] = FirstArrival(**values["first_arrival"])
    if values["last_observation"]:
        last = dict(values["last_observation"])
        result = dict(last["response"])
        result["quality"] = MeasurementQuality(**result["quality"])
        if result["first_arrival"]:
            result["first_arrival"] = FirstArrival(**result["first_arrival"])
        last["response"] = ObservationResult(**result)
        values["last_observation"] = LastObservation(**last)
    return TrackingSession(**values)


class MongoTrackingRepository:
    def __init__(self, database):
        # No index creation or writes on construction. Mongo creates _id on insert.
        self.collection = database.get_collection(
            "tracking_sessions", codec_options=CodecOptions(tz_aware=True, tzinfo=UTC),
            write_concern=WriteConcern(w=1, j=True))

    async def _run(self, operation, *args, **kwargs):
        try:
            return await asyncio.to_thread(operation, *args, **kwargs)
        except DuplicateKeyError as error:
            raise TrackingError("tracking_session_conflict") from error
        except PyMongoError as error:
            # A timeout can mean the write was applied. Never retry unconditionally.
            raise TrackingStoreUnavailable() from error

    async def create(self, session):
        document = asdict(session)
        document["_id"] = document.pop("tracking_id")
        await self._run(self.collection.insert_one, document)

    async def get(self, tracking_id):
        document = await self._run(self.collection.find_one, {"_id": tracking_id})
        if document is None:
            return None
        try:
            return decode_session(document)
        except (KeyError, TypeError, ValueError) as error:
            raise TrackingStoreUnavailable() from error

    async def get_by_issuance_key(self, key_hash):
        document = await self._run(self.collection.find_one, {"issuance.key_hash": key_hash})
        if document is None:
            return None
        try:
            return decode_session(document)
        except (KeyError, TypeError, ValueError) as error:
            raise TrackingStoreUnavailable() from error

    async def ensure_issuance_index(self):
        await self._run(self.collection.create_index, "issuance.key_hash", unique=True,
                        partialFilterExpression={"issuance.key_hash": {"$type": "string"}},
                        name="unique_issuance_key")

    async def accept(self, session, observation, arrival):
        query = {"_id": session.tracking_id, "version": session.version,
                 "state": "enabled"}
        changes = {"last_observation": asdict(observation)}
        if arrival is not None:
            query["first_arrival"] = None
            changes["first_arrival"] = asdict(arrival)
        result = await self._run(self.collection.update_one, query,
                                 {"$set": changes, "$inc": {"version": 1}}, upsert=False)
        return result.matched_count == 1

    async def disable(self, session, disabled_at):
        result = await self._run(self.collection.update_one,
            {"_id": session.tracking_id, "version": session.version, "state": "enabled"},
            {"$set": {"state": "disabled", "disabled_at": disabled_at},
             "$inc": {"version": 1}}, upsert=False)
        return result.matched_count == 1

    async def accept_with_pending(self, session, observation, arrival, position):
        query = {"_id": session.tracking_id, "version": session.version,
                 "state": "enabled", "pending_position": None}
        changes = {"last_observation": asdict(observation),
                   "pending_position": asdict(position)}
        if arrival is not None:
            query["first_arrival"] = None
            changes["first_arrival"] = asdict(arrival)
        result = await self._run(self.collection.update_one, query,
                                 {"$set": changes, "$inc": {"version": 1}}, upsert=False)
        return result.matched_count == 1

    async def clear_pending(self, session):
        pending = session.pending_position
        if pending is None:
            return True
        result = await self._run(self.collection.update_one,
            {"_id": session.tracking_id, "version": session.version,
             "pending_position.sequence": pending.sequence,
             "pending_position.request_hash": pending.request_hash},
            {"$set": {"pending_position": None}, "$inc": {"version": 1}}, upsert=False)
        return result.matched_count == 1

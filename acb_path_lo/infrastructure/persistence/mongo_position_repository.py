import asyncio
from dataclasses import asdict
from datetime import UTC

from bson.codec_options import CodecOptions
from pymongo import ASCENDING
from pymongo.errors import DuplicateKeyError, PyMongoError
from pymongo.write_concern import WriteConcern

from ...domain.model.gps_position import GpsPosition
from ...domain.tracking_errors import TrackingStoreUnavailable


class MongoPositionRepository:
    def __init__(self, database):
        self.collection = database.get_collection(
            "gps_positions", codec_options=CodecOptions(tz_aware=True, tzinfo=UTC),
            write_concern=WriteConcern(w=1, j=True))
        self.collection.create_index([("tracking_id", ASCENDING), ("sequence", ASCENDING)],
                                     unique=True)
        self.collection.create_index("expires_at", expireAfterSeconds=0)

    async def ensure(self, position: GpsPosition) -> None:
        try:
            await asyncio.to_thread(self.collection.insert_one, asdict(position))
            return
        except DuplicateKeyError:
            pass
        except PyMongoError as error:
            # An acknowledgement failure may follow a successful insert.
            try:
                existing = await asyncio.to_thread(self.collection.find_one,
                    {"tracking_id": position.tracking_id, "sequence": position.sequence})
            except PyMongoError:
                raise TrackingStoreUnavailable() from error
            if existing is None:
                raise TrackingStoreUnavailable() from error
            if self._same(existing, position):
                return
            raise TrackingStoreUnavailable() from error
        try:
            existing = await asyncio.to_thread(self.collection.find_one,
                {"tracking_id": position.tracking_id, "sequence": position.sequence})
        except PyMongoError as error:
            raise TrackingStoreUnavailable() from error
        if not self._same(existing, position):
            raise TrackingStoreUnavailable()

    @staticmethod
    def _same(existing, position):
        return existing is not None and {
            key: value for key, value in existing.items() if key != "_id"
        } == asdict(position)

    async def list_page(self, tracking_id, after_sequence, max_sequence, now, limit):
        query = {"tracking_id": tracking_id,
                 "sequence": {"$gt": after_sequence, "$lte": max_sequence},
                 "expires_at": {"$gt": now}}
        try:
            documents = await asyncio.to_thread(lambda: list(
                self.collection.find(query).sort("sequence", ASCENDING).limit(limit + 1)))
        except PyMongoError as error:
            raise TrackingStoreUnavailable() from error
        has_more = len(documents) > limit
        return [GpsPosition(**{k: v for k, v in doc.items() if k != "_id"})
                for doc in documents[:limit]], has_more

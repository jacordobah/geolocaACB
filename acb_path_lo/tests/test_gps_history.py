from dataclasses import replace
from datetime import UTC, datetime, timedelta
import unittest
from unittest.mock import patch
from uuid import uuid4

from acb_path_lo.application.use_cases.list_positions import ListPositions
from acb_path_lo.application.use_cases.receive_gps_observation import ReceiveGpsObservation
from acb_path_lo.application.use_cases.repair_pending_position import RepairPendingPosition
from acb_path_lo.domain.model.gps_position import GpsPosition, HistoryPolicy
from acb_path_lo.domain.tracking_errors import TrackingError, TrackingStoreUnavailable
from acb_path_lo.infrastructure.config.history import load_history_policy
from acb_path_lo.infrastructure.persistence.mongo_position_repository import MongoPositionRepository
from pymongo.errors import AutoReconnect
from acb_path_lo.infrastructure.security.tracking_credentials import verify_credential
from acb_path_lo.tests.test_tracking import TrackingFixture, MemoryTrackingRepository


class HistoryTrackingRepository(MemoryTrackingRepository):
    def __init__(self):
        super().__init__()
        self.fail_clear = False
        self.fail_accept = False

    async def accept_with_pending(self, session, receipt, arrival, position):
        current = self.sessions[session.tracking_id]
        if (current.version != session.version or current.state != 'enabled'
                or current.pending_position is not None
                or arrival is not None and current.first_arrival is not None):
            return False
        self.sessions[session.tracking_id] = replace(current, version=current.version + 1,
            last_observation=receipt, first_arrival=arrival or current.first_arrival,
            pending_position=position)
        self.writes += 1
        if self.fail_accept:
            self.fail_accept = False
            raise TrackingStoreUnavailable()
        return True

    async def clear_pending(self, session):
        current = self.sessions[session.tracking_id]
        if current.pending_position is None:
            return True
        if (current.version != session.version
                or current.pending_position.sequence != session.pending_position.sequence
                or current.pending_position.request_hash != session.pending_position.request_hash):
            return False
        self.sessions[session.tracking_id] = replace(current, version=current.version + 1,
                                                     pending_position=None)
        self.writes += 1
        if self.fail_clear:
            self.fail_clear = False
            raise TrackingStoreUnavailable()
        return True


class MemoryPositionRepository:
    def __init__(self):
        self.documents = {}
        self.writes = 0
        self.fail_before = False
        self.fail_after = False

    async def ensure(self, position):
        if self.fail_before:
            self.fail_before = False
            raise TrackingStoreUnavailable()
        key = (position.tracking_id, position.sequence)
        existing = self.documents.get(key)
        if existing is not None and existing != position:
            raise TrackingStoreUnavailable()
        if existing is None:
            self.documents[key] = position
            self.writes += 1
        if self.fail_after:
            self.fail_after = False
            raise TrackingStoreUnavailable()

    async def list_page(self, tracking_id, after_sequence, max_sequence, now, limit):
        values = sorted((p for (tid, seq), p in self.documents.items()
                         if tid == tracking_id and after_sequence < seq <= max_sequence
                         and p.expires_at > now), key=lambda p: p.sequence)
        return values[:limit], len(values) > limit


class HistoryTests(TrackingFixture):
    async def asyncSetUp(self):
        await super().asyncSetUp()
        self.repository = HistoryTrackingRepository()
        self.positions = MemoryPositionRepository()
        self.session = replace(self.session, history_policy=HistoryPolicy(8, 0))
        await self.repository.create(self.session)
        self.get.repository = self.repository
        self.receive = ReceiveGpsObservation(self.get, lambda: self.now, self.positions)
        self.disable.repository = self.repository
        self.list = ListPositions(self.get, self.positions, lambda: self.now)
        self.repair = RepairPendingPosition(self.repository, self.positions, lambda: self.now)

    async def test_store_all_geographic_statuses_and_preserve_arrival(self):
        values = [(1, 4.01, 5, 'outside'), (2, 4, None, 'indeterminate'),
                  (3, 4, 5, 'inside'), (4, 4.01, 5, 'outside')]
        arrival = None
        for sequence, latitude, accuracy, status in values:
            result = await self.send(self.observation(sequence, latitude, accuracy))
            self.assertEqual(result.status, status)
            self.assertEqual(self.positions.documents[(self.identifier, sequence)].status, status)
            if sequence == 3:
                arrival = result.first_arrival
            if sequence == 4:
                self.assertEqual(result.first_arrival, arrival)
        self.assertEqual((await self.repository.get(self.identifier)).first_arrival, arrival)
        self.assertEqual(len(self.positions.documents), 4)

    async def test_rejected_and_replay_do_not_duplicate(self):
        first = self.observation(4)
        original = await self.send(first)
        self.assertEqual(await self.send(first), original)
        self.assertEqual(self.positions.writes, 1)
        await self.assert_code('observation_sequence_conflict',
                               self.send(self.observation(4, 4.01)))
        await self.assert_code('observation_out_of_order', self.send(self.observation(3)))
        await self.assert_code('observation_too_old',
                               self.send(self.observation(5, captured=self.now - timedelta(minutes=1))))
        await self.disable.execute(self.identifier)
        await self.assert_code('tracking_disabled', self.send(self.observation(5)))
        self.assertEqual(self.positions.writes, 1)
        self.now += timedelta(hours=1)
        self.assertEqual(await self.send(first), original)

    async def test_failure_after_accept_and_repair_after_disable(self):
        self.positions.fail_before = True
        first = self.observation()
        await self.assert_code('tracking_store_unavailable', self.send(first))
        pending = (await self.repository.get(self.identifier)).pending_position
        self.assertIsNotNone(pending)
        self.assertEqual(self.positions.writes, 0)
        await self.disable.execute(self.identifier)
        result = await self.send(first)
        self.assertEqual(result.status, 'inside')
        self.assertEqual(self.positions.writes, 1)
        self.assertIsNone((await self.repository.get(self.identifier)).pending_position)
        self.assertEqual((await self.repository.get(self.identifier)).first_arrival, result.first_arrival)

    async def test_uncertain_accept_insert_and_clear(self):
        first = self.observation()
        self.repository.fail_accept = True
        await self.assert_code('tracking_store_unavailable', self.send(first))
        self.positions.fail_after = True
        await self.assert_code('tracking_store_unavailable', self.send(first))
        self.assertEqual(self.positions.writes, 1)
        self.repository.fail_clear = True
        await self.assert_code('tracking_store_unavailable', self.send(first))
        self.assertEqual(self.positions.writes, 1)
        recovered = await self.send(first)
        self.assertEqual(recovered.first_arrival.sequence, 1)
        self.assertEqual(self.positions.writes, 1)
        self.assertIsNone((await self.repository.get(self.identifier)).pending_position)

    async def test_pending_blocks_next_sequence_and_get(self):
        self.positions.fail_before = True
        await self.assert_code('tracking_store_unavailable', self.send())
        self.positions.fail_before = True
        await self.assert_code('tracking_store_unavailable', self.send(self.observation(2)))
        await self.assert_code('position_history_pending', self.list.execute(
            self.identifier, self.credential, 0, 100))
        self.assertEqual((await self.repository.get(self.identifier)).last_observation.sequence, 1)
        await self.repair.execute(self.identifier)
        self.assertEqual((await self.send(self.observation(2))).sequence, 2)

    async def test_expired_pending_closes_without_reinsertion(self):
        self.positions.fail_before = True
        first = self.observation()
        await self.assert_code('tracking_store_unavailable', self.send(first))
        self.now = (await self.repository.get(self.identifier)).pending_position.expires_at
        self.assertEqual((await self.send(first)).sequence, 1)
        self.assertEqual(self.positions.writes, 0)
        self.assertIsNone((await self.repository.get(self.identifier)).pending_position)

    async def test_pages_expiry_isolation_and_legacy_session(self):
        for sequence in (2, 4, 8):
            await self.send(self.observation(sequence))
        first = await self.list.execute(self.identifier, self.credential, 0, 2)
        self.assertEqual([p.sequence for p in first.positions], [2, 4])
        self.assertEqual(first.next_after_sequence, 4)
        second = await self.list.execute(self.identifier, self.credential, 4, 2)
        self.assertEqual([p.sequence for p in second.positions], [8])
        self.assertIsNone(second.next_after_sequence)
        foreign = replace(self.positions.documents[self.identifier, 8], tracking_id=str(uuid4()))
        self.positions.documents[(foreign.tracking_id, 8)] = foreign
        self.assertEqual(len((await self.list.execute(self.identifier, self.credential, 0, 500)).positions), 3)
        self.now = self.positions.documents[self.identifier, 8].expires_at
        self.assertEqual((await self.list.execute(self.identifier, self.credential, 0, 500)).positions, [])
        self.repository.sessions[self.identifier] = replace(self.session, history_policy=None)
        await self.assert_code('history_not_configured', self.list.execute(
            self.identifier, self.credential, 0, 100))

    def test_cutoff_and_configuration(self):
        policy = HistoryPolicy(8, 0)
        self.assertEqual(load_history_policy({'ACB_GPS_HISTORY_CUTOFF_BOGOTA': '08:00'}), policy)
        for value in ['', '8:00', '24:00', '08:60', '08:00:00', 'abc']:
            self.assertIsNone(load_history_policy({'ACB_GPS_HISTORY_CUTOFF_BOGOTA': value}))
        self.assertIsNone(load_history_policy({}))
        for received, expiry in [
            (datetime(2026, 10, 13, 4, 58, tzinfo=UTC), datetime(2026, 10, 13, 13, tzinfo=UTC)),
            (datetime(2026, 10, 13, 5, 2, tzinfo=UTC), datetime(2026, 10, 14, 13, tzinfo=UTC)),
        ]:
            self.assertEqual(policy.expires_at(received), expiry)

    async def test_adapter_checks_unique_key_after_uncertain_write(self):
        position = GpsPosition(self.identifier, 'guide-test', 'route-test', 1, 4, -74,
            5, self.now, self.now, HistoryPolicy(8, 0).expires_at(self.now),
            'inside', 'known-hash')
        class Collection:
            document = None
            write_before_error = True
            def insert_one(collection, document):
                if collection.write_before_error and collection.document is None:
                    collection.document = {'_id': 'internal', **document}
                raise AutoReconnect('acknowledgement lost after write')
            def find_one(collection, query):
                return collection.document
        repository = MongoPositionRepository.__new__(MongoPositionRepository)
        repository.collection = Collection()
        await repository.ensure(position)
        with self.assertRaises(TrackingStoreUnavailable):
            await repository.ensure(replace(position, request_hash='different'))
        repository.collection.document = None
        repository.collection.write_before_error = False
        with self.assertRaises(TrackingStoreUnavailable):
            await repository.ensure(position)

import unittest
from datetime import UTC, datetime, timedelta, timezone
from unittest.mock import AsyncMock

from acb_path_lo.application.use_cases.get_first_meeting_point import GetFirstMeetingPoint
from acb_path_lo.application.use_cases.validate_first_meeting_point import ValidateFirstMeetingPoint
from acb_path_lo.domain.errors import FirstMeetingPointNotConfigured, RouteNotFound, InvalidGeographicData
from acb_path_lo.domain.model.coordinates import Coordinates
from acb_path_lo.domain.model.first_meeting_point import FirstMeetingPoint
from acb_path_lo.domain.model.geographic_validation import GeofencePolicy


class ValidationApplicationTests(unittest.IsolatedAsyncioTestCase):
    async def test_reuses_single_query_and_injected_clock(self):
        repository = AsyncMock()
        repository.get_first_meeting_point.return_value = FirstMeetingPoint('route', 'point', 'PE01', -74, 4)
        now = datetime(2026, 10, 12, 15, tzinfo=UTC)
        capture = datetime(2000, 1, 1, 10, tzinfo=timezone(timedelta(hours=-5)))
        use_case = ValidateFirstMeetingPoint(GetFirstMeetingPoint(repository), GeofencePolicy(30, 10), lambda: now)
        result = await use_case.execute('route', Coordinates(4, -74), 5, capture)
        self.assertEqual(result.distance_m, 0)
        self.assertEqual(result.status, 'inside')
        self.assertEqual(result.captured_at, capture.astimezone(UTC))
        self.assertEqual(result.validated_at, now)
        repository.get_first_meeting_point.assert_awaited_once_with('route')
        repository.set_first_meeting_point.assert_not_called()
        repository.save_import.assert_not_called()

    async def test_propagates_existing_geographic_errors(self):
        for error in [RouteNotFound, FirstMeetingPointNotConfigured, InvalidGeographicData]:
            repository = AsyncMock()
            repository.get_first_meeting_point.side_effect = error('test')
            use_case = ValidateFirstMeetingPoint(GetFirstMeetingPoint(repository), GeofencePolicy(30, 10), lambda: datetime.now(UTC))
            with self.subTest(error=error), self.assertRaises(error):
                await use_case.execute('route', Coordinates(0, 0), 5)

    async def test_naive_timestamps_are_rejected(self):
        repository = AsyncMock()
        repository.get_first_meeting_point.return_value = FirstMeetingPoint('route', 'point', 'PE01', 0, 0)
        use_case = ValidateFirstMeetingPoint(GetFirstMeetingPoint(repository), GeofencePolicy(30, 10), lambda: datetime(2026, 1, 1))
        with self.assertRaises(ValueError):
            await use_case.execute('route', Coordinates(0, 0), 5)
        with self.assertRaises(ValueError):
            await use_case.execute('route', Coordinates(0, 0), 5, datetime(2026, 1, 1))

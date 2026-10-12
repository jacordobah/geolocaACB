import math
import unittest
from dataclasses import FrozenInstanceError

from acb_path_lo.domain.model.coordinates import Coordinates
from acb_path_lo.domain.model.geographic_validation import GeofencePolicy
from acb_path_lo.domain.services.geographic_validation import (
    EARTH_RADIUS_M, evaluate_geofence, haversine_distance_m,
)


class DistanceTests(unittest.TestCase):
    def test_coordinates_are_immutable(self):
        coordinates = Coordinates(0, 0)
        with self.assertRaises(FrozenInstanceError):
            coordinates.latitude = 1

    def test_identical_coordinates_and_equivalent_longitudes(self):
        for point in [Coordinates(0, 0), Coordinates(90, 180), Coordinates(-90, -180)]:
            self.assertEqual(haversine_distance_m(point, point), 0)
        self.assertEqual(haversine_distance_m(Coordinates(0, 180), Coordinates(0, -180)), 0)
        self.assertEqual(haversine_distance_m(Coordinates(90, 0), Coordinates(90, 150)), 0)

    def test_known_spherical_distances(self):
        for destination, angle in [(Coordinates(0, 1), math.pi / 180),
                                   (Coordinates(0, 90), math.pi / 2),
                                   (Coordinates(0, 180), math.pi)]:
            with self.subTest(destination=destination):
                self.assertAlmostEqual(haversine_distance_m(Coordinates(0, 0), destination),
                                       EARTH_RADIUS_M * angle, delta=0.001)

    def test_antimeridian_poles_and_symmetry(self):
        first, second = Coordinates(0, 179.9), Coordinates(0, -179.9)
        self.assertAlmostEqual(haversine_distance_m(first, second),
                               EARTH_RADIUS_M * math.radians(0.2), delta=0.001)
        self.assertAlmostEqual(haversine_distance_m(first, second),
                               haversine_distance_m(second, first), delta=0.001)
        self.assertAlmostEqual(haversine_distance_m(Coordinates(-90, 0), Coordinates(90, 50)),
                               EARTH_RADIUS_M * math.pi, delta=0.001)
        self.assertTrue(math.isfinite(haversine_distance_m(Coordinates(89.999, 180), Coordinates(89.999, -179))))

    def test_invalid_coordinates(self):
        for latitude, longitude in [(91, 0), (-91, 0), (0, 181), (0, -181),
                                    (True, 0), (0, False), ('4', 0),
                                    (float('nan'), 0), (0, float('inf'))]:
            with self.subTest(values=(latitude, longitude)), self.assertRaises(ValueError):
                Coordinates(latitude, longitude)


class GeofenceTests(unittest.TestCase):
    def test_nominal_boundary_without_rounding(self):
        policy = GeofencePolicy(30, 10)
        for distance, inside in [(math.nextafter(30, 0), True), (30, True),
                                 (math.nextafter(30, math.inf), False)]:
            result = evaluate_geofence(distance, policy, 1)
            self.assertEqual(result.point_inside, inside)
            self.assertEqual(result.status, 'indeterminate')

    def test_interval_boundaries(self):
        policy = GeofencePolicy(30, 10)
        cases = [(0, 5, 'inside'), (25, 5, 'inside'), (26, 5, 'indeterminate'),
                 (35, 5, 'indeterminate'), (36, 5, 'outside')]
        for distance, accuracy, status in cases:
            with self.subTest(distance=distance):
                result = evaluate_geofence(distance, policy, accuracy)
                self.assertEqual(result.status, status)
                self.assertEqual(result.quality.estimated_min_distance_m, max(0, distance-accuracy))
                self.assertEqual(result.quality.estimated_max_distance_m, distance+accuracy)

    def test_missing_and_insufficient_accuracy(self):
        policy = GeofencePolicy(30, 10)
        for accuracy, reason in [(None, 'accuracy_missing'), (11, 'accuracy_exceeds_limit')]:
            for distance in (0, 100):
                result = evaluate_geofence(distance, policy, accuracy)
                self.assertEqual((result.status, result.quality.reason), ('indeterminate', reason))
        self.assertEqual(evaluate_geofence(0, policy, 10).status, 'inside')

    def test_invalid_policy_accuracy_and_distance(self):
        for value in [0, -1, True, '30', float('nan'), float('inf')]:
            with self.subTest(value=value):
                with self.assertRaises(ValueError):
                    GeofencePolicy(value, 10)
                with self.assertRaises(ValueError):
                    GeofencePolicy(30, value)
                with self.assertRaises(ValueError):
                    evaluate_geofence(0, GeofencePolicy(30, 10), value)
        for value in [-1, float('nan'), float('inf'), True]:
            with self.assertRaises(ValueError):
                evaluate_geofence(value, GeofencePolicy(30, 10), 1)

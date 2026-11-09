package geo

import (
	"fmt"
	"math"
)

// Restrict long, polar and extensive GeoJSON lines: KML/GeoJSON does not
// establish that a long jump follows the shortest great-circle arc. In the
// available school KMZ every segment is below 1.6 km; a longer jump needs
// route-data review before this spherical interpretation can be trusted.
// These are algorithm support limits, not operational geofence radii.
const MaxSupportedSegmentMeters = 5_000.0
const MaxSupportedRouteMeters = 100_000.0
const MaxSupportedAbsLatitude = 70.0
const MaxRouteVertices = 20_000
const MinSegmentMeters = 0.01

type preparedSegment struct {
	a, b, n vector3
	arc     float64
}
type PreparedRoute struct{ segments []preparedSegment }
type RouteDistance struct {
	Meters       float64
	SegmentIndex int
}

// PrepareRoute copies derived geometry. The returned value has no mutable
// exported state and can be shared safely by concurrent readers.
func PrepareRoute(line LineString) (PreparedRoute, error) {
	if err := line.Validate(); err != nil {
		return PreparedRoute{}, err
	}
	if len(line.Coordinates) > MaxRouteVertices {
		return PreparedRoute{}, fmt.Errorf("LineString exceeds supported vertex count")
	}
	segments := make([]preparedSegment, 0, len(line.Coordinates)-1)
	totalLength := 0.0
	for i := 0; i < len(line.Coordinates)-1; i++ {
		a, b := line.Coordinates[i], line.Coordinates[i+1]
		if math.Abs(a.Latitude) > MaxSupportedAbsLatitude || math.Abs(b.Latitude) > MaxSupportedAbsLatitude {
			return PreparedRoute{}, fmt.Errorf("segment %d lies outside supported latitude", i)
		}
		if math.Abs(b.Longitude-a.Longitude) > 180 {
			return PreparedRoute{}, fmt.Errorf("segment %d crosses ambiguous antimeridian", i)
		}
		av, bv := unitVector(a), unitVector(b)
		crossed := cross(av, bv)
		arc := math.Atan2(norm(crossed), dot(av, bv))
		length := arc * EarthRadiusMeters
		if length < MinSegmentMeters || length > MaxSupportedSegmentMeters {
			return PreparedRoute{}, fmt.Errorf("segment %d length outside supported range", i)
		}
		totalLength += length
		if totalLength > MaxSupportedRouteMeters {
			return PreparedRoute{}, fmt.Errorf("route exceeds supported length")
		}
		inverse := 1 / norm(crossed)
		segments = append(segments, preparedSegment{a: av, b: bv, n: vector3{crossed.x * inverse, crossed.y * inverse, crossed.z * inverse}, arc: arc})
	}
	return PreparedRoute{segments: segments}, nil
}

// DistanceMeters measures the nearest point on each supported spherical arc.
// A projection beyond an endpoint uses that endpoint instead of an infinite line.
func (route PreparedRoute) DistanceMeters(position Coordinate) (RouteDistance, error) {
	if err := position.Validate(); err != nil {
		return RouteDistance{}, err
	}
	if len(route.segments) == 0 {
		return RouteDistance{}, fmt.Errorf("route is not prepared")
	}
	p := unitVector(position)
	best := RouteDistance{Meters: math.Inf(1), SegmentIndex: -1}
	for i, seg := range route.segments {
		metres := math.Min(angularDistance(p, seg.a), angularDistance(p, seg.b)) * EarthRadiusMeters
		crossTrack := dot(p, seg.n)
		projected := vector3{p.x - seg.n.x*crossTrack, p.y - seg.n.y*crossTrack, p.z - seg.n.z*crossTrack}
		magnitude := norm(projected)
		if magnitude > 1e-12 {
			q := vector3{projected.x / magnitude, projected.y / magnitude, projected.z / magnitude}
			if angularDistance(seg.a, q)+angularDistance(q, seg.b) <= seg.arc+1e-12 {
				candidate := EarthRadiusMeters * math.Asin(math.Min(1, math.Abs(crossTrack)))
				if candidate < metres {
					metres = candidate
				}
			}
		}
		if metres < best.Meters {
			best = RouteDistance{Meters: metres, SegmentIndex: i}
		}
	}
	return best, nil
}

func (route PreparedRoute) EvaluateCorridor(position Coordinate, accuracy *float64, policy GeofencePolicy) (Evaluation, error) {
	distance, err := route.DistanceMeters(position)
	if err != nil {
		return Evaluation{}, err
	}
	return EvaluateDistance(distance.Meters, accuracy, policy)
}

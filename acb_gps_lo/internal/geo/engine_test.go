package geo

import (
	"fmt"
	"math"
	"sync"
	"testing"
)

func metres(value float64) *float64 { return &value }

func near(t *testing.T, actual, want, tolerance float64) {
	t.Helper()
	if math.Abs(actual-want) > tolerance {
		t.Fatalf("distance %.9f m, want %.9f ± %.9f", actual, want, tolerance)
	}
}

func TestHaversineReferenceCases(t *testing.T) {
	cases := []struct {
		name string
		a, b Coordinate
		want float64
		tol  float64
	}{
		{"same", Coordinate{0, 0}, Coordinate{0, 0}, 0, 0},
		{"equivalent dateline", Coordinate{180, 0}, Coordinate{-180, 0}, 0, 0.001},
		{"same north pole", Coordinate{0, 90}, Coordinate{150, 90}, 0, 0.001},
		{"one degree equator", Coordinate{0, 0}, Coordinate{1, 0}, EarthRadiusMeters * math.Pi / 180, 0.001},
		{"quarter globe", Coordinate{0, 0}, Coordinate{90, 0}, EarthRadiusMeters * math.Pi / 2, 0.001},
		{"half globe", Coordinate{0, 0}, Coordinate{180, 0}, EarthRadiusMeters * math.Pi, 0.001},
		{"short equator", Coordinate{0, 0}, Coordinate{0.0001, 0}, 11.119492664, 0.001},
		{"dateline short", Coordinate{179.9, 0}, Coordinate{-179.9, 0}, EarthRadiusMeters * 0.2 * math.Pi / 180, 0.001},
		{"opposite poles", Coordinate{0, -90}, Coordinate{50, 90}, EarthRadiusMeters * math.Pi, 0.001},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := HaversineMeters(tc.a, tc.b)
			if err != nil {
				t.Fatal(err)
			}
			near(t, got, tc.want, tc.tol)
			reverse, err := HaversineMeters(tc.b, tc.a)
			if err != nil {
				t.Fatal(err)
			}
			near(t, got, reverse, 0.001)
		})
	}
	got, err := HaversineMeters(Coordinate{-74.1, 4.6}, Coordinate{-74.1001, 4.6001})
	if err != nil || got < 10 || got > 20 {
		t.Fatalf("negative longitude short distance %v %v", got, err)
	}
	if got, err := HaversineMeters(Coordinate{180, 89.999}, Coordinate{-179, 89.999}); err != nil || math.IsNaN(got) || math.IsInf(got, 0) {
		t.Fatalf("near pole %v %v", got, err)
	}
	for _, bad := range []Coordinate{{181, 0}, {0, -91}, {math.NaN(), 0}, {0, math.Inf(1)}} {
		if _, err := HaversineMeters(bad, Coordinate{}); err == nil {
			t.Fatalf("invalid coordinate %+v", bad)
		}
	}
}

func TestGeofencePythonIntervalSemantics(t *testing.T) {
	policy := GeofencePolicy{RadiusMeters: 30, MaxAccuracyMeters: 10}
	cases := []struct {
		distance, accuracy float64
		status             Status
		reason             Reason
	}{
		{0, 5, Inside, UncertaintyWithinRadius},
		{25, 5, Inside, UncertaintyWithinRadius},
		{26, 5, Indeterminate, BoundaryUncertainty},
		{30, 1, Indeterminate, BoundaryUncertainty},
		{35, 5, Indeterminate, BoundaryUncertainty},
		{36, 5, Outside, UncertaintyOutsideRadius},
	}
	for _, tc := range cases {
		result, err := EvaluateDistance(tc.distance, metres(tc.accuracy), policy)
		if err != nil || result.Status != tc.status || result.Reason != tc.reason || !result.BoundsAvailable {
			t.Fatalf("distance %v: %+v %v", tc.distance, result, err)
		}
	}
	for _, accuracy := range []*float64{nil, metres(11)} {
		result, err := EvaluateDistance(100, accuracy, policy)
		if err != nil || result.Status != Indeterminate {
			t.Fatalf("insufficient accuracy %+v %v", result, err)
		}
	}
	if result, _ := EvaluateDistance(30, metres(1), policy); !result.PointInside || result.Status != Indeterminate {
		t.Fatalf("nominal boundary %+v", result)
	}
	for _, accuracy := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if _, err := EvaluateDistance(0, &accuracy, policy); err == nil {
			t.Fatalf("accepted invalid accuracy %v", accuracy)
		}
	}
	if _, err := EvaluateDistance(-1, metres(1), policy); err == nil {
		t.Fatal("negative distance")
	}
	if _, err := EvaluateDistance(1, metres(1), GeofencePolicy{}); err == nil {
		t.Fatal("invalid policy")
	}
	center := Coordinate{Longitude: 0, Latitude: 0}
	inside, err := EvaluateCircle(center, center, metres(5), policy)
	if err != nil || inside.Status != Inside {
		t.Fatalf("circle inside %+v %v", inside, err)
	}
	far, err := EvaluateCircle(Coordinate{Longitude: 0.001, Latitude: 0}, center, metres(5), policy)
	if err != nil || far.Status != Outside {
		t.Fatalf("circle outside %+v %v", far, err)
	}
}

func TestPreparedRouteSegmentsAndCorridor(t *testing.T) {
	line := LineString{Coordinates: []Coordinate{{0, 0}, {0.001, 0}, {0.001, 0.001}}}
	prepared, err := PrepareRoute(line)
	if err != nil {
		t.Fatal(err)
	}
	// Mutating the input cannot alter the prepared route.
	line.Coordinates[1] = Coordinate{Longitude: 50, Latitude: 50}
	between, err := prepared.DistanceMeters(Coordinate{0.0005, 0.0002})
	if err != nil || between.SegmentIndex != 0 {
		t.Fatalf("between vertices %+v %v", between, err)
	}
	near(t, between.Meters, 22.238985, 0.05) // 0.0002° latitude on the equator.
	second, err := prepared.DistanceMeters(Coordinate{0.0012, 0.0005})
	if err != nil || second.SegmentIndex != 1 {
		t.Fatalf("second segment %+v %v", second, err)
	}
	near(t, second.Meters, 22.238985, 0.05)
	before, err := prepared.DistanceMeters(Coordinate{-0.001, 0})
	if err != nil {
		t.Fatal(err)
	}
	near(t, before.Meters, 111.194927, 0.05)
	after, err := prepared.DistanceMeters(Coordinate{0.001, 0.002})
	if err != nil {
		t.Fatal(err)
	}
	near(t, after.Meters, 111.194927, 0.05)
	policy := GeofencePolicy{RadiusMeters: 30, MaxAccuracyMeters: 10}
	for _, tc := range []struct {
		point    Coordinate
		accuracy *float64
		want     Status
	}{
		{Coordinate{0.0005, 0.0002}, metres(5), Inside},
		{Coordinate{0.0005, 0.0004}, metres(5), Outside},
		{Coordinate{0.0005, 0.00025}, metres(5), Indeterminate},
		{Coordinate{0.0005, 0.0002}, nil, Indeterminate},
		{Coordinate{0.0005, 0.0004}, metres(11), Indeterminate},
	} {
		result, err := prepared.EvaluateCorridor(tc.point, tc.accuracy, policy)
		if err != nil || result.Status != tc.want {
			t.Fatalf("corridor %+v: %+v %v", tc.point, result, err)
		}
	}
}

func TestPreparedRouteRejectsUnsupportedGeometry(t *testing.T) {
	for name, line := range map[string]LineString{
		"short":              {Coordinates: []Coordinate{{0, 0}}},
		"invalid":            {Coordinates: []Coordinate{{0, 0}, {181, 0}}},
		"degenerate":         {Coordinates: []Coordinate{{0, 0}, {0, 0}}},
		"near degenerate":    {Coordinates: []Coordinate{{0, 0}, {1e-11, 0}}},
		"long":               {Coordinates: []Coordinate{{0, 0}, {0.2, 0}}},
		"ambiguous dateline": {Coordinates: []Coordinate{{179.9, 0}, {-179.9, 0}}},
		"polar route":        {Coordinates: []Coordinate{{0, 89.9}, {1, 89.9}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := PrepareRoute(line); err == nil {
				t.Fatal("unsupported route accepted")
			}
		})
	}
	valid, _ := PrepareRoute(LineString{Coordinates: []Coordinate{{0, 0}, {0.001, 0}}})
	if _, err := valid.DistanceMeters(Coordinate{Longitude: math.NaN()}); err == nil {
		t.Fatal("invalid position accepted")
	}
	if _, err := (PreparedRoute{}).DistanceMeters(Coordinate{}); err == nil {
		t.Fatal("unprepared route accepted")
	}
	extensive := LineString{Coordinates: make([]Coordinate, 1100)}
	for i := range extensive.Coordinates {
		extensive.Coordinates[i] = Coordinate{Longitude: float64(i) * 0.001, Latitude: 0}
	}
	if _, err := PrepareRoute(extensive); err == nil {
		t.Fatal("extensive route accepted")
	}
}

func TestPreparedRouteConcurrentReaders(t *testing.T) {
	line := LineString{Coordinates: make([]Coordinate, 200)}
	for i := range line.Coordinates {
		line.Coordinates[i] = Coordinate{Longitude: float64(i) * 0.0001, Latitude: 0}
	}
	prepared, err := PrepareRoute(line)
	if err != nil {
		t.Fatal(err)
	}
	const workers = 64
	results := make(chan float64, workers)
	var group sync.WaitGroup
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := prepared.DistanceMeters(Coordinate{0.005, 0.0002})
			if err != nil {
				results <- math.NaN()
				return
			}
			results <- result.Meters
		}()
	}
	group.Wait()
	close(results)
	for distance := range results {
		if math.IsNaN(distance) {
			t.Fatal("concurrent evaluation failed")
		}
		near(t, distance, 22.238985, 0.05)
	}
}

func BenchmarkPreparedRouteDistance(b *testing.B) {
	for _, vertices := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("vertices_%d", vertices), func(b *testing.B) {
			line := LineString{Coordinates: make([]Coordinate, vertices)}
			for i := range line.Coordinates {
				line.Coordinates[i] = Coordinate{Longitude: float64(i) * 0.0005, Latitude: 0}
			}
			prepared, err := PrepareRoute(line)
			if err != nil {
				b.Fatal(err)
			}
			position := Coordinate{Longitude: float64(vertices/2) * 0.0005, Latitude: 0.0002}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				distance, err := prepared.DistanceMeters(position)
				if err != nil || distance.Meters < 0 {
					b.Fatal(err)
				}
			}
		})
	}
}

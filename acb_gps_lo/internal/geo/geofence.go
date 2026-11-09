package geo

import (
	"fmt"
	"math"
)

type Status string

const (
	Inside        Status = "inside"
	Outside       Status = "outside"
	Indeterminate Status = "indeterminate"
)

type Reason string

const (
	AccuracyMissing          Reason = "accuracy_missing"
	AccuracyExceedsLimit     Reason = "accuracy_exceeds_limit"
	UncertaintyWithinRadius  Reason = "uncertainty_within_radius"
	UncertaintyOutsideRadius Reason = "uncertainty_outside_radius"
	BoundaryUncertainty      Reason = "boundary_uncertainty"
)

type GeofencePolicy struct {
	RadiusMeters      float64
	MaxAccuracyMeters float64
}

func (p GeofencePolicy) Validate() error {
	if !positiveFinite(p.RadiusMeters) || !positiveFinite(p.MaxAccuracyMeters) {
		return fmt.Errorf("geofence radius and maximum accuracy must be positive finite metres")
	}
	return nil
}
func positiveFinite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) && x > 0 }

// Evaluation represents a performed measurement. A nil *Evaluation in a
// tracking position means not_evaluated; Indeterminate means measured but
// insufficiently certain.
type Evaluation struct {
	Status           Status
	Reason           Reason
	DistanceMeters   float64
	PointInside      bool
	AccuracyMeters   *float64
	LowerBoundMeters float64
	UpperBoundMeters float64
	BoundsAvailable  bool
}

// EvaluateDistance follows the Python uncertainty interval: inside only when
// distance+accuracy <= radius; outside only when distance-accuracy > radius.
func EvaluateDistance(distance float64, accuracy *float64, policy GeofencePolicy) (Evaluation, error) {
	if err := policy.Validate(); err != nil {
		return Evaluation{}, err
	}
	if math.IsNaN(distance) || math.IsInf(distance, 0) || distance < 0 {
		return Evaluation{}, fmt.Errorf("distance must be finite and nonnegative")
	}
	result := Evaluation{Status: Indeterminate, DistanceMeters: distance, PointInside: distance <= policy.RadiusMeters, AccuracyMeters: accuracy}
	if accuracy == nil {
		result.Reason = AccuracyMissing
		return result, nil
	}
	if !positiveFinite(*accuracy) {
		return Evaluation{}, fmt.Errorf("accuracy must be positive finite metres")
	}
	result.LowerBoundMeters = math.Max(0, distance-*accuracy)
	result.UpperBoundMeters = distance + *accuracy
	if math.IsInf(result.UpperBoundMeters, 0) {
		return Evaluation{}, fmt.Errorf("uncertainty bound overflow")
	}
	result.BoundsAvailable = true
	if *accuracy > policy.MaxAccuracyMeters {
		result.Reason = AccuracyExceedsLimit
		return result, nil
	}
	if result.UpperBoundMeters <= policy.RadiusMeters {
		result.Status = Inside
		result.Reason = UncertaintyWithinRadius
		return result, nil
	}
	if result.LowerBoundMeters > policy.RadiusMeters {
		result.Status = Outside
		result.Reason = UncertaintyOutsideRadius
		return result, nil
	}
	result.Reason = BoundaryUncertainty
	return result, nil
}

func EvaluateCircle(position, center Coordinate, accuracy *float64, policy GeofencePolicy) (Evaluation, error) {
	distance, err := HaversineMeters(position, center)
	if err != nil {
		return Evaluation{}, err
	}
	return EvaluateDistance(distance, accuracy, policy)
}

package geo

import (
	"fmt"
	"math"
)

// Coordinate uses GeoJSON order: longitude, latitude (WGS84 degrees).
type Coordinate struct {
	Longitude float64
	Latitude  float64
}

func (c Coordinate) Validate() error {
	if math.IsNaN(c.Longitude) || math.IsInf(c.Longitude, 0) ||
		math.IsNaN(c.Latitude) || math.IsInf(c.Latitude, 0) ||
		c.Longitude < -180 || c.Longitude > 180 ||
		c.Latitude < -90 || c.Latitude > 90 {
		return fmt.Errorf("coordinate is outside WGS84 bounds or not finite")
	}
	return nil
}

type LineString struct {
	Coordinates []Coordinate
}

func (line LineString) Validate() error {
	if len(line.Coordinates) < 2 {
		return fmt.Errorf("LineString requires at least two coordinates")
	}
	for index, coordinate := range line.Coordinates {
		if err := coordinate.Validate(); err != nil {
			return fmt.Errorf("LineString coordinate %d: %w", index, err)
		}
	}
	return nil
}

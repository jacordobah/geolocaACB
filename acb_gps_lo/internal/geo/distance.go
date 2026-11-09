package geo

import "math"

const EarthRadiusMeters = 6_371_000.0
const degreesToRadians = math.Pi / 180

// HaversineMeters uses the same mean Earth radius and longitude normalization
// as the Python geographic validation. Coordinates are WGS84 degrees.
func HaversineMeters(a, b Coordinate) (float64, error) {
	if err := a.Validate(); err != nil {
		return 0, err
	}
	if err := b.Validate(); err != nil {
		return 0, err
	}
	latA, latB := a.Latitude*degreesToRadians, b.Latitude*degreesToRadians
	dLat := latB - latA
	dLon := math.Remainder((b.Longitude-a.Longitude)*degreesToRadians, 2*math.Pi)
	cosA, cosB := math.Cos(latA), math.Cos(latB)
	if math.Abs(a.Latitude) == 90 {
		cosA = 0
	}
	if math.Abs(b.Latitude) == 90 {
		cosB = 0
	}
	h := math.Pow(math.Sin(dLat/2), 2) + cosA*cosB*math.Pow(math.Sin(dLon/2), 2)
	h = math.Max(0, math.Min(1, h))
	return 2 * EarthRadiusMeters * math.Atan2(math.Sqrt(h), math.Sqrt(1-h)), nil
}

type vector3 struct{ x, y, z float64 }

func unitVector(c Coordinate) vector3 {
	lat, lon := c.Latitude*degreesToRadians, c.Longitude*degreesToRadians
	cl := math.Cos(lat)
	return vector3{cl * math.Cos(lon), cl * math.Sin(lon), math.Sin(lat)}
}
func dot(a, b vector3) float64 { return a.x*b.x + a.y*b.y + a.z*b.z }
func cross(a, b vector3) vector3 {
	return vector3{a.y*b.z - a.z*b.y, a.z*b.x - a.x*b.z, a.x*b.y - a.y*b.x}
}
func norm(a vector3) float64               { return math.Sqrt(dot(a, a)) }
func angularDistance(a, b vector3) float64 { return math.Atan2(norm(cross(a, b)), dot(a, b)) }

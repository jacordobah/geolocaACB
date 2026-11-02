package routes

import (
	"context"
	"errors"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/geo"
)

var (
	ErrNotFound              = errors.New("geographic route not found")
	ErrInvalidIdentifier     = errors.New("invalid geographic route identifier")
	ErrInvalidResponse       = errors.New("invalid geographic response")
	ErrResponseTooLarge      = errors.Join(ErrInvalidResponse, errors.New("geographic response too large"))
	ErrInvalidGeographicData = errors.New("invalid geographic association")
	ErrUnavailable           = errors.New("geographic service unavailable")
)

// Reader is the contract needed by future GPS use cases. Reading a route does
// not authorize its use for tracking.
type Reader interface {
	Get(context.Context, string) (Route, error)
}

type Route struct {
	ID                string
	Name              string
	RouteCode         *string
	Code              *string
	DayShift          *string
	GeometryVersion   *string
	AssociationStatus *string
	Geometry          geo.LineString
	MeetingPoints     []MeetingPoint // order is exactly the order returned by Python
	Endpoints         Endpoints
	// Unavailable names metadata that the current Python response cannot prove.
	// This is not a route eligibility decision.
	Unavailable []string
}

type MeetingPoint struct {
	ID          string
	Name        string
	Kind        string
	Description *string
	Location    geo.Coordinate
}

type EndpointMatch struct {
	Source             *string
	DistanceMeters     *float64
	Confidence         *string
	MatchedVertexIndex *int
}

type Endpoint struct {
	PointID *string
	Match   *EndpointMatch
}

type Endpoints struct {
	Status       *string
	Reason       *string
	Start        Endpoint
	End          Endpoint
	FirstPointID *string
}

package pathshttp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/geo"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/routes"
)

type Client struct {
	baseURL  string
	http     *http.Client
	maxBytes int64
}

var _ routes.Reader = (*Client)(nil)

// New expects the origin of the Python service, not an /api/v1 path.
func New(baseURL string, timeout time.Duration, maxBytes int64) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, fmt.Errorf("invalid geographic service base URL")
	}
	if timeout <= 0 || maxBytes < 1 || maxBytes > 64<<20 {
		return nil, fmt.Errorf("invalid geographic HTTP limits")
	}
	httpClient := &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &Client{strings.TrimRight(baseURL, "/"), httpClient, maxBytes}, nil
}

// StatusError retains the upstream status and its documented error code.
type StatusError struct {
	StatusCode int
	Code       string
}

func (e *StatusError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("geographic service returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("geographic service returned HTTP %d (%s)", e.StatusCode, e.Code)
}

func (e *StatusError) Unwrap() error {
	switch e.StatusCode {
	case http.StatusNotFound:
		return routes.ErrNotFound
	case http.StatusUnprocessableEntity:
		return routes.ErrInvalidIdentifier
	case http.StatusConflict:
		return routes.ErrInvalidGeographicData
	default:
		if e.StatusCode >= 500 {
			return routes.ErrUnavailable
		}
		return nil
	}
}

func (c *Client) Get(ctx context.Context, routeID string) (routes.Route, error) {
	if routeID == "" || strings.TrimSpace(routeID) != routeID || strings.ContainsAny(routeID, "/?#") {
		return routes.Route{}, routes.ErrInvalidIdentifier
	}
	requestURL := c.baseURL + "/api/v1/routes/" + url.PathEscape(routeID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return routes.Route{}, fmt.Errorf("prepare geographic request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return routes.Route{}, ctx.Err()
		}
		return routes.Route{}, fmt.Errorf("%w: %w", routes.ErrUnavailable, err)
	}
	defer response.Body.Close()
	if response.ContentLength > c.maxBytes {
		return routes.Route{}, routes.ErrResponseTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, c.maxBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return routes.Route{}, ctx.Err()
		}
		return routes.Route{}, fmt.Errorf("%w: read body: %w", routes.ErrUnavailable, err)
	}
	if int64(len(data)) > c.maxBytes {
		return routes.Route{}, routes.ErrResponseTooLarge
	}
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Detail struct {
				Code string `json:"code"`
			} `json:"detail"`
		}
		_ = json.Unmarshal(data, &failure)
		return routes.Route{}, &StatusError{response.StatusCode, failure.Detail.Code}
	}
	return decodeRoute(data, routeID)
}

// These transport types describe the current Python response. They intentionally
// allow nullable metadata and do not expose MongoDB types to the route model.
type lineResponse struct {
	Type        string              `json:"type"`
	Coordinates [][]json.RawMessage `json:"coordinates"`
}

type pointResponse struct {
	Type        string            `json:"type"`
	Coordinates []json.RawMessage `json:"coordinates"`
}

type meetingPointResponse struct {
	ID          string        `json:"_id"`
	Name        string        `json:"name"`
	Kind        string        `json:"kind"`
	Description *string       `json:"description"`
	Location    pointResponse `json:"location"`
}

type endpointMatchResponse struct {
	Source             *string  `json:"source"`
	DistanceMeters     *float64 `json:"distance_m"`
	Confidence         *string  `json:"confidence"`
	MatchedVertexIndex *int     `json:"matched_vertex_index"`
}

type routeResponse struct {
	ID                  string                  `json:"_id"`
	Name                string                  `json:"name"`
	RouteCode           *string                 `json:"route_code"`
	Code                *string                 `json:"code"`
	DayShift            *string                 `json:"day_shift"`
	Geometry            lineResponse            `json:"geometry"`
	MeetingPoints       *[]meetingPointResponse `json:"meeting_points"`
	EndpointStatus      *string                 `json:"endpoint_status"`
	EndpointReason      *string                 `json:"endpoint_reason"`
	StartMeetingPointID *string                 `json:"start_meeting_point_id"`
	EndMeetingPointID   *string                 `json:"end_meeting_point_id"`
	FirstMeetingPointID *string                 `json:"first_meeting_point_id"`
	Endpoints           *struct {
		Start *endpointMatchResponse `json:"start"`
		End   *endpointMatchResponse `json:"end"`
	} `json:"endpoints"`
}

func decodeRoute(data []byte, requestedID string) (routes.Route, error) {
	var raw routeResponse
	if err := json.Unmarshal(data, &raw); err != nil {
		return routes.Route{}, fmt.Errorf("%w: JSON: %v", routes.ErrInvalidResponse, err)
	}
	if raw.ID == "" || raw.ID != requestedID {
		return routes.Route{}, fmt.Errorf("%w: route identifier mismatch", routes.ErrInvalidResponse)
	}
	if strings.TrimSpace(raw.Name) == "" {
		return routes.Route{}, fmt.Errorf("%w: route name is missing", routes.ErrInvalidResponse)
	}
	if raw.Geometry.Type != "LineString" {
		return routes.Route{}, fmt.Errorf("%w: route geometry must be LineString", routes.ErrInvalidResponse)
	}
	if raw.MeetingPoints == nil {
		return routes.Route{}, fmt.Errorf("%w: meeting_points must be an array", routes.ErrInvalidResponse)
	}
	line := geo.LineString{Coordinates: make([]geo.Coordinate, 0, len(raw.Geometry.Coordinates))}
	for _, pair := range raw.Geometry.Coordinates {
		coordinate, err := parseCoordinate(pair)
		if err != nil {
			return routes.Route{}, fmt.Errorf("%w: route geometry: %v", routes.ErrInvalidResponse, err)
		}
		line.Coordinates = append(line.Coordinates, coordinate)
	}
	if err := line.Validate(); err != nil {
		return routes.Route{}, fmt.Errorf("%w: %v", routes.ErrInvalidResponse, err)
	}

	result := routes.Route{
		ID: raw.ID, Name: raw.Name, RouteCode: raw.RouteCode, Code: raw.Code,
		DayShift: raw.DayShift, Geometry: line,
		MeetingPoints: make([]routes.MeetingPoint, 0, len(*raw.MeetingPoints)),
		Endpoints: routes.Endpoints{
			Status: raw.EndpointStatus, Reason: raw.EndpointReason,
			FirstPointID: raw.FirstMeetingPointID,
		},
		Unavailable: []string{"variant", "geometry_version", "association_status", "meeting_point_association_confidence", "endpoint_geometry"},
	}
	seen := make(map[string]bool, len(*raw.MeetingPoints))
	for index, point := range *raw.MeetingPoints {
		if point.ID == "" || strings.TrimSpace(point.ID) != point.ID || seen[point.ID] ||
			strings.TrimSpace(point.Name) == "" || point.Kind != "meeting_point" || point.Location.Type != "Point" {
			return routes.Route{}, fmt.Errorf("%w: meeting point %d has invalid identity or type", routes.ErrInvalidResponse, index)
		}
		coordinate, err := parseCoordinate(point.Location.Coordinates)
		if err != nil {
			return routes.Route{}, fmt.Errorf("%w: meeting point %d: %v", routes.ErrInvalidResponse, index, err)
		}
		seen[point.ID] = true
		result.MeetingPoints = append(result.MeetingPoints, routes.MeetingPoint{
			ID: point.ID, Name: point.Name, Kind: point.Kind,
			Description: point.Description, Location: coordinate,
		})
	}
	if err := validateReference(raw.StartMeetingPointID, "start"); err != nil {
		return routes.Route{}, err
	}
	if err := validateReference(raw.EndMeetingPointID, "end"); err != nil {
		return routes.Route{}, err
	}
	if err := validateReference(raw.FirstMeetingPointID, "first"); err != nil {
		return routes.Route{}, err
	}
	result.Endpoints.Start.PointID = raw.StartMeetingPointID
	result.Endpoints.End.PointID = raw.EndMeetingPointID
	if raw.Endpoints != nil {
		var err error
		result.Endpoints.Start.Match, err = parseMatch(raw.Endpoints.Start, raw.StartMeetingPointID, len(line.Coordinates))
		if err != nil {
			return routes.Route{}, fmt.Errorf("%w: start endpoint: %v", routes.ErrInvalidResponse, err)
		}
		result.Endpoints.End.Match, err = parseMatch(raw.Endpoints.End, raw.EndMeetingPointID, len(line.Coordinates))
		if err != nil {
			return routes.Route{}, fmt.Errorf("%w: end endpoint: %v", routes.ErrInvalidResponse, err)
		}
	}
	if raw.FirstMeetingPointID != nil && !seen[*raw.FirstMeetingPointID] {
		result.Unavailable = append(result.Unavailable, "first_point_membership")
	}
	if len(result.MeetingPoints) == 0 {
		result.Unavailable = append(result.Unavailable, "meeting_point_association_provenance")
	}
	if raw.EndpointStatus == nil {
		result.Unavailable = append(result.Unavailable, "endpoint_status")
	}
	return result, nil
}

func parseCoordinate(pair []json.RawMessage) (geo.Coordinate, error) {
	if len(pair) != 2 {
		return geo.Coordinate{}, fmt.Errorf("coordinate must contain longitude and latitude")
	}
	var longitude, latitude *float64
	if err := json.Unmarshal(pair[0], &longitude); err != nil || longitude == nil {
		return geo.Coordinate{}, fmt.Errorf("longitude must be a finite number")
	}
	if err := json.Unmarshal(pair[1], &latitude); err != nil || latitude == nil {
		return geo.Coordinate{}, fmt.Errorf("latitude must be a finite number")
	}
	coordinate := geo.Coordinate{Longitude: *longitude, Latitude: *latitude}
	if err := coordinate.Validate(); err != nil {
		return geo.Coordinate{}, err
	}
	return coordinate, nil
}

func validateReference(id *string, role string) error {
	if id != nil && (*id == "" || strings.TrimSpace(*id) != *id) {
		return fmt.Errorf("%w: %s endpoint identifier is empty or malformed", routes.ErrInvalidResponse, role)
	}
	return nil
}

func parseMatch(raw *endpointMatchResponse, id *string, vertexCount int) (*routes.EndpointMatch, error) {
	if raw == nil {
		return nil, nil
	}
	if id == nil {
		return nil, fmt.Errorf("endpoint metadata has no point identifier")
	}
	if raw.DistanceMeters != nil && (math.IsNaN(*raw.DistanceMeters) || math.IsInf(*raw.DistanceMeters, 0) || *raw.DistanceMeters < 0) {
		return nil, fmt.Errorf("invalid endpoint distance")
	}
	if raw.MatchedVertexIndex != nil && (*raw.MatchedVertexIndex < 0 || *raw.MatchedVertexIndex >= vertexCount) {
		return nil, fmt.Errorf("endpoint vertex index is outside route geometry")
	}
	return &routes.EndpointMatch{
		Source: raw.Source, DistanceMeters: raw.DistanceMeters,
		Confidence: raw.Confidence, MatchedVertexIndex: raw.MatchedVertexIndex,
	}, nil
}

package tracking

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/geo"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/routes"
)

var ErrRouteIneligible = errors.New("route is not authorized for geographic tracking")
var ErrInvalidGeometry = errors.New("route geometry is unsupported")

// GeographicPolicy is supplied by an authorized internal caller. It has no
// operational defaults and is frozen with the route snapshot.
type GeographicPolicy struct {
	CorridorRadiusMeters float64 `json:"corridor_radius_meters"`
	PointRadiusMeters    float64 `json:"point_radius_meters"`
	MaxAccuracyMeters    float64 `json:"max_accuracy_meters"`
}

func (p GeographicPolicy) Validate() error {
	if err := (geo.GeofencePolicy{RadiusMeters: p.CorridorRadiusMeters, MaxAccuracyMeters: p.MaxAccuracyMeters}).Validate(); err != nil {
		return fmt.Errorf("%w: corridor policy: %v", ErrInvalid, err)
	}
	if err := (geo.GeofencePolicy{RadiusMeters: p.PointRadiusMeters, MaxAccuracyMeters: p.MaxAccuracyMeters}).Validate(); err != nil {
		return fmt.Errorf("%w: point policy: %v", ErrInvalid, err)
	}
	return nil
}

type GeographicSnapshot struct {
	Route                 routes.Route      `json:"route"`
	Policy                GeographicPolicy  `json:"policy"`
	TransitionPolicy      *TransitionPolicy `json:"transition_policy,omitempty"`
	SnapshotFormatVersion int               `json:"snapshot_format_version,omitempty"`
	PolicyReference       *PolicyReference  `json:"policy_reference,omitempty"`
	EngineVersion         string            `json:"engine_version"`
	Limits                GeometryLimits    `json:"geometry_limits"`
	// GeometryFingerprint is a locally calculated content hash, not a version
	// issued by Python. SnapshotHash also binds the fixed policy.
	GeometryFingerprint string `json:"geometry_fingerprint"`
	SnapshotHash        string `json:"snapshot_hash"`
}

// PolicyReference identifies a YAML definition. Its hash is distinct from
// SnapshotHash (route + effective policy) and GeometryFingerprint (route only).
type PolicyReference struct {
	PolicyID       string `json:"policy_id"`
	Revision       int    `json:"revision"`
	DefinitionHash string `json:"definition_hash"`
}

func NewPolicyReference(id string, revision int, geoPolicy GeographicPolicy, transitions TransitionPolicy) (PolicyReference, error) {
	if !ValidID(id) || revision < 1 || geoPolicy.Validate() != nil || transitions.Validate() != nil {
		return PolicyReference{}, ErrInvalid
	}
	b, err := json.Marshal(struct {
		Domain      string           `json:"domain"`
		ID          string           `json:"policy_id"`
		Revision    int              `json:"revision"`
		Geographic  GeographicPolicy `json:"geographic"`
		Transitions TransitionPolicy `json:"transitions"`
	}{"gps-policy-definition-v1", id, revision, geoPolicy, transitions})
	if err != nil {
		return PolicyReference{}, err
	}
	sum := sha256.Sum256(b)
	return PolicyReference{PolicyID: id, Revision: revision, DefinitionHash: hex.EncodeToString(sum[:])}, nil
}

// BuildSnapshotWithPolicy preserves the legacy hash algorithm for old records.
// Version two additionally binds a canonical, versioned policy identity.
func BuildSnapshotWithPolicy(route routes.Route, policy GeographicPolicy, transitions TransitionPolicy, ref PolicyReference) (GeographicSnapshot, error) {
	want, err := NewPolicyReference(ref.PolicyID, ref.Revision, policy, transitions)
	if err != nil || want != ref {
		return GeographicSnapshot{}, ErrInvalid
	}
	s, err := BuildSnapshotWithTransitions(route, policy, &transitions)
	if err != nil {
		return GeographicSnapshot{}, err
	}
	s.SnapshotFormatVersion = 2
	s.PolicyReference = &want
	b, err := json.Marshal(struct {
		Domain     string          `json:"domain"`
		LegacyHash string          `json:"legacy_hash"`
		Format     int             `json:"snapshot_format_version"`
		Reference  PolicyReference `json:"policy_reference"`
	}{"gps-snapshot-v2", s.SnapshotHash, s.SnapshotFormatVersion, want})
	if err != nil {
		return GeographicSnapshot{}, err
	}
	sum := sha256.Sum256(b)
	s.SnapshotHash = hex.EncodeToString(sum[:])
	return s, nil
}

type GeometryLimits struct {
	MaxSegmentMeters float64 `json:"max_segment_meters"`
	MaxRouteMeters   float64 `json:"max_route_meters"`
	MaxAbsLatitude   float64 `json:"max_abs_latitude"`
	MaxVertices      int     `json:"max_vertices"`
}

func BuildSnapshot(route routes.Route, policy GeographicPolicy) (GeographicSnapshot, error) {
	return BuildSnapshotWithTransitions(route, policy, nil)
}

// BuildSnapshotWithTransitions binds explicit temporal rules to the immutable
// geographic reference. A nil policy preserves pre-Increment-7 snapshots.
func BuildSnapshotWithTransitions(route routes.Route, policy GeographicPolicy, transitions *TransitionPolicy) (GeographicSnapshot, error) {
	if err := policy.Validate(); err != nil {
		return GeographicSnapshot{}, err
	}
	if transitions != nil {
		if err := transitions.Validate(); err != nil {
			return GeographicSnapshot{}, err
		}
	}
	if !ValidID(route.ID) || strings.TrimSpace(route.Name) == "" {
		return GeographicSnapshot{}, ErrRouteIneligible
	}
	if _, err := geo.PrepareRoute(route.Geometry); err != nil {
		return GeographicSnapshot{}, fmt.Errorf("%w: %v", ErrInvalidGeometry, err)
	}
	if len(route.MeetingPoints) == 0 {
		return GeographicSnapshot{}, ErrRouteIneligible
	}
	seen := make(map[string]bool, len(route.MeetingPoints))
	for _, point := range route.MeetingPoints {
		if !ValidID(point.ID) || seen[point.ID] || strings.TrimSpace(point.Name) == "" || point.Kind != "meeting_point" {
			return GeographicSnapshot{}, ErrRouteIneligible
		}
		if err := point.Location.Validate(); err != nil {
			return GeographicSnapshot{}, fmt.Errorf("%w: point %s: %v", ErrInvalidGeometry, point.ID, err)
		}
		seen[point.ID] = true
	}
	first := route.MeetingPoints[0].ID
	if route.Endpoints.FirstPointID != nil && *route.Endpoints.FirstPointID != first {
		return GeographicSnapshot{}, ErrRouteIneligible
	}
	if route.Endpoints.Start.PointID != nil && *route.Endpoints.Start.PointID != first {
		return GeographicSnapshot{}, ErrRouteIneligible
	}
	if route.Endpoints.End.PointID != nil && seen[*route.Endpoints.End.PointID] && *route.Endpoints.End.PointID != route.MeetingPoints[len(route.MeetingPoints)-1].ID {
		return GeographicSnapshot{}, ErrRouteIneligible
	}
	for _, missing := range route.Unavailable {
		if missing == "first_point_membership" || missing == "meeting_point_association_provenance" {
			return GeographicSnapshot{}, ErrRouteIneligible
		}
	}
	// JSON round trip detaches slices and pointer metadata from the HTTP model.
	b, err := json.Marshal(route)
	if err != nil {
		return GeographicSnapshot{}, fmt.Errorf("%w: route encoding", ErrInvalidGeometry)
	}
	var detached routes.Route
	if err := json.Unmarshal(b, &detached); err != nil {
		return GeographicSnapshot{}, fmt.Errorf("%w: route decoding", ErrInvalidGeometry)
	}
	geographicContent, err := json.Marshal(struct {
		ID        string                `json:"id"`
		Geometry  geo.LineString        `json:"geometry"`
		Points    []routes.MeetingPoint `json:"meeting_points"`
		Endpoints routes.Endpoints      `json:"endpoints"`
	}{detached.ID, detached.Geometry, detached.MeetingPoints, detached.Endpoints})
	if err != nil {
		return GeographicSnapshot{}, fmt.Errorf("%w: geographic content", ErrInvalidGeometry)
	}
	routeHash := sha256.Sum256(geographicContent)
	s := GeographicSnapshot{Route: detached, Policy: policy, EngineVersion: "short-arc-v1", Limits: GeometryLimits{geo.MaxSupportedSegmentMeters, geo.MaxSupportedRouteMeters, geo.MaxSupportedAbsLatitude, geo.MaxRouteVertices}, GeometryFingerprint: hex.EncodeToString(routeHash[:])}
	if transitions != nil {
		copy := *transitions
		s.TransitionPolicy = &copy
	}
	b, err = json.Marshal(struct {
		Route            routes.Route      `json:"route"`
		Policy           GeographicPolicy  `json:"policy"`
		EngineVersion    string            `json:"engine_version"`
		Limits           GeometryLimits    `json:"geometry_limits"`
		TransitionPolicy *TransitionPolicy `json:"transition_policy,omitempty"`
	}{detached, policy, s.EngineVersion, s.Limits, s.TransitionPolicy})
	if err != nil {
		return GeographicSnapshot{}, fmt.Errorf("%w: snapshot encoding", ErrInvalidGeometry)
	}
	sum := sha256.Sum256(b)
	s.SnapshotHash = hex.EncodeToString(sum[:])
	return s, nil
}

func (s GeographicSnapshot) Validate() error {
	if s.EngineVersion != "short-arc-v1" {
		return ErrInvalidGeometry
	}
	var rebuilt GeographicSnapshot
	var err error
	switch {
	case s.SnapshotFormatVersion == 0 && s.PolicyReference == nil:
		rebuilt, err = BuildSnapshotWithTransitions(s.Route, s.Policy, s.TransitionPolicy)
	case s.SnapshotFormatVersion == 2 && s.PolicyReference != nil && s.TransitionPolicy != nil:
		rebuilt, err = BuildSnapshotWithPolicy(s.Route, s.Policy, *s.TransitionPolicy, *s.PolicyReference)
	default:
		return ErrInvalidGeometry
	}
	if err != nil {
		return err
	}
	if rebuilt.GeometryFingerprint != s.GeometryFingerprint || rebuilt.SnapshotHash != s.SnapshotHash || rebuilt.Limits != s.Limits {
		return ErrInvalidGeometry
	}
	return nil
}

// OperationalReady is deliberately stricter than syntactic and mathematical
// validity. The current Python response cannot satisfy it.
func (s GeographicSnapshot) OperationalReady() error {
	if err := s.Validate(); err != nil {
		return err
	}
	r := s.Route
	if r.GeometryVersion == nil || strings.TrimSpace(*r.GeometryVersion) == "" || r.AssociationStatus == nil || *r.AssociationStatus != "verified" || r.Endpoints.Status == nil || *r.Endpoints.Status != "resolved" || r.Endpoints.FirstPointID == nil || r.Endpoints.Start.PointID == nil || r.Endpoints.End.PointID == nil {
		return ErrRouteIneligible
	}
	for _, missing := range r.Unavailable {
		if missing == "geometry_version" || missing == "association_status" || missing == "endpoint_status" || missing == "first_point_membership" || missing == "meeting_point_association_provenance" {
			return ErrRouteIneligible
		}
	}
	if *r.Endpoints.FirstPointID != r.MeetingPoints[0].ID || *r.Endpoints.Start.PointID != r.MeetingPoints[0].ID {
		return ErrRouteIneligible
	}
	return nil
}

func (s GeographicSnapshot) Evaluate(position Position) (*GeographicResult, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	prepared, err := geo.PrepareRoute(s.Route.Geometry)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidGeometry, err)
	}
	return s.evaluatePrepared(position, prepared)
}

func (s GeographicSnapshot) evaluatePrepared(position Position, prepared geo.PreparedRoute) (*GeographicResult, error) {
	location := geo.Coordinate{Longitude: position.Longitude, Latitude: position.Latitude}
	routePolicy := geo.GeofencePolicy{RadiusMeters: s.Policy.CorridorRadiusMeters, MaxAccuracyMeters: s.Policy.MaxAccuracyMeters}
	result, err := prepared.EvaluateCorridor(location, position.AccuracyMeters, routePolicy)
	if err != nil {
		return nil, fmt.Errorf("%w: corridor: %v", ErrInvalid, err)
	}
	d := result.DistanceMeters
	out := &GeographicResult{SnapshotHash: s.SnapshotHash, RouteStatus: string(result.Status), RouteReason: string(result.Reason), RouteDistanceMeters: &d, MeetingPoints: make([]PointEvaluation, 0, len(s.Route.MeetingPoints))}
	pointPolicy := geo.GeofencePolicy{RadiusMeters: s.Policy.PointRadiusMeters, MaxAccuracyMeters: s.Policy.MaxAccuracyMeters}
	for _, point := range s.Route.MeetingPoints {
		e, err := geo.EvaluateCircle(location, point.Location, position.AccuracyMeters, pointPolicy)
		if err != nil {
			return nil, fmt.Errorf("%w: meeting point: %v", ErrInvalid, err)
		}
		out.MeetingPoints = append(out.MeetingPoints, PointEvaluation{ID: point.ID, Status: string(e.Status), Reason: string(e.Reason), DistanceMeters: e.DistanceMeters})
	}
	return out, nil
}

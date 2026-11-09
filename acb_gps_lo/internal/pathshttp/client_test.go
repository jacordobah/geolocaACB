package pathshttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/routes"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/server"
)

const routeID = "507f1f77bcf86cd799439011"
const firstID = "507f1f77bcf86cd799439012"
const secondID = "507f1f77bcf86cd799439013"
const schoolID = "507f1f77bcf86cd799439014"

// Shape and values follow test_route_detail_http.py and the current MongoMapRepository.
const routeJSON = `{
  "_id":"507f1f77bcf86cd799439011", "name":"541 RUTA DEMO",
  "route_code":"MM01", "code":"541", "day_shift":"JORNADA MAÑANA",
  "geometry":{"type":"LineString","coordinates":[[-74.1,4.6],[-74.2,4.7]]},
  "endpoint_status":"resolved",
  "start_meeting_point_id":"507f1f77bcf86cd799439012",
  "end_meeting_point_id":"507f1f77bcf86cd799439014",
  "first_meeting_point_id":"507f1f77bcf86cd799439012",
  "endpoints":{
    "start":{"source":"folder","distance_m":1.5,"confidence":"high","matched_vertex_index":0},
    "end":{"source":"folder","distance_m":8.25,"confidence":"high","matched_vertex_index":1}
  },
  "meeting_points":[
    {"_id":"507f1f77bcf86cd799439013","name":"PE02","kind":"meeting_point","description":null,"location":{"type":"Point","coordinates":[-74.15,4.65]}},
    {"_id":"507f1f77bcf86cd799439012","name":"PE01","kind":"meeting_point","description":null,"location":{"type":"Point","coordinates":[-74.1,4.6]}}
  ]
}`

func fixture(t *testing.T) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal([]byte(routeJSON), &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func jsonBody(t *testing.T, document map[string]any) string {
	t.Helper()
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func testClient(t *testing.T, handler http.HandlerFunc, timeout time.Duration, maxBytes int64) *Client {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	client, err := New(upstream.URL, "/api/v1/routes/{route_id}", "", timeout, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestGatewayPrefixAndServiceCredential(t *testing.T) {
	var requests atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/paths/api/v1/routes/"+routeID || r.Header.Get("Authorization") != "Bearer test-gateway-token" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("Gateway request changed: path=%q headers=%v", r.URL.Path, r.Header)
		}
		_, _ = io.WriteString(w, routeJSON)
	}))
	defer gateway.Close()
	client, err := New(gateway.URL, "/paths/api/v1/routes/{route_id}", "test-gateway-token", time.Second, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(context.Background(), routeID); err != nil || requests.Load() != 1 {
		t.Fatalf("Gateway route query: requests=%d err=%v", requests.Load(), err)
	}
	for _, path := range []string{"/api/v1/api/v1/routes/{route_id}?x=1", "/paths/api/v1/routes", "/paths/api/v1/routes/{route_id}/extra"} {
		if _, err := New(gateway.URL, path, "", time.Second, 1<<20); err == nil {
			t.Fatalf("accepted ambiguous route path %q", path)
		}
	}
}

func TestGatewayFailureDoesNotRedirectToPython(t *testing.T) {
	var directRequests atomic.Int32
	python := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directRequests.Add(1)
		_, _ = io.WriteString(w, routeJSON)
	}))
	defer python.Close()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", python.URL+"/api/v1/routes/"+routeID)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer gateway.Close()
	client, err := New(gateway.URL, "/paths/api/v1/routes/{route_id}", "", time.Second, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(context.Background(), routeID); err == nil || directRequests.Load() != 0 {
		t.Fatalf("Gateway redirect followed: err=%v direct=%d", err, directRequests.Load())
	}
}

func TestGetRoutePreservesCurrentContract(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/routes/"+routeID || r.Header.Get("Accept") != "application/json" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, routeJSON)
	}, time.Second, 1<<20)
	route, err := client.Get(context.Background(), routeID)
	if err != nil {
		t.Fatal(err)
	}
	if route.ID != routeID || route.RouteCode == nil || *route.RouteCode != "MM01" ||
		route.Code == nil || *route.Code != "541" || route.DayShift == nil ||
		*route.DayShift != "JORNADA MAÑANA" {
		t.Fatalf("metadata not preserved: %+v", route)
	}
	if route.GeometryVersion != nil || route.AssociationStatus != nil {
		t.Fatal("current Python contract must not invent version or association status")
	}
	if len(route.Geometry.Coordinates) != 2 || route.Geometry.Coordinates[0].Longitude != -74.1 ||
		route.Geometry.Coordinates[0].Latitude != 4.6 {
		t.Fatalf("GeoJSON coordinate order changed: %+v", route.Geometry)
	}
	if len(route.MeetingPoints) != 2 || route.MeetingPoints[0].ID != secondID ||
		route.MeetingPoints[1].ID != firstID {
		t.Fatalf("meeting point order changed: %+v", route.MeetingPoints)
	}
	if route.Endpoints.Start.PointID == nil || *route.Endpoints.Start.PointID != firstID ||
		route.Endpoints.End.PointID == nil || *route.Endpoints.End.PointID != schoolID ||
		route.Endpoints.Start.Match == nil || *route.Endpoints.Start.Match.Confidence != "high" ||
		route.Endpoints.Status == nil || *route.Endpoints.Status != "resolved" {
		t.Fatalf("endpoint information lost: %+v", route.Endpoints)
	}
	for _, expected := range []string{"variant", "geometry_version", "association_status"} {
		if !contains(route.Unavailable, expected) {
			t.Fatalf("%s must remain explicitly unavailable: %v", expected, route.Unavailable)
		}
	}
}

func TestGetRouteWithoutMeetingPointsIsBasicOnly(t *testing.T) {
	document := fixture(t)
	document["meeting_points"] = []any{}
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, jsonBody(t, document))
	}, time.Second, 1<<20)
	route, err := client.Get(context.Background(), routeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(route.MeetingPoints) != 0 || !contains(route.Unavailable, "meeting_point_association_provenance") ||
		!contains(route.Unavailable, "first_point_membership") {
		t.Fatalf("legacy association uncertainty lost: %+v", route)
	}
}

func TestUnlistedFirstPointIsNotAssumedToBeAssociated(t *testing.T) {
	document := fixture(t)
	document["first_meeting_point_id"] = schoolID
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, jsonBody(t, document))
	}, time.Second, 1<<20)
	route, err := client.Get(context.Background(), routeID)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(route.Unavailable, "first_point_membership") {
		t.Fatalf("unverified first point membership was hidden: %+v", route)
	}
}

func TestInvalidRequestIdentifierDoesNotCallPython(t *testing.T) {
	called := false
	client := testClient(t, func(http.ResponseWriter, *http.Request) { called = true }, time.Second, 1<<20)
	for _, id := range []string{"", " bad ", "part/other"} {
		_, err := client.Get(context.Background(), id)
		if !errors.Is(err, routes.ErrInvalidIdentifier) {
			t.Fatalf("id %q: %v", id, err)
		}
	}
	if called {
		t.Fatal("invalid ID reached Python")
	}
}

func TestHTTPStatuses(t *testing.T) {
	for _, tt := range []struct {
		status int
		body   string
		want   error
	}{
		{404, `{"detail":{"code":"route_not_found"}}`, routes.ErrNotFound},
		{422, `{"detail":{"code":"invalid_identifier"}}`, routes.ErrInvalidIdentifier},
		{409, `{"detail":{"code":"invalid_geographic_association"}}`, routes.ErrInvalidGeographicData},
		{500, `{"detail":"error"}`, routes.ErrUnavailable},
		{503, `{"detail":{"code":"geographic_store_unavailable"}}`, routes.ErrUnavailable},
	} {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}, time.Second, 1<<20)
			_, err := client.Get(context.Background(), routeID)
			if !errors.Is(err, tt.want) {
				t.Fatalf("status %d: got %v, wanted %v", tt.status, err, tt.want)
			}
			var statusError *StatusError
			if !errors.As(err, &statusError) || statusError.StatusCode != tt.status {
				t.Fatalf("HTTP status lost: %v", err)
			}
		})
	}
}

func TestRedirectIsReportedWithoutFollowing(t *testing.T) {
	redirected := false
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected = true
	}))
	defer destination.Close()
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", destination.URL)
		w.WriteHeader(http.StatusFound)
	}, time.Second, 1<<20)
	_, err := client.Get(context.Background(), routeID)
	var statusError *StatusError
	if !errors.As(err, &statusError) || statusError.StatusCode != http.StatusFound || redirected {
		t.Fatalf("redirect handling failed: err=%v followed=%v", err, redirected)
	}
}

func TestNetworkErrorTimeoutAndCancellation(t *testing.T) {
	t.Run("connection refused", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		address := upstream.URL
		upstream.Close()
		client, err := New(address, "/api/v1/routes/{route_id}", "", time.Second, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Get(context.Background(), routeID)
		if !errors.Is(err, routes.ErrUnavailable) {
			t.Fatalf("network error not classified: %v", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}, 20*time.Millisecond, 1<<20)
		_, err := client.Get(context.Background(), routeID)
		if !errors.Is(err, routes.ErrUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("timeout not propagated: %v", err)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		started := make(chan struct{})
		client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			close(started)
			<-r.Context().Done()
		}, time.Second, 1<<20)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error, 1)
		go func() { _, err := client.Get(ctx, routeID); result <- err }()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("request did not reach upstream")
		}
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation not propagated: %v", err)
		}
	})
}

func TestMalformedResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"invalid JSON", `{"geometry":`},
		{"wrong geometry", mutate(t, func(doc map[string]any) { doc["geometry"].(map[string]any)["type"] = "Point" })},
		{"missing geometry", mutate(t, func(doc map[string]any) { delete(doc, "geometry") })},
		{"short line", mutate(t, func(doc map[string]any) { doc["geometry"].(map[string]any)["coordinates"] = []any{[]any{-74.1, 4.6}} })},
		{"null longitude", mutate(t, func(doc map[string]any) {
			doc["geometry"].(map[string]any)["coordinates"] = []any{[]any{nil, 4.6}, []any{-74.2, 4.7}}
		})},
		{"string latitude", mutate(t, func(doc map[string]any) {
			doc["geometry"].(map[string]any)["coordinates"] = []any{[]any{-74.1, "4.6"}, []any{-74.2, 4.7}}
		})},
		{"nonfinite longitude", strings.Replace(routeJSON, "-74.1,4.6", "1e309,4.6", 1)},
		{"reversed bounds", mutate(t, func(doc map[string]any) {
			doc["geometry"].(map[string]any)["coordinates"] = []any{[]any{4.6, -200.0}, []any{-74.2, 4.7}}
		})},
		{"out of range point", mutate(t, func(doc map[string]any) {
			points := doc["meeting_points"].([]any)
			points[0].(map[string]any)["location"].(map[string]any)["coordinates"] = []any{-181.0, 4.6}
		})},
		{"missing meeting point array", mutate(t, func(doc map[string]any) { delete(doc, "meeting_points") })},
		{"missing route name", mutate(t, func(doc map[string]any) { delete(doc, "name") })},
		{"route ID mismatch", mutate(t, func(doc map[string]any) { doc["_id"] = secondID })},
		{"duplicate meeting point ID", mutate(t, func(doc map[string]any) {
			points := doc["meeting_points"].([]any)
			points[1].(map[string]any)["_id"] = secondID
		})},
		{"wrong point kind", mutate(t, func(doc map[string]any) {
			points := doc["meeting_points"].([]any)
			points[0].(map[string]any)["kind"] = "school"
		})},
		{"null point coordinate", mutate(t, func(doc map[string]any) {
			points := doc["meeting_points"].([]any)
			points[0].(map[string]any)["location"].(map[string]any)["coordinates"] = []any{-74.15, nil}
		})},
		{"endpoint metadata without ID", mutate(t, func(doc map[string]any) { delete(doc, "start_meeting_point_id") })},
		{"endpoint vertex outside line", mutate(t, func(doc map[string]any) {
			doc["endpoints"].(map[string]any)["start"].(map[string]any)["matched_vertex_index"] = 4
		})},
		{"negative endpoint distance", mutate(t, func(doc map[string]any) {
			doc["endpoints"].(map[string]any)["start"].(map[string]any)["distance_m"] = -1
		})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tt.body)
			}, time.Second, 1<<20)
			_, err := client.Get(context.Background(), routeID)
			if !errors.Is(err, routes.ErrInvalidResponse) {
				t.Fatalf("expected invalid response, got %v", err)
			}
		})
	}
}

func TestResponseSizeLimit(t *testing.T) {
	for _, setLength := range []bool{false, true} {
		t.Run(map[bool]string{false: "streamed", true: "content length"}[setLength], func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				if setLength {
					w.Header().Set("Content-Length", "4096")
				}
				_, _ = io.WriteString(w, strings.Repeat("x", 4096))
			}, time.Second, 128)
			_, err := client.Get(context.Background(), routeID)
			if !errors.Is(err, routes.ErrResponseTooLarge) || !errors.Is(err, routes.ErrInvalidResponse) {
				t.Fatalf("response size not enforced: %v", err)
			}
		})
	}
}

type healthyRedis struct{}

func (healthyRedis) Ping(context.Context) error { return nil }

func TestHealthDoesNotDependOnPython(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	address := upstream.URL
	upstream.Close()
	client, err := New(address, "/api/v1/routes/{route_id}", "", time.Second, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Get(context.Background(), routeID)
	if !errors.Is(err, routes.ErrUnavailable) {
		t.Fatal(err)
	}
	handler := server.Handler(healthyRedis{}, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, path := range []string{"/health/live", "/health/ready"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s returned %d while Python was down", path, response.Code)
		}
	}
}

// Opt-in read-only check through an already running API Gateway.
func TestGatewayIntegrationOptIn(t *testing.T) {
	baseURL := os.Getenv("GPS_GATEWAY_BASE_URL")
	path := os.Getenv("GPS_GATEWAY_ROUTES_PATH")
	id := os.Getenv("GPS_PATHS_TEST_ROUTE_ID")
	if baseURL == "" && path == "" && id == "" {
		t.Skip("set GPS_GATEWAY_BASE_URL, GPS_GATEWAY_ROUTES_PATH and GPS_PATHS_TEST_ROUTE_ID for a read-only integration check")
	}
	if baseURL == "" || path == "" || id == "" {
		t.Fatal("Gateway origin, route path and route ID are required")
	}
	client, err := New(baseURL, path, os.Getenv("GPS_GATEWAY_ROUTES_TOKEN"), 3*time.Second, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	route, err := client.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if route.ID != id || len(route.Geometry.Coordinates) < 2 {
		t.Fatalf("unexpected route detail: %+v", route)
	}
	pointIDs := make([]string, len(route.MeetingPoints))
	for index, point := range route.MeetingPoints {
		pointIDs[index] = point.ID
	}
	t.Logf("route=%s vertices=%d first_coordinate=[%v,%v] ordered_point_ids=%v unavailable=%v",
		route.ID, len(route.Geometry.Coordinates),
		route.Geometry.Coordinates[0].Longitude, route.Geometry.Coordinates[0].Latitude,
		pointIDs, route.Unavailable)
}

func mutate(t *testing.T, change func(map[string]any)) string {
	t.Helper()
	document := fixture(t)
	change(document)
	return jsonBody(t, document)
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

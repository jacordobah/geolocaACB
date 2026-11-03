package gatewayurl

import "testing"

func TestRouteAndEndpointUseCompleteGatewayPaths(t *testing.T) {
	got, err := Route("https://gateway.example.test/", "/paths/api/v1/routes/{route_id}", "r%42")
	if err != nil || got != "https://gateway.example.test/paths/api/v1/routes/r%2542" {
		t.Fatalf("route URL: %q %v", got, err)
	}
	got, err = Endpoint("https://gateway.example.test", "/operations/api/v1/events")
	if err != nil || got != "https://gateway.example.test/operations/api/v1/events" {
		t.Fatalf("event URL: %q %v", got, err)
	}
}

func TestRejectAmbiguousOrUnsafeGatewayPaths(t *testing.T) {
	for _, origin := range []string{"http://user@gateway.test", "http://gateway.test/api/v1", "gateway.test:8080", "http://gateway.test?x=1"} {
		if _, err := Origin(origin); err == nil {
			t.Fatalf("accepted origin %q", origin)
		}
	}
	for _, path := range []string{"api/v1/routes/{route_id}", "/api/v1/routes/{route_id}/extra", "/api/v1/routes/{route_id}/{route_id}", "/api/v1//routes/{route_id}", "/api/v1/../routes/{route_id}", "/api/v1/routes/%7Broute_id%7D", "/api/v1/routes/{route_id}?x=1", "/api/v1/api/v1/routes/{route_id}"} {
		if err := RoutePath(path); err == nil {
			t.Fatalf("accepted route path %q", path)
		}
	}
	for _, path := range []string{"/events/{route_id}", "/events?token=secret", "//events", "/events/"} {
		if _, err := Endpoint("http://gateway.test", path); err == nil {
			t.Fatalf("accepted event path %q", path)
		}
	}
}

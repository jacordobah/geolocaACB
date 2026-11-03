package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Runs the real binary with an isolated Redis and an in-process fake Gateway.
// GPS_TEST_REDIS_ADDR must refer to a disposable instance, never a live service.
func TestBinaryTrustedGatewayFlow(t *testing.T) {
	if os.Getenv("GPS_TEST_REDIS_ISOLATED") != "1" || os.Getenv("GPS_TEST_REDIS_ADDR") == "" {
		t.Skip("requires explicitly isolated Redis")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("requires Go toolchain to build the binary")
	}
	workdir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "acb-gps-test")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build binary: %v %s", err, output)
	}
	baseEnv := append(os.Environ(), "GPS_REDIS_ADDR="+os.Getenv("GPS_TEST_REDIS_ADDR"), "GPS_OPERATIONS_DELIVERY_ENABLED=false")
	port := freePort(t)
	off := startGPS(t, binary, append(baseEnv, fmt.Sprintf("GPS_HTTP_PORT=%d", port), "GPS_CONTROL_API_ENABLED=false", "GPS_READ_API_ENABLED=false"), port)
	status, _ := gpsRequest(t, http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/api/v1/tracking-sessions", port), "", "{}", nil)
	if status != 404 {
		t.Fatalf("disabled control HTTP %d", status)
	}
	off()

	var gatewayHits atomic.Int32
	const routeFixture = `{"_id":"route-integrated","name":"Synthetic route","geometry_version":"revision-test-1","association_status":"verified","geometry":{"type":"LineString","coordinates":[[-74.1,4.6],[-74.1005,4.6005]]},"endpoint_status":"resolved","first_meeting_point_id":"pe-01","start_meeting_point_id":"pe-01","end_meeting_point_id":"pe-02","meeting_points":[{"_id":"pe-01","name":"PE01","kind":"meeting_point","location":{"type":"Point","coordinates":[-74.1,4.6]}},{"_id":"pe-02","name":"PE02","kind":"meeting_point","location":{"type":"Point","coordinates":[-74.1005,4.6005]}}]}`
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gatewayHits.Add(1)
		if r.Header.Get("Authorization") != "Bearer synthetic-egress" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/paths/api/v1/routes/route-integrated":
			_, _ = io.WriteString(w, routeFixture)
		case "/paths/api/v1/routes/bad-route":
			bad := strings.Replace(routeFixture, `"route-integrated"`, `"bad-route"`, 1)
			bad = strings.Replace(bad, `"geometry_version":"revision-test-1",`, "", 1)
			_, _ = io.WriteString(w, bad)
		default:
			w.WriteHeader(404)
		}
	}))
	defer gateway.Close()
	policyPath := filepath.Join(workdir, "..", "..", "config", "tracking-policies.yaml")
	token := randomToken(t)
	onEnv := append(baseEnv,
		fmt.Sprintf("GPS_HTTP_PORT=%d", port), "GPS_CONTROL_API_ENABLED=true", "GPS_READ_API_ENABLED=true",
		"GPS_GATEWAY_BASE_URL="+gateway.URL, "GPS_GATEWAY_ROUTES_PATH=/paths/api/v1/routes/{route_id}",
		"GPS_GATEWAY_ROUTES_TOKEN=synthetic-egress", "GPS_POLICIES_FILE="+policyPath,
		"GPS_GATEWAY_INTERNAL_TOKEN="+token, "GPS_CONTROL_CREDENTIAL_KEY="+randomToken(t))
	// An enabled but incomplete configuration fails before opening the port.
	missing := exec.Command(binary)
	missing.Env = append(baseEnv, fmt.Sprintf("GPS_HTTP_PORT=%d", freePort(t)), "GPS_CONTROL_API_ENABLED=true")
	if err := missing.Run(); err == nil {
		t.Fatal("incomplete control configuration started")
	}
	badCatalog := filepath.Join(t.TempDir(), "invalid-policies.yaml")
	if err := os.WriteFile(badCatalog, []byte("schema_version: no\n"), 0600); err != nil {
		t.Fatal(err)
	}
	invalid := exec.Command(binary)
	invalid.Env = append(onEnv, "GPS_POLICIES_FILE="+badCatalog)
	if err := invalid.Run(); err == nil {
		t.Fatal("invalid tracking policy catalog started")
	}
	start := startGPS(t, binary, onEnv, port)
	defer func() { start() }()
	root := fmt.Sprintf("http://127.0.0.1:%d/api/v1/tracking-sessions", port)
	createBody := `{"trip_id":"trip-integrated","guide_id":"guide-integrated","geo_route_id":"route-integrated","policy_id":"demo-school-v1"}`
	createHeaders := map[string]string{"Idempotency-Key": randomToken(t), "Content-Type": "application/json"}
	status, _ = gpsRequest(t, http.MethodPost, root, "", createBody, createHeaders)
	if status != 401 {
		t.Fatalf("missing Gateway credential HTTP %d", status)
	}
	status, _ = gpsRequest(t, http.MethodPost, root, randomToken(t), createBody, createHeaders)
	if status != 401 {
		t.Fatalf("wrong Gateway credential HTTP %d", status)
	}
	status, body := gpsRequest(t, http.MethodPost, root, token, createBody, createHeaders)
	if status != 201 {
		t.Fatalf("create HTTP %d: %s", status, body)
	}
	var created struct {
		TrackingID string `json:"tracking_id"`
		Credential string `json:"observation_credential"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.TrackingID == "" || created.Credential == "" {
		t.Fatalf("create response %v: %s", err, body)
	}
	status, body = gpsRequest(t, http.MethodPost, root, token, createBody, createHeaders)
	if status != 200 || !bytes.Contains(body, []byte(created.TrackingID)) {
		t.Fatalf("replay HTTP %d: %s", status, body)
	}
	status, _ = gpsRequest(t, http.MethodPost, root, token, strings.Replace(createBody, "guide-integrated", "guide-other", 1), createHeaders)
	if status != 409 {
		t.Fatalf("conflict HTTP %d", status)
	}
	status, _ = gpsRequest(t, http.MethodPost, root, token, strings.Replace(createBody, "demo-school-v1", "unknown-v1", 1), map[string]string{"Idempotency-Key": randomToken(t), "Content-Type": "application/json"})
	if status != 400 {
		t.Fatalf("unknown policy HTTP %d", status)
	}
	status, _ = gpsRequest(t, http.MethodPost, root, token, strings.Replace(createBody, "route-integrated", "missing-route", 1), map[string]string{"Idempotency-Key": randomToken(t), "Content-Type": "application/json"})
	if status != 404 {
		t.Fatalf("missing route HTTP %d", status)
	}
	status, _ = gpsRequest(t, http.MethodPost, root, token, strings.Replace(createBody, "route-integrated", "bad-route", 1), map[string]string{"Idempotency-Key": randomToken(t), "Content-Type": "application/json"})
	if status != 422 {
		t.Fatalf("ineligible route HTTP %d", status)
	}
	if gatewayHits.Load() == 0 {
		t.Fatal("route was not requested through Gateway")
	}
	path := root + "/" + created.TrackingID
	status, _ = gpsRequest(t, http.MethodGet, path, token, "", nil)
	if status != 200 {
		t.Fatalf("status HTTP %d", status)
	}
	status, _ = gpsRequest(t, http.MethodGet, path+"/positions", created.Credential, "", nil)
	if status != 401 {
		t.Fatalf("GPS credential read HTTP %d", status)
	}
	observation := fmt.Sprintf(`{"sequence":1,"latitude":4.6,"longitude":-74.1,"accuracy_m":5,"captured_at":%q}`, time.Now().UTC().Truncate(time.Millisecond).Format(time.RFC3339Nano))
	status, body = gpsRequest(t, http.MethodPost, path+"/observations", created.Credential, observation, map[string]string{"Content-Type": "application/json"})
	if status != 201 || !bytes.Contains(body, []byte(`"geographic_evaluation"`)) {
		t.Fatalf("observation HTTP %d: %s", status, body)
	}
	status, body = gpsRequest(t, http.MethodGet, path+"/latest-position", token, "", nil)
	if status != 200 || !bytes.Contains(body, []byte(`"sequence":1`)) {
		t.Fatalf("latest HTTP %d: %s", status, body)
	}
	status, body = gpsRequest(t, http.MethodGet, path+"/positions?limit=1", token, "", nil)
	if status != 200 || !bytes.Contains(body, []byte(`"sequence":1`)) {
		t.Fatalf("history HTTP %d: %s", status, body)
	}
	status, body = gpsRequest(t, http.MethodPost, path+"/finish", token, "", nil)
	if status != 200 || !bytes.Contains(body, []byte(`"state":"finished"`)) {
		t.Fatalf("finish HTTP %d: %s", status, body)
	}
	status, _ = gpsRequest(t, http.MethodPost, path+"/finish", token, "", nil)
	if status != 200 {
		t.Fatalf("repeated finish HTTP %d", status)
	}
	status, _ = gpsRequest(t, http.MethodPost, path+"/observations", created.Credential, observation, map[string]string{"Content-Type": "application/json"})
	if status != 401 {
		t.Fatalf("revoked observation HTTP %d", status)
	}
	start()
	start = startGPS(t, binary, onEnv, port)
	status, body = gpsRequest(t, http.MethodGet, path, token, "", nil)
	if status != 200 || !bytes.Contains(body, []byte(`"state":"finished"`)) {
		t.Fatalf("restart recovery HTTP %d: %s", status, body)
	}
	status, body = gpsRequest(t, http.MethodGet, path+"/positions", token, "", nil)
	if status != 200 || !bytes.Contains(body, []byte(`"sequence":1`)) {
		t.Fatalf("retained history HTTP %d: %s", status, body)
	}
}

func randomToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func startGPS(t *testing.T, binary string, env []string, port int) func() {
	t.Helper()
	cmd := exec.Command(binary)
	cmd.Env = env
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = cmd.Process.Signal(syscall.SIGTERM)
		if err := cmd.Wait(); err != nil {
			t.Errorf("GPS process exit: %v %s", err, logs.String())
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health/live", port))
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return stop
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()
	t.Fatalf("GPS did not start: %s", logs.String())
	return nil
}

func gpsRequest(t *testing.T, method, endpoint, token, body string, headers map[string]string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, data
}

package loadtest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"runtime/metrics"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/geo"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/httpapi"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/operationshttp"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/outbox"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/redisstore"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/routes"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/server"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
	"github.com/redis/go-redis/v9"
)

type redisPinger struct{ client *redis.Client }

func (p redisPinger) Ping(ctx context.Context) error { return p.client.Ping(ctx).Err() }

func percentile(values []time.Duration, p float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	index := int(float64(len(values)-1) * p)
	return values[index]
}
func cpuSeconds() float64 {
	samples := []metrics.Sample{{Name: "/cpu/classes/total:cpu-seconds"}}
	metrics.Read(samples)
	if samples[0].Value.Kind() == metrics.KindFloat64 {
		return samples[0].Value.Float64()
	}
	return 0
}
func redisMetric(raw, name string) int64 {
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, name+":") {
			value, _ := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, name+":")), 10, 64)
			return value
		}
	}
	return 0
}

// Opt-in only. Use a disposable Redis with GPS_TEST_REDIS_ISOLATED=1; this
// test clears logical DB 6 and never addresses the production Compose Redis.
func TestSyntheticLoad(t *testing.T) {
	if os.Getenv("GPS_LOAD_TEST") != "1" {
		t.Skip("opt-in synthetic load")
	}
	if os.Getenv("GPS_TEST_REDIS_ISOLATED") != "1" {
		t.Fatal("isolated Redis flag required")
	}
	addr := os.Getenv("GPS_TEST_REDIS_ADDR")
	if addr == "" {
		t.Fatal("isolated Redis address required")
	}
	sessions, err := strconv.Atoi(os.Getenv("GPS_LOAD_SESSIONS"))
	if err != nil || sessions < 1 || sessions > 1000 {
		t.Fatal("GPS_LOAD_SESSIONS must be 1..1000")
	}
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: addr, DB: 6, PoolSize: 96, DialTimeout: time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, MaxRetries: 0})
	defer client.Close()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.FlushDB(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	store, err := redisstore.New(client, 24*time.Hour, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	tracker, err := tracking.NewService(store, 15*time.Minute, 30*time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, last, status, verified, version := "synthetic-PE01", "synthetic-PE02", "resolved", "verified", "synthetic-v1"
	route := routes.Route{ID: "synthetic-route", Name: "synthetic load route", GeometryVersion: &version, AssociationStatus: &verified, Geometry: geo.LineString{Coordinates: []geo.Coordinate{{Longitude: -74.1, Latitude: 4.6}, {Longitude: -74.101, Latitude: 4.6}}}, MeetingPoints: []routes.MeetingPoint{{ID: first, Name: first, Kind: "meeting_point", Location: geo.Coordinate{Longitude: -74.1, Latitude: 4.6}}, {ID: last, Name: last, Kind: "meeting_point", Location: geo.Coordinate{Longitude: -74.101, Latitude: 4.6}}}, Endpoints: routes.Endpoints{Status: &status, FirstPointID: &first, Start: routes.Endpoint{PointID: &first}, End: routes.Endpoint{PointID: &last}}}
	snapshot, err := tracking.BuildSnapshotWithTransitions(route, tracking.GeographicPolicy{CorridorRadiusMeters: 30, PointRadiusMeters: 20, MaxAccuracyMeters: 25}, &tracking.TransitionPolicy{ArrivalObservations: 1, DeviationObservations: 2, RecoveryObservations: 2, MaxSampleGap: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	type loadIdentity struct{ id, token string }
	identities := make([]loadIdentity, sessions)
	started := time.Now().UTC().Truncate(time.Millisecond)
	for i := range identities {
		id := fmt.Sprintf("synthetic-load-%d-%d", started.UnixNano(), i)
		secret := sha256.Sum256([]byte("synthetic-load-only:" + id))
		session := tracking.Session{ID: id, GuideID: "synthetic-guide", TripID: "synthetic-trip", RouteID: route.ID, State: tracking.Active, StartedAt: started, MaxObservationAge: 15 * time.Minute, FutureSkew: 30 * time.Second, SnapshotHash: snapshot.SnapshotHash}
		if err := tracker.ProvisionPrepared(ctx, session, secret[:], &snapshot); err != nil {
			t.Fatalf("provision %d: %v", i, err)
		}
		identities[i] = loadIdentity{id: id, token: base64.RawURLEncoding.EncodeToString(secret[:])}
	}
	var delivered atomic.Int64
	var delayMu sync.Mutex
	var delays []time.Duration
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Event struct {
				ID         string    `json:"event_id"`
				Type       string    `json:"event_type"`
				OccurredAt time.Time `json:"occurred_at"`
			} `json:"event"`
		}
		if json.NewDecoder(r.Body).Decode(&payload) != nil || r.Header.Get("Idempotency-Key") != payload.Event.ID {
			w.WriteHeader(400)
			return
		}
		if payload.Event.Type == string(tracking.FirstMeetingPointArrival) {
			delayMu.Lock()
			delays = append(delays, time.Since(payload.Event.OccurredAt))
			delayMu.Unlock()
		}
		delivered.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"schema_version":"gps.ack.v1","event_id":%q,"status":"persisted"}`, payload.Event.ID)
	}))
	defer receiver.Close()
	operations, err := operationshttp.New(receiver.URL, "/events", "synthetic-service-token", time.Second, 4096)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := outbox.New(store, operations, outbox.Config{Workers: 4, Poll: 100 * time.Millisecond, Lease: 3 * time.Second, RetryMin: time.Second, RetryMax: time.Minute, MaxPerHour: 3600, DeliveredRetention: time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	deliveryCtx, cancelDelivery := context.WithCancel(ctx)
	deliveryDone := dispatcher.Start(deliveryCtx)
	defer func() { cancelDelivery(); <-deliveryDone }()
	observations, _ := httpapi.NewObservations(tracker, 4096, 4*time.Second)
	api := httptest.NewServer(server.HandlerWithTracking(redisPinger{client}, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), observations))
	defer api.Close()
	httpClient := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{MaxIdleConns: 256, MaxIdleConnsPerHost: 128, MaxConnsPerHost: 128}}
	var beforeMem runtime.MemStats
	runtime.ReadMemStats(&beforeMem)
	beforeCPU := cpuSeconds()
	redisBefore, _ := client.Info(ctx, "memory", "persistence").Result()
	latency := make(chan time.Duration, sessions*2)
	var errorsCount atomic.Int64
	var readCount atomic.Int64
	begin := time.Now()
	var wg sync.WaitGroup
	for i, identity := range identities {
		wg.Add(1)
		go func(i int, identity loadIdentity) {
			defer wg.Done()
			for seq := 1; seq <= 2; seq++ {
				target := begin.Add(time.Duration(seq-1)*2*time.Second + time.Duration(i)*2*time.Second/time.Duration(sessions))
				if delay := time.Until(target); delay > 0 {
					time.Sleep(delay)
				}
				body, _ := json.Marshal(map[string]any{"sequence": seq, "latitude": 4.6, "longitude": -74.1, "accuracy_m": 5, "captured_at": time.Now().UTC().Truncate(time.Millisecond).Format(time.RFC3339Nano)})
				r, err := http.NewRequest(http.MethodPost, api.URL+"/api/v1/tracking-sessions/"+identity.id+"/observations", bytes.NewReader(body))
				if err != nil {
					errorsCount.Add(1)
					return
				}
				r.Header.Set("Authorization", "Bearer "+identity.token)
				r.Header.Set("Content-Type", "application/json")
				start := time.Now()
				response, err := httpClient.Do(r)
				if err != nil {
					errorsCount.Add(1)
					return
				}
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				if response.StatusCode != 201 {
					errorsCount.Add(1)
					return
				}
				latency <- time.Since(start)
			}
			if i%10 == 0 {
				if _, err := store.Latest(ctx, identity.id); err == nil {
					readCount.Add(1)
				}
				if _, err := store.History(ctx, identity.id, 0, 10); err == nil {
					readCount.Add(1)
				}
			}
		}(i, identity)
	}
	wg.Wait()
	elapsed := time.Since(begin)
	close(latency)
	values := make([]time.Duration, 0, len(latency))
	for value := range latency {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	time.Sleep(time.Second)
	pending, blocked, oldest, err := store.OutboxStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var afterMem runtime.MemStats
	runtime.ReadMemStats(&afterMem)
	redisAfter, _ := client.Info(ctx, "memory", "persistence").Result()
	delayMu.Lock()
	sort.Slice(delays, func(i, j int) bool { return delays[i] < delays[j] })
	deliveryP95 := percentile(delays, .95)
	delayMu.Unlock()
	t.Logf("sessions=%d target_rps=%.0f actual_rps=%.1f requests=%d errors=%d reads=%d elapsed=%s p50=%s p95=%s p99=%s go_heap_before=%d go_heap_after=%d go_cpu_seconds_delta=%.3f redis_used_memory_before=%d redis_used_memory_after=%d aof_size_before=%d aof_size_after=%d aof_delayed_fsync=%d outbox_pending=%d blocked=%d oldest_due=%s delivered=%d delivery_p95=%s", sessions, float64(sessions)/2, float64(len(values))/elapsed.Seconds(), len(values), errorsCount.Load(), readCount.Load(), elapsed, percentile(values, .5), percentile(values, .95), percentile(values, .99), beforeMem.Alloc, afterMem.Alloc, cpuSeconds()-beforeCPU, redisMetric(redisBefore, "used_memory"), redisMetric(redisAfter, "used_memory"), redisMetric(redisBefore, "aof_current_size"), redisMetric(redisAfter, "aof_current_size"), redisMetric(redisAfter, "aof_delayed_fsync"), pending, blocked, oldest, delivered.Load(), deliveryP95)
	if errorsCount.Load() != 0 {
		t.Fatalf("%d failed requests", errorsCount.Load())
	}
}

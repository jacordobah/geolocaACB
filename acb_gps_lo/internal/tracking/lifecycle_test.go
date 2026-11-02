package tracking

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLifecycleEventHasNoInventedObservation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	session := Session{ID: "synthetic-session", GuideID: "synthetic-guide", TripID: "synthetic-trip", RouteID: "synthetic-route", State: Active, StartedAt: now}
	for _, kind := range []EventType{TrackingSessionActivated, TrackingSessionFinished} {
		event, err := NewLifecycleEvent(session, kind, now)
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"sequence", "latitude", "longitude", "captured_at", "accuracy_meters"} {
			if strings.Contains(string(body), forbidden) {
				t.Fatalf("%s contains %s", kind, forbidden)
			}
		}
		var decoded Event
		if err := json.Unmarshal(body, &decoded); err != nil || decoded.Validate() != nil || decoded.ID != event.ID {
			t.Fatalf("round trip %s: %v %+v", kind, err, decoded)
		}
		repeated, _ := NewLifecycleEvent(session, kind, now.Add(time.Second))
		if repeated.ID != event.ID {
			t.Fatal("lifecycle event ID changed on retry")
		}
	}
}

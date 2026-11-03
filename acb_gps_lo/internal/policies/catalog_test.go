package policies

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
)

const valid = `schema_version: 1
policies:
  - policy_id: demo-v1
    revision: 1
    geographic:
      corridor_radius_meters: 30
      point_radius_meters: 20
      max_accuracy_meters: 25
    transitions:
      arrival_observations: 2
      arrival_dwell: 1s
      deviation_observations: 2
      deviation_dwell: 1s
      recovery_observations: 2
      recovery_dwell: 1s
      max_sample_gap: 5s
`

func TestCatalogSelectionAndCopy(t *testing.T) {
	second := strings.ReplaceAll(strings.ReplaceAll(valid, "demo-v1", "demo-v2"), "revision: 1", "revision: 2")
	second = strings.ReplaceAll(second, "point_radius_meters: 20", "point_radius_meters: 21")
	second = strings.TrimPrefix(second, "schema_version: 1\npolicies:\n")
	c, err := Parse([]byte(valid + second))
	if err != nil {
		t.Fatal(err)
	}
	p1, err := c.Resolve(context.Background(), "demo-v1")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := c.Resolve(context.Background(), "demo-v2")
	if err != nil {
		t.Fatal(err)
	}
	if p1.Reference.DefinitionHash == p2.Reference.DefinitionHash || p2.Geographic.PointRadiusMeters != 21 {
		t.Fatal("versioned definitions were confused")
	}
	p1.Transitions.ArrivalObservations = 99
	again, _ := c.Resolve(context.Background(), "demo-v1")
	if again.Transitions.ArrivalObservations != 2 {
		t.Fatal("catalog was mutated")
	}
	if _, err := c.Resolve(context.Background(), "missing-v1"); err == nil {
		t.Fatal("missing policy selected")
	}
}

func TestCatalogRejectsInvalidDocuments(t *testing.T) {
	cases := map[string]string{
		"duplicate key":         strings.Replace(valid, "    revision: 1", "    revision: 1\n    revision: 1", 1),
		"duplicate id":          valid + strings.TrimPrefix(valid, "schema_version: 1\npolicies:\n"),
		"unknown field":         strings.Replace(valid, "    revision: 1", "    revision: 1\n    secret: no", 1),
		"null":                  strings.Replace(valid, "point_radius_meters: 20", "point_radius_meters: null", 1),
		"nan":                   strings.Replace(valid, "point_radius_meters: 20", "point_radius_meters: .nan", 1),
		"infinity":              strings.Replace(valid, "point_radius_meters: 20", "point_radius_meters: .inf", 1),
		"string number":         strings.Replace(valid, "point_radius_meters: 20", "point_radius_meters: '20'", 1),
		"no duration unit":      strings.Replace(valid, "arrival_dwell: 1s", "arrival_dwell: 1", 1),
		"bad duration":          strings.Replace(valid, "arrival_dwell: 1s", "arrival_dwell: forever", 1),
		"negative radius":       strings.Replace(valid, "point_radius_meters: 20", "point_radius_meters: -1", 1),
		"bad observation count": strings.Replace(valid, "arrival_observations: 2", "arrival_observations: 0", 1),
		"wrong revision":        strings.Replace(valid, "revision: 1", "revision: 2", 1),
		"missing field":         strings.Replace(valid, "      max_sample_gap: 5s\n", "", 1),
		"multiple documents":    valid + "---\nschema_version: 1\n",
		"malformed":             "schema_version: [\n",
		"empty":                 "",
		"alias":                 strings.Replace(valid, "    geographic:", "    geographic: &geo", 1) + "  - policy_id: other-v1\n    revision: 1\n    geographic: *geo\n    transitions: {}\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(body)); err == nil {
				t.Fatal("accepted invalid YAML")
			}
		})
	}
}

func TestLoadFileAndSnapshotImmutability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policies.yaml")
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	old, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	selected, _ := old.Resolve(context.Background(), "demo-v1")
	changed := strings.Replace(valid, "point_radius_meters: 20", "point_radius_meters: 40", 1)
	if err := os.WriteFile(path, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	still, _ := old.Resolve(context.Background(), "demo-v1")
	newCatalog, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	next, _ := newCatalog.Resolve(context.Background(), "demo-v1")
	if still.Geographic.PointRadiusMeters != 20 || selected.Reference.DefinitionHash != still.Reference.DefinitionHash || next.Reference.DefinitionHash == selected.Reference.DefinitionHash {
		t.Fatal("catalog unexpectedly reloaded or hash unchanged")
	}
	if _, err := LoadFile(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing file accepted")
	}
	if _, err := tracking.NewPolicyReference("demo-v1", 1, selected.Geographic, tracking.TransitionPolicy{ArrivalObservations: 1, DeviationObservations: 2, RecoveryObservations: 2, MaxSampleGap: time.Second}); err != nil {
		t.Fatal(err)
	}
}

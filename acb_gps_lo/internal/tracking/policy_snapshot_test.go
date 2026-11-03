package tracking

import (
	"encoding/json"
	"testing"
)

func TestVersionedPolicySnapshotAndLegacyCompatibility(t *testing.T) {
	geoPolicy := GeographicPolicy{CorridorRadiusMeters: 30, PointRadiusMeters: 20, MaxAccuracyMeters: 25}
	transition := *testTransitionPolicy()
	legacy, err := BuildSnapshotWithTransitions(syntheticRoute(), geoPolicy, &transition)
	if err != nil || legacy.Validate() != nil {
		t.Fatalf("legacy: %v", err)
	}
	legacyJSON, _ := json.Marshal(legacy)
	var restoredLegacy GeographicSnapshot
	if err := json.Unmarshal(legacyJSON, &restoredLegacy); err != nil || restoredLegacy.Validate() != nil || restoredLegacy.SnapshotHash != legacy.SnapshotHash {
		t.Fatal("legacy hash changed")
	}
	ref, err := NewPolicyReference("demo-v1", 1, geoPolicy, transition)
	if err != nil {
		t.Fatal(err)
	}
	versioned, err := BuildSnapshotWithPolicy(syntheticRoute(), geoPolicy, transition, ref)
	if err != nil || versioned.Validate() != nil || versioned.OperationalReady() != nil {
		t.Fatalf("versioned: %v", err)
	}
	if versioned.SnapshotHash == legacy.SnapshotHash || versioned.GeometryFingerprint != legacy.GeometryFingerprint {
		t.Fatal("hash semantics confused")
	}
	versionedJSON, _ := json.Marshal(versioned)
	var restored GeographicSnapshot
	if err := json.Unmarshal(versionedJSON, &restored); err != nil || restored.Validate() != nil || restored.PolicyReference.DefinitionHash != ref.DefinitionHash {
		t.Fatal("versioned snapshot did not restore")
	}
	changed := restored
	changed.Policy.PointRadiusMeters = 21
	if changed.Validate() == nil {
		t.Fatal("modified effective policy accepted")
	}
	changed = restored
	changed.PolicyReference = &PolicyReference{PolicyID: "demo-v2", Revision: 2, DefinitionHash: ref.DefinitionHash}
	if changed.Validate() == nil {
		t.Fatal("modified identity accepted")
	}
	changed = restored
	changed.SnapshotFormatVersion = 0
	if changed.Validate() == nil {
		t.Fatal("versioned snapshot downgraded")
	}
}

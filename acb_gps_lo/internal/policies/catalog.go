package policies

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/control"
	"github.com/jacordobah/geolocaACB/ACB_GPS_Lo/internal/tracking"
	"go.yaml.in/yaml/v3"
)

// Catalog is immutable after parsing. Resolve returns fresh copies of pointer fields.
type Catalog struct{ entries map[string]control.Policy }

var versionedID = regexp.MustCompile(`^(.+)-v([1-9][0-9]*)$`)

func LoadFile(path string) (*Catalog, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("policy file path is empty")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open policy file: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil {
		return nil, fmt.Errorf("read policy file: %w", err)
	}
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("policy file exceeds 1 MiB")
	}
	return Parse(data)
}

func Parse(data []byte) (*Catalog, error) {
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("policy YAML exceeds 1 MiB")
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("policy YAML: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("policy YAML must contain exactly one document")
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, fmt.Errorf("policy YAML document is empty")
	}
	root, err := mapping(doc.Content[0], "catalog", "schema_version", "policies")
	if err != nil {
		return nil, err
	}
	version, err := integer(root["schema_version"], "schema_version")
	if err != nil || version != 1 {
		return nil, fmt.Errorf("schema_version must equal 1")
	}
	list := root["policies"]
	if list.Kind != yaml.SequenceNode || list.Tag != "!!seq" || list.Anchor != "" || len(list.Content) == 0 || len(list.Content) > 256 {
		return nil, fmt.Errorf("policies must be a nonempty list of at most 256 definitions")
	}
	c := &Catalog{entries: make(map[string]control.Policy, len(list.Content))}
	for i, node := range list.Content {
		prefix := fmt.Sprintf("policies[%d]", i)
		item, err := mapping(node, prefix, "policy_id", "revision", "geographic", "transitions")
		if err != nil {
			return nil, err
		}
		id, err := scalar(item["policy_id"], "!!str", prefix+".policy_id")
		if err != nil {
			return nil, err
		}
		revision, err := integer(item["revision"], prefix+".revision")
		if err != nil || revision < 1 {
			return nil, fmt.Errorf("%s.revision must be positive", prefix)
		}
		match := versionedID.FindStringSubmatch(id)
		if !tracking.ValidID(id) || match == nil || match[2] != strconv.Itoa(revision) {
			return nil, fmt.Errorf("%s.policy_id must end in -v%d", prefix, revision)
		}
		if _, exists := c.entries[id]; exists {
			return nil, fmt.Errorf("duplicate policy_id %q", id)
		}
		geoMap, err := mapping(item["geographic"], prefix+".geographic", "corridor_radius_meters", "point_radius_meters", "max_accuracy_meters")
		if err != nil {
			return nil, err
		}
		corridor, err := number(geoMap["corridor_radius_meters"], prefix+".geographic.corridor_radius_meters")
		if err != nil {
			return nil, err
		}
		point, err := number(geoMap["point_radius_meters"], prefix+".geographic.point_radius_meters")
		if err != nil {
			return nil, err
		}
		accuracy, err := number(geoMap["max_accuracy_meters"], prefix+".geographic.max_accuracy_meters")
		if err != nil {
			return nil, err
		}
		geoPolicy := tracking.GeographicPolicy{CorridorRadiusMeters: corridor, PointRadiusMeters: point, MaxAccuracyMeters: accuracy}
		if err := geoPolicy.Validate(); err != nil {
			return nil, fmt.Errorf("%s.geographic: %w", prefix, err)
		}
		tm, err := mapping(item["transitions"], prefix+".transitions", "arrival_observations", "arrival_dwell", "deviation_observations", "deviation_dwell", "recovery_observations", "recovery_dwell", "max_sample_gap")
		if err != nil {
			return nil, err
		}
		arrival, err := integer(tm["arrival_observations"], prefix+".transitions.arrival_observations")
		if err != nil {
			return nil, err
		}
		arrivalDwell, err := duration(tm["arrival_dwell"], prefix+".transitions.arrival_dwell")
		if err != nil {
			return nil, err
		}
		deviation, err := integer(tm["deviation_observations"], prefix+".transitions.deviation_observations")
		if err != nil {
			return nil, err
		}
		deviationDwell, err := duration(tm["deviation_dwell"], prefix+".transitions.deviation_dwell")
		if err != nil {
			return nil, err
		}
		recovery, err := integer(tm["recovery_observations"], prefix+".transitions.recovery_observations")
		if err != nil {
			return nil, err
		}
		recoveryDwell, err := duration(tm["recovery_dwell"], prefix+".transitions.recovery_dwell")
		if err != nil {
			return nil, err
		}
		gap, err := duration(tm["max_sample_gap"], prefix+".transitions.max_sample_gap")
		if err != nil {
			return nil, err
		}
		transition := tracking.TransitionPolicy{ArrivalObservations: arrival, ArrivalDwell: arrivalDwell, DeviationObservations: deviation, DeviationDwell: deviationDwell, RecoveryObservations: recovery, RecoveryDwell: recoveryDwell, MaxSampleGap: gap}
		if err := transition.Validate(); err != nil {
			return nil, fmt.Errorf("%s.transitions: %w", prefix, err)
		}
		ref, err := tracking.NewPolicyReference(id, revision, geoPolicy, transition)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", prefix, err)
		}
		c.entries[id] = control.Policy{Geographic: geoPolicy, Transitions: &transition, Reference: ref}
	}
	return c, nil
}

func (c *Catalog) Resolve(_ context.Context, id string) (control.Policy, error) {
	if c == nil {
		return control.Policy{}, tracking.ErrInvalid
	}
	p, ok := c.entries[id]
	if !ok {
		return control.Policy{}, fmt.Errorf("%w: policy_id %q is not in the catalog", tracking.ErrInvalid, id)
	}
	copy := *p.Transitions
	p.Transitions = &copy
	return p, nil
}

func (c *Catalog) References() []tracking.PolicyReference {
	if c == nil {
		return nil
	}
	refs := make([]tracking.PolicyReference, 0, len(c.entries))
	for _, p := range c.entries {
		refs = append(refs, p.Reference)
	}
	return refs
}

func mapping(node *yaml.Node, path string, keys ...string) (map[string]*yaml.Node, error) {
	if node == nil || node.Kind != yaml.MappingNode || node.Tag != "!!map" || node.Anchor != "" {
		return nil, fmt.Errorf("%s must be a mapping", path)
	}
	allowed := make(map[string]bool, len(keys))
	for _, key := range keys {
		allowed[key] = true
	}
	values := make(map[string]*yaml.Node, len(keys))
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || !allowed[key.Value] {
			return nil, fmt.Errorf("%s contains an unknown field at line %d", path, key.Line)
		}
		if _, duplicate := values[key.Value]; duplicate {
			return nil, fmt.Errorf("%s.%s is duplicated", path, key.Value)
		}
		if value.Kind == yaml.AliasNode || value.Anchor != "" {
			return nil, fmt.Errorf("%s.%s cannot use YAML anchors or aliases", path, key.Value)
		}
		values[key.Value] = value
	}
	for _, key := range keys {
		if values[key] == nil {
			return nil, fmt.Errorf("%s.%s is required", path, key)
		}
	}
	return values, nil
}

func scalar(node *yaml.Node, tag, path string) (string, error) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != tag || node.Anchor != "" {
		return "", fmt.Errorf("%s has an invalid type", path)
	}
	return node.Value, nil
}
func integer(node *yaml.Node, path string) (int, error) {
	raw, err := scalar(node, "!!int", path)
	if err != nil {
		return 0, err
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", path)
	}
	return value, nil
}
func number(node *yaml.Node, path string) (float64, error) {
	if node == nil || node.Kind != yaml.ScalarNode || (node.Tag != "!!float" && node.Tag != "!!int") || node.Anchor != "" {
		return 0, fmt.Errorf("%s must be a finite number", path)
	}
	v, err := strconv.ParseFloat(node.Value, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%s must be a finite number", path)
	}
	return v, nil
}
func duration(node *yaml.Node, path string) (time.Duration, error) {
	raw, err := scalar(node, "!!str", path)
	if err != nil {
		return 0, err
	}
	v, err := time.ParseDuration(raw)
	if err != nil || !strings.ContainsAny(raw, "nsuµmh") {
		return 0, fmt.Errorf("%s must be a duration with explicit unit", path)
	}
	return v, nil
}

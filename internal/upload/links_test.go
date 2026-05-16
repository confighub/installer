package upload

import (
	"sort"
	"testing"

	fapi "github.com/confighub/sdk/core/function/api"
)

// TestPlanLinks_ResolvedAndUnmatched exercises the two outputs of
// planLinks together. The Deployment Unit references two resources by
// name: a ConfigMap that exists as a Unit in the Space (matched) and a
// Secret that doesn't (unmatched — typically a cluster-side Secret the
// installer doesn't manage). Matched refs produce a LinkEdge; unmatched
// refs land in the second return so the caller can surface them as a
// non-fatal reminder.
func TestPlanLinks_ResolvedAndUnmatched(t *testing.T) {
	resources := &resourceIndex{
		resourcesByUnit: map[string][]fapi.Resource{
			"dep-unit": {{ResourceInfo: fapi.ResourceInfo{ResourceType: "apps/v1/Deployment", ResourceNameWithoutScope: "app"}}},
			"cm-unit":  {{ResourceInfo: fapi.ResourceInfo{ResourceType: "v1/ConfigMap", ResourceNameWithoutScope: "app-config"}}},
		},
		unitsByTypeName: map[string][]string{
			resourceLookupKey("apps/v1/Deployment", "app"):  {"dep-unit"},
			resourceLookupKey("v1/ConfigMap", "app-config"): {"cm-unit"},
		},
	}
	refs := map[string][]referenceEntry{
		"dep-unit": {
			{targetType: "v1/ConfigMap", targetName: "app-config"},                  // resolves → edge
			{targetType: "v1/Secret", targetName: "app-secret"},                     // unmanaged → unmatched
		},
	}

	edges, unmatched := planLinks(resources, refs, nil, nil)

	if len(edges) != 1 {
		t.Fatalf("edges: want 1, got %d (%+v)", len(edges), edges)
	}
	if edges[0].FromUnit != "dep-unit" || edges[0].ToUnit != "cm-unit" || edges[0].Reason != "reference:v1/ConfigMap" {
		t.Errorf("edge: want dep-unit->cm-unit (reference:v1/ConfigMap), got %+v", edges[0])
	}

	if len(unmatched) != 1 {
		t.Fatalf("unmatched: want 1, got %d (%+v)", len(unmatched), unmatched)
	}
	if unmatched[0] != (UnmatchedReference{FromUnit: "dep-unit", TargetType: "v1/Secret", TargetName: "app-secret"}) {
		t.Errorf("unmatched: want dep-unit -> v1/Secret app-secret, got %+v", unmatched[0])
	}
}

// TestPlanLinks_UnmatchedDeduped ensures a reference repeated across
// multiple paths within the same workload Unit (e.g., the same Secret
// named in both envFrom and volumes) is reported once, not twice.
func TestPlanLinks_UnmatchedDeduped(t *testing.T) {
	resources := &resourceIndex{
		resourcesByUnit: map[string][]fapi.Resource{
			"dep-unit": {{ResourceInfo: fapi.ResourceInfo{ResourceType: "apps/v1/Deployment", ResourceNameWithoutScope: "app"}}},
		},
		unitsByTypeName: map[string][]string{},
	}
	refs := map[string][]referenceEntry{
		"dep-unit": {
			{targetType: "v1/Secret", targetName: "shared"},
			{targetType: "v1/Secret", targetName: "shared"}, // same target, different path
		},
	}
	_, unmatched := planLinks(resources, refs, nil, nil)
	if len(unmatched) != 1 {
		t.Fatalf("unmatched: want 1 (deduped), got %d (%+v)", len(unmatched), unmatched)
	}
}

// TestPlanLinks_UnmatchedSorted asserts deterministic output ordering so
// tests + user-facing logs are stable.
func TestPlanLinks_UnmatchedSorted(t *testing.T) {
	resources := &resourceIndex{unitsByTypeName: map[string][]string{}}
	refs := map[string][]referenceEntry{
		"unit-b": {{targetType: "v1/Secret", targetName: "x"}},
		"unit-a": {
			{targetType: "v1/Secret", targetName: "z"},
			{targetType: "v1/ConfigMap", targetName: "y"},
		},
	}
	_, unmatched := planLinks(resources, refs, nil, nil)
	got := make([]string, 0, len(unmatched))
	for _, u := range unmatched {
		got = append(got, u.FromUnit+":"+u.TargetType+"/"+u.TargetName)
	}
	want := []string{
		"unit-a:v1/ConfigMap/y",
		"unit-a:v1/Secret/z",
		"unit-b:v1/Secret/x",
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("unmatched not sorted: %v", got)
	}
	if len(got) != len(want) {
		t.Fatalf("want %d entries, got %d (%v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: want %q, got %q", i, want[i], got[i])
		}
	}
}

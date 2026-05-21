package cli

import (
	"slices"
	"testing"
)

// TestLabelArgs_FirstUploadDefaultsComponent verifies that on a first
// upload the Component label is always emitted, defaulting to the package
// name, and that the explicitly-set well-known labels and --space-label
// pairs follow.
func TestLabelArgs_FirstUploadDefaultsComponent(t *testing.T) {
	m := spaceMetaInput{
		layer:      "App",
		layerSet:   true,
		labels:     []string{"team=payments"},
		variantSet: false, // not passed → omitted
	}
	got := m.labelArgs("hello-app", true)
	want := []string{"Component=hello-app", "Layer=App", "team=payments"}
	if !slices.Equal(got, want) {
		t.Fatalf("labelArgs first upload = %v, want %v", got, want)
	}
}

// TestLabelArgs_ComponentOverride verifies --component overrides the
// default Component value on a first upload.
func TestLabelArgs_ComponentOverride(t *testing.T) {
	m := spaceMetaInput{component: "frontend", componentSet: true}
	got := m.labelArgs("hello-app", true)
	want := []string{"Component=frontend"}
	if !slices.Equal(got, want) {
		t.Fatalf("labelArgs = %v, want %v", got, want)
	}
}

// TestLabelArgs_ReconcileSetOnce verifies that on a reconcile the
// well-known labels are emitted only when their flag was passed this run
// ("set once, update if re-passed"); Component is NOT re-emitted from its
// default.
func TestLabelArgs_ReconcileSetOnce(t *testing.T) {
	// Nothing passed → no labels (so applySpaceMeta is a no-op and we
	// never clobber the Space's existing well-known labels).
	if got := (spaceMetaInput{}).labelArgs("hello-app", false); len(got) != 0 {
		t.Fatalf("reconcile with no flags = %v, want empty", got)
	}
	// Only --environment re-passed → only Environment patched.
	m := spaceMetaInput{environment: "Prod", environmentSet: true}
	got := m.labelArgs("hello-app", false)
	want := []string{"Environment=Prod"}
	if !slices.Equal(got, want) {
		t.Fatalf("reconcile labelArgs = %v, want %v", got, want)
	}
}

// TestAnnotationArgs_TargetID verifies the TargetID annotation rides
// alongside --space-annotation pairs only when a resolved id is given.
func TestAnnotationArgs_TargetID(t *testing.T) {
	m := spaceMetaInput{annotations: []string{"note=hi"}}
	if got := m.annotationArgs(""); !slices.Equal(got, []string{"note=hi"}) {
		t.Fatalf("annotationArgs without id = %v", got)
	}
	got := m.annotationArgs("11111111-1111-1111-1111-111111111111")
	want := []string{"note=hi", "TargetID=11111111-1111-1111-1111-111111111111"}
	if !slices.Equal(got, want) {
		t.Fatalf("annotationArgs with id = %v, want %v", got, want)
	}
}

func TestValidateSpaceLabelFlags_ReservedKeys(t *testing.T) {
	for _, kv := range []string{"Component=x", "Layer=App", "Environment=Prod", "Region=us-east1", "Owner=Eng", "Variant=Base"} {
		if err := validateSpaceLabelFlags([]string{kv}); err == nil {
			t.Errorf("--space-label %q should be rejected as reserved", kv)
		}
	}
	if err := validateSpaceLabelFlags([]string{"team=payments"}); err != nil {
		t.Errorf("non-reserved --space-label should be accepted: %v", err)
	}
	if err := validateSpaceLabelFlags([]string{"missing-eq"}); err == nil {
		t.Errorf("--space-label without '=' should be rejected")
	}
}

func TestValidateSpaceAnnotationFlags_ReservedTargetID(t *testing.T) {
	if err := validateSpaceAnnotationFlags([]string{"TargetID=abc"}); err == nil {
		t.Errorf("--space-annotation TargetID should be rejected as reserved")
	}
	if err := validateSpaceAnnotationFlags([]string{"note=hi"}); err != nil {
		t.Errorf("non-reserved --space-annotation should be accepted: %v", err)
	}
}

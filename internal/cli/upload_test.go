// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"testing"
)

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

func TestMergeKeyValues(t *testing.T) {
	got, err := mergeKeyValues(map[string]string{"Layer": "App"}, []string{"team=payments", "note=a=b"})
	if err != nil {
		t.Fatal(err)
	}
	if got["Layer"] != "App" || got["team"] != "payments" || got["note"] != "a=b" {
		t.Errorf("got %v", got)
	}
	if got, err := mergeKeyValues(nil, nil); err != nil || got != nil {
		t.Errorf("no values should leave the map nil: %v, %v", got, err)
	}
	if _, err := mergeKeyValues(nil, []string{"missing-eq"}); err == nil {
		t.Error("a value without '=' should be rejected")
	}
}

// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"slices"
	"testing"

	goclientnew "github.com/confighub/sdk/core/openapi/goclient-new"

	"github.com/confighub/installer/internal/upload"
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

func TestUploadOrder_DependenciesBeforeParent(t *testing.T) {
	ordered := uploadOrder([]upload.Package{
		{Name: "app", IsParent: true},
		{Name: "db"},
		{Name: "cache"},
	})
	var names []string
	for _, pkg := range ordered {
		names = append(names, pkg.Name)
	}
	if want := []string{"db", "cache", "app"}; !slices.Equal(names, want) {
		t.Errorf("upload order = %v, want %v", names, want)
	}
}

func TestDryRunRequest_LeavesOutComponentsNotCreatedYet(t *testing.T) {
	prepared := &preparedUpload{
		requests: []goclientnew.UploadRequest{
			{Components: []goclientnew.UploadComponentRequest{{Name: "db"}}},
			{Components: []goclientnew.UploadComponentRequest{{Name: "app", DependsOn: []string{"db", "cache"}}}},
		},
		newComponents: map[string]bool{"db": true},
	}

	req, omitted := prepared.dryRunRequest(1)
	if got := req.Components[0].DependsOn; !slices.Equal(got, []string{"cache"}) {
		t.Errorf("dry-run DependsOn = %v, want [cache]", got)
	}
	if !slices.Equal(omitted, []string{"db"}) {
		t.Errorf("omitted = %v, want [db]", omitted)
	}
	// The upload itself still names every dependency.
	if got := prepared.requests[1].Components[0].DependsOn; !slices.Equal(got, []string{"db", "cache"}) {
		t.Errorf("request DependsOn = %v after a dry run, want [db cache]", got)
	}

	if _, omitted := prepared.dryRunRequest(0); len(omitted) != 0 {
		t.Errorf("a request without DependsOn omitted %v", omitted)
	}
}

// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package installer

import (
	"slices"
	"testing"

	goclientnew "github.com/confighub/sdk/core/openapi/goclient-new"

	"github.com/confighub/installer/internal/upload"
)

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
	prepared := &UploadPlan{
		Packages: []PlannedPackage{
			{Request: goclientnew.UploadRequest{Components: []goclientnew.UploadComponentRequest{{Name: "db"}}}},
			{Request: goclientnew.UploadRequest{Components: []goclientnew.UploadComponentRequest{{Name: "app", DependsOn: []string{"db", "cache"}}}}},
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
	if got := prepared.Packages[1].Request.Components[0].DependsOn; !slices.Equal(got, []string{"db", "cache"}) {
		t.Errorf("request DependsOn = %v after a dry run, want [db cache]", got)
	}

	if _, omitted := prepared.dryRunRequest(0); len(omitted) != 0 {
		t.Errorf("a request without DependsOn omitted %v", omitted)
	}
}

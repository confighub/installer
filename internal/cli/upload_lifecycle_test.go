// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	goclientnew "github.com/confighub/sdk/core/openapi/goclient-new"

	"github.com/confighub/installer/internal/testregistry"
)

// hubState records the Spaces, Components and Units the fake ConfigHub holds.
func (tr *transcript) hubState(spaces ...string) {
	tr.section("ConfigHub state")
	tr.note("Components: %s", strings.Join(hub.Components(), ", "))
	for _, slug := range spaces {
		sp := hub.Space(slug)
		if sp == nil {
			tr.note("Space %s: none", slug)
			continue
		}
		tr.note("Space %s", slug)
		tr.note("  labels: %s", formatMap(sp.Labels))
		if len(sp.Annotations) > 0 {
			tr.note("  annotations: %s", formatMap(sp.Annotations))
		}
		if sp.ReleaseTargetID != nil {
			tr.note("  release target: %s", sp.ReleaseTargetID)
		}
		for _, u := range hub.Units(slug) {
			line := fmt.Sprintf("  Unit %s labels=%s annotations=%s", u.Slug, formatMap(u.Labels), formatMap(u.Annotations))
			if u.HiddenReason != "" {
				line += " hidden=" + u.HiddenReason
			}
			if u.Data == "" {
				line += " (empty)"
			}
			tr.note("%s", line)
		}
	}
}

func formatMap(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// setupHello installs hello-app v1 into a new work-dir and forgets the
// requests setup sent.
func (e *env) setupHello(name string, args ...string) string {
	e.t.Helper()
	work := e.dir(name)
	mustRun(e.t, append([]string{"setup", "--pull", e.helloV1(), "--work-dir", work, "--non-interactive",
		"--namespace", "demo"}, args...)...)
	hub.ClearRequests()
	return work
}

// plan and upload into a Space that doesn't exist yet, an unchanged upload,
// and an upload that empties a Unit.
func TestLifecycleUpload(t *testing.T) {
	e := newEnv(t)
	work := e.setupHello("WORK", "--select", "monitoring")
	tr := e.transcript()

	tr.ok("plan", "--work-dir", work, "--space", "hello")
	tr.requests()

	// Without --yes, the upload previews itself and goes ahead, since nothing
	// is emptied.
	tr.ok("upload", "--work-dir", work, "--space", "hello")
	tr.requests()
	tr.hubState("hello")

	tr.ok("upload", "--work-dir", work, "--space", "hello")
	tr.requests()

	// Dropping the monitoring component leaves its Unit's resource out of the
	// render, which an upload empties, and asks first.
	mustRun(t, "setup", "--work-dir", work, "--non-interactive", "--components", "minimal")
	hub.ClearRequests()
	tr.ok("plan", "--work-dir", work, "--space", "hello")
	tr.requests()
	tr.fail("upload", "--work-dir", work, "--space", "hello")
	tr.requests()
	tr.ok("upload", "--work-dir", work, "--space", "hello", "--yes")
	tr.requests()
	tr.hubState("hello")

	tr.check("upload")
}

// The label, annotation, Target and ChangeSet flags, and the labels an
// existing Space keeps.
func TestLifecycleUploadPlacement(t *testing.T) {
	e := newEnv(t)
	work := e.setupHello("WORK")
	space := hub.AddSpace("hello", map[string]string{"Component": "custom", "Variant": "prod"}, nil)
	hub.AddTarget(space, "tgt")
	hub.AddChangeSet(space, "cs")
	other := hub.AddSpace("other", nil, nil)
	hub.AddTarget(other, "t2")
	hub.ClearRequests()
	tr := e.transcript()

	tr.section("an existing Space keeps its Component and Variant")
	tr.ok("upload", "--work-dir", work, "--space", "hello", "--yes",
		"--target", "tgt", "--changeset", "cs",
		"--layer", "App", "--environment", "Prod", "--region", "r1", "--owner", "Eng",
		"--space-label", "team=a", "--space-annotation", "note=x",
		"--unit-label", "l=1", "--unit-annotation", "a=2")
	tr.requests()
	tr.hubState("hello")

	tr.section("flags override them")
	tr.ok("plan", "--work-dir", work, "--space", "hello",
		"--component", "renamed", "--variant", "dev", "--target", "other/t2")
	tr.requests()

	tr.section("a new Space's Target and ChangeSet named by Space")
	tr.ok("plan", "--work-dir", work, "--space", "fresh", "--target", "other/t2", "--changeset", "hello/cs")
	tr.requests()

	tr.section("refusals")
	tr.fail("plan", "--work-dir", work, "--space", "missing", "--target", "tgt")
	tr.fail("plan", "--work-dir", work, "--space", "missing", "--changeset", "cs")
	tr.fail("plan", "--work-dir", work, "--space", "hello", "--target", "no-such-target")
	tr.fail("plan", "--work-dir", work, "--space", "hello", "--space-label", "Component=x")
	tr.fail("plan", "--work-dir", work, "--space", "hello", "--space-label", "novalue")
	tr.fail("plan", "--work-dir", work, "--space", "hello", "--space-annotation", "TargetID=x")
	tr.fail("plan", "--work-dir", work, "--space", "hello", "--unit-label", "novalue")
	tr.fail("plan", "--work-dir", work, "--space", "hello", "--unit-annotation", "novalue")
	tr.fail("plan", "--work-dir", work, "--space", "hello", "--variant", "")
	tr.fail("plan", "--work-dir", e.dir("NOPKG"), "--space", "hello")
	tr.requests()

	tr.check("upload-placement")
}

// A failed Unit write is reported, the rest land, and the upload fails.
func TestLifecycleUploadFailure(t *testing.T) {
	e := newEnv(t)
	work := e.setupHello("WORK")
	hub.SetUploadHook(func(req goclientnew.UploadRequest, dryRun bool, r *goclientnew.UploadResult) *goclientnew.UploadResult {
		if dryRun {
			return r
		}
		units := r.Components[0].Spaces[0].Units
		for i := range units {
			if strings.HasPrefix(units[i].Slug, "service-") {
				units[i].Error = &goclientnew.ResponseError{Message: "simulated failure"}
			}
		}
		return r
	})
	tr := e.transcript()
	tr.fail("upload", "--work-dir", work, "--space", "hello", "--yes")
	tr.check("upload-failure")
}

// A package with a dependency uploads the dependency first, each into its own
// Space, and the parent's Space depends on the dependency's Component.
func TestLifecycleUploadDependencies(t *testing.T) {
	e := newEnv(t)
	reg := testregistry.New()
	defer reg.Close()
	e.norm.add(reg.Host, "$REGISTRY")
	e.norm.addRegexp(`root-dependencies-hash: [0-9a-f]+`, "root-dependencies-hash: $HASH")
	work := e.dir("WORK")
	mustRun(t, "setup", "--pull", e.exampleStack(reg), "--work-dir", work, "--non-interactive", "--namespace", "e2e-ns")
	hub.ClearRequests()
	tr := e.transcript()

	tr.section("the dependency's Component does not exist yet")
	tr.ok("plan", "--work-dir", work, "--space-pattern", "{{.PackageName}}-{{.Variant}}")
	tr.requests()
	tr.ok("upload", "--work-dir", work, "--space-pattern", "{{.PackageName}}-{{.Variant}}")
	tr.requests()
	tr.hubState("example-base-base", "example-stack-base")

	tr.section("it does now")
	tr.ok("plan", "--work-dir", work, "--space-pattern", "{{.PackageName}}-{{.Variant}}")
	tr.requests()

	// When the parent's upload is refused, the dependency's has already
	// landed, and its revert hint is printed; plan shows the dependency's
	// preview before the refused one.
	tr.section("the parent's upload is refused")
	hub.RefuseUploads(func(req goclientnew.UploadRequest, dryRun bool) bool {
		return req.Components[0].Name == "example-stack"
	})
	tr.fail("plan", "--work-dir", work, "--space-pattern", "{{.PackageName}}-refused")
	tr.fail("upload", "--work-dir", work, "--space-pattern", "{{.PackageName}}-refused", "--yes")
	hub.RefuseUploads(nil)
	hub.ClearRequests()

	tr.section("refusals")
	tr.fail("plan", "--work-dir", work, "--space", "one")
	if err := os.Remove(filepath.Join(work, "out", "record", "lock.yaml")); err != nil {
		t.Fatal(err)
	}
	tr.fail("plan", "--work-dir", work)
	tr.requests()

	tr.check("upload-dependencies")
}

// The installer record an upload leaves in ConfigHub is enough to change the
// install from an empty work-dir.
func TestLifecycleRecordReentry(t *testing.T) {
	e := newEnv(t)
	tr := e.transcript()
	first := e.setupHello("FIRST", "--select", "ingress", "--input", "team=platform")
	tr.ok("upload", "--work-dir", first, "--space", "hello", "--yes")
	hub.ClearRequests()

	tr.section("re-enter from the record in a named Space, with no package")
	second := e.dir("SECOND")
	tr.ok("setup", "--space", "hello", "--work-dir", second, "--non-interactive", "--input", "team=infra")
	tr.requests()
	tr.tree("re-entered record", filepath.Join(second, "out", "record"), "function-chain.yaml")
	tr.ok("upload", "--work-dir", second, "--space", "hello", "--yes")
	hub.ClearRequests()

	tr.section("re-enter from the package's only install, found across the organization")
	third := e.dir("THIRD")
	tr.ok("setup", "--pull", e.helloV1(), "--work-dir", third, "--non-interactive")
	tr.requests()
	tr.tree("found record", filepath.Join(third, "out", "record"), "function-chain.yaml", "source.yaml")

	tr.section("a Space with no installer record is a fresh install")
	tr.ok("setup", "--space", "elsewhere", "--pull", e.helloV1(), "--work-dir", e.dir("FRESH"),
		"--non-interactive", "--namespace", "fresh")
	tr.requests()
	tr.fail("setup", "--space", "elsewhere", "--work-dir", e.dir("NOPULL"), "--non-interactive")
	tr.requests()

	tr.section("several installs are not chosen between")
	tr.ok("upload", "--work-dir", first, "--space", "hello-two", "--yes")
	hub.ClearRequests()
	tr.ok("setup", "--pull", e.helloV1(), "--work-dir", e.dir("AMBIGUOUS"), "--non-interactive", "--namespace", "x")
	tr.requests()

	tr.section("a record that names no source")
	hub.AddSpace("nosource", nil, nil)
	bare := e.setupHello("BARE")
	if err := os.Remove(filepath.Join(bare, "out", "record", "source.yaml")); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "upload", "--work-dir", bare, "--space", "nosource", "--yes")
	hub.ClearRequests()
	tr.fail("setup", "--space", "nosource", "--work-dir", e.dir("NOSOURCE"), "--non-interactive")
	tr.requests()

	tr.section("show, for a Space holding two packages' records")
	other := e.copyExample("hello-app", "PKG_OTHER")
	e.edit(filepath.Join(other, "installer.yaml"), func(s string) string {
		return strings.Replace(s, "  name: hello-app", "  name: other-app", 1)
	})
	for _, pkg := range []string{e.helloV1(), other} {
		w := e.dir("SHARED")
		mustRun(t, "setup", "--pull", pkg, "--work-dir", w, "--non-interactive", "--namespace", "shared")
		mustRun(t, "upload", "--work-dir", w, "--space", "shared", "--yes")
	}
	hub.ClearRequests()
	tr.fail("show", "--space", "shared")

	tr.check("record-reentry")
}

// What the commands that talk to ConfigHub say when there is no session to
// talk with: an expired one, or no cub configuration at all.
func TestLifecycleNoSession(t *testing.T) {
	e := newEnv(t)
	work := e.setupHello("WORK")
	tr := e.transcript()

	tr.section("ConfigHub refuses the session")
	hub.RefuseAuth(true)
	tr.fail("plan", "--work-dir", work, "--space", "hello")
	tr.fail("upload", "--work-dir", work, "--space", "hello", "--yes")
	// Setup only warns: it starts fresh.
	tr.ok("setup", "--pull", e.helloV1(), "--work-dir", e.dir("FRESH"), "--non-interactive", "--namespace", "demo")
	hub.RefuseAuth(false)
	tr.requests()

	tr.section("no cub configuration")
	t.Setenv("CUB_SERVER", "")
	t.Setenv("CUB_TOKEN", "")
	tr.fail("plan", "--work-dir", work, "--space", "hello")
	// A fresh install doesn't look for an installer record.
	tr.ok("setup", "--pull", e.helloV1(), "--work-dir", e.dir("LOCAL"), "--non-interactive", "--namespace", "demo")
	tr.fail("setup", "--space", "hello", "--work-dir", e.dir("NOCLIENT"), "--non-interactive")
	tr.requests()

	tr.check("no-session")
}

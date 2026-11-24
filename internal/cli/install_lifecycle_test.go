// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// install takes a package to its Space in one command, installs again without
// writing, upgrades from the installer record in a new work-dir, asks before
// emptying a Unit, needs --space, and checks the session before pulling
// anything.
func TestLifecycleInstall(t *testing.T) {
	e := newEnv(t)
	v1 := e.helloV1()
	v2 := e.helloV2()
	tr := e.transcript()

	tr.section("a fresh install")
	work := e.dir("WORK")
	tr.ok("install", v1, "--work-dir", work, "--space", "hello", "--non-interactive",
		"--namespace", "demo", "--select", "monitoring", "--input", "team=platform")
	tr.requests()
	tr.hubState("hello")

	tr.section("installing again writes nothing")
	tr.ok("install", v1, "--work-dir", work, "--space", "hello", "--non-interactive")
	tr.requests()

	tr.section("an upgrade in a new work-dir re-enters the install from its installer record")
	upgrade := e.dir("UPGRADE")
	tr.ok("install", v2, "--work-dir", upgrade, "--space", "hello", "--non-interactive")
	tr.requests()
	tr.hubState("hello")

	tr.section("dropping a component asks before emptying its Unit")
	tr.fail("install", v2, "--work-dir", upgrade, "--space", "hello", "--non-interactive", "--components", "minimal")
	tr.requests()
	tr.ok("install", v2, "--work-dir", upgrade, "--space", "hello", "--non-interactive", "--components", "minimal", "--yes")
	tr.requests()
	tr.hubState("hello")

	tr.section("an install names its Space")
	tr.fail("install", v1, "--work-dir", e.dir("NOSPACE"), "--non-interactive", "--namespace", "demo")

	tr.section("ConfigHub refuses the session")
	hub.RefuseAuth(true)
	refused := e.dir("REFUSED")
	tr.fail("install", v1, "--work-dir", refused, "--space", "hello", "--non-interactive", "--namespace", "demo")
	hub.RefuseAuth(false)
	tr.requests()
	if _, err := os.Stat(filepath.Join(refused, "package")); !os.IsNotExist(err) {
		t.Errorf("install pulled the package without a session: %v", err)
	}

	tr.check("install")
}

// package is build's former name, and still bundles, saying so.
func TestPackageIsBuild(t *testing.T) {
	e := newEnv(t)
	dir := e.dir("OUT")
	built := mustRun(t, "build", e.helloV1(), "-o", filepath.Join(dir, "build.tgz"))
	packaged := mustRun(t, "package", e.helloV1(), "-o", filepath.Join(dir, "package.tgz"))
	if !strings.Contains(packaged.stdout+packaged.stderr, `"package" is deprecated, use build`) {
		t.Errorf("package did not say it is deprecated:\nstdout:\n%s\nstderr:\n%s", packaged.stdout, packaged.stderr)
	}
	digest := func(out string) string {
		_, d, _ := strings.Cut(out, "digest: ")
		return strings.TrimSpace(d)
	}
	if digest(built.stdout) == "" || digest(built.stdout) != digest(packaged.stdout) {
		t.Errorf("build and package bundled differently:\n%s\n%s", built.stdout, packaged.stdout)
	}
}

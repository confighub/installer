// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/confighub/installer/internal/testregistry"
)

// helloV1 copies hello-app with one input, team, that the next version drops.
func (e *env) helloV1() string {
	pkg := e.copyExample("hello-app", "PKG_V1")
	e.edit(filepath.Join(pkg, "installer.yaml"), func(s string) string {
		return strings.Replace(s, "  components:\n", `  inputs:
    - name: team
      type: string
      default: core
      description: Team that owns the app.

  components:
`, 1)
	})
	return pkg
}

// helloV2 copies hello-app at version 0.2.0, without v1's team input and with
// a new input, log_level, that has a default.
func (e *env) helloV2() string {
	pkg := e.copyExample("hello-app", "PKG_V2")
	e.edit(filepath.Join(pkg, "installer.yaml"), func(s string) string {
		s = strings.Replace(s, "  version: 0.1.0", "  version: 0.2.0", 1)
		return strings.Replace(s, "  components:\n", `  inputs:
    - name: log_level
      type: string
      default: info
      description: Log verbosity.

  components:
`, 1)
	})
	return pkg
}

// Setup's first install, re-render, upgrade with an input schema change, and
// image overrides carried forward.
func TestLifecycleSetup(t *testing.T) {
	e := newEnv(t)
	v1 := e.helloV1()
	v2 := e.helloV2()
	work := e.dir("WORK")
	tr := e.transcript()

	tr.ok("setup", "--pull", v1, "--work-dir", work, "--non-interactive",
		"--namespace", "demo", "--select", "monitoring", "--input", "team=platform")
	tr.tree("after first install", work, "package/")
	// A fresh work-dir looks for an installer record in ConfigHub.
	tr.requests()

	tr.ok("setup", "--work-dir", work, "--non-interactive")
	tr.tree("after re-render", filepath.Join(work, "out"))
	tr.requests()

	tr.ok("setup", "--pull", v2, "--work-dir", work, "--non-interactive")
	tr.tree("after upgrade", filepath.Join(work, "out", "record"))

	tr.ok("setup", "--work-dir", work, "--non-interactive",
		"--set-image", "nginxdemos/hello=nginxdemos/hello:v2")
	tr.tree("after --set-image", filepath.Join(work, "out"), "compose/", "record/function-chain.yaml")

	// The override carries forward without the flag.
	tr.ok("setup", "--work-dir", work, "--non-interactive")
	tr.tree("override carried forward", filepath.Join(work, "out", "record"), "function-chain.yaml")

	// Flags override carried-forward choices.
	tr.ok("setup", "--work-dir", work, "--non-interactive",
		"--namespace", "other", "--components", "all", "--input", "log_level=debug")
	tr.tree("after overrides", filepath.Join(work, "out", "record"), "function-chain.yaml")

	// --reuse takes the prior choices as they are.
	tr.ok("setup", "--work-dir", work, "--reuse")
	tr.tree("after --reuse", filepath.Join(work, "out", "record"), "function-chain.yaml")

	tr.check("setup")
}

// The ways setup refuses to run, and what it says.
func TestLifecycleSetupErrors(t *testing.T) {
	e := newEnv(t)
	v1 := e.helloV1()
	tr := e.transcript()

	tr.section("no package and no --pull")
	tr.fail("setup", "--work-dir", e.dir("EMPTY"), "--non-interactive", "--namespace", "x")

	tr.section("a --pull that cannot be read")
	tr.fail("setup", "--work-dir", e.dir("BADPULL"), "--pull", filepath.Join(v1, "missing"),
		"--non-interactive", "--namespace", "x")

	tr.section("fresh install without --namespace")
	tr.fail("setup", "--pull", v1, "--work-dir", e.dir("NONS"), "--non-interactive")

	tr.section("--components with --select")
	tr.fail("setup", "--pull", v1, "--work-dir", e.dir("BOTH"), "--non-interactive",
		"--namespace", "x", "--components", "all", "--select", "monitoring")

	tr.section("--reuse with nothing to reuse")
	tr.fail("setup", "--pull", v1, "--work-dir", e.dir("NOREUSE"), "--reuse")

	tr.section("an unknown component")
	tr.fail("setup", "--pull", v1, "--work-dir", e.dir("NOCOMP"), "--non-interactive",
		"--namespace", "x", "--select", "no-such-component")

	tr.section("a malformed --input")
	tr.fail("setup", "--pull", v1, "--work-dir", e.dir("BADINPUT"), "--non-interactive",
		"--namespace", "x", "--input", "team")

	tr.section("a malformed --set-image")
	tr.fail("setup", "--pull", v1, "--work-dir", e.dir("BADIMAGE"), "--non-interactive",
		"--namespace", "x", "--set-image", "nginxdemos/hello")

	// An upgrade that adds a required input without a default can't be
	// answered without a terminal.
	tr.section("upgrade adds a required input")
	work := e.dir("WORK")
	mustRun(t, "setup", "--pull", v1, "--work-dir", work, "--non-interactive", "--namespace", "demo")
	required := e.copyExample("hello-app", "PKG_REQUIRED")
	e.edit(filepath.Join(required, "installer.yaml"), func(s string) string {
		return strings.Replace(s, "  components:\n", `  inputs:
    - name: team
      type: string
      default: core
    - name: owner
      type: string
      required: true
      prompt: Who owns the app

  components:
`, 1)
	})
	tr.fail("setup", "--pull", required, "--work-dir", work, "--non-interactive")

	tr.section("upgrade changes an input's type")
	retyped := e.copyExample("hello-app", "PKG_RETYPED")
	e.edit(filepath.Join(retyped, "installer.yaml"), func(s string) string {
		return strings.Replace(s, "  components:\n", `  inputs:
    - name: team
      type: int
      default: 1

  components:
`, 1)
	})
	tr.fail("setup", "--pull", retyped, "--work-dir", work, "--non-interactive")

	tr.check("setup-errors")
}

// wizard writes the record without rendering when asked, and render renders it.
func TestLifecycleWizardThenRender(t *testing.T) {
	e := newEnv(t)
	v1 := e.helloV1()
	work := e.dir("WORK")
	tr := e.transcript()

	tr.ok("wizard", v1, "--work-dir", work, "--non-interactive", "--namespace", "demo", "--render=false")
	tr.tree("after wizard", work, "package/")
	tr.ok("render", "--work-dir", work)
	tr.tree("after render", filepath.Join(work, "out"))

	tr.section("render without a record")
	empty := e.dir("NORECORD")
	mustRun(t, "pull", v1, "--work-dir", empty)
	tr.fail("render", "--work-dir", empty)

	tr.check("wizard-render")
}

// A package's collector runs during setup, and its facts are recorded.
func TestLifecycleCollector(t *testing.T) {
	e := newEnv(t)
	pkg := e.copyExample("hello-app", "PKG")
	e.edit(filepath.Join(pkg, "installer.yaml"), func(s string) string {
		return strings.Replace(s, "  components:\n", `  inputs:
    - name: greeting
      type: string
      default: hello
  collector:
    command: ./collector.sh
    description: Reports what the installer passes a collector.

  components:
`, 1)
	})
	e.write(filepath.Join(pkg, "collector.sh"), `#!/bin/sh
set -eu
cat <<EOF
namespace: ${INSTALLER_NAMESPACE}
base: ${INSTALLER_BASE}
selected: "${INSTALLER_SELECTED}"
greeting: ${INSTALLER_INPUT_GREETING}
EOF
`)
	if err := os.Chmod(filepath.Join(pkg, "collector.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	work := e.dir("WORK")
	tr := e.transcript()
	tr.ok("setup", "--pull", pkg, "--work-dir", work, "--non-interactive",
		"--namespace", "demo", "--select", "ingress", "--input", "greeting=hi")
	tr.tree("record", filepath.Join(work, "out", "record"))
	tr.check("collector")
}

// A package with a dependency: setup pulls, locks and renders both.
func TestLifecycleDependencies(t *testing.T) {
	e := newEnv(t)
	reg := testregistry.New()
	defer reg.Close()
	e.norm.add(reg.Host, "$REGISTRY")
	// The hash covers the dependencies' references, which name the registry.
	e.norm.addRegexp(`root-dependencies-hash: [0-9a-f]+`, "root-dependencies-hash: $HASH")
	stack := e.exampleStack(reg)
	work := e.dir("WORK")
	tr := e.transcript()

	tr.ok("setup", "--pull", stack, "--work-dir", work, "--non-interactive", "--namespace", "e2e-ns")
	tr.tree("after setup", work, "package/", "out/base/package/")

	// A second render is the same.
	tr.ok("render", "--work-dir", work)

	tr.section("a stale lock")
	e.edit(filepath.Join(work, "package", "installer.yaml"), func(s string) string {
		return strings.Replace(s, `version: "^0.1.0"`, `version: "^0.1.1"`, 1)
	})
	tr.fail("render", "--work-dir", work)

	tr.check("dependencies")
}

// exampleStack pushes example-base to reg and returns a copy of example-stack
// that depends on it there.
func (e *env) exampleStack(reg *testregistry.Registry) string {
	e.t.Helper()
	mustRun(e.t, "push", filepath.Join(e.repo, "examples", "example-base"),
		"oci://"+reg.Host+"/installer-e2e/example-base:0.1.0")
	stack := e.copyExample("example-stack", "STACK")
	e.edit(filepath.Join(stack, "installer.yaml"), func(s string) string {
		return strings.ReplaceAll(s, "localhost:5555", reg.Host)
	})
	return stack
}

// --output-oci writes the render as an OCI artifact, to a local layout or a
// registry, and records an oci:// source's digest.
func TestLifecycleOutputOCI(t *testing.T) {
	e := newEnv(t)
	reg := testregistry.New()
	defer reg.Close()
	e.norm.add(reg.Host, "$REGISTRY")
	v1 := e.helloV1()
	tr := e.transcript()

	work := e.dir("WORK")
	tr.ok("setup", "--pull", v1, "--work-dir", work, "--non-interactive",
		"--namespace", "demo", "--output-oci", filepath.Join(work, "rendered.oci"))

	mustRun(t, "push", v1, "oci://"+reg.Host+"/pkgs/hello-app:0.1.0")
	fromRegistry := e.dir("FROMREG")
	args := []string{"setup", "--pull", "oci://" + reg.Host + "/pkgs/hello-app:0.1.0", "--work-dir", fromRegistry,
		"--non-interactive", "--namespace", "demo",
		"--output-oci", "oci://" + reg.Host + "/rendered/hello-app:demo"}
	r := mustRun(t, args...)
	// The rendered artifact's config names its source, at the registry's
	// address, so its digest differs every run.
	if m := regexp.MustCompile(`manifest:\s+(sha256:[0-9a-f]+)`).FindStringSubmatch(r.stdout); m != nil {
		e.norm.add(m[1], "$RENDERED_MANIFEST_DIGEST")
	}
	tr.record(args, r)
	tr.tree("record", filepath.Join(fromRegistry, "out", "record"), "function-chain.yaml")
	tr.check("output-oci")
}

# Package and Dependency Management — Implementation Plan

Companion to [`package-management.md`](./package-management.md). That doc is
the spec; this one is the build plan. Phases are ordered so each leaves the
installer working end-to-end with the existing examples; no phase requires
the next to land.

## Phase 0 — Scaffolding

Visible CLI surface, stubs only. Lets later phases land in small PRs without
touching `root.go` each time.

- New: `internal/cli/{package.go, push.go, inspect.go, list.go, tag.go, login.go, logout.go, deps.go, sign.go, verify.go}` — cobra commands returning "not yet implemented" with `--help` text taken from the spec.
- Modify: `internal/cli/root.go` — register the new subcommands. Group `deps update|build|tree` under `installer deps`.
- Modify: `internal/cli/stubs.go` — remove the stub-only helper once each command moves to its own file.
- Modify: `README.md` Roadmap — link to the design doc; mark the new commands "planned" until their phase ships.

No tests yet. Acceptance: `installer --help` lists every command in the spec.

## Phase 1 — Deterministic bundler + `installer package`

Goal: a `.tgz` of the source tree that is byte-identical across machines.

- New: `internal/bundle/bundle.go`
  - `Bundle(srcDir, dstFile string, opts Options) (digest string, err error)`.
  - Deterministic tar: sorted entries; zeroed `mtime`, `uid`, `gid`, `uname`, `gname`; canonicalized mode (0644 / 0755); gzip with no original-filename and zeroed mtime.
  - Reject: `*.env.secret` anywhere; anything under `out/`; anything matched by `.installerignore` (gitignore syntax via `go-git/go-billy`'s gitignore or a small in-tree matcher).
  - Include: `installer.yaml` + every directory referenced by `bases[].path`, `components[].path`, `validation.*`, `collector.command` (if relative), plus an opt-in `examples/` (gated by a `package.bundleExamples` field in `installer.yaml`, default true).
- New: `internal/bundle/bundle_test.go` — two `Bundle` invocations of the same source tree produce equal digests; rename of a single file changes the digest; `.env.secret` triggers a refusal.
- Modify: `internal/cli/package.go` — `-o`, default output filename `<metadata.name>-<metadata.version>.tgz` in cwd; print the digest on success.
- Decision deferred: reachability-scan vs ignore-list. v1 ships ignore-list; reachability becomes a `installer package --strict` follow-up.

Acceptance: `installer package examples/hello-app` produces a tarball whose `sha256` is reproducible across machines.

## Phase 2 — OCI publish (`push`, `inspect`, `list`, `tag`, hardened `pull`)

Goal: round-trip native artifacts; existing Helm-OCI pull keeps working.

- New: `pkg/api/oci.go` — exported constants for media types (`ArtifactType`, `ConfigMediaType`, `LayerMediaType`) and annotation keys.
- New: `pkg/api/configblob.go` — `ConfigBlob` type (bundle header + parsed `Package`), serialize/deserialize.
- New: `internal/pkg/oci.go`
  - `Push(ctx, tgz, ref) (manifestDigest string, err error)` — builds an oras manifest with the package layer, the config blob, and annotations.
  - `Inspect(ctx, ref) (*api.ConfigBlob, error)` — fetches only the manifest + config blob (no layer pull).
  - `List(ctx, repoRef) ([]string, error)` — `/tags/list`.
  - `Tag(ctx, srcRef, dstTag) error`.
- Modify: `internal/pkg/pull.go`
  - Detect native `artifactType` first; fall back to current Helm heuristic only when the artifact does not identify as ours.
  - Parse `@sha256:...` suffix on refs; verify on pull, error on mismatch.
- Modify: `internal/cli/{push.go, inspect.go, list.go, tag.go, pull.go}` — wire to `internal/pkg`.
- New: `internal/cli/login.go`, `logout.go` — reuse docker config (`~/.docker/config.json`); no custom credential file in v1.
- Tests:
  - `internal/pkg/oci_test.go` against a `zot` or `registry:2` container started by the test (skip if Docker unavailable).
  - `internal/pkg/pull_test.go` regression: pulling an existing Helm-OCI chart still works.

Acceptance: `installer package` → `installer push` → `installer pull` on a fresh machine yields a byte-identical source tree.

## Phase 3 — Schema additions (parse-only)

Goal: packages can *declare* dependencies; nothing acts on them yet.

- Modify: `pkg/api/package.go`
  - `Metadata`: add `KubeVersion`, `InstallerVersion` (SemVer ranges, strings).
  - `PackageSpec`: add `Dependencies []Dependency`, `Conflicts []ConflictRef`, `Replaces []ReplaceRef`, `BundleExamples *bool`.
- New types in `pkg/api/dependency.go`: `Dependency`, `ConflictRef`, `ReplaceRef` (fields per the design doc).
- New: `pkg/api/lock.go` — `Lock`, `LockedDependency`.
- Modify: `pkg/api/parse.go` and `parse_test.go` — round-trip tests.
- Modify: `internal/cli/doc.go` — render `dependencies`, `conflicts`, `replaces`, `kubeVersion`, `installerVersion` in `installer doc`.
- No changes to `wizard`, `render`, `upload`.

Acceptance: a fixture package with the new fields parses and `installer doc` shows them; the existing examples still parse unchanged.

## Phase 4 — Resolver + `installer deps update`

Goal: produce `out/spec/lock.yaml` from `installer.yaml` + the current `Selection`.

- New: `internal/deps/semver.go` — wrap `github.com/Masterminds/semver/v3`.
- New: `internal/deps/resolver.go`
  - DAG walk: for each `Dependency`, call `pkg.Inspect` to read the dep's manifest from the config blob (no layer fetch), recurse.
  - One version per package name. Conflict report names the chain of parents that produced each incompatible constraint.
  - `optional: true` + `whenComponent: <name>` honored against the current `out/spec/selection.yaml`. Missing selection ⇒ resolver treats all optionals as not-followed and notes them in `deps tree` output.
  - `conflicts`, `replaces`, and `satisfies → externalRequires` linkage all applied at resolve time.
  - Cycle detection ⇒ hard error.
- New: `internal/deps/lock.go` — write `out/spec/lock.yaml`; reject load if the parent's `dependencies:` summary disagrees with the lock (stale-lock detection).
- Modify: `internal/cli/deps.go` — wire `update`, `tree`, `build`. `build` populates `out/vendor/<name>@<version>/` from the OCI cache.
- New: shared content-addressed cache under `~/.cache/installer/oci/sha256/...`, used by `pull` and `deps build` (a small helper in `internal/pkg/cache.go`).
- Tests: `internal/deps/testdata/` with golden-file fixtures: linear chain, diamond, conflict, optional toggled by component, channel-tag pinning.

Acceptance: a fixture parent that depends on two published packages resolves and writes a lock with stable digests across runs.

## Phase 5 — Multi-package render

Goal: parent and each dep render into their own subtrees, deterministically.

- Modify: `internal/render/` — factor the current single-package render path into `RenderPackage(loaded *pkg.Loaded, selection, inputs, outDir)` used for both the parent and every locked dep.
- Modify: `internal/cli/render.go`
  - Require a lock when `Dependencies` is non-empty; otherwise behave as today.
  - Render parent into `out/manifests/` + `out/spec/` (unchanged).
  - For each `LockedDependency`: ensure the dep is fetched (use the cache, populated either by `deps build` or fetched on demand), then `RenderPackage` into `out/<dep-name>/manifests/` + `out/<dep-name>/spec/` using the lock's `selection` and `inputs` as wizard pre-answers.
- Tests: render a 2-level fixture; assert subtree layout, manifest counts, and deterministic file digests.

Acceptance: re-rendering the same lock + inputs produces byte-identical `out/` trees.

### Phase 5 limits

- **Collectors on dependencies are not supported.** Collectors run via the
  wizard, which is not invoked per-dep — the dep's Selection/Inputs come
  straight from the lock. `installer deps update` rejects any candidate
  whose installer.yaml declares `spec.collector` so the user discovers the
  limit at resolve time, not at render. Lifting the limit means either
  running the collector at render time for deps (and dragging the
  collector's runtime requirements — e.g. `cub` on PATH — into render) or
  defining a smaller resolve-time fact-collection contract. Deferred until
  a real dep needs it.

## Phase 6 — Upload: per-dep Spaces + installer-record Unit + cross-Space links

Goal: each package gets its own Space; the lock survives as a Unit.

- Modify: `internal/cli/upload.go`
  - Iterate packages in dependency-topological order (parent last is fine; cross-Space Links are created after both Spaces exist).
  - `--space-pattern <go-template>` with vars `{{.PackageName}} {{.PackageVersion}} {{.Variant}}` (no variant resolution yet — left empty). Default: `"{{.PackageName}}"`.
  - For each package:
    - Build a multi-doc YAML stream from `installer.yaml` + every file in `<pkg>/spec/` (selection, inputs, function-chain, manifest-index, and — for the parent — the lock). Create one `Kubernetes/YAML` Unit holding the stream; do **not** assign a Target.
    - Upload rendered manifests as today, one Unit per file. `--target` applies to these only.
    - Run the existing intra-Space link inference on the new Units.
  - After all Spaces exist, for every dep edge in the lock create a Link from the parent's installer-record Unit to the dep's installer-record Unit (Link slug derived from the edge, idempotent).
- Decision: the parent's installer-record Unit embeds the full lock; each dep's installer-record Unit holds only its own slice (its `installer.yaml` + `out/<dep>/spec/*`). This makes a dep's Space self-describing.

Acceptance: `installer upload` on a fixture multi-package render creates N Spaces, N untargeted installer-record Units, M rendered Units, and the expected cross-Space Links.

## Phase 7 — Signing

Goal: trust at the artifact level.

- New: `internal/cli/sign.go`, `verify.go`. v1 shells out to the `cosign` binary; the Go SDK is large and re-pulls a lot of sigstore dependencies. Document the requirement in `installer doc` output and README.
- New: `~/.config/installer/policy.yaml` — list of trusted issuers / keys, plus an `enforce: true|false` flag. Loaded by `pull` and `deps update` to enforce verification before trusting any digest.
- Tests: integration test gated on `cosign` being on PATH.

Acceptance: signed artifact verifies; tampered artifact fails verification with a clear error.

## Phase 8 — Hardening and docs

- Add a multi-package fixture under `examples/` (a small "stack" that depends on a shared base package), used by the e2e script.
- New: `test/e2e/package-and-deps.sh` (or whatever the existing convention is) — `package` → push to a local `zot` started by the test → `deps update` → `render` → `upload` against a mocked ConfigHub or a real local one.
- Update README Roadmap: drop the "Bundling" item; link the design doc; mention the multi-package example.

## Cross-cutting concerns (apply across phases)

- **Caching**: one content-addressed OCI cache under `~/.cache/installer/oci/sha256/...`, used by `pull`, `inspect`, and `deps build`. Concurrency-safe via per-digest temp + rename.
- **Backwards compatibility**: every existing example must keep working through phase 6 with no edits. The renderer only requires a lock when `Dependencies` is non-empty.
- **Determinism budget**: every step that hashes (bundler, lock, render output) must produce stable digests across machines. Each phase adds a determinism test before claiming acceptance.
- **Error surface**: resolver, signer, and pull errors include the full chain of refs and reasons. No silent fallbacks once a native artifactType is observed.
- **Skipped because deferred**: hooks/triggers, variants, Helm chart interop, mirror, yanking. None of the phases introduce schema or CLI surface that pre-commits us to a design for these.

## What ships when

| Cut    | Phases | User-visible value |
|--------|--------|--------------------|
| 0.3    | 0, 1   | `installer package` produces shippable tarballs. |
| 0.4    | 2      | OCI publish round-trips; existing Helm pull still works. |
| 0.5    | 3, 4   | Packages can declare deps; lock is generated. No behavior change at render. |
| 0.6    | 5, 6   | Multi-package render + upload with per-dep Spaces and installer-record Units. |
| 0.7    | 7, 8   | Signed artifacts and the multi-package example. |

# Day-2 Lifecycle — Implementation Plan

Companion to [`lifecycle.md`](./lifecycle.md). Phases are ordered so each
leaves the installer working end-to-end with the existing examples; no
phase requires the next to land.

## Phase A — Interactive wizard

Goal: replace the "interactive wizard not yet implemented" stub with a
working interactive flow built on `survey/v2`.

- New: `internal/wizard/interactive.go`
  - Prompt order: confirm `Re-use last choices?` (only if prior state
    loaded) → base (single-select, default = package default) →
    component preset (single-select: minimal / default / all / selected)
    → if `selected`, multi-select of components → namespace (text,
    default = prior or empty) → one prompt per declared input that has
    no default and is required, plus any input the user wants to
    override (a "tweak inputs?" yes/no gate).
  - All widgets are `survey/v2`. No bubbletea, no termenv, no terminal
    capability queries.
  - TTY check at entry; non-TTY falls through to the existing
    non-interactive path with a clear error if required flags are
    missing.
- New: `internal/wizard/prior.go`
  - `LoadPriorState(ctx, workDir) (*Result, source string, err error)`.
  - Source order: ConfigHub (via `out/spec/upload.yaml`) → local
    `out/spec/*.yaml` → none.
  - ConfigHub fetch shells out to `cub unit get --space <slug>
    installer-record -o body`, then splits the multi-doc YAML body
    using the same code path that builds it (`upload.BuildInstallerRecord`'s
    inverse — extract a `SplitInstallerRecord` helper).
  - On ConfigHub fetch failure: warn, fall back to local files.
- Modify: `pkg/api/package.go` — add `Component.Default bool`.
- Modify: `pkg/api/parse_test.go` — round-trip the new field.
- New: `pkg/api/upload.go` (or `upload_doc.go` to avoid conflict with the
  internal package name) — `Upload`, `UploadSpec`, `UploadedSpace`
  types. `UploadSpec` includes `OrganizationID` and `Server`.
- New: `internal/cubctx/cubctx.go` — `OrganizationID(ctx) (string,
  error)` shells out to `cub context get -o
  jq=".coordinate.organizationID"`. `CheckOrganization(ctx, want
  string) error` compares against the recorded value and returns a
  formatted error naming both IDs and pointing at `cub context set` /
  `cub auth login`. Used by `wizard`, `plan`, `update`, and
  `upgrade`.
- Modify: `internal/upload/upload.go`
  - At end of successful `Discover` + per-package upload + cross-Space
    link creation, write `<work-dir>/out/spec/upload.yaml` (including
    `OrganizationID` from the live cub context).
  - `BuildInstallerRecord` includes `upload.yaml` in the multi-doc
    body.
- Modify: `internal/cli/wizard.go`
  - Drop the "not yet implemented" return when stdin is a TTY.
  - Add `--reuse` (skip the prompt and use prior state as-is) and
    `--components <preset>` flags.
  - When `--non-interactive` is passed, behavior is unchanged.
- Tests:
  - `internal/wizard/interactive_test.go` — survey `WithStdio` test
    feeding scripted input, asserting the resulting `Result`.
  - `internal/wizard/prior_test.go` — local-only prior state load;
    ConfigHub fetch is exercised in the e2e script.
- Acceptance: `installer wizard ./examples/hello-app` (no flags) walks
  through prompts and emits the same files the non-interactive path
  produces. Re-running it offers "Re-use last choices?" and renders an
  unchanged spec. Re-running it after `cub context set` to a different
  org fails fast naming both org IDs.

## Phase B — `installer plan`

Goal: read-only diff of `<work-dir>/out` against ConfigHub.

- New: `internal/diff/diff.go`
  - `Compute(ctx, pkg upload.Package) (Plan, error)`.
  - Lists current Units via
    `cub unit list --space <slug> --where "Labels.Package='<pkg.Name>'" -o json`
    to bucket adds vs updates (the dry-run path errors on a
    non-existent Unit, so the list step is required).
  - For each intersection slug, runs
    `cub unit update --space <slug> --merge-external-source <basename> --dry-run -o mutations <slug> <path>`
    and decodes the mutation list.
  - Returns `Plan{ Adds, Updates, Deletes []SlugDiff }` per Space.
- New: `internal/diff/print.go`
  - Terraform-style renderer (`+ slug`, `~ slug` with mutation paths,
    `- slug`).
  - Per-Space `Images:` footer from
    `cub function do --space <slug> get-container-image '*'` against the
    just-rendered manifests (not against ConfigHub).
- New: `internal/cli/plan.go` — `installer plan <work-dir>`. Reuses
  `upload.Discover` to walk packages.
- New: `internal/cli/plan_test.go` (table tests for print rendering).
- Add to existing e2e: after `upload`, edit one rendered file, re-render,
  run `plan`, assert the diff names that file.
- Acceptance: plan against an unchanged work-dir prints
  "No changes." Hand-edit one resource, re-render, plan shows the diff.

## Phase C — `installer update`

Goal: execute the Phase B plan inside a ChangeSet.

- New: `internal/changeset/changeset.go` — `Open(ctx, parentSpace,
  description string) (slug string, err error)` shells out to `cub
  changeset create`. `RestoreCommand(slug, component string) string`
  formats the user-facing revert hint.
- New: `internal/diff/apply.go` — `Apply(ctx, plan Plan, opts
  ApplyOptions)`.
  - `cub unit create --label Package=<pkg> --merge-external-source ...`
    for adds.
  - `cub unit update --merge-external-source --changeset <slug> ...`
    for updates (no `--dry-run`).
  - `cub unit update --merge-external-source` with empty content for
    Units that dropped out of the render — **empty**, never `cub unit
    delete` (deleting would orphan deployed resources, discard
    post-install edits, and fail on DeleteGates). The merge drops only
    installer-contributed resources, preserving post-install edits.
    Gated on `opts.Yes` or interactive confirm-each, and refused for
    Units carrying a DestroyGate. Stale links are auto-removed by
    ConfigHub when the Data no longer references them.
- New: `internal/cli/update.go` — `installer update <work-dir> [--yes]
  [--changeset <slug>]`. Default ChangeSet slug:
  `installer-update-<RFC3339-timestamp>`. Plan output names the
  ChangeSet that will be opened and prints the revert command.
- Refactor: extract upload's link-inference into
  `internal/upload/links.go` so update reuses it. The sequence is:
  Apply unit changes → re-run inference → diff against existing links
  (`cub link list --space <slug>`) → create missing. Existing links
  pointing at still-present Units are left as-is.
- Acceptance:
  - `update` on an unchanged work-dir is a no-op (no ChangeSet opened).
  - `update` after a render diff converges on one call.
  - Plan output names the ChangeSet and revert command verbatim.
  - Restoring `Before:ChangeSet:<slug>` reverts the updates from that
    invocation.

## Phase D — `installer upgrade` and `installer upgrade-apply`

Goal: split the upgrade flow into "stage" (pull, re-wizard non-
interactively, re-collect facts, re-render, plan) and "promote"
(swap directories + update).

- New: `internal/wizard/schemadiff.go`
  - `DiffInputs(oldPkg, newPkg, priorValues) (carry map[string]any,
    newRequired []Input, dropped []string, typeChanged []Input)`.
  - `carry` is the values to keep verbatim. `newRequired` is the set
    that must be answered (interactive prompt or fail-fast in
    non-interactive mode). `dropped` are silently omitted from the new
    `inputs.yaml`. `typeChanged` errors with a re-run-the-wizard hint.
  - Mirror helpers for components: `DiffComponents(oldPkg, newPkg,
    priorSelection)` honoring the prior preset (recorded in
    `selection.yaml`) for `default: true` adoption.
- New: `internal/cli/upgrade.go` — `installer upgrade <work-dir> <ref>
  [--set-image …] [--apply]`.
  - Pulls into `<work-dir>/.upgrade/package`.
  - Runs `schemadiff.DiffInputs` and `DiffComponents`. Interactive
    mode prompts for new required inputs; non-interactive mode fails
    with the missing input names and the `installer wizard <work-dir>`
    hint.
  - Builds `RawAnswers` from carry + answers + prior selection.
  - Runs the collector against the new package.
  - Calls `render.Render` with `<work-dir>/.upgrade` as the work-dir.
  - Calls `diff.Compute` against current ConfigHub state and prints.
  - `--apply` is sugar for `installer upgrade-apply <work-dir>`
    invoked immediately on success — does not change the staging
    contract.
- New: `internal/cli/upgrade_apply.go` — `installer upgrade-apply
  <work-dir> [--yes]`.
  - Refuses if `.upgrade/` is missing or if the staged spec has
    unsatisfied required inputs (records this in
    `.upgrade/out/spec/upgrade-status.yaml` so it survives a re-shell).
  - Atomic rename: archives the current `package/` and `out/` to
    `.upgrade-prev/`, then renames `.upgrade/package` → `package` and
    `.upgrade/out` → `out`.
  - Invokes `installer update <work-dir>` with a ChangeSet slug
    `installer-upgrade-<from>-to-<to>-<timestamp>`.
- Acceptance:
  - Starting from an installed `examples/hello-app`, `installer
    upgrade <work-dir> ./examples/hello-app` produces a `.upgrade/`
    tree and a "no changes" plan.
  - Editing `examples/hello-app/bases/default/...` and re-running
    `installer upgrade` surfaces the change in plan; `installer
    upgrade-apply <work-dir>` materializes it.
  - Adding a new required input to the package and running `installer
    upgrade` non-interactively fails fast with the new input named.
  - Removing an input from the package and running `installer
    upgrade` succeeds; the value is dropped from `out/spec/inputs.yaml`.

## Phase E — Image affordances

Goal: one-flag image bumps; visible image set in plan output. Anchored
to [Principle 5](./principles.md#5-image-management-declare-a-kustomize-transformer-use-functions-when-changes-are-common).

- Modify: `internal/cli/{plan,update,upgrade}.go` — add `--set-image
  <name>=<ref>` (repeatable). Validate `name=ref` shape.
- Modify: `pkg/api/inputs.go` — add `InputsSpec.ImageOverrides
  map[string]string` (the `_images:` reserved key referenced from the
  design doc, promoted to a top-level Spec field per the open
  question's preferred resolution).
- Modify: `internal/wizard/wizard.go` — `Run` accepts new
  `RawAnswers.ImageOverrides` map; merges it into `Inputs.Spec.
  ImageOverrides`. The wizard does not prompt for image overrides
  interactively.
- Modify: `internal/render/render.go` (or new
  `internal/render/images.go`) — before kustomize build, run
  `kustomize edit set image <name>=<ref>` against the selected base
  directory in the work-dir's package copy (or `.upgrade/package`).
  Pre-flight: parse the base's `kustomization.yaml`; if there is no
  `images:` field, fail fast with the message named below.
- Modify: `internal/diff/print.go` — `Images:` footer (added in Phase
  B) reflects post-`--set-image` values.
- Tests:
  - Unit: `--set-image foo=bar:1` against a package with a kustomize
    `images:` transformer changes the rendered Deployment image.
  - Unit: `--set-image foo=bar:1` against a package _without_ an
    `images:` block fails fast with the message: "package's base
    kustomization.yaml has no `images:` block; declare one to use
    --set-image, or use a function-chain input."
  - Unit: image overrides round-trip — `inputs.yaml` written by an
    upgrade-with-`--set-image` carries forward to a subsequent
    upgrade without `--set-image`.
- Acceptance: `installer upgrade <work-dir> <same-ref> --set-image
  hello=hello:v2 && installer upgrade-apply <work-dir>` produces a
  plan whose only diff is the image change.

## Cross-cutting

### Auth context check

`installer wizard`'s ConfigHub fetch and `installer
plan/update/upgrade/upgrade-apply` all inherit the user's `cub` auth
context. Each command starts by calling
`cubctx.CheckOrganization(ctx, upload.Spec.OrganizationID)` (Phase A
helper) and, if that passes, comparing `upload.Spec.Server` to the
current server. Either mismatch fails fast with the recorded vs
current values plus the `cub context set` / `cub auth login`
remediation. We do not silently switch contexts.

### Backwards compatibility

Existing work-dirs without `out/spec/upload.yaml` fall through to local
spec on wizard re-entry — no migration needed. Existing Units without
the `Package=<pkg>` label are not visible to plan/update; once the
prior task ships and the Space is re-uploaded with the label, they
participate normally. (We could add an
`installer migrate-labels <work-dir>` one-shot for users who do not want
to re-upload, but it is not required for the new commands to work.)

### Roadmap entries to update

- README "Roadmap" section — replace the stub line about "interactive
  wizard (e.g., `survey`)" and the "`installer plan`" stub with phase
  pointers.
- README "Status" section — same.
- README "Design docs" section — link `principles.md` and
  `lifecycle.md`.

## Phase F — Unified `--work-dir`, `setup` command, `upload`-absorbs-`update`, drop `upgrade`/`upgrade-apply`

Goal: reduce the consumer-facing command count and remove the three-way
inconsistency in how the working directory is named, by collapsing the
"prepare files locally" half of the lifecycle into a single `setup`
command and folding `update` into `upload`. See
[`lifecycle.md`](./lifecycle.md) for the user-facing spec; this section
is the implementation plan.

### Working directory unification

- `pull`: `--out <dir>` → `--work-dir <dir>` (default `.`). Pull now
  writes to `<work-dir>/package/`, not directly into the destination
  dir. Pulls into a sibling temp dir under `<work-dir>` and uses
  `os.Rename` to swap into `package/` on success; any prior
  `<work-dir>/package/` is renamed to a `.tmp-pkg-prev-<rand>` first
  and removed only after the swap succeeds.
- `render`, `upload`, `plan`, `vet`, `deps update`, `deps tree`,
  `deps build` (stub), `preflight` (stub): positional `<work-dir>`
  argument removed. Each gains `--work-dir <dir>` flag (default `.`).
  No backward-compat positional accepted — pre-1.0, breaking change
  approved.

### New `setup` command

`internal/cli/setup.go`:

- Flags: `--pull <ref>` (optional, replaces `installer pull
  <ref> --work-dir`), plus every flag the existing `wizard` and
  `render` commands accept (`--namespace`, `--select`, `--input`,
  `--non-interactive`, `--components`, `--set-image`, `--reuse`,
  `--base`, `--clean`).
- Flow:
  1. Resolve `--work-dir` (default `.`).
  2. If `--pull <ref>`: pull into `<work-dir>/package/` (via the
     new atomic-rename `pull` core).
  3. Else: require `<work-dir>/package/installer.yaml` to exist;
     error with a hint to pass `--pull` otherwise.
  4. Load prior state (`wizard.LoadPriorState`).
  5. Build raw answers via either the regular (`buildRawAnswers`) or
     upgrade-style (`buildUpgradeAnswers`) path, dispatched on
     `prior != nil && prior has Selection || Inputs`.
  6. Run `wizard.Run` to write spec docs.
  7. If multi-package, run `deps.Resolve` + write lock.
  8. Run `render.Render` (+ `render.RenderDependencies`).
- Auto-detection: prior state present → upgrade flow; absent → fresh
  install flow. There is no `--upgrade` / `--first-install` flag in
  this iteration; if the heuristic proves insufficient we can add one
  later.

The shared answer-building logic currently lives in `cli/wizard.go`
(`buildRawAnswers`) and `cli/upgrade.go` (`buildUpgradeAnswers`); both
move into the `setup` source file (or a shared `cli/answers.go`) so
`wizard` and `setup` both call them.

### `wizard` --render flag

`internal/cli/wizard.go` gains `--render` (default `true`). When
`true`, after `wizard.Run` writes spec docs it calls `render.Render`
(and `render.RenderDependencies` for multi-package). When `false`,
behavior matches today's `wizard`. The render path is the same code
`render.go` runs.

### `upload` absorbs `update`

`internal/cli/upload.go`:

- Auto-detects "first upload" vs "reconcile" by presence of
  `<work-dir>/out/spec/upload.yaml`.
- First upload: existing `uploadOnePackage` / cross-Space links /
  `PlanCrossSpaceLinks` paths, writes `upload.yaml` at the end.
- Reconcile: invokes the existing `diff.Compute` + `diff.Apply` path
  (the body that `cli/update.go` currently calls).
- Rename `--allow-exists` → `--retry`. Same semantics: idempotent
  creates on first upload after a partial failure. Ignored on
  reconcile.
- New flags from old `update`: `--yes`, `--changeset`.

`internal/cli/update.go` deleted. The hook function
`refreshInstallerRecordHook` and helpers move into
`internal/cli/upload.go` (or stay where they are if not in
`update.go`).

### Drop `upgrade` / `upgrade-apply`

- Delete `internal/cli/upgrade.go` and
  `internal/cli/upgrade_apply.go`.
- Remove `newUpgradeCmd`, `newUpgradeApplyCmd` from `root.go`.
- Move `buildUpgradeAnswers` and related helpers
  (`stringifyAnyForUpgrade`, `runDepsUpdate`, `runRender`, etc.) into
  `cli/setup.go` (or wherever they're shared). Drop the
  `.upgrade-prev/` / `.upgrade/` constants and the `prepareStageDir` /
  `copyUploadDoc` / `computeStagedPlan` / `loadPackageOptional` /
  `moveIfExists` / `upgradeChangeSetSlug` / `upgradeChangeSetDescription`
  helpers that exist only to serve the staged-upgrade path.
- The pull-into-tmp-then-rename pattern in the new `pull` core makes
  the `.upgrade/` staging area unnecessary: a failed pull leaves the
  prior `package/` intact via the rename rollback.
- Keep the ChangeSet name `installer-update-<timestamp>` for all
  reconcile invocations. Upgrade-specific ChangeSet slug formatting
  (`installer-upgrade-<from>-to-<to>-<timestamp>`) is dropped — the
  schema-diff log lines that setup prints (`Adopted new default for
  input X`, etc.) make the upgrade nature visible enough.

### Internal helpers

- `internal/pkg/pull.go`: factor out the existing
  `pullOCI` / `pullLocal` paths so they take a "final destination"
  (`<work-dir>/package/`) and use a sibling temp dir; add the
  rename-with-rollback wrapper at the call site.
- `internal/wizard`: no API changes; the existing
  `LoadPriorState` / `DiffInputs` / `DiffComponents` / `AskOneInput`
  helpers are exactly what setup needs.
- `pkg/api`: no schema changes. `Upload` doc unchanged.

### Tests

- `test/e2e/package-and-deps.sh`: rewritten to use the new command
  surface (single-package: `setup --pull` → `upload` → re-edit →
  `setup` re-render → `upload` reconcile → `setup --pull <new-ref>` →
  `upload` reconcile).
- New `test/e2e/setup-flow.sh` (or absorbed into `package-and-deps.sh`)
  that exercises the explicit setup flows: zero-to-render with
  `setup --pull`, idempotent re-render with bare `setup`, upgrade via
  `setup --pull <new>`, and `--set-image` carry-forward.
- `internal/cli/upload_test.go` (new) — table-test that the first-vs-
  reconcile detection picks the right code path on a synthetic
  work-dir.

### Acceptance

- `bin/installer setup --pull oci://… --work-dir /tmp/foo
  --non-interactive --namespace foo` produces a render in
  `/tmp/foo/out/manifests/` and `/tmp/foo/package/` exists. Re-running
  the same command (no `--pull`) re-renders without re-pulling.
- `bin/installer upload --work-dir /tmp/foo --space foo-test` creates
  Units; a second `bin/installer upload --work-dir /tmp/foo` (no
  `--space`) is a no-op on an unchanged work-dir.
- Editing a rendered manifest and re-running `upload` opens a
  ChangeSet, applies the diff.
- `setup --pull <newer-version>` against an existing install produces
  the expected schema-diff log lines, re-renders, and the next
  `upload` reconciles.
- `setup --set-image foo=foo:v2` (no `--pull`) re-renders with the
  image override; the override persists in `out/spec/inputs.yaml`
  across subsequent setup invocations.
- No `.upgrade/` or `.upgrade-prev/` directories are created at any
  point.
- `installer help` no longer lists `update`, `upgrade`, or
  `upgrade-apply`.

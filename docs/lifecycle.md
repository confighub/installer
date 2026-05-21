# Install lifecycle — Design

Status: shipped. The current installer implements the design described
here. This document covers the install + day-2 lifecycle: pulling a
package, collecting inputs, rendering, uploading to ConfigHub,
re-rendering, and upgrading.

Companion to [`package-management.md`](./package-management.md), which
covers the upstream half (bundle, publish, resolve, render). Where that
doc ends — Units in ConfigHub — this one picks up. The decisions here
are anchored to the design principles in
[`principles.md`](./principles.md); where this doc says "we don't let
the user do X," that doc says why.

## Goals

- One canonical way to name the working directory: `--work-dir <dir>`
  (default `.`). Everything else — package source, spec, manifests —
  lives inside it under the layout `package/` + `out/{spec,manifests,...}`.
- Operators get from zero to ready-to-upload with one command
  (`setup`), and the same command handles upgrades.
- The granular commands (`pull`, `wizard`, `render`, `upload`, `plan`)
  stay available for advanced use and step-by-step debugging.
- Upload reconciles local with ConfigHub the way `kubectl apply`
  reconciles a manifest with the cluster — first call creates, later
  calls update / add / delete inside a ChangeSet.
- Interactive wizard with high-level component presets (`minimal`,
  `default`, `all`, `selected`) and aggressive use of declared defaults
  so most users press Enter through it.
- Re-running anything starts from prior choices: prefer ConfigHub if
  the spec has been uploaded, fall back to local `out/spec/` files.
- Image upgrades — the most common day-2 change — are one flag away.
- No new heavy TUI dependency that breaks on terminals where capability
  detection hangs.

## Non-goals

- **Cluster drift detection.** `install plan` diffs the new render
  against ConfigHub state; it does not look at the live cluster. Cluster
  reconciliation is `cub-apply` / `verify-apply` / `drift-reconcile`.
- **Bidirectional sync.** ConfigHub state is the source of truth for
  in-place edits made after install. The installer never silently
  overwrites them — updates use `cub unit update --merge-external-source`
  so post-install ConfigHub edits survive a re-render.
- **Variants.** Same exclusion as `package-management.md`. One Space per
  package per upload.
- **Apply orchestration.** Upload operates on ConfigHub Unit data;
  cluster apply remains the `cub-apply` skill's job.

## Command surface

The lifecycle splits into two halves with a clean boundary:

| Phase | Commands | Operates on |
|---|---|---|
| Local: package + spec + rendered manifests | `pull`, `wizard`, `render`, `setup`, `plan` | Files in `<work-dir>` |
| ConfigHub entities | `upload` | Units, Targets, Links, ChangeSets |

`plan` straddles — it reads local files and queries ConfigHub but does
not mutate. It is grouped with the local commands because it consumes
local manifests as input.

### Working directory convention

Every command accepts `--work-dir <dir>` (default `.`). No positional
working-dir argument anywhere. A typical session is:

```bash
mkdir my-install && cd my-install
install setup --pull oci://ghcr.io/foo/bar:1.0 --namespace foo
install upload --space foo-prod
```

Or for advanced use with an explicit work-dir:

```bash
install setup --pull oci://… --work-dir /tmp/foo --namespace foo
install upload --work-dir /tmp/foo --space foo-prod
```

The layout under `<work-dir>` is:

```
<work-dir>/
├── package/                  # what 'pull' (or 'setup --pull') fetched
│   ├── installer.yaml
│   ├── bases/
│   └── components/
└── out/
    ├── manifests/            # per-resource YAML, ready to upload
    ├── compose/              # synthesized kustomization driving render
    └── spec/                 # the "installer record" (also uploadable as Units)
        ├── selection.yaml
        ├── inputs.yaml
        ├── facts.yaml         (optional, when the package has a collector)
        ├── function-chain.yaml
        ├── manifest-index.yaml
        ├── lock.yaml          (multi-package only)
        └── upload.yaml        (written by first upload)
```

`pull` and `setup --pull` both write the package into
`<work-dir>/package/`, atomically — they pull into a sibling temp dir
and rename on success, so a failed/interrupted pull never leaves the
prior `package/` in an indeterminate state. There is no durable
`.upgrade/` staging area; if you want a record of the prior package
source, commit `<work-dir>/package/` to git before re-pulling.

### `install setup`

One-shot install + upgrade. Combines `pull` (optional) + `wizard` +
`render` into a single command:

```bash
install setup [--pull <ref>] [--work-dir <dir>] [wizard flags] [render flags]
```

- `--pull <ref>` is optional. When present, fetches the package and
  replaces `<work-dir>/package/`. When absent, assumes the package is
  already pulled (use this form to re-render after editing
  `out/spec/inputs.yaml`, or after the package was pulled via a
  separate `install pull` invocation).
- Supports every flag the granular `wizard` and `render` commands
  accept: `--namespace`, `--select`, `--input`, `--non-interactive`,
  `--components`, `--set-image`, `--reuse`, `--base`, `--clean`.
- **Auto-detects install vs upgrade** by checking for prior state:
  - `<work-dir>/out/spec/upload.yaml` exists → load prior from
    ConfigHub (`installer-record` Unit in the recorded Space).
  - Else `<work-dir>/out/spec/{selection,inputs}.yaml` exist → load
    prior locally.
  - Else → fresh install.
- When prior state is present, runs the schema-diff machinery against
  the (possibly newer) package's input + component schema: silently
  carries forward values that still apply, adopts new defaults, drops
  removed inputs, prompts (interactive) or fails fast (non-interactive)
  for newly-required inputs without defaults. Same machinery used by
  the old `install upgrade` command, now applied uniformly to every
  re-run.
- Even when `--pull <same-version>` is passed (no version change),
  setup still goes through the upgrade flow — re-runs the collector,
  re-applies image overrides, etc. This is the explicit "cluster state
  changed, re-collect facts" idiom.
- Does NOT upload. The local/ConfigHub boundary stays clean and
  `--target` / `--space` flags can't be confused for setup options.

The end-to-end one-time install:

```bash
mkdir my-install && cd my-install
install setup --pull oci://ghcr.io/foo/bar:1.0 --namespace foo \
    --input replicas=3
install upload --space foo-prod
```

The upgrade (same command, different version):

```bash
install setup --pull oci://ghcr.io/foo/bar:2.0
install upload
```

Re-render after editing inputs:

```bash
$EDITOR out/spec/inputs.yaml
install setup            # no --pull → reuses package, re-renders
install upload
```

Image-only bump:

```bash
install setup --set-image foo=foo:v2
install upload
```

### `install pull`

```bash
install pull <ref> [--work-dir <dir>]
```

Fetches a package reference and writes it to `<work-dir>/package/`. The
`<ref>` may be `oci://…`, a local directory, or a `.tgz`. Pulls to a
temp sibling directory inside `<work-dir>` and atomically renames on
success; any prior `<work-dir>/package/` is replaced.

This is the granular form. Most operators don't need it — `setup
--pull <ref>` runs pull as its first step.

### `install wizard`

```bash
install wizard <ref> [--work-dir <dir>] [--render=false]
```

Pulls the package (same as `pull`) and runs the interactive (or
flag-driven non-interactive) Q&A to produce
`<work-dir>/out/spec/{selection,inputs,facts}.yaml`. Renders by
default; pass `--render=false` to write only the spec docs and skip
manifest generation.

Same auto-detection of prior state as `setup`. The difference from
`setup` is that `wizard` always pulls (the `<ref>` is required and
positional) and is more explicit about its Q&A role — useful when
the operator wants the wizard to walk through prompts explicitly
rather than carry forward silently.

### `install render`

```bash
install render [--work-dir <dir>] [--clean]
```

Reads `<work-dir>/package/` + `<work-dir>/out/spec/` and produces
`<work-dir>/out/manifests/`. Deterministic — same package + same spec
+ same collector output = byte-identical rendered Units.

### `install upload`

```bash
install upload [--work-dir <dir>] [--space <slug> | --space-pattern <tmpl>]
                 [--target <slug>] [--annotation k=v] [--label k=v]
                 [--retry] [--yes] [--changeset <slug>]
```

Reconciles `<work-dir>/out/manifests/` (and dep subtrees, if any) with
the configured ConfigHub Spaces. Behavior depends on whether the
work-dir has been uploaded before:

- **First upload** — `out/spec/upload.yaml` does not exist. Creates
  Spaces (idempotent), creates one Unit per rendered manifest plus the
  untargeted `installer-record` Unit, creates an AppConfig Unit +
  `render-configmap` Invocation + placeholder Unit + Upsert link for
  any AppConfig-tagged manifests, creates cross-Space
  `installer-record → installer-record` links for dependencies, runs
  intra-Space NeedsProvides link inference. Writes
  `out/spec/upload.yaml` at the end.
- **Reconcile** — `out/spec/upload.yaml` exists. Re-computes the same
  plan `install plan` would produce, opens one ChangeSet per Space,
  runs `cub unit update --merge-external-source --changeset <slug>` for
  updates, `cub unit create` for adds, and for Units that dropped out of
  the rendered output, `cub unit update --merge-external-source` with
  empty content to **empty** them (gated on `--yes` or interactive
  confirmation). Emptying — never `cub unit delete` — is a 3-way merge
  against the last installer push, so it removes only the installer-
  contributed resources and preserves the Unit record, its target
  binding, and any post-install edits, leaving prior Data in revision
  history; applying the emptied Unit later removes its deployed
  resources through the normal deploy path. Units guarded by a
  DestroyGate are refused. Refreshes the `installer-record` Unit so
  subsequent setups re-enter from up-to-date state.

The two modes share most of the implementation; the detection rule is
purely "is this the first time we're talking to ConfigHub for this
work-dir?" — captured by the presence of `upload.yaml`.

Flags:

- `--space` / `--space-pattern` — destination Space(s). Required on
  first upload; ignored on reconcile (reads `upload.yaml`).
- `--target` — forwarded to `cub unit create` on adds. Not applied to
  existing Units (would clobber post-install metadata edits).
- `--annotation`, `--label` — same.
- `--retry` — relaxes the "Unit already exists" / "Link already exists"
  error on first upload so a partially-failed first upload can be
  retried without erroring on the entries that did succeed. (Replaces
  the previous `--allow-exists` flag — same behavior, clearer name.)
  Ignored on reconcile (reconcile is always idempotent).
- `--yes` — skip the empty-Units confirmation on reconcile (required
  when stdin is not a TTY and the plan empties Units that dropped out of
  the rendered output).
- `--changeset` — explicit ChangeSet slug on reconcile. Default:
  `installer-update-<RFC3339-timestamp>`.

Reconcile on an unchanged work-dir is a no-op — no ChangeSet is opened.

The choice to keep `upload` as one command spanning both phases (versus
splitting "create" and "reconcile") follows the
`kubectl apply` model — the operator says "make ConfigHub match this
work-dir" and the command figures out what that means.

### `install plan`

```bash
install plan [--work-dir <dir>]
```

Read-only diff vs ConfigHub. Same code path as `upload` reconcile, but
prints the plan and stops. Useful for previewing what `upload` will do
before letting it loose. Fails fast if the work-dir hasn't been
uploaded yet (no `upload.yaml`).

## Re-entering the wizard from prior state

`setup` and `wizard` both check for prior state in this order:

1. `<work-dir>/out/spec/upload.yaml` exists → fetch the
   `installer-record` Unit from the recorded Space and use the
   Selection + Inputs + Facts embedded in it as the starting state.
2. Else `<work-dir>/out/spec/{selection,inputs,facts}.yaml` exist →
   use them.
3. Else: fresh wizard.

When prior state is loaded and the wizard is running interactively, it
offers one yes/no first:

> Re-use last choices? [Y/n]

`Y` runs render directly. `n` walks every prompt with the prior values
pre-filled as defaults.

If the ConfigHub fetch in step 1 fails (server down, record deleted,
Space renamed), the wizard prints a warning, falls back to step 2, and
notes that the next successful upload will refresh `upload.yaml`.

### Upload doc

`out/spec/upload.yaml` persists where this work-dir's spec was last
uploaded:

```yaml
apiVersion: installer.confighub.com/v1alpha1
kind: Upload
metadata: { name: hello-app-upload }
spec:
  package: hello-app
  packageVersion: 0.1.0
  spacePattern: "{{.PackageName}}"
  spaces:
    - { package: hello-app, version: 0.1.0, slug: hello-app, isParent: true }
    - { package: dep-pkg,   version: 0.2.0, slug: dep-pkg }
  uploadedAt: 2026-05-14T10:21:00Z
  server: https://hub.confighub.com
  organizationID: org_01JDQP70M348Z3M2FK7ZKS9Q1A
```

`install upload` writes this file at the end of a successful first
upload, and rewrites it on each successful reconcile (timestamp +
member list refresh). All subsequent commands read it on entry to
locate the current Spaces. It is also embedded in the
`installer-record` Unit body so a freshly cloned work-dir can be
rebuilt from ConfigHub alone — but the local `upload.yaml` is what
bootstraps the lookup.

### Organization sanity check

Every command that touches ConfigHub reads the current cub
organization and compares it against `spec.organizationID` in
`out/spec/upload.yaml`. A mismatch fails fast with a message naming
both org IDs and pointing at `cub context set` / `cub auth login`.
Same treatment for `spec.server` mismatch.

## Schema-diff handling (setup acting as upgrade)

When `setup` runs against a work-dir with prior state, the new
package's input/component schema may differ from what the prior
install answered. The schema-diff machinery handles the cases:

- **New input with default**: silently adopted. Logged.
- **New required input without default**: prompted in interactive
  mode; non-interactive mode fails fast naming each missing input and
  pointing at running setup interactively (or `install wizard
  <ref>`).
- **Removed input**: silently dropped from the new `inputs.yaml`.
- **Type-changed input**: errors with a re-run-interactively hint.

For components, similar rules: if the prior selection matched the old
package's `default` preset exactly, the upgrade adopts the new
package's default preset (so a newly-flagged `default: true`
component flows in automatically). Otherwise the prior list is
filtered to components that still exist in the new package.

The schema-diff runs **whenever prior state is present**, including
when `setup --pull` pulls the same version — that scenario is the
explicit "re-collect facts" idiom and behaves the same as the
schema-diff just-happens-to-be-empty case.

## Image overrides

Per [Principle 5](./principles.md#5-image-management-declare-a-kustomize-transformer-use-functions-when-changes-are-common),
the recommended path depends on how often the override is expected:

- **Occasional override (mirror, patch bump)** — package author
  declares a kustomize `images:` transformer in the chosen base.
  Operator passes `--set-image name=ref` (repeatable) to `installer
  setup` or `install wizard`; the installer runs `kustomize edit set
  image` against the package's working copy before render. The
  `--set-image` value is recorded in `out/spec/inputs.yaml` under
  `spec.imageOverrides` so the next setup carries it forward without
  the operator re-typing it.
- **Frequent / structured override** — package author declares an
  image input and a `set-container-image` group in `transformers`.
  Operator answers the input through the wizard.
- **Post-install one-off** — operator runs `cub function do
  set-container-image` on the uploaded Unit. Survives re-render
  because `install upload` uses `--merge-external-source`.

`install plan` and `install upload` print a per-Space `Images:`
footer built from `cub function do --space <slug> get-container-image '*'`
against the new render (locally, before any update), so the operator
can see the eventual image set without applying anything.

`install setup --set-image` against a package whose base has no
`images:` transformer fails fast with a useful message: "package's
base kustomization.yaml has no `images:` block; declare one to use
--set-image, or use a `spec.transformers` input."

## Common scenarios

- **Image tag bump** — the day-2 case:
  ```bash
  install setup --set-image hello=hello:v2
  install upload
  ```
  Plan shows a one-line image change.
- **Adding a component**:
  ```bash
  $EDITOR out/spec/selection.yaml
  install setup --non-interactive   # re-renders against edited selection
  install plan                       # preview
  install upload                     # materialize
  ```
- **Package version bump with new required input**:
  ```bash
  install setup --pull oci://reg/hello-app:0.2.0
  ```
  In interactive mode, prompts for the new required input. In
  non-interactive mode, fails fast with a hint to re-run
  interactively.
- **Cluster state changed (collector picks up new fact)**:
  ```bash
  install setup --pull oci://reg/hello-app:<same-version>
  install upload
  ```
  Re-runs the collector, re-renders, upload reconciles.
- **Reverting an `install upload` reconcile**:
  ```bash
  cub unit update --restore Before:ChangeSet:<slug> \
      --where "Labels.Package='hello-app'"
  ```
  Reverts updates only. Creates and deletes from that upload are not
  reverted by ChangeSet restore — to undo a create, delete the Unit; to
  undo a delete, re-render and re-run upload.

## Why this shape (vs the prior shape)

The earlier installer surface had:

- `pull --out <dir>`, `wizard --work-dir <dir>`, `render <work-dir>`,
  `upload <work-dir>` — three different ways to name the working
  directory.
- `update <work-dir>` — separate command for "reconcile uploaded
  state with new render."
- `upgrade <work-dir> <ref>` — staged a re-pull into `<work-dir>/.upgrade/`
  with its own out/ tree, kept by-then-stale `.upgrade-prev/` as a
  one-step rollback.
- `upgrade-apply <work-dir>` — promoted `.upgrade/` over the working
  tree and ran update.

The current shape removes friction:

- **One way to name the working directory** (`--work-dir`, default
  `.`) eliminates the conceptual difference between "out dir" and
  "work dir" and lets the natural `mkdir foo && cd foo && installer
  setup …` workflow just work.
- **Pull writes to `<work-dir>/package/` atomically** (not directly
  into `--out`) so pull is composable with setup's auto-detection. The
  pull-into-tmp-then-rename pattern gives the same failure-safety
  `.upgrade/` was providing without a durable staging directory.
- **`update` folded into `upload`**: the operator's mental model is
  "make ConfigHub match my work-dir." Whether that means create or
  update is detection-dispatched, the same way `kubectl apply` chooses
  between create and patch.
- **`upgrade` and `upgrade-apply` folded into `setup`**: same Q&A flow,
  same schema-diff logic, applied uniformly whether the prior state
  exists or not. The operator doesn't have to know whether they're
  upgrading — setup figures that out.
- **`apply` term reclaimed**: not used anywhere in the installer's
  surface now, leaving it free for its ConfigHub / kubectl meaning.

## Open questions

- Should `setup` print the eventual plan it would write to ConfigHub
  before exiting? Today the operator runs `install plan` separately;
  we could chain the plan readout into setup the way `upgrade` used to
  print plan. Current decision: keep them separate so setup is
  ConfigHub-free (no `cub` call on the local-only path).

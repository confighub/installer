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
  lives inside it under the layout `package/` + `out/{record,manifests,...}`.
- Operators get from zero to ready-to-upload with one command
  (`setup`), and the same command handles upgrades.
- The granular commands (`pull`, `wizard`, `render`, `upload`, `plan`)
  stay available for advanced use and step-by-step debugging.
- Upload makes ConfigHub match the render the way `kubectl apply` makes a
  cluster match a manifest: every call is create-or-update, and a resource
  the render drops is emptied, never deleted.
- Interactive wizard with high-level component presets (`minimal`,
  `default`, `all`, `selected`) and aggressive use of declared defaults
  so most users press Enter through it.
- Re-running anything starts from prior choices: the work-dir's own
  `out/record/` files, or, for a fresh clone, the installer record an
  earlier upload left in ConfigHub.
- Image upgrades — the most common day-2 change — are one flag away.
- No new heavy TUI dependency that breaks on terminals where capability
  detection hangs.

## Non-goals

- **Cluster drift detection.** `installer plan` diffs the new render
  against ConfigHub state; it does not look at the live cluster. Cluster
  reconciliation is `cub-apply` / `verify-apply` / `drift-reconcile`.
- **Bidirectional sync.** ConfigHub state is the source of truth for
  in-place edits made after install. Upload 3-way merges a changed
  resource into its Unit, so a post-install edit to a path the render does
  not change survives, and one the render does change is withheld as a
  conflict if the path is protected.
- **Variants.** Same exclusion as `package-management.md`. One Space per
  package per upload.
- **Apply orchestration.** Upload operates on ConfigHub Unit data;
  cluster apply remains the `cub-apply` skill's job.

## Command surface

The lifecycle splits into two halves with a clean boundary:

| Phase | Commands | Operates on |
|---|---|---|
| Local: package + spec + rendered manifests | `pull`, `wizard`, `render`, `setup`, `plan` | Files in `<work-dir>` |
| ConfigHub entities | `upload` | Spaces, Units, Links, Invocations, ChangeSets |

`plan` straddles — it reads local files and queries ConfigHub but does
not mutate. It is grouped with the local commands because it consumes
local manifests as input.

### Working directory convention

Every command accepts `--work-dir <dir>` (default `.`). No positional
working-dir argument anywhere. A typical session is:

```bash
mkdir my-install && cd my-install
installer setup --pull oci://ghcr.io/foo/bar:1.0 --namespace foo
installer upload --space foo-prod
```

Or for advanced use with an explicit work-dir:

```bash
installer setup --pull oci://… --work-dir /tmp/foo --namespace foo
installer upload --work-dir /tmp/foo --space foo-prod
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
    └── record/               # the record of the render: selection, inputs, facts, chain, lock
        ├── selection.yaml
        ├── inputs.yaml
        ├── facts.yaml         (optional, when the package has a collector)
        ├── function-chain.yaml
        ├── manifest-index.yaml
        └── lock.yaml          (multi-package only)
```

`pull` and `setup --pull` both write the package into
`<work-dir>/package/`, atomically — they pull into a sibling temp dir
and rename on success, so a failed/interrupted pull never leaves the
prior `package/` in an indeterminate state. There is no durable
`.upgrade/` staging area; if you want a record of the prior package
source, commit `<work-dir>/package/` to git before re-pulling.

### `installer setup`

One-shot install + upgrade. Combines `pull` (optional) + `wizard` +
`render` into a single command:

```bash
installer setup [--pull <ref>] [--work-dir <dir>] [wizard flags] [render flags]
```

- `--pull <ref>` is optional. When present, fetches the package and
  replaces `<work-dir>/package/`. When absent, assumes the package is
  already pulled (use this form to re-render after editing
  `out/record/inputs.yaml`, or after the package was pulled via a
  separate `installer pull` invocation).
- Supports every flag the granular `wizard` and `render` commands
  accept: `--namespace`, `--select`, `--input`, `--non-interactive`,
  `--components`, `--set-image`, `--reuse`, `--base`, `--clean`.
- **Auto-detects install vs upgrade** by checking for prior state:
  - `<work-dir>/out/record/{selection,inputs}.yaml` exist → load prior
    locally.
  - Else an earlier upload left an `installer` record Unit in ConfigHub →
    load prior from it (see "Re-entering the wizard from prior state").
  - Else → fresh install.
- When prior state is present, runs the schema-diff machinery against
  the (possibly newer) package's input + component schema: silently
  carries forward values that still apply, adopts new defaults, drops
  removed inputs, prompts (interactive) or fails fast (non-interactive)
  for newly-required inputs without defaults. Same machinery used by
  the old `installer upgrade` command, now applied uniformly to every
  re-run.
- Even when `--pull <same-version>` is passed (no version change),
  setup still goes through the upgrade flow — re-runs the collector,
  re-applies image overrides, etc. This is the explicit "cluster state
  changed, re-collect facts" idiom.
- Does NOT upload. The local/ConfigHub boundary stays clean: setup's only
  ConfigHub read is recovering prior state, which its `--space` flag
  scopes.

The end-to-end one-time install:

```bash
mkdir my-install && cd my-install
installer setup --pull oci://ghcr.io/foo/bar:1.0 --namespace foo \
    --input replicas=3
installer upload --space foo-prod
```

The upgrade (same command, different version). Nothing local records the
Space, so every upload names it:

```bash
installer setup --pull oci://ghcr.io/foo/bar:2.0
installer upload --space foo-prod
```

Re-render after editing inputs:

```bash
$EDITOR out/record/inputs.yaml
installer setup            # no --pull → reuses package, re-renders
installer upload --space foo-prod
```

Image-only bump:

```bash
installer setup --set-image foo=foo:v2
installer upload --space foo-prod
```

### `installer pull`

```bash
installer pull <ref> [--work-dir <dir>]
```

Fetches a package reference and writes it to `<work-dir>/package/`. The
`<ref>` may be `oci://…`, a local directory, or a `.tgz`. Pulls to a
temp sibling directory inside `<work-dir>` and atomically renames on
success; any prior `<work-dir>/package/` is replaced.

This is the granular form. Most operators don't need it — `setup
--pull <ref>` runs pull as its first step.

### `installer wizard`

```bash
installer wizard <ref> [--work-dir <dir>] [--render=false]
```

Pulls the package (same as `pull`) and runs the interactive (or
flag-driven non-interactive) Q&A to produce
`<work-dir>/out/record/{selection,inputs,facts}.yaml`. Renders by
default; pass `--render=false` to write only the record docs and skip
manifest generation.

Same auto-detection of prior state as `setup`. The difference from
`setup` is that `wizard` always pulls (the `<ref>` is required and
positional) and is more explicit about its Q&A role — useful when
the operator wants the wizard to walk through prompts explicitly
rather than carry forward silently.

### `installer render`

```bash
installer render [--work-dir <dir>] [--clean]
```

Reads `<work-dir>/package/` + `<work-dir>/out/record/` and produces
`<work-dir>/out/manifests/`. Deterministic — same package + same spec
+ same collector output = byte-identical rendered Units.

### `installer upload`

```bash
installer upload [--work-dir <dir>] [--space <slug> | --space-pattern <tmpl>]
                 [--component <name>] [--variant <name>] [--layer/--environment/--region/--owner <value>]
                 [--space-label k=v] [--space-annotation k=v]
                 [--unit-label k=v] [--unit-annotation k=v]
                 [--target <ref>] [--changeset <ref>] [--yes]
```

Sends each package's render to ConfigHub's upload API — the parent, then
each locked dependency, each into its own Space — and ConfigHub makes the
Space match it. There is no first-upload mode: every upload is
create-or-update.

- **What each request carries.** The package's rendered manifests
  (`out/manifests/`, or `out/<dep>/manifests/`) and its installer record
  (see below). Secrets are rendered to `out/secrets/` and never uploaded.
- **What ConfigHub does with it.** Every resource becomes its own Unit,
  named for the resource. A resource new to the render is created, a
  changed one is 3-way merged into its Unit, an unchanged one is left
  alone, and one the render no longer produces is **emptied** — never
  deleted — so its Unit keeps its ID, links, and history, and the next
  release withdraws the object. A Unit guarded by a DestroyGate is never
  emptied: the upload is refused. AppConfig carried in annotated ConfigMaps
  becomes an AppConfig Unit, a `render-configmap` Invocation, and a
  placeholder Unit the rendered ConfigMap lands in. Links between Units are
  inferred from references, label selectors, and CRDs. An unchanged
  work-dir writes nothing.
- **Ownership.** Every Unit, Link, and Invocation an upload writes is
  labeled `UploadSource=<package>`. An upload only ever writes or empties
  what its package owns, so a Space can also hold Units added by hand.
- **Namespaces.** The install namespace is the Space's release namespace,
  recorded as its `Namespace` label. Resources a package places in other
  namespaces stay in the package's Space.
- **ChangeSets.** ConfigHub records each Space's writes in a ChangeSet, and
  upload prints the command that reverts them — restoring the package's
  Units to before that ChangeSet, which empties the Units the upload
  created and reverts the rest.

Flags:

- `--space` / `--space-pattern` — the Space for a package with no
  dependencies, or a Go template over `.PackageName`, `.PackageVersion`,
  and `.Variant` naming each package's Space (default `{{.PackageName}}`).
  Nothing local records it, so pass the same flag to every upload and plan.
- `--component` — the parent's `Component` label (default: the package
  name). A dependency's is its package name. The parent's Space records its
  dependencies' components in its `DependsOn` annotation.
- `--variant` — every package's `Variant` label (default `base`).
- `--layer`, `--environment`, `--region`, `--owner`, `--space-label`,
  `--space-annotation` — Space metadata. Labels and annotations given on a
  later upload are updated; ones omitted are left alone, and so are
  `Component` and `Variant` when their flags are omitted.
- `--unit-label`, `--unit-annotation` — set on every Unit the upload writes.
- `--target` — the Target new Units are created on: `<space>/<target>`, a
  UUID, or a slug in the package's Space. Recorded as the Space's
  `TargetID` annotation, which a later upload without `--target` uses.
  Existing Units keep their Target.
- `--changeset` — an existing open ChangeSet every package's writes join,
  so one restore reverts the whole install. Default: one per Space.
- `--yes` — empty Units without asking. Without it, an upload that would
  empty anything lists the Units and asks, and refuses when there is no
  terminal to ask on.

### Installer record

Each package's request includes one AppConfig/YAML document, uploaded as
the untargeted `installer` Unit. It uses the AppConfig/YAML schema's
reserved `configHub` paths rather than KRM `apiVersion`, `kind`, and
`metadata`:

```yaml
configHub:
  configSchema: InstallerRecord
  configName: installer
package:            # installer.yaml, without apiVersion, kind, and metadata
  name: hello-app
  installerMetadata: {version: 0.1.0}
  spec: {...}
selection: {base: default, components: [...]}
inputs: {namespace: demo, values: {...}, imageOverrides: {...}}
facts: {values: {...}}
lock: {...}         # the parent only
```

It is the record of the render in `out/record/`, as ConfigHub keeps it: a
fresh clone re-enters the install from it. Being application
configuration, no Kubernetes function or Trigger touches it.

### `installer plan`

```bash
installer plan [upload's flags]
```

Runs the requests `installer upload` would send, as dry runs, and prints
what each would do per Space: the Units it would create, update, empty,
revive, or adopt, and the Links it would create or delete. ConfigHub
computes the plan with the same code an upload runs, so an upload with the
same flags does what plan shows. Plan takes upload's flags because they
decide the Spaces, and prints the images the render runs after each Space.

## Re-entering the wizard from prior state

`setup` and `wizard` both check for prior state in this order:

1. `<work-dir>/out/record/{selection,inputs,facts}.yaml` exist → use them.
2. Else an `installer` record Unit from an earlier upload of the package
   exists in ConfigHub → use the selection, inputs, facts, and package
   definition it holds. `setup --space <slug>` names the Space to recover
   from; without it, setup looks for the package's install across the
   organization and refuses when it finds more than one.
3. Else: fresh wizard.

When prior state is loaded and the wizard is running interactively, it
offers one yes/no first:

> Re-use last choices? [Y/n]

`Y` runs render directly. `n` walks every prompt with the prior values
pre-filled as defaults.

If the ConfigHub lookup in step 2 fails (no session, several installs
without `--space`), setup prints a warning and starts a fresh install.

## Schema-diff handling (setup acting as upgrade)

When `setup` runs against a work-dir with prior state, the new
package's input/component schema may differ from what the prior
install answered. The schema-diff machinery handles the cases:

- **New input with default**: silently adopted. Logged.
- **New required input without default**: prompted in interactive
  mode; non-interactive mode fails fast naming each missing input and
  pointing at running setup interactively (or `installer wizard
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
  setup` or `installer wizard`; the installer runs `kustomize edit set
  image` against the package's working copy before render. The
  `--set-image` value is recorded in `out/record/inputs.yaml` under
  `spec.imageOverrides` so the next setup carries it forward without
  the operator re-typing it.
- **Frequent / structured override** — package author declares an
  image input and a `set-container-image` group in `transformers`.
  Operator answers the input through the wizard.
- **Post-install one-off** — operator runs `cub function do
  set-container-image` on the uploaded Unit. Survives re-render as long
  as the render does not change that path, because `installer upload`
  3-way merges; protect the path to hold it against a render that does.

`installer plan` prints a per-Space `Images:` footer built from the
rendered manifests, so the operator can see the eventual image set
without applying anything.

`installer setup --set-image` against a package whose base has no
`images:` transformer fails fast with a useful message: "package's
base kustomization.yaml has no `images:` block; declare one to use
--set-image, or use a `spec.transformers` input."

## Common scenarios

- **Image tag bump** — the day-2 case:
  ```bash
  installer setup --set-image hello=hello:v2
  installer upload --space hello-prod
  ```
  Plan shows the Deployment's Unit as an update, and the new image in its
  Images footer.
- **Adding a component**:
  ```bash
  $EDITOR out/record/selection.yaml
  installer setup --non-interactive       # re-renders against edited selection
  installer plan --space hello-prod       # preview
  installer upload --space hello-prod     # materialize
  ```
- **Package version bump with new required input**:
  ```bash
  installer setup --pull oci://reg/hello-app:0.2.0
  ```
  In interactive mode, prompts for the new required input. In
  non-interactive mode, fails fast with a hint to re-run
  interactively.
- **Cluster state changed (collector picks up new fact)**:
  ```bash
  installer setup --pull oci://reg/hello-app:<same-version>
  installer upload --space hello-prod
  ```
  Re-runs the collector, re-renders, and uploads what changed.
- **Reverting an `installer upload`** — run the command upload printed:
  ```bash
  cub unit update --patch --space hello-prod \
      --restore Before:ChangeSet:<slug> --where "Labels.UploadSource = 'hello-app'"
  ```
  The ChangeSet holds every write the upload made, so this empties the
  Units it created and reverts the ones it updated or emptied.

## Why this shape (vs the prior shape)

The earlier installer surface had:

- `pull --out <dir>`, `wizard --work-dir <dir>`, `render <work-dir>`,
  `upload <work-dir>` — three different ways to name the working
  directory.
- `update <work-dir>` — separate command for "bring uploaded state up to
  date with a new render."
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
  "make ConfigHub match my work-dir." ConfigHub decides, per resource,
  whether that means create, merge, or empty, the same way `kubectl apply`
  chooses between create and patch.
- **`upgrade` and `upgrade-apply` folded into `setup`**: same Q&A flow,
  same schema-diff logic, applied uniformly whether the prior state
  exists or not. The operator doesn't have to know whether they're
  upgrading — setup figures that out.
- **`apply` term reclaimed**: not used anywhere in the installer's
  surface now, leaving it free for its ConfigHub / kubectl meaning.

## Open questions

- Should `setup` print the eventual plan it would write to ConfigHub
  before exiting? Today the operator runs `installer plan` separately;
  we could chain the plan readout into setup the way `upgrade` used to
  print plan. Current decision: keep them separate, so setup's only
  ConfigHub call is recovering prior state for a work-dir without one.

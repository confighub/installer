# Interactive Wizard, Plan, Update, Upgrade — Design

Status: design — none of this is implemented yet. The current installer ships
with a non-interactive wizard, render, and upload. This document covers
day-2 lifecycle: an interactive wizard that re-enters from prior state, and
a `plan` / `update` / `upgrade` trio that brings ConfigHub Unit state in
line with re-rendered output the way `terraform plan` + `apply` do for
infrastructure.

Companion to [`package-management.md`](./package-management.md), which
covers the upstream half (bundle, publish, resolve, render). Where that doc
ends — Units in ConfigHub — this one picks up. The decisions here are
anchored to the design principles in [`principles.md`](./principles.md);
where this doc says "we don't let the user do X," that doc says why.

## Goals

- Interactive wizard with high-level component presets (`minimal`,
  `default`, `all`, `selected`) and aggressive use of declared defaults so
  most users press Enter through it.
- Re-running the wizard starts from the prior choices: prefer ConfigHub if
  the spec has been uploaded, fall back to local `out/spec/` files.
- A `plan` / `update` / `upgrade` trio that diffs and reconciles ConfigHub
  Unit state against re-rendered output, like `kubectl apply` or
  `terraform plan` + `apply`.
- Image upgrades — the most common day-2 change — should be one flag away.
- No new heavy TUI dependency that breaks on terminals where capability
  detection hangs.

## Non-goals

- **Cluster drift detection.** `installer plan` diffs the new render
  against ConfigHub state; it does not look at the live cluster. Cluster
  reconciliation is `cub-apply` / `verify-apply` / `drift-reconcile`.
- **Bidirectional sync.** ConfigHub state is the source of truth for
  in-place edits made after install. The installer never silently
  overwrites them — updates use `cub unit update --merge-external-source`
  so post-install ConfigHub edits survive a re-render.
- **Variants.** Same exclusion as `package-management.md`. One Space per
  package per upload.
- **Apply orchestration.** Plan/update operate on ConfigHub Unit data;
  cluster apply remains the `cub-apply` skill's job.

## Wizard library choice

We have a known problem with the `charmbracelet` libraries: `termenv`
issues OSC capability queries (OSC 10/11 for fg/bg color, others) and waits
for terminal responses with a generous timeout. On terminals that never
answer (older WezTerm releases, certain SSH/mosh setups, container shells,
some IDE-embedded terminals) every query stalls for the full timeout
window. Glow exhibits this; bubbletea and lipgloss inherit it via termenv.
Re-using bubbletea here would not solve the problem — it would just move
it.

There are known workarounds (force a color profile, disable background
detection, pre-set `CLICOLOR_FORCE` / `COLORTERM`), but they have to be
threaded into every entry point and shipped as guidance, and the failure
mode if they are missed is multi-second hangs that look like the binary
crashed.

For a wizard with ~5–15 prompts (base, components, a handful of inputs)
the widgets we actually need are confirm, single-select, multi-select, and
text-with-default. `github.com/AlecAivazis/survey/v2` covers all of them
with proven cross-terminal stability — it does not issue OSC queries. We
will adopt survey/v2 and skip bubbletea entirely for the wizard.

When stdin is not a TTY (CI, piped input), the interactive wizard refuses
to start and tells the user to pass `--non-interactive` with `--base` /
`--select` / `--input`. The non-interactive path keeps working unchanged.

If we later want a richer UI (a status pane during render, e.g.) bubbletea
remains an option, with the termenv hardening done once and contained.

## Component presets

Three new tokens accepted by `--components <preset>` and offered as the
first prompt of the interactive wizard:

- `minimal` — empty set. Just the base.
- `default` — the components the package marks `default: true` (new
  `Component.Default bool` field). If no components are flagged, behaves
  like `minimal`.
- `all` — every component, modulo conflict resolution. Conflicting pairs
  prompt; the default answer is the first in declaration order.
- `selected` — the existing per-component multi-select. Today's behavior.

The wizard always asks the preset question first; if the answer is anything
other than `selected`, the per-component step is skipped. Required-deps
closure runs after the preset is resolved, exactly like today.

`--select <name>` (existing flag) still works in non-interactive mode. New
`--components <preset>` and `--select` are mutually exclusive.

## Re-entering the wizard from prior state

`installer wizard <ref>` checks for prior state in this order:

1. `<work-dir>/out/spec/upload.yaml` exists → fetch the installer-record
   Unit from the recorded Space and use the Selection + Inputs + Facts
   embedded in it as the starting state.
2. Else `<work-dir>/out/spec/{selection,inputs,facts}.yaml` exist → use
   them.
3. Else: fresh wizard.

When prior state is loaded, the wizard offers one yes/no first:

> Re-use last choices? [Y/n]

`Y` runs render directly. `n` walks every prompt with the prior values
pre-filled as defaults (Enter accepts, edit changes).

If the ConfigHub fetch in step 1 fails (server down, record deleted,
Space renamed), the wizard prints a warning, falls back to step 2, and
notes that the next successful upload will refresh `upload.yaml`.

### Upload doc

A new document type, `Upload`, persists where this work-dir's spec was
last uploaded:

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

`installer upload` writes this file at the end of a successful run.
`installer wizard`, `plan`, `update`, and `upgrade` all read it on entry to
locate the current Spaces. The file is included in the installer-record
Unit body so a freshly cloned work-dir can be rebuilt from ConfigHub
alone — but the local `upload.yaml` is what bootstraps the lookup.

### Organization sanity check

Every command that touches ConfigHub reads the current cub
organization with

    cub context get -o jq=".coordinate.organizationID"

and compares it against `spec.organizationID` in `out/spec/upload.yaml`.
A mismatch fails fast with a message naming both org IDs and pointing at
`cub context set` / `cub auth login`. Same treatment for `spec.server`
mismatch. The check is purely defensive: it catches the common
foot-gun where an operator switches accounts between sessions and
otherwise would silently materialize Units in the wrong place.

### Spec directory shape

We considered consolidating `out/spec/{selection,inputs,facts,upload,...}.yaml`
into one multi-doc file in the same format `BuildInstallerRecord` produces
for upload. We will keep the per-file layout: small individual files diff
better, edit better, and the existing `BuildInstallerRecord` already builds
the consolidated stream when needed. The new `upload.yaml` joins the
existing files with no other layout changes.

## Plan, update, upgrade

### `installer plan <work-dir>`

Prints a Terraform-style diff vs ConfigHub for every package in the
work-dir's lock. Pure read; no mutations.

```
Plan: 3 to add, 5 to change, 1 to delete.

Space hello-app:
  + ingress-tls-cert
  ~ deployment-demo-hello
      spec.template.spec.containers[0].image: hello:v1 -> hello:v2
      spec.replicas: 2 -> 3
  - configmap-old-thing

Images (post-render):
  Deployment/hello                hello:v2
  Deployment/sidecar              sidecar:1.4
```

How the plan is computed, per package:

- **Expected Units** = files in `<work-dir>/out/manifests/` (and
  `out/<dep>/manifests/` for deps). Slug = filename minus extension.
- **Current Units** = `cub unit list --space <slug> --where "Labels.Component='<pkg>'"`
  (the `Component` label was added by upload — see prior task).
- **Adds** = expected − current.
- **Deletes** = current − expected, excluding the installer-record Unit.
- **Updates** = intersection. For each, run
  `cub unit update --space <slug> --merge-external-source <filename> --dry-run -o mutations <slug> <path>`.
  ConfigHub returns the JSON mutations list; render to a human-readable
  diff. An empty mutations list means no change.

The list step is required: `cub unit update --merge-external-source
--dry-run` errors if the Unit does not exist, so the adds-vs-updates
split has to come from the Unit list. (`cub unit create --allow-exists`
is for idempotent retry, not create-or-update — it does not update an
existing Unit and so cannot replace the split.)

`DataHash` is **not** the change predicate. Post-install ConfigHub edits
change `DataHash` even when the new render is byte-identical to the prior
render. The mutations list — which is computed against the prior
`MergeExternal` revision recorded under the same source name — is the
correct signal. `DataHash` is shown in plan output as identifying info
only.

The `Images:` footer per Space is built from
`cub function do --space <slug> get-container-image '*'` after the plan is
computed. It runs against the new render (locally, before any update), so
the user can see the eventual image set without applying anything.

### `installer update <work-dir>`

Re-computes the plan and executes it inside a ChangeSet so updates are
revertable:

1. `cub changeset create --space <parent-slug> installer-update-<timestamp>
   --description "installer update from <package>@<version>"`. One
   ChangeSet per `installer update` invocation; spans the parent Space
   (deps' Spaces participate via the cross-Space links and per-Unit
   ChangeSet recording).
2. `cub unit create --label Component=<pkg>` for adds.
3. `cub unit update --merge-external-source <filename>` (no
   `--dry-run`) for updates. Each update is recorded against the
   ChangeSet so the operator can revert the whole batch with
   `cub unit update --restore Before:ChangeSet:<changeset> --where
   Labels.Component='<pkg>'`.
4. `cub unit delete` for deletes. Prompts unless `--yes`. Delete also
   removes every link from and to the deleted Unit (ConfigHub does this
   automatically), so stale-link cleanup is not the installer's
   responsibility.
5. Link reconciliation: re-run upload's link inference, diff against
   existing links, add missing. Existing links pointing at still-present
   Units are left alone; links pointing at deleted Units are gone via
   step 4.

Plan output names the ChangeSet that `update` will create and notes its
revert scope explicitly:

> Updates in this ChangeSet are revertable with `cub unit update
> --restore Before:ChangeSet:<slug>`. Creates and deletes are not
> reverted by ChangeSet restore — to undo a create, delete the Unit; to
> undo a delete, re-run `installer update` (the next render will
> recreate it from spec).

`update` is a no-op against an unchanged work-dir. Calling it twice
converges on the second call.

`update` does not call `cub unit apply`. Materializing Units in
ConfigHub is the installer's job; pushing them to the cluster is
`cub-apply`'s. They are deliberately separate so a fleet operator can
review the plan, let `update` write Units, and trigger apply through
their existing promotion workflow.

### `installer upgrade <work-dir> <new-ref>`

Plan + version bump, in one command. Always writes to `.upgrade/`;
never mutates the working tree directly.

1. Pull `<new-ref>` into `<work-dir>/.upgrade/package` (sibling to the
   current `package/`, never clobbers).
2. Diff the new package's input schema against the prior `inputs.yaml`:
   - **New input with default**: silently adopt the default.
   - **New required input without default**: must be supplied. In
     interactive mode, prompt for it. In non-interactive mode, fail
     fast with a message naming each missing input and pointing at
     `installer wizard <work-dir>` (which will start from the new
     package's schema and the prior values, prompting only for the new
     required ones).
   - **Removed input**: drop from the new `inputs.yaml`. Operator does
     not have to do anything.
   - **Type-changed input**: surface as an error; operator must
     re-answer interactively.
   - **New component / removed component**: closure runs against the
     new package's component graph. Removed components silently drop
     from `selection.yaml`. New `default: true` components are
     adopted only if the prior selection used the `default` preset
     (recorded in `selection.yaml`); otherwise they require an explicit
     opt-in via wizard.
3. Re-run the collector. (Mirrors `cub worker upgrade`'s "fetch latest
   from server" behavior — facts are cluster- and time-dependent and
   are not assumed to survive a version bump.)
4. Re-render into `<work-dir>/.upgrade/out`.
5. Plan against current ConfigHub state, print the diff. Done.

`upgrade` does not modify ConfigHub. To execute the upgrade, the
operator runs `installer upgrade-apply <work-dir>` (next section).
This is intentionally a separate command so the operator's workflow is
"plan, review, then apply" by default — the same shape as `terraform
plan` / `terraform apply`.

`<new-ref>` may be the same ref as the current install (no version
bump, just re-collect facts and re-render — useful when cluster state
has changed in a way the collector picks up) or a different ref (true
upgrade).

### `installer upgrade-apply <work-dir>`

Promotes the pending `.upgrade/` tree over the working tree and runs
`update`:

1. Refuse to run if `.upgrade/` is missing (no pending upgrade) or if
   the prior `installer upgrade` left the spec in an unsatisfied state
   (missing required inputs).
2. Atomic rename: `.upgrade/package` → `package` and `.upgrade/out` →
   `out`. The previous tree is moved to `.upgrade-prev/` and kept until
   the next successful `upgrade-apply` (one rollback step).
3. Run `installer update <work-dir>` to materialize the changes in
   ConfigHub (same ChangeSet semantics as a standalone `update`).

If the operator wants the one-shot path — pull, plan, apply — they can
run `installer upgrade <ref> && installer upgrade-apply <work-dir>`,
or use the convenience flag `installer upgrade <work-dir> <new-ref>
--apply` which composes them. The default is the two-step flow.

### Image overrides

Per [Principle 5](./principles.md#5-image-management-declare-a-kustomize-transformer-use-functions-when-changes-are-common),
the recommended path depends on how often the override is expected:

- **Occasional override (mirror, patch bump)** — package author declares
  a kustomize `images:` transformer in the chosen base. Operator passes
  `--set-image name=ref` (repeatable) to `installer plan`, `update`, or
  `upgrade`; the installer runs `kustomize edit set image` against the
  package's working copy before render. The `--set-image` value is
  recorded in `out/spec/inputs.yaml` under a reserved
  `_images:` map so the next `upgrade` carries it forward without the
  operator re-typing it (and so the override survives an `upgrade`
  re-render — see Principle 1: package files are read-only; we never
  rely on prior `kustomize edit` output surviving an `installer pull`).
- **Frequent / structured override** — package author declares an image
  input and a `set-container-image` group in `transformers`.
  Operator answers the input through the wizard. No `--set-image`
  involved; `installer upgrade` carries the value through the spec
  like any other input.
- **Post-install one-off** — operator runs `cub function do
  set-container-image` on the uploaded Unit. Survives re-render
  because `installer update` uses `--merge-external-source`.

`installer plan` and `update` print a per-Space `Images:` footer built
from `cub function do --space <slug> get-container-image '*'` so the
operator can see the eventual image set without applying anything.

`installer plan` against an `--set-image` value targeting a package
whose base has no `images:` transformer fails fast with a useful
message: "package's base kustomization.yaml has no `images:` block;
declare one to use --set-image, or use a function-chain input." This
keeps the package author's contract explicit.

## Common scenarios

- **Image tag bump** — the day-2 case:
  ```
  installer upgrade <work-dir> <same-ref> --set-image hello=hello:v2
  installer upgrade-apply <work-dir>
  ```
  Or, one-shot: `installer upgrade <work-dir> <same-ref> --set-image
  hello=hello:v2 --apply`. Plan shows a one-line image change.
- **Adding a component**:
  ```
  installer wizard <ref>          # "Re-use last choices?" → n
                                   # bump preset, or pick the new component
  installer plan <work-dir>        # preview the additions
  installer update <work-dir>      # materialize (in a ChangeSet)
  ```
- **Package version bump with new required input**:
  ```
  installer upgrade <work-dir> oci://reg/hello-app:0.2.0
  ```
  In interactive mode, prompts for the new required input. In
  non-interactive mode, fails fast and asks the operator to run
  `installer wizard <work-dir>` first; after the wizard captures the
  new input, `installer upgrade` re-runs cleanly. Plan shows the
  upgrade diff. `installer upgrade-apply` to execute.
- **Cluster state changed (collector picks up new fact)**:
  ```
  installer upgrade <work-dir> <same-ref>
  installer upgrade-apply <work-dir>
  ```
  Re-runs the collector, re-renders, plan shows what facts changed.
- **Reverting an `installer update`**:
  ```
  cub unit update --restore Before:ChangeSet:<slug> \
      --where "Labels.Component='hello-app'"
  ```
  Reverts updates only. Creates and deletes from that update are not
  reverted by ChangeSet restore (see [Update](#installer-update-work-dir)).

## Open questions

- ChangeSet for `installer upgrade-apply`: should it be the same
  ChangeSet `update` opens, or a distinct one with a clearer name? Lean
  toward the latter (`installer-upgrade-<from>-to-<to>-<timestamp>`) so
  upgrade reverts are visually distinct from routine updates in the
  ChangeSet list.
- The `_images:` reserved key in `inputs.yaml` (under
  [Image overrides](#image-overrides)): should this be a top-level
  Spec field on `Inputs` instead of a magic key in `Values`? Top-level
  is cleaner if more than one such reserved override category emerges
  (e.g., a `_resources:` for cpu/memory bumps).

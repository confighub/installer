# Package Consumer Guide

For operators who pull packages someone else has authored, install
them, and manage day-2 changes — image bumps, configuration tweaks,
package upgrades, reverts.

If you author packages, see [author-guide.md](./author-guide.md) +
[author-tutorial.md](./author-tutorial.md). The doctrine the
installer is anchored to lives in [principles.md](./principles.md).

## What the installer does

```
                             setup --pull            (setup auto-renders)
  oci://registry/pkg:1.2 ──────────────→ package/  +  out/{spec,manifests}/
                                                            │
                                                            │ upload
                                                            ▼
                                                       ConfigHub Spaces
                                                       (one Unit per file)
                                                            │
                                                            │ apply (cub-side)
                                                            ▼
                                                         Kubernetes
```

Day-2 commands operate on the same work-dir:

- `install setup` — re-runs wizard + render against the existing
  package, picking up edits to `out/spec/inputs.yaml` or a different
  pulled package version.
- `install plan` — show what's different between the work-dir and
  ConfigHub.
- `install upload` — reconcile the work-dir with ConfigHub. First
  upload creates Units; subsequent uploads open a ChangeSet and
  update/add/delete.

The installer never pushes to your cluster. Cluster apply is
ConfigHub's job (typically via `cub unit apply`, ArgoCD, or Flux —
configured separately).

## Find a package

Today, package discovery is convention-based — you find a package by
its OCI ref (`oci://host/repo:tag`). The installer repo ships two
packages under `packages/` as starting points:

- `packages/kubernetes-resources/` — eleven canonical Kubernetes
  resource templates with best-practice defaults pre-applied. Used
  by `install new` to scaffold resources into your own packages
  (see [author guide](./author-guide.md#kubernetes-resources-package)).
- `packages/worker/` — the ConfigHub bridge worker.

These will move to a separate registry as the catalog grows. Once
you have a candidate ref:

```bash
# What's in this artifact? Reads only the manifest + config blob,
# does not pull the layer.
install inspect oci://ghcr.io/myorg/statusboard:0.1.0

# What versions are available?
install list oci://ghcr.io/myorg/statusboard
```

For private registries, log in first:

```bash
install login ghcr.io
# uses ~/.docker/config.json; same auth as docker / podman
```

## Install: setup → upload

The end-to-end install is two commands. `setup` runs locally; `upload`
is the only command that talks to ConfigHub.

```bash
mkdir my-statusboard && cd my-statusboard

# 1. Pull the package, answer the wizard, and render.
install setup --pull oci://ghcr.io/myorg/statusboard:0.1.0 \
    --namespace statusboard

# Interactive prompts (skip with --non-interactive + flags):
#   Components: [minimal/default/all/selected]
#   Number of replicas:  [1]
#   ...
```

`setup` pulls the package into `./package/` and writes the wizard's
output to `./out/spec/`, then renders manifests to `./out/manifests/`.
If you prefer to script the wizard:

```bash
install setup \
    --pull oci://ghcr.io/myorg/statusboard:0.1.0 \
    --non-interactive \
    --namespace statusboard \
    --components default \
    --input replicas=3
```

The working directory defaults to `.`. To work in an explicit dir
instead, pass `--work-dir <dir>` (no `cd`-into-it needed):

```bash
install setup --pull oci://ghcr.io/myorg/statusboard:0.1.0 \
    --work-dir /tmp/statusboard --namespace statusboard
```

Inspect what was produced before pushing to ConfigHub:

```bash
ls out/manifests/
# deployment-statusboard-statusboard.yaml
# namespace-statusboard.yaml
# service-statusboard-statusboard.yaml
```

You can edit these files directly — the next `plan` / `upload` will
diff your edits against ConfigHub. But editing rendered output is
usually the wrong layer; prefer editing `out/spec/inputs.yaml` and
re-running `install setup`. See "Where to make changes" below.

Finally, upload to ConfigHub:

```bash
# 2. Upload: one Unit per file, plus an installer-record Unit
#    holding installer.yaml + spec/ docs.
install upload --space statusboard-prod
```

`install upload` records the destination Space (and your active
cub organization + server) into `./out/spec/upload.yaml` so all
subsequent commands re-enter without you re-typing.

For multi-package installs (a parent that declares dependencies),
use `--space-pattern` instead of `--space`:

```bash
install upload --space-pattern '{{.PackageName}}-prod'
# Each package — parent + each locked dep — gets its own Space.
```

If the package ships application-config files (a `configMapGenerator`
tagged with `installer.confighub.com/toolchain`, e.g.
`AppConfig/Properties` or `AppConfig/Env`), `install upload` also
creates a separate AppConfig Unit holding the raw config body, a
`render-configmap` Invocation, and a placeholder Kubernetes/YAML Unit
wired by an Upsert link that renders the ConfigMap into the
placeholder. No bridge worker is required — rendering runs as a
built-in function on the server.

## Where to make changes

There are three layers of override, in increasing flexibility and
decreasing reversibility. Use the lowest layer that fits.

### 1. Wizard inputs (install-time)

When you re-render with a different selection / inputs, the install
re-derives the manifests. This is the right layer for choices the
package author exposed as inputs: replica counts, names, tunable
behaviors. Edit `out/spec/inputs.yaml` (or re-run `setup`
interactively to walk every prompt with prior values pre-filled):

```bash
# Re-run setup. If a prior install is recorded, it loads those values
# and offers "Re-use last choices?" — answer no to walk every prompt
# with the prior values pre-filled.
install setup

# Or hand-edit and re-render via setup --non-interactive:
$EDITOR out/spec/inputs.yaml
install setup --non-interactive
```

Then `install plan` to see what the change would do, and
`install upload --yes` to apply it.

### 2. `--set-image` overrides (install-time, image-only)

The most common day-2 change is a container image tag bump (mirror,
patch release). If the package declares an `images:` block in its
chosen base, you can override at setup time without editing the
package source:

```bash
install setup --set-image myorg/statusboard=myorg/statusboard:1.2.4
install upload
```

The override is recorded in `out/spec/inputs.yaml` under
`spec.imageOverrides`, so subsequent setups carry it forward unless
you pass a different `--set-image` for the same name. If the package
doesn't declare an `images:` block, this fails fast with a message
naming the missing block.

### 3. Post-install ConfigHub mutations

Once Units are in ConfigHub, you can mutate them directly:

```bash
cub function do --space statusboard-prod set-container-image \
    deployment-statusboard-statusboard app myorg/statusboard:1.2.5
```

These edits survive re-render: `install upload` uses
`--merge-external-source`, which only writes paths that changed in
the new render. Your post-install ConfigHub edits are preserved.

This is the right layer for changes that don't warrant a re-render
— ad-hoc fixes, exploratory tuning, anything where you want the
change tracked in cub's revision history rather than your work-dir.

Post-install mutations can also be made with [kpt](https://kpt.dev)
instead of ConfigHub — a git-based configuration-as-data tool whose
package merge preserves your edits across re-renders the same way
`--merge-external-source` does. This is an alternative to `install
upload` + `cub unit apply` for kpt users, or for trying post-install
changes without ConfigHub first. See the [kpt guide](./kpt-guide.md).

### What NOT to do

- **Don't edit the package source tree** (`./package/`). The next
  `install setup --pull` overwrites it. If you find yourself
  running `kustomize edit` against `./package/...`, stop —
  use `--set-image` or post-install mutations instead. (See
  [Principle 1](./principles.md#1-package-files-are-read-only-to-consumers).)

## Day-2: plan, upload (reconcile), revert

### Plan

`install plan` is read-only. It shows three things per Space:

```
Plan: 1 to add, 2 to change, 0 to delete.

Space statusboard-prod:
  + ingress-tls-cert
  ~ deployment-statusboard-statusboard
      Resource: apps/v1/Deployment statusboard/statusboard
        ~ [Update] spec.replicas
          1 →     3
  ~ service-statusboard-statusboard
      ...

Images in statusboard-prod (post-render):
      Deployment/statusboard [app] myorg/statusboard:1.2.4
```

Plan computes the diff by listing existing Units (filtered by the
`Component=<package>` label) and dry-running a merge of each
rendered file against ConfigHub. Empty diff (after filtering
ConfigHub bookkeeping) means no change.

The `Images:` footer is built from the rendered manifests locally,
so it reflects what would land if you ran `upload` — independent
of whether plan shows other changes.

### Upload reconcile

`install upload` on an already-uploaded work-dir reconciles the
local render with ConfigHub. It re-runs the same plan and executes
it inside a ChangeSet:

```bash
install upload --yes
# == Space statusboard-prod (statusboard@0.1.0) ==
# ChangeSet: statusboard-prod/installer-update-20260514-…
# Successfully updated unit deployment-statusboard-statusboard …
#
# Applied: 0 created, 1 updated, 0 emptied.
# Updates revertable via:
#   cub unit update --patch --space statusboard-prod \
#       --restore Before:ChangeSet:installer-update-20260514-… \
#       --where "Slug IN ('deployment-statusboard-statusboard')"
```

The ChangeSet name is printed and the precise revert command is
written to stdout — copy/paste it later if you need to roll back.

`--yes` is required when stdin isn't a TTY and the plan empties Units
(those that dropped out of the rendered output); otherwise upload
prompts before emptying. Upload never runs `cub unit delete`: a Unit
that left the render is **emptied** rather than deleted. Emptying is a
`--merge-external-source` 3-way merge that drops only the installer-
contributed resources, so the Unit record, target binding, and any
post-install edits survive, and applying the emptied Unit later removes
its deployed resources. Units guarded by a DestroyGate are refused —
clear the gate first if you really intend to tear them down.

A re-run on a converged work-dir is a no-op (no ChangeSet opened):

```bash
install upload
# No changes.
```

### Revert

To revert a reconcile upload, run the printed `cub unit update --patch
--restore` command. **Note the ChangeSet revert scope:**

- Only **updates** are reverted by `--restore Before:ChangeSet:…`.
- **Creates** from that upload are not reverted automatically — to
  undo a create, delete the Unit (`cub unit delete --space S
  <slug>`).
- **Empties** from that upload (Units that dropped out of the render)
  are not part of the ChangeSet, but the prior Data is preserved in the
  Unit's revision history — restore it with `cub unit update --restore`
  against the pre-empty revision, or re-render and re-run `install
  upload` to repopulate it.

If you need to roll back a multi-Unit change, this is where having
the Component label pays off:

```bash
# Delete every Unit this package owns in this Space.
cub unit delete --space statusboard-prod \
    --where "Labels.Component='statusboard'"
# Then re-render + re-upload from the work-dir's prior state.
```

## Upgrade: re-pull, re-render, plan, upload

An upgrade is just `setup --pull <new-ref>` against an existing
work-dir. The same auto-detection that handles re-renders also
handles version bumps — `setup` notices the prior install state and
runs the schema-diff machinery (carry forward existing values, adopt
new defaults, drop removed inputs, prompt for new required-without-
default, etc.).

### Routine upgrade

```bash
install setup --pull oci://ghcr.io/myorg/statusboard:0.2.0
# Loaded prior install state from confighub.
# Adopted new default for input "metrics_port": 9090
# Adopted new default-flagged component(s): metrics-collector
# Wizard wrote out/spec/{selection,inputs}.yaml
# Rendered 4 manifest(s) to out/manifests/
# Next: install upload --work-dir … --space <slug>

install plan                         # preview
install upload --yes                 # apply
```

The pull is atomic — it stages into a sibling temp dir and renames
into `package/` on success. A failed pull leaves the prior `package/`
intact. If you want a record of the prior package source, commit
`package/` to git before pulling the new version.

For a one-shot upgrade + execute, chain:

```bash
install setup --pull oci://ghcr.io/myorg/statusboard:0.2.0 && \
    install upload --yes
```

### Image-only upgrade

A common case is "same package version, new image tag" — e.g., a
patch-level container bump:

```bash
install setup --set-image myorg/statusboard=myorg/statusboard:0.2.1
install upload --yes
```

(`--pull` is optional here — if you've already got the version
installed, just `--set-image` suffices.) Plan output should be a
one-line image change. The override is persisted; the next setup
without `--set-image` carries it forward.

### Schema-diff handling

When the new package's input schema differs from the old, setup
behaves as follows (no operator action needed in most cases):

- **New input with default**: silently adopted. Logged.
- **New required input without default**: prompted in interactive
  mode; in non-interactive mode, setup fails fast naming each
  missing input. Re-run setup interactively to answer them.
- **Removed input**: silently dropped from the new `inputs.yaml`.
- **Type-changed input**: setup errors. Re-run setup interactively
  to re-answer.

For components, similar rules: if your prior selection matched the
old package's `default` preset exactly, the upgrade adopts the new
package's default preset (so a newly-flagged `default: true`
component flows in automatically). Otherwise the prior list is
filtered to components that still exist.

### Re-collecting facts (collector packages)

If the package declares a collector, setup re-runs it on every
invocation. This is the right behavior when cluster state has
changed in a way the collector picks up (a worker was rotated, the
cub server moved, etc.):

```bash
install setup --pull oci://ghcr.io/myorg/statusboard:0.2.0
# even if 0.2.0 is what you already have — re-runs the collector.
```

## Granular commands

Most operators only need `setup` and `upload`. The granular commands
are available for step-by-step debugging or advanced workflows:

- `install pull <ref> --work-dir <dir>` — fetch only, no wizard.
  Writes to `<work-dir>/package/`.
- `install wizard <ref> --work-dir <dir> [--render=false]` — pull
  + Q&A. Renders by default; pass `--render=false` to skip.
- `install render --work-dir <dir>` — render only; reads existing
  `<work-dir>/package/` + `<work-dir>/out/spec/`.
- `install deps update --work-dir <dir>` — multi-package only:
  resolve the dependency DAG and write `out/spec/lock.yaml`. (`setup`
  runs this automatically before render.)

The semantics are equivalent: `setup --pull <ref>` is
`pull --work-dir <dir>` + `wizard --work-dir <dir>` + `render
--work-dir <dir>` (plus `deps update` for multi-package packages),
all sharing the same work-dir.

## Trust + signing

When a package author signs their releases, you can verify on every
pull. Two ways:

### One-off verification

```bash
install verify oci://ghcr.io/myorg/statusboard:0.1.0 --key cosign.pub
# or for keyless (Sigstore Fulcio + OIDC):
install verify oci://ghcr.io/myorg/statusboard:0.1.0 \
    --identity author@myorg.com --issuer https://accounts.google.com
```

### Enforced policy

Configure `~/.config/installer/policy.yaml` to require signatures on
every fetch. When the file exists, `install pull`, `installer
setup --pull`, and `install deps update` enforce verification
automatically.

```yaml
# ~/.config/installer/policy.yaml
apiVersion: installer.confighub.com/v1alpha1
kind: SigningPolicy
spec:
  enforce: true
  trustedKeys:
    - publicKey: |
        -----BEGIN PUBLIC KEY-----
        MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE...
        -----END PUBLIC KEY-----
      repos:                              # each entry is a prefix; empty list = all repos
        - oci://ghcr.io/myorg/
  trustedKeyless:
    - identity: author@myorg.com
      issuer: https://accounts.google.com
      repos:
        - oci://ghcr.io/myorg/
```

`enforce: true` makes verification mandatory. Setting `enforce:
false` keeps the policy advisory (pulls still succeed on mismatch
but log a warning).

## Multi-package installs

Some packages declare dependencies on other installer packages. The
flow is the same — `setup` runs `deps update` automatically before
render — but upload needs `--space-pattern` to give each dep its own
Space:

```bash
WD=stack-install
mkdir $WD && cd $WD
install setup --pull oci://ghcr.io/myorg/stack:1.0.0 --namespace stack

ls out/manifests/                # parent's manifests
ls out/<dep-name>/manifests/     # each dep's manifests

# Upload one Space per package.
install upload --space-pattern '{{.PackageName}}-prod'
```

Plan / upload work the same way — each operates across all locked
packages, opens one ChangeSet per Space when there are updates, and
prints a per-Space revert command.

`install deps tree` shows the resolved DAG if you want to audit
who depends on what.

## Re-entering an install from a fresh machine

The `out/spec/upload.yaml` file written by `install upload` is
what bootstraps everything. From a fresh clone of the work-dir, all
day-2 commands work because they read `upload.yaml` to find the
Spaces.

If the work-dir is genuinely lost (disk failure, lost laptop), the
package's `installer-record` Unit on ConfigHub holds the full spec
+ a copy of `upload.yaml`. Recover with:

```bash
mkdir recovered && cd recovered

# Pull the package source.
install pull oci://ghcr.io/myorg/statusboard:0.1.0

# Pull the installer-record Unit body and split it into spec docs.
mkdir -p out/spec
cub unit data --space statusboard-prod installer-record \
    > out/spec/installer-record.yaml
# (Splitting it back into selection.yaml / inputs.yaml / facts.yaml
# / upload.yaml is a manual step today; an `installer recover`
# command will automate this.)

install render
install plan       # should be No changes if cub is in sync
```

## Common errors

### `--set-image was given but bases/.../kustomization.yaml has no images: block`

The package author hasn't declared the image as overridable. Two
options: (1) ask the author to add an `images:` block declaring the
image you want to override; (2) make the change post-install via
`cub function do set-container-image` instead.

### `cub context organization mismatch: upload.yaml recorded org_… current cub context is org_…`

Your active cub context is signed into a different organization than
the one the install was uploaded to. Switch with `cub context set
<name>` or `cub auth login` against the recorded organization.

### `no package found in <work-dir>/package/ — pass --pull <ref> to fetch one`

You ran `install setup` (without `--pull`) in a work-dir that has
never been populated. Pass `--pull <ref>` to fetch the package, or
run `install pull <ref> --work-dir <dir>` first.

### `package declares dependencies but … lock.yaml does not exist`

You ran a granular command (`install render` / `install upload`)
before `install deps update` on a multi-package install. Run
`install deps update` first. `install setup` runs this
automatically.

### `the new package adds N required input(s) that the prior install did not answer`

A non-interactive `install setup --pull <new-ref>` ran against a
package version that adds new required inputs. Re-run setup
interactively to answer them.

### Non-existent revert: `change_set_id value does not match Unit ChangeSetID`

You're trying to update or restore a Unit that's currently locked
inside an open ChangeSet (typically a still-running `installer
upload` reconcile from another shell). Wait for it to finish, then
re-run.

### `cub unit data installer-record: … not found`

The installer-record Unit was deleted from cub, or the recorded
Space slug in `upload.yaml` is stale. Setup's prior-state load falls
back to local `out/spec/*.yaml` automatically with a warning. If you
want to refresh ConfigHub from local state, re-run `install upload
--space <slug>` against the same Space.

## Quick reference

| Task | Command |
|---|---|
| Discover what's in a registry | `install inspect <ref>` / `install list <repo>` |
| Pull a package locally | `install pull <ref> [--work-dir <dir>]` |
| Read the package's surface | `install doc <dir>` |
| Install (interactive) | `install setup --pull <ref> --namespace <ns>` |
| Install (scripted) | `install setup --pull <ref> --non-interactive --namespace <ns> --components default` |
| Re-render after editing inputs | `install setup --non-interactive` |
| Push to ConfigHub | `install upload --space <slug>` |
| Preview cub-side changes | `install plan` |
| Apply cub-side changes | `install upload --yes` |
| Bump an image | `install setup --set-image NAME=REF && install upload --yes` |
| Upgrade to a new version | `install setup --pull <new-ref> && install upload --yes` |
| Resolve deps (advanced) | `install deps update` (multi-package only; setup does this automatically) |
| Verify a signature | `install verify <ref> --key cosign.pub` |
| Make signature mandatory | edit `~/.config/installer/policy.yaml` |

## Where to go next

- [author-guide.md](./author-guide.md) — what package authors are
  responsible for. Reading it once will make the consumer side feel
  obvious.
- [principles.md](./principles.md) — the doctrine the installer is
  anchored to. Worth re-reading after your first few installs.

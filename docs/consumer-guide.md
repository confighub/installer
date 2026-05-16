# Package Consumer Guide

For operators who pull packages someone else has authored, install
them, and manage day-2 changes — image bumps, configuration tweaks,
package upgrades, reverts.

If you author packages, see [author-guide.md](./author-guide.md) +
[author-tutorial.md](./author-tutorial.md). The doctrine the
installer is anchored to lives in [principles.md](./principles.md).

## What the installer does

```
                             pull        wizard         render
  oci://registry/pkg:1.2 ──→ package/ ──→ spec/   ───→ manifests/
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

- `installer plan` — show what's different between the work-dir and
  ConfigHub.
- `installer update` — apply local changes to ConfigHub inside a
  ChangeSet.
- `installer upgrade` — re-pull a (new or same) package version,
  re-render, and stage the result. `installer upgrade-apply`
  promotes the staged tree and runs update.

The installer never pushes to your cluster. Cluster apply is
ConfigHub's job (typically via `cub unit apply`, ArgoCD, or Flux —
configured separately).

## Find a package

Today, package discovery is convention-based — you find a package by
its OCI ref (`oci://host/repo:tag`). The installer repo ships two
packages under `packages/` as starting points:

- `packages/kubernetes-resources/` — eleven canonical Kubernetes
  resource templates with best-practice defaults pre-applied. Used
  by `installer new` to scaffold resources into your own packages
  (see [author guide](./author-guide.md#kubernetes-resources-package)).
- `packages/worker/` — the ConfigHub bridge worker.

These will move to a separate registry as the catalog grows. Once
you have a candidate ref:

```bash
# What's in this artifact? Reads only the manifest + config blob,
# does not pull the layer.
installer inspect oci://ghcr.io/myorg/statusboard:0.1.0

# What versions are available?
installer list oci://ghcr.io/myorg/statusboard
```

For private registries, log in first:

```bash
installer login ghcr.io
# uses ~/.docker/config.json; same auth as docker / podman
```

Once you've decided to install, pull it locally:

```bash
installer pull oci://ghcr.io/myorg/statusboard:0.1.0 ./statusboard
installer doc ./statusboard
# Shows components, inputs, externalRequires, dependencies — the
# operator-facing surface of the package.
```

## Install: pull → wizard → render → upload

The end-to-end install is four commands. The first three operate
locally; only the fourth touches ConfigHub.

```bash
WD=/tmp/statusboard-install
mkdir -p $WD

# 1. Pull the package + answer the wizard.
installer wizard oci://ghcr.io/myorg/statusboard:0.1.0 \
  --work-dir $WD \
  --namespace statusboard

# Interactive prompts (skip with --non-interactive + flags):
#   Components: [minimal/default/all/selected]
#   Number of replicas:  [1]
#   ...
```

The wizard pulls the package into `$WD/package/` and writes its
output to `$WD/out/spec/`. If you prefer to script the wizard:

```bash
installer wizard oci://ghcr.io/myorg/statusboard:0.1.0 \
  --work-dir $WD --non-interactive \
  --namespace statusboard \
  --components default \
  --input replicas=3
```

Then render:

```bash
# 2. Render: kustomize build with the ConfigHub function chain wired in
#    as a kustomize transformer plugin → out/manifests/.
installer render $WD
```

Inspect what was produced before pushing to ConfigHub:

```bash
ls $WD/out/manifests/
# deployment-statusboard-statusboard.yaml
# namespace-statusboard.yaml
# service-statusboard-statusboard.yaml
```

You can edit these files directly — the next `plan` / `update` will
diff your edits against ConfigHub. But editing rendered output is
usually the wrong layer; prefer editing `out/spec/inputs.yaml` and
re-running `installer render`. See "Where to make changes" below.

Finally, upload to ConfigHub:

```bash
# 3. Upload: one Unit per file, plus an installer-record Unit
#    holding installer.yaml + spec/ docs.
installer upload $WD --space statusboard-prod
```

`installer upload` records the destination Space (and your active
cub organization + server) into `$WD/out/spec/upload.yaml` so
subsequent commands re-enter without you re-typing.

For multi-package installs (a parent that declares dependencies),
use `--space-pattern` instead of `--space`:

```bash
installer upload $WD --space-pattern '{{.PackageName}}-prod'
# Each package — parent + each locked dep — gets its own Space.
```

If the package ships application-config files (a `configMapGenerator`
tagged with `installer.confighub.com/toolchain`, e.g.
`AppConfig/Properties` or `AppConfig/Env`), `installer upload` also
creates a `ConfigMapRenderer` Target plus a separate AppConfig Unit
holding the raw config body. The renderer Target needs a worker; the
installer auto-creates a server-side worker named `renderer-worker` in
the destination Space (idempotent — re-uploading is safe). Override
with `installer upload $WD --space ... --appconfig-worker <slug>` if
you'd rather point at an existing worker.

## Where to make changes

There are three layers of override, in increasing flexibility and
decreasing reversibility. Use the lowest layer that fits.

### 1. Wizard inputs (install-time)

When you re-render with a different selection / inputs, the install
re-derives the manifests. This is the right layer for choices the
package author exposed as inputs: replica counts, names, tunable
behaviors. Edit `$WD/out/spec/inputs.yaml` and re-render, or re-run
the wizard:

```bash
# Re-run the wizard. If a prior install is recorded, it offers
# "Re-use last choices?" — answer no to walk every prompt with the
# prior values pre-filled.
installer wizard oci://ghcr.io/myorg/statusboard:0.1.0 --work-dir $WD

# Or hand-edit:
$EDITOR $WD/out/spec/inputs.yaml
installer render $WD
```

Then `installer plan $WD` to see what the change would do, and
`installer update $WD --yes` to apply it.

### 2. `--set-image` overrides (install-time, image-only)

The most common day-2 change is a container image tag bump (mirror,
patch release). If the package declares an `images:` block in its
chosen base, you can override at upgrade time without editing the
package source:

```bash
installer upgrade $WD oci://ghcr.io/myorg/statusboard:0.1.0 \
    --set-image myorg/statusboard=myorg/statusboard:1.2.4 \
    --apply
```

The override is recorded in `$WD/out/spec/inputs.yaml` under
`spec.imageOverrides`, so subsequent upgrades carry it forward unless
you pass a different `--set-image` for the same name. If the package
doesn't declare an `images:` block, this fails fast with a message
naming the missing block.

### 3. Post-install ConfigHub mutations

Once Units are in ConfigHub, you can mutate them directly:

```bash
cub function do --space statusboard-prod set-container-image \
    deployment-statusboard-statusboard app myorg/statusboard:1.2.5
```

These edits survive re-render: `installer update` uses
`--merge-external-source`, which only writes paths that changed in
the new render. Your post-install ConfigHub edits are preserved.

This is the right layer for changes that don't warrant a re-render
— ad-hoc fixes, exploratory tuning, anything where you want the
change tracked in cub's revision history rather than your work-dir.

### What NOT to do

- **Don't edit the package source tree** (`$WD/package/`). The next
  `installer pull` or `installer upgrade` overwrites it. If you find
  yourself running `kustomize edit` against `$WD/package/...`, stop —
  use `--set-image` or post-install mutations instead. (See
  [Principle 1](./principles.md#1-package-files-are-read-only-to-consumers).)

## Day-2: plan, update, revert

### Plan

`installer plan $WD` is read-only. It shows three things per Space:

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
so it reflects what would land if you ran update — independent of
whether plan shows other changes.

### Update

`installer update $WD` re-runs the same plan and executes it inside
a ChangeSet:

```bash
installer update $WD --yes
# == Space statusboard-prod (statusboard@0.1.0) ==
# ChangeSet: statusboard-prod/installer-update-20260514-…
# Successfully updated unit deployment-statusboard-statusboard …
#
# Applied: 0 created, 1 updated, 0 deleted.
# Updates revertable via:
#   cub unit update --patch --space statusboard-prod \
#       --restore Before:ChangeSet:installer-update-20260514-… \
#       --where "Slug IN ('deployment-statusboard-statusboard')"
```

The ChangeSet name is printed and the precise revert command is
written to stdout — copy/paste it later if you need to roll back.

`--yes` is required when stdin isn't a TTY and the plan contains
deletes; otherwise update prompts per delete.

A re-run on a converged work-dir is a no-op (no ChangeSet opened):

```bash
installer update $WD
# No changes.
```

### Revert

To revert an update, run the printed `cub unit update --patch
--restore` command. **Note the ChangeSet revert scope:**

- Only **updates** are reverted by `--restore Before:ChangeSet:…`.
- **Creates** from that update are not reverted automatically — to
  undo a create, delete the Unit (`cub unit delete --space S
  <slug>`).
- **Deletes** from that update are not reverted automatically —
  re-render and re-run `installer update` to re-create.

If you need to roll back a multi-Unit change, this is where having
the Component label pays off:

```bash
# Delete every Unit this package owns in this Space.
cub unit delete --space statusboard-prod \
    --where "Labels.Component='statusboard'"
# Then re-render + re-update from the work-dir's prior state.
```

## Upgrade: re-pull, re-render, plan, apply

`installer upgrade` stages a re-pull + re-render in `$WD/.upgrade/`
without touching the active install. `installer upgrade-apply`
atomically promotes the staged tree and runs update.

### Routine upgrade

```bash
installer upgrade $WD oci://ghcr.io/myorg/statusboard:0.2.0
# Staging upgrade in /tmp/.../.upgrade
# Prior install loaded from confighub.
# Wizard wrote .upgrade/out/spec/{selection,inputs}.yaml
# Adopted new default for input "metrics_port": 9090
# Adopted new default-flagged component(s): metrics-collector
# Plan: 0 to add, 2 to change, 0 to delete.
# ...
#
# Next: installer upgrade-apply /tmp/...   (or rerun with --apply)
```

The plan is printed; nothing in ConfigHub changed yet. Review, then
promote:

```bash
installer upgrade-apply $WD --yes
```

This atomically swaps `.upgrade/package` → `package` and
`.upgrade/out` → `out`, archives the prior tree to `.upgrade-prev/`
(kept for one rollback step), then runs update with a distinctive
ChangeSet slug `installer-upgrade-<from>-to-<to>-<timestamp>`.

For a one-shot upgrade + execute, chain with `--apply`:

```bash
installer upgrade $WD oci://ghcr.io/myorg/statusboard:0.2.0 --apply --yes
```

### Image-only upgrade

A common case is "same package version, new image tag" — e.g., a
patch-level container bump:

```bash
installer upgrade $WD oci://ghcr.io/myorg/statusboard:0.2.0 \
    --set-image myorg/statusboard=myorg/statusboard:0.2.1 \
    --apply
```

Plan output should be a one-line image change. The override is
persisted; the next upgrade without `--set-image` carries it
forward.

### Schema-diff handling

When the new package's input schema differs from the old, upgrade
behaves as follows (no operator action needed in most cases):

- **New input with default**: silently adopted. Logged.
- **New required input without default**: prompted in interactive
  mode; in non-interactive mode, upgrade fails fast naming each
  missing input. Run `installer wizard $WD <new-ref>` interactively
  to answer them, then re-run upgrade.
- **Removed input**: silently dropped from the new `inputs.yaml`.
- **Type-changed input**: upgrade errors. Re-run `installer wizard`
  to re-answer.

For components, similar rules: if your prior selection matched the
old package's `default` preset exactly, the upgrade adopts the new
package's default preset (so a newly-flagged `default: true`
component flows in automatically). Otherwise the prior list is
filtered to components that still exist.

### Re-collecting facts (collector packages)

If the package declares a collector, upgrade re-runs it. This is the
right behavior when cluster state has changed in a way the
collector picks up (a worker was rotated, the cub server moved,
etc.) — even with the same `<ref>`:

```bash
installer upgrade $WD oci://ghcr.io/myorg/statusboard:0.2.0
# even if 0.2.0 is what you already have — re-runs the collector.
```

## Trust + signing

When a package author signs their releases, you can verify on every
pull. Two ways:

### One-off verification

```bash
installer verify oci://ghcr.io/myorg/statusboard:0.1.0 --key cosign.pub
# or for keyless (Sigstore Fulcio + OIDC):
installer verify oci://ghcr.io/myorg/statusboard:0.1.0 \
    --identity author@myorg.com --issuer https://accounts.google.com
```

### Enforced policy

Configure `~/.config/installer/policy.yaml` to require signatures on
every fetch. When the file exists, `installer pull` and `installer
deps update` enforce verification automatically.

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
flow gets one extra step (`deps update`):

```bash
WD=/tmp/stack-install
installer wizard oci://ghcr.io/myorg/stack:1.0.0 --work-dir $WD --namespace stack

# Resolve the dependency DAG — writes out/spec/lock.yaml pinning
# each dep to a manifest digest.
installer deps update $WD

# Render parent + each dep into its own subtree.
installer render $WD
ls $WD/out/manifests/         # parent's manifests
ls $WD/out/<dep-name>/manifests/  # each dep's manifests

# Upload one Space per package.
installer upload $WD --space-pattern '{{.PackageName}}-prod'
```

Plan / update / upgrade work the same way — each operates across
all locked packages, opens one ChangeSet per Space when there are
updates, and prints a per-Space revert command.

`installer deps tree $WD` shows the resolved DAG if you want to
audit who depends on what.

## Re-entering an install from a fresh machine

The `out/spec/upload.yaml` file written by `installer upload` is
what bootstraps everything. From a fresh clone of the work-dir, all
day-2 commands work because they read `upload.yaml` to find the
Spaces.

If the work-dir is genuinely lost (disk failure, lost laptop), the
package's `installer-record` Unit on ConfigHub holds the full spec
+ a copy of `upload.yaml`. Recover with:

```bash
WD=/tmp/recovered
mkdir -p $WD
# Pull the package source.
installer pull oci://ghcr.io/myorg/statusboard:0.1.0 $WD/package

# Pull the installer-record Unit body and split it into spec docs.
mkdir -p $WD/out/spec
cub unit data --space statusboard-prod installer-record \
    > $WD/out/spec/installer-record.yaml
# (Splitting it back into selection.yaml / inputs.yaml / facts.yaml
# / upload.yaml is a manual step today; an `installer recover`
# command will automate this.)

installer render $WD
installer plan $WD       # should be No changes if cub is in sync
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

### `nothing to apply: …/.upgrade missing package and/or out`

You ran `installer upgrade-apply` without first staging an upgrade.
Run `installer upgrade $WD <ref>` first, then upgrade-apply.

### `package declares dependencies but … lock.yaml does not exist`

You ran `installer render` before `installer deps update` on a
multi-package install. Run `installer deps update $WD` first.

### `the new package adds N required input(s) that the prior install did not answer`

A non-interactive `installer upgrade` ran against a package version
that adds new required inputs. Run `installer wizard $WD <new-ref>`
interactively to answer them, then re-run upgrade.

### Non-existent revert: `change_set_id value does not match Unit ChangeSetID`

You're trying to update or restore a Unit that's currently locked
inside an open ChangeSet (typically a still-running `installer
update` from another shell). Wait for it to finish, then re-run.

### `cub unit data installer-record: … not found`

The installer-record Unit was deleted from cub, or the recorded
Space slug in `upload.yaml` is stale. The wizard's prior-state load
falls back to local `out/spec/*.yaml` automatically with a warning.
If you want to refresh ConfigHub from local state, re-run
`installer upload $WD --space <slug>` against the same Space.

## Quick reference

| Task | Command |
|---|---|
| Discover what's in a registry | `installer inspect <ref>` / `installer list <repo>` |
| Pull a package locally | `installer pull <ref> <dir>` |
| Read the package's surface | `installer doc <dir>` |
| Install (interactive) | `installer wizard <ref> --work-dir $WD --namespace <ns>` |
| Install (scripted) | `installer wizard <ref> --work-dir $WD --non-interactive --namespace <ns> --components default` |
| Render after editing inputs | `installer render $WD` |
| Push to ConfigHub | `installer upload $WD --space <slug>` |
| Preview cub-side changes | `installer plan $WD` |
| Apply cub-side changes | `installer update $WD --yes` |
| Bump an image | `installer upgrade $WD <same-ref> --set-image NAME=REF --apply` |
| Upgrade to a new version | `installer upgrade $WD <new-ref>` then `installer upgrade-apply $WD` |
| One-shot upgrade | `installer upgrade $WD <new-ref> --apply` |
| Resolve deps | `installer deps update $WD` (multi-package only) |
| Verify a signature | `installer verify <ref> --key cosign.pub` |
| Make signature mandatory | edit `~/.config/installer/policy.yaml` |

## Where to go next

- [author-guide.md](./author-guide.md) — what package authors are
  responsible for. Reading it once will make the consumer side feel
  obvious.
- [principles.md](./principles.md) — the doctrine the installer is
  anchored to. Worth re-reading after your first few installs.

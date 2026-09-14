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

- `installer setup` — re-runs wizard + render against the existing
  package, picking up edits to `out/record/inputs.yaml` or a different
  pulled package version.
- `installer plan` — show what `installer upload` would change in
  ConfigHub.
- `installer upload` — make ConfigHub match the work-dir. Every upload is
  create-or-update: new resources become Units, changed ones are merged,
  and ones the render dropped are emptied.

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

## Install: setup → upload

The end-to-end install is two commands. `setup` runs locally; `upload`
is the only command that talks to ConfigHub.

```bash
mkdir my-statusboard && cd my-statusboard

# 1. Pull the package, answer the wizard, and render.
installer setup --pull oci://ghcr.io/myorg/statusboard:0.1.0 \
    --namespace statusboard

# Interactive prompts (skip with --non-interactive + flags):
#   Components: [minimal/default/all/selected]
#   Number of replicas:  [1]
#   ...
```

`setup` pulls the package into `./package/` and writes the wizard's
output to `./out/record/`, then renders manifests to `./out/manifests/`.
If you prefer to script the wizard:

```bash
installer setup \
    --pull oci://ghcr.io/myorg/statusboard:0.1.0 \
    --non-interactive \
    --namespace statusboard \
    --components default \
    --input replicas=3
```

The working directory defaults to `.`. To work in an explicit dir
instead, pass `--work-dir <dir>` (no `cd`-into-it needed):

```bash
installer setup --pull oci://ghcr.io/myorg/statusboard:0.1.0 \
    --work-dir /tmp/statusboard --namespace statusboard
```

Inspect what was produced before pushing to ConfigHub:

```bash
ls out/manifests/
# deployment-statusboard-statusboard.yaml
# namespace-statusboard.yaml
# service-statusboard-statusboard.yaml
```

You can also make an OCI artifact from those exact files without a
ConfigHub account. A local path writes an OCI image layout:

```bash
installer setup \
    --pull oci://ghcr.io/myorg/statusboard:0.1.0 \
    --namespace statusboard \
    --output-oci ./statusboard-rendered.oci
```

An OCI reference pushes the rendered artifact to a registry:

```bash
installer setup \
    --pull oci://ghcr.io/myorg/statusboard:0.1.0 \
    --namespace statusboard \
    --output-oci oci://ghcr.io/myorg/statusboard-rendered:0.1.0
```

The OCI layer contains the non-secret Kubernetes files and a root
`kustomization.yaml`, so Argo CD, Flux, or another OCI-aware tool can
consume the same files you inspected. The OCI config records the source
reference and digest, chosen base, namespace, input and function-chain
digests, validator results, and the rendered object-set digest. Input
values and files under `out/secrets/` are not published. After writing
or pushing the artifact, the installer reads it back and compares the
recorded object-set digest before reporting success.

You can edit these files directly — the next `plan` / `upload` will
diff your edits against ConfigHub. But editing rendered output is
usually the wrong layer; prefer editing `out/record/inputs.yaml` and
re-running `installer setup`. See "Where to make changes" below.

Finally, upload to ConfigHub:

```bash
# 2. Upload: one Unit per resource, plus an "installer" record Unit
#    holding installer.yaml and the record of the render.
installer upload --space statusboard-prod
```

Nothing local records the Space, so pass the same `--space` (or
`--space-pattern`) to every later `upload` and `plan`. The Space is
labeled `Component` (the package name, or `--component`), `Variant`
(`base`, or `--variant`), and `Namespace` (the install namespace).

For multi-package installs (a parent that declares dependencies),
use `--space-pattern` instead of `--space`:

```bash
installer upload --space-pattern '{{.PackageName}}-prod'
# Each package — parent + each locked dep — gets its own Space.
```

If the package ships application-config files (a `configMapGenerator`
tagged with `installer.confighub.com/toolchain`, e.g.
`AppConfig/Properties` or `AppConfig/Env`), the upload also creates a
separate AppConfig Unit holding the raw config body, a
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
behaviors. Edit `out/record/inputs.yaml` (or re-run `setup`
interactively to walk every prompt with prior values pre-filled):

```bash
# Re-run setup. If a prior install is recorded, it loads those values
# and offers "Re-use last choices?" — answer no to walk every prompt
# with the prior values pre-filled.
installer setup

# Or hand-edit and re-render via setup --non-interactive:
$EDITOR out/record/inputs.yaml
installer setup --non-interactive
```

Then `installer plan` to see what the change would do, and
`installer upload --yes` to apply it.

### 2. `--set-image` overrides (install-time, image-only)

The most common day-2 change is a container image tag bump (mirror,
patch release). If the package declares an `images:` block in its
chosen base, you can override at setup time without editing the
package source:

```bash
installer setup --set-image myorg/statusboard=myorg/statusboard:1.2.4
installer upload
```

The override is recorded in `out/record/inputs.yaml` under
`spec.imageOverrides`, so subsequent setups carry it forward unless
you pass a different `--set-image` for the same name. If the package
doesn't declare an `images:` block, this fails fast with a message
naming the missing block.

### 3. Post-install ConfigHub mutations

Once Units are in ConfigHub, you can mutate them directly:

```bash
cub function do --space statusboard-prod --unit statusboard \
    set-container-image app myorg/statusboard:1.2.5
```

These edits survive re-render: `installer upload` 3-way merges the new
render into each Unit, so it only writes the paths the render changed.
To hold an edit even against a render that changes the same path, make it
with `--protect`.

This is the right layer for changes that don't warrant a re-render
— ad-hoc fixes, exploratory tuning, anything where you want the
change tracked in cub's revision history rather than your work-dir.

Post-install mutations can also be made with [kpt](https://kpt.dev)
instead of ConfigHub — a git-based configuration-as-data tool whose
package merge preserves your edits across re-renders the same way
upload's 3-way merge does. This is an alternative to `installer
upload` + `cub unit apply` for kpt users, or for trying post-install
changes without ConfigHub first. See the [kpt guide](./kpt-guide.md).

### What NOT to do

- **Don't edit the package source tree** (`./package/`). The next
  `installer setup --pull` overwrites it. If you find yourself
  running `kustomize edit` against `./package/...`, stop —
  use `--set-image` or post-install mutations instead. (See
  [Principle 1](./principles.md#1-package-files-are-read-only-to-consumers).)

## Day-2: plan, upload, revert

### Plan

`installer plan` runs the requests `installer upload` would send, as dry
runs, and writes nothing. It takes upload's flags, since they decide the
Spaces:

```
installer plan --space statusboard-prod
Space statusboard-prod (Update)
  Create    ingress-tls-cert-certificate
  Update    statusboard
  Unchanged 5 Unit(s)
  Create    link ingress-tls-cert-certificate -> namespace (reference:v1/Namespace)

Images in statusboard-prod (post-render):
      Deployment/statusboard [app] myorg/statusboard:1.2.4

Plan: 1 to create, 1 to update, 0 to empty, 0 to revive, 0 to adopt.
```

ConfigHub computes the plan with the same code an upload runs, so an
upload with the same flags does what plan shows. The `Images:` footer is
built from the rendered manifests locally, so it reflects what would land
whether or not anything else changes.

### Upload

Every upload is create-or-update, so there is no separate day-2 command:

```bash
installer upload --space statusboard-prod
# == statusboard@0.1.0 → Space statusboard-prod ==
# Space statusboard-prod (Update)
#   Update    statusboard
#   Unchanged 5 Unit(s)
#
# Revert this upload of statusboard-prod with:
#   cub unit update --patch --space statusboard-prod --restore Before:ChangeSet:upload-20260514-… --where "Labels.UploadSource = 'statusboard'"
#
# Applied: 0 created, 1 updated, 0 emptied, 0 revived, 0 adopted.
```

- **Changed resources are merged.** A change you made in ConfigHub after
  the upload survives unless the render changes the same path; protect the
  path (`cub function do --protect …`) to hold it even then.
- **Dropped resources are emptied, never deleted.** A Unit whose resource
  left the render keeps its ID, links, and history, and the next release
  withdraws the object. Upload lists the Units it would empty and asks
  first; pass `--yes` when there is no terminal to ask on. A Unit guarded
  by a DestroyGate is never emptied — the upload is refused.
- **Ownership.** Every Unit an upload writes is labeled
  `UploadSource=<package>`, and an upload only writes or empties what its
  package owns, so Units you add to the Space by hand are left alone.

A re-run on an unchanged work-dir writes nothing:

```bash
installer upload --space statusboard-prod
# ...
# No changes.
```

### Revert

Run the command upload printed. ConfigHub records each Space's writes in
a ChangeSet — creates, updates, and empties alike — so restoring the
package's Units to before that ChangeSet empties the Units the upload
created and reverts the rest. Re-render and upload again to move forward
from there.

## Upgrade: re-pull, re-render, plan, upload

An upgrade is just `setup --pull <new-ref>` against an existing
work-dir. The same auto-detection that handles re-renders also
handles version bumps — `setup` notices the prior install state and
runs the schema-diff machinery (carry forward existing values, adopt
new defaults, drop removed inputs, prompt for new required-without-
default, etc.).

### Routine upgrade

```bash
installer setup --pull oci://ghcr.io/myorg/statusboard:0.2.0
# Loaded prior install state from confighub.
# Adopted new default for input "metrics_port": 9090
# Adopted new default-flagged component(s): metrics-collector
# Wizard wrote out/record/{selection,inputs}.yaml
# Rendered 4 manifest(s) to out/manifests/
# Next: installer upload --work-dir … --space <slug>

installer plan                         # preview
installer upload --yes                 # apply
```

The pull is atomic — it stages into a sibling temp dir and renames
into `package/` on success. A failed pull leaves the prior `package/`
intact. If you want a record of the prior package source, commit
`package/` to git before pulling the new version.

For a one-shot upgrade + execute, chain:

```bash
installer setup --pull oci://ghcr.io/myorg/statusboard:0.2.0 && \
    installer upload --yes
```

### Image-only upgrade

A common case is "same package version, new image tag" — e.g., a
patch-level container bump:

```bash
installer setup --set-image myorg/statusboard=myorg/statusboard:0.2.1
installer upload --yes
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
installer setup --pull oci://ghcr.io/myorg/statusboard:0.2.0
# even if 0.2.0 is what you already have — re-runs the collector.
```

## Granular commands

Most operators only need `setup` and `upload`. The granular commands
are available for step-by-step debugging or advanced workflows:

- `installer pull <ref> --work-dir <dir>` — fetch only, no wizard.
  Writes to `<work-dir>/package/`.
- `installer wizard <ref> --work-dir <dir> [--render=false]` — pull
  + Q&A. Renders by default; pass `--render=false` to skip.
- `installer render --work-dir <dir>` — render only; reads existing
  `<work-dir>/package/` + `<work-dir>/out/record/`.
- `installer deps update --work-dir <dir>` — multi-package only:
  resolve the dependency DAG and write `out/record/lock.yaml`. (`setup`
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
installer verify oci://ghcr.io/myorg/statusboard:0.1.0 --key cosign.pub
# or for keyless (Sigstore Fulcio + OIDC):
installer verify oci://ghcr.io/myorg/statusboard:0.1.0 \
    --identity author@myorg.com --issuer https://accounts.google.com
```

### Enforced policy

Configure `~/.config/installer/policy.yaml` to require signatures on
every fetch. When the file exists, `installer pull`, `installer
setup --pull`, and `installer deps update` enforce verification
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
installer setup --pull oci://ghcr.io/myorg/stack:1.0.0 --namespace stack

ls out/manifests/                # parent's manifests
ls out/<dep-name>/manifests/     # each dep's manifests

# Upload one Space per package.
installer upload --space-pattern '{{.PackageName}}-prod'
```

Plan and upload send one request per package, parent first, with the
same `--space-pattern` each time. Each dependency's Space is labeled with
its own package name as `Component`, and the parent's Space lists them in
its `DependsOn` annotation. Upload prints a revert command per Space it
writes to; `--changeset <ref>` puts every package's writes in one existing
ChangeSet instead, so one restore reverts the whole install.

`installer deps tree` shows the resolved DAG if you want to audit
who depends on what.

## Re-entering an install from a fresh machine

Everything `setup` needs to re-enter an install is in the work-dir's
`out/record/`. If the work-dir is lost, each package's `installer` record
Unit in ConfigHub holds the same thing, and `setup` recovers from it when
the work-dir has no `out/record/` of its own:

```bash
mkdir recovered && cd recovered
installer setup --pull oci://ghcr.io/myorg/statusboard:0.1.0 --space statusboard-prod
# Loaded prior install state from confighub.
installer plan --space statusboard-prod     # No changes if nothing drifted
```

`--space` names the install to recover. Without it, setup looks for the
package's install across the organization, uses it if there is exactly
one, and refuses with a list of the Spaces if there are several.

## Common errors

### `--set-image was given but bases/.../kustomization.yaml has no images: block`

The package author hasn't declared the image as overridable. Two
options: (1) ask the author to add an `images:` block declaring the
image you want to override; (2) make the change post-install via
`cub function do set-container-image` instead.

### `no package found in <work-dir>/package/ — pass --pull <ref> to fetch one`

You ran `installer setup` (without `--pull`) in a work-dir that has
never been populated. Pass `--pull <ref>` to fetch the package, or
run `installer pull <ref> --work-dir <dir>` first.

### `package declares dependencies but … lock.yaml does not exist`

You ran a granular command (`installer render` / `installer upload`)
before `installer deps update` on a multi-package install. Run
`installer deps update` first. `installer setup` runs this
automatically.

### `the new package adds N required input(s) that the prior install did not answer`

A non-interactive `installer setup --pull <new-ref>` ran against a
package version that adds new required inputs. Re-run setup
interactively to answer them.

### `change_set_id value does not match Unit ChangeSetID`

You're trying to update or restore a Unit that's currently in an open
ChangeSet (typically an `installer upload` still running from another
shell). Wait for it to finish, then re-run.

### `<package> is installed in N Spaces (…); pass --space to choose one`

`setup` in a work-dir with no `out/record/` looked for the package's
installer record in ConfigHub and found more than one install. Pass
`--space` to name the one to recover. Setup warns and starts a fresh
install until you do.

### `refusing to empty N Unit(s) without a terminal to confirm on; pass --yes`

The render no longer produces some resources, so the upload would empty
their Units, and it had no terminal to ask on. Check the listed Units and
pass `--yes`.

## Quick reference

| Task | Command |
|---|---|
| Discover what's in a registry | `installer inspect <ref>` / `installer list <repo>` |
| Pull a package locally | `installer pull <ref> [--work-dir <dir>]` |
| Read the package's surface | `installer doc <dir>` |
| Install (interactive) | `installer setup --pull <ref> --namespace <ns>` |
| Install (scripted) | `installer setup --pull <ref> --non-interactive --namespace <ns> --components default` |
| Render to a local OCI layout | `installer setup --pull <ref> --namespace <ns> --output-oci ./rendered.oci` |
| Render and push OCI | `installer setup --pull <ref> --namespace <ns> --output-oci oci://host/repo:tag` |
| Re-render after editing inputs | `installer setup --non-interactive` |
| Push to ConfigHub | `installer upload --space <slug>` |
| Preview cub-side changes | `installer plan` |
| Apply cub-side changes | `installer upload --yes` |
| Bump an image | `installer setup --set-image NAME=REF && installer upload --yes` |
| Upgrade to a new version | `installer setup --pull <new-ref> && installer upload --yes` |
| Resolve deps (advanced) | `installer deps update` (multi-package only; setup does this automatically) |
| Verify a signature | `installer verify <ref> --key cosign.pub` |
| Make signature mandatory | edit `~/.config/installer/policy.yaml` |

## Where to go next

- [author-guide.md](./author-guide.md) — what package authors are
  responsible for. Reading it once will make the consumer side feel
  obvious.
- [principles.md](./principles.md) — the doctrine the installer is
  anchored to. Worth re-reading after your first few installs.

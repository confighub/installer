# Package Author Guide

Reference for authoring an installer package — file layout, the
`installer.yaml` schema, what each declaration becomes at install time,
publishing, signing, and version-to-version evolution.

If you're new to the installer, read [the consumer
guide](./consumer-guide.md) first to understand what an operator does
with your package, then come back here. For a hands-on walkthrough of
authoring a small package from scratch, see [the author
tutorial](./author-tutorial.md).

> **Note.** This guide describes _what_ to put in a package and _why_.
> The doctrine the installer is anchored to lives in
> [docs/principles.md](./principles.md). When in doubt, that's the
> tiebreaker.

## Mental model

A package is a kustomize tree wrapped with an `installer.yaml`
manifest. The manifest declares:

- **Bases** — alternative top-level kustomize trees the operator
  picks one of.
- **Components** — opt-in kustomize Components the operator may
  layer on.
- **Inputs** — wizard prompts the operator answers.
- **Function chain template** — the resolved-at-install-time chain of
  ConfigHub functions that mutate the kustomize output before it's
  written.
- **Dependencies** — other installer packages this package composes
  with.
- **External requirements** — cluster preconditions the operator
  must satisfy independently (CRDs, GatewayClass, etc.).

The installer pulls your package into the operator's work-dir, the
wizard collects answers, render produces literal Kubernetes YAML,
upload writes the YAML to ConfigHub as Units, and day-2 commands
(plan / update / upgrade) drive the install over time. None of those
steps re-templates your package: the rendered YAML is the source of
truth from upload onward.

## File organization

Minimum:

```
my-package/
├── installer.yaml
└── bases/
    └── default/
        ├── kustomization.yaml
        └── ...resources...
```

A typical package with optional components, validation, and a
collector:

```
my-package/
├── installer.yaml                 # the manifest (this guide)
├── bases/
│   ├── default/                   # primary base
│   │   ├── kustomization.yaml
│   │   ├── deployment.yaml
│   │   └── service.yaml
│   └── alt/                       # an alternative shape
│       └── kustomization.yaml
├── components/
│   ├── monitoring/                # opt-in: add ServiceMonitor
│   │   ├── kustomization.yaml     # kind: Component
│   │   └── servicemonitor.yaml
│   └── ingress/
│       ├── kustomization.yaml
│       └── ingress.yaml
├── validation/                    # optional: bundled docs about
│   ├── env.schema.json            # configurable env vars / flags /
│   ├── command.yaml               # runtime expectations. Surfaced
│   └── runtime.yaml               # by `installer doc`.
└── collector/                     # optional: install-time fact
    └── collect.sh                 # discovery script (see Collector)
```

Paths in `installer.yaml`'s `bases[].path` and `components[].path` are
relative to the package root and must point at directories containing
a `kustomization.yaml`. Components' `kustomization.yaml` must use
`kind: Component` (kustomize Components, not Kustomizations).

## installer.yaml — the manifest

Every package declares one document of:

```yaml
apiVersion: installer.confighub.com/v1alpha1
kind: Package
metadata:
  name: my-package
  version: 0.1.0          # SemVer
  kubeVersion: ">= 1.28"  # SemVer range; empty means unconstrained
  installerVersion: ">= 0.2.0"
spec:
  ...
```

`metadata.name` and `metadata.version` are required. The two
`*Version` fields constrain what cluster Kubernetes / installer
versions the package supports; mismatches are an error at resolve
and render time.

The rest of this section walks `spec.*` field by field.

### `spec.bases` (required)

Alternative top-level kustomize trees. Exactly one is selected at
install time (the wizard's `--base` flag, or the `default: true` base
when the operator doesn't pick).

```yaml
spec:
  bases:
    - name: default
      path: bases/default
      default: true
      description: A minimal app — Namespace, Deployment, Service.
    - name: alt
      path: bases/alt
      description: An alternative deployment shape.
      externalRequires:
        - kind: GatewayClass
          capability: ext-proc
```

Use multiple bases when your package supports orthogonal deployment
shapes that cannot be expressed as opt-in Components — e.g., KServe
Knative vs Raw, llm-d colocated vs P/D-disaggregated.

Only one base may set `default: true`. Each base may declare its own
`externalRequires` in addition to the package-level requires.

### `spec.components`

Opt-in kustomize Components (the kustomize `kind: Component`) layered
on top of the chosen base. The wizard's preset prompt
(`minimal` / `default` / `all` / `selected`) is the most common way
operators pick components.

```yaml
spec:
  components:
    - name: monitoring
      path: components/monitoring
      description: Adds a ServiceMonitor for Prometheus scraping.
      default: true                  # picked by the `default` preset
      externalRequires:
        - kind: CRD
          name: servicemonitors.monitoring.coreos.com
          suggestedSource: oci://ghcr.io/.../kube-prometheus-stack
    - name: ingress
      path: components/ingress
    - name: ingress-tls
      path: components/ingress-tls
      requires: [ingress]            # auto-pulled in if ingress is selected
      conflicts: [no-tls]            # cannot coexist
      validForBases: [default]       # only valid against this base
```

- `default: true` makes a component part of the `default` preset.
  Components without `default` are only installed under the `all`
  preset or via explicit `--select <name>`.
- `requires:` is closure-resolved by the wizard's solver. Selecting
  `ingress-tls` automatically pulls in `ingress`.
- `conflicts:` is a hard error at solve time.
- `validForBases:` constrains which base the component can layer on.
  Empty means valid for all bases.
- `externalRequires:` declares this component's preconditions, in
  addition to package-level + base-level requires.

### `spec.inputs`

Wizard prompts. Referenced in the function-chain template as
`{{ .Inputs.<name> }}`.

```yaml
spec:
  inputs:
    - name: replicas
      type: int
      default: 2
      prompt: "How many replicas?"
      description: "Number of pod replicas the Deployment will run."
    - name: license
      type: string
      required: true
      prompt: "License key"
    - name: tier
      type: enum
      options: [basic, pro, enterprise]
      default: basic
    - name: hostnames
      type: list
      default: []
    - name: enable_tls
      type: bool
      default: false
    - name: cert_issuer
      type: string
      whenExternalRequire: WebhookCertProvider
      default: letsencrypt-prod
```

Field reference:

- `name` — variable name in chain templates. Must be a valid
  identifier.
- `type` — `string` (default), `int`, `bool`, `enum`, `list`.
- `default` — used when the operator doesn't supply a value. Must
  match `type`.
- `required` — fails if missing and no default.
- `prompt` — the human-readable question shown by the interactive
  wizard.
- `description` — longer help text.
- `options` — required when `type: enum`.
- `whenExternalRequire` — only prompt when the package declares an
  ExternalRequire of this kind. Useful for inputs that only make
  sense when a particular requirement applies (e.g., `cert_issuer`
  only matters if your package needs a `WebhookCertProvider`).

**Best practice**: declare a `default:` whenever you can name a
reasonable one. Required inputs without defaults should be rare —
[Principle 4](./principles.md#4-optimize-for-the-zero-override-case)
is "optimize for the zero-override case." If you have ten required
inputs, your package needs a sensible default profile, not ten more
prompts.

### `spec.functionChainTemplate`

A list of function-invocation groups. At render time the template is
resolved against the operator's answers (via Go `text/template`,
with `.Inputs`, `.Selection`, `.Package`, `.Namespace`, `.Facts` in
scope), then each group is executed by the ConfigHub function
executor SDK. The output of each invocation feeds the next
invocation; the output of each group feeds the next group.

```yaml
spec:
  functionChainTemplate:
    - toolchain: Kubernetes/YAML        # required per group
      whereResource: ""                 # optional; empty = all resources
      description: Set the namespace on every namespaced resource.
      invocations:
        - name: set-namespace
          args: ["{{ .Namespace }}"]
        - name: set-replicas
          args: ["{{ .Inputs.replicas }}"]
        - name: set-container-image
          args: ["app", "{{ .Inputs.image }}"]
```

- `toolchain` — `Kubernetes/YAML`, `AppConfig/Properties`, etc.
  Each group declares its own toolchain so a single chain can mutate
  raw Kubernetes YAML and AppConfig units in the same render.
- `whereResource` — a function-executor filter expression scoping
  this group to a subset of resources. Empty = all.
- `invocations` — ordered. Each invocation names a function and its
  string args (templated).

Common functions: `set-namespace`, `set-name`, `set-replicas`,
`set-container-image`, `set-container-image-reference`,
`set-container-resources`, `set-env`, `set-hostname`, `yq-i`,
`set-string-path`, `delete-path`. The full list is in `cub function
list` against a running ConfigHub.

**When to use the function chain vs kustomize itself**: kustomize
handles structural composition (resources, components, overlays,
patches). The function chain handles install-time mutations driven by
operator answers. Image overrides for occasional patch-level bumps
are a special case — see "Image overrides" below.

### `spec.externalRequires`

Cluster preconditions your package needs but does not provide. The
wizard surfaces them; `installer preflight` (when shipped) probes them
against a live cluster.

```yaml
spec:
  externalRequires:
    - kind: CRD
      name: servicemonitors.monitoring.coreos.com
      version: ">= v1"
      suggestedSource: oci://ghcr.io/.../kube-prometheus-stack
    - kind: GatewayClass
      capability: ext-proc
      suggestedProviders: [istio, envoy-gateway, kgateway]
    - kind: WebhookCertProvider
      name: cert-manager
      issuerKind: ClusterIssuer
```

Recognized `kind` values: `CRD`, `ClusterFeature`,
`WebhookCertProvider`, `GatewayClass`, `Operator`, `StorageClass`,
`RuntimeClass`. Use `suggestedSource` (a single OCI ref) or
`suggestedProviders` (a list of well-known providers) so operators
have a path forward when the requirement is missing.

### `spec.provides`

What your package itself supplies. Used by the dependency resolver to
mark the corresponding `externalRequires` from other packages as
satisfied.

```yaml
spec:
  provides:
    - kind: CRD
      name: gateway.networking.k8s.io
    - kind: GatewayClass
      name: istio
```

### `spec.clusterSingleton`

Leader-election leases your package claims at cluster scope. Two
packages claiming the same lease cannot coexist; the resolver fails
on conflict.

```yaml
spec:
  clusterSingleton:
    - lease: my-app-leader
      namespace: kube-system
```

### `spec.externalManifests`

Remote manifest files (typically release-tarball CRDs) that get
fetched at render time and merged into the rendered output as
additional Units. Used by projects that distribute CRDs outside any
chart.

```yaml
spec:
  externalManifests:
    - name: gaie-crds
      url: https://github.com/.../v0.4.0/manifests.yaml
      digest: sha256:abcd1234...
      splitByResource: true            # default true; one Unit per resource
      phase: crds                      # routes into spec.phases (if used)
```

The `digest` is mandatory and must pin the file you tested against —
fetch is verified against it before render uses the bytes.

### `spec.collector`

An executable bundled in the package that the wizard runs to discover
install-time facts (server URL, image tag, worker IDs, etc.) and to
materialize sensitive material as `.env.secret` files consumed by
kustomize secretGenerator.

```yaml
spec:
  collector:
    command: collector/collect.sh
    args: []
    description: Discover the active cub server URL + bootstrap a worker secret.
```

The wizard runs the command with the package root as the working
directory and the following env vars set (parent env is also
inherited so `cub` works inside the script):

- `INSTALLER_PACKAGE_DIR` — absolute path to the package working copy
- `INSTALLER_WORK_DIR` — absolute path to the parent working
  directory
- `INSTALLER_OUT_DIR` — absolute path to `<work-dir>/out`
- `INSTALLER_NAMESPACE` — value of `--namespace`
- `INSTALLER_BASE` — chosen base name
- `INSTALLER_SELECTED` — comma-separated selected component names
- `INSTALLER_INPUT_<NAME>` — one variable per declared input
  (uppercased)

The collector's stdout is parsed as a YAML map and persisted to
`out/spec/facts.yaml`. The map's keys become available in chain
templates as `{{ .Facts.<key> }}`. The collector may also write
`.env.secret` files inside the package working copy at paths its
kustomize secretGenerator references; the installer never reads or
uploads those files. Rendered Secret resources are routed to
`out/secrets/` and never uploaded as Units.

**Re-run on upgrade**. `installer upgrade` re-runs the collector
against the new package; facts are not assumed to survive a version
bump.

### `spec.validation`

Points at machine-readable component documentation bundled in the
package (typically generated by `docker run <image> docgen
{command,env,runtime}` and committed under `./validation/`). Surfaced
by `installer doc` so an AI agent or human can read what env vars
your workload accepts and what runtime expectations it has without
re-pulling the image.

```yaml
spec:
  validation:
    commandHelp: validation/command.yaml
    envSchema: validation/env.schema.json
    runtimeSpec: validation/runtime.yaml
    howToRegenerate: |
      docker run --rm myorg/myapp:0.1.0 docgen command > validation/command.yaml
      docker run --rm myorg/myapp:0.1.0 docgen env > validation/env.schema.json
      docker run --rm myorg/myapp:0.1.0 docgen runtime > validation/runtime.yaml
```

Optional. The installer doesn't consume these files at render time.

### `spec.dependencies`

Other installer packages this one composes with.

```yaml
spec:
  dependencies:
    - name: gateway-api                     # local handle (must be unique)
      package: oci://ghcr.io/myorg/gateway-api  # OCI ref without tag
      version: "^1.2.0"                     # SemVer range
      selection:                            # pre-answers the dep's wizard
        base: default
        components: [crds, kgateway]
      inputs:                               # pre-answers the dep's inputs
        namespace: gateway-system
      whenComponent: ingress                # only follow when parent
                                            # selects "ingress"
      satisfies:                            # marks parent's externalRequires
        - kind: CRD
          name: gateway.networking.k8s.io
        - kind: GatewayClass
          capability: ext-proc
    - name: cert-manager
      package: oci://ghcr.io/myorg/cert-manager
      version: ">= 1.15.0"
      optional: true
```

The resolver walks the DAG, picks one version per package satisfying
every constraint, honors `conflicts:` and `replaces:`, and writes
`out/spec/lock.yaml` pinning each dependency to a manifest digest.

### `spec.conflicts` and `spec.replaces`

```yaml
spec:
  conflicts:
    - package: oci://ghcr.io/foo/old-gateway
      version: "*"
      reason: "Replaced by gateway-api dependency."
  replaces:
    - package: oci://ghcr.io/foo/old-name
      version: "< 2.0.0"
```

`conflicts` hard-excludes other packages from the resolution set.
`replaces` declares this package supersedes another (typically across
a rename) — a request for the named package matching the version
range is treated as satisfied by this package.

### `spec.bundleExamples`

Controls whether the `examples/` subtree is bundled by `installer
package`. Default `true`. Set to `false` to exclude examples from
published artifacts.

## How the install pipeline consumes your package

Knowing the pipeline helps you reason about what's load-bearing and
what isn't.

1. **`installer pull <ref> <work-dir>/package`** — fetches the
   bundled tgz from OCI, verifies signature if a policy is configured,
   extracts into `<work-dir>/package/`. Your `installer.yaml`,
   `bases/`, `components/`, etc. land here verbatim.

2. **`installer wizard <ref>`** — loads `installer.yaml`, runs the
   selection solver (closure of `requires:`, conflict / `validForBases`
   checks), runs your collector if present, writes
   `out/spec/{selection,inputs,facts}.yaml`.

3. **`installer deps update <work-dir>`** (only if you declare
   dependencies) — resolves the DAG and writes `out/spec/lock.yaml`.

4. **`installer render <work-dir>`** — composes a synthetic top-level
   kustomization that references your chosen base + components, runs
   `kustomize build`, then runs your function-chain template
   (resolved with `.Inputs` / `.Selection` / `.Package` / `.Namespace`
   / `.Facts`). For multi-package installs, each dep is rendered into
   its own subtree under `out/<dep-name>/`. Output lands as one
   resource per file in `out/manifests/`.

5. **`installer upload <work-dir>`** — creates one Space per package
   (parent + each locked dep) and one Unit per rendered file, plus
   one untargeted `installer-record` Unit per Space carrying your
   `installer.yaml` + the spec docs (so a freshly cloned work-dir is
   recoverable from cub alone). Cross-Space Links wire the parent's
   record to each dep's record.

6. **`installer plan` / `update` / `upgrade` / `upgrade-apply`** —
   day-2 operations. The operator's job, but several things you author
   shape how they behave: your `images:` block enables `--set-image`;
   your `default: true` components are adopted by `default`-preset
   re-renders; your input schema diff (across versions) drives what an
   upgrade prompts for vs carries forward silently.

## Authoring best practices

These are the prescriptive ones. The full doctrine is
[principles.md](./principles.md); read it.

### Defaults everywhere

Inputs without defaults force prompts. Components without `default:
true` are invisible to the `default` preset. Operators of a
well-authored package can install with `installer wizard <ref>
--namespace foo` and walk away. Aim for that.

### Declare an `images:` block

Put a kustomize `images:` block in your chosen base's
`kustomization.yaml`, with `newName` / `newTag` matching what's
hard-coded in your manifests. This enables `installer
wizard --set-image name=ref` and `installer upgrade --set-image` for
operators. Without it, `--set-image` fails fast with a message
pointing at this guide. Cost: three lines of YAML; benefit: image
mirroring + patch-version bumps without your operator hand-editing
your package tree.

```yaml
# bases/default/kustomization.yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - deployment.yaml
images:
  - name: myorg/myapp
    newName: myorg/myapp
    newTag: v1.2.3
```

If image changes are expected to be frequent (per-component image
registries, multi-arch by tag, image-by-URI rewrites), declare image
inputs and a `set-container-image` group in
`functionChainTemplate` instead. Reach for that only if the
`images:` block is genuinely insufficient.

### Components, not flags

When something is binary ("with monitoring" / "without monitoring"),
prefer a component over a `bool` input. Components compose better
with kustomize, are visible to the wizard's preset prompt, and let
operators see what's installed at a glance.

### Don't smuggle templates into rendered YAML

Your package's rendered output ends up as literal Kubernetes YAML in
ConfigHub Units. Don't ship Go-template syntax, Helm placeholders, or
`{{ }}`-style variables in resources after kustomize build — they
won't be re-templated. If a value needs to vary at install time,
either declare an input + function-chain mutation, or expose it as a
kustomize image / replicas / patch transformer.

### Design for re-render

Every install state (selection, inputs, facts, dependency lock) is
captured in `out/spec/`. Your render must be deterministic from
those files plus your package source — same package + same spec +
same facts = byte-identical Unit bodies. If you have a non-determinism
(timestamps, random IDs), push it into the collector's facts so it's
captured once and replayed thereafter.

### Don't add inputs an operator could change post-install

Container images, replica counts, env vars — anything ConfigHub has a
function for (`set-container-image`, `set-replicas`, `set-env`) — is
post-install ConfigHub mutation territory. Don't add an input and
function-chain step for these unless the value is install-time-only
(captured at install and never tuned afterward). The wizard should
ask the small number of things that genuinely need to be answered
once, up front.

## Publishing

The publish flow is `installer package` → `installer push`:

```bash
# Build a deterministic .tgz of the source tree.
installer package ./my-package -o my-package-0.1.0.tgz
# Output names the digest:
#   sha256:bc4e...

# Push to an OCI registry as an installer artifact.
installer push my-package-0.1.0.tgz oci://ghcr.io/myorg/my-package:0.1.0

# Inspect what's in a registry without pulling the layer.
installer inspect oci://ghcr.io/myorg/my-package:0.1.0
installer list oci://ghcr.io/myorg/my-package
```

`installer package` is byte-deterministic: sorted entries, zeroed
mtimes / uids / gids, canonicalized modes, gzip without filename or
mtime metadata. The same source tree on two machines produces an
identical tarball.

What's bundled:

- `installer.yaml`
- Every directory referenced by `bases[].path`,
  `components[].path`, `validation.*`, and `collector.command` (if
  relative).
- `examples/` (when `spec.bundleExamples` is unset or `true`).

What's refused:

- `*.env.secret` anywhere.
- Anything under `out/`.
- Anything matched by `.installerignore` (gitignore syntax).

## Signing

`installer push` accepts `--sign` to attach a cosign signature
(keyed or keyless). Operators verify with `installer verify` or by
configuring `~/.config/installer/policy.yaml` to require signatures
matching trusted keys / identities — when that policy exists,
`installer pull` and `installer deps update` enforce verification on
every fetch.

```bash
# Keyed signing.
installer push my-package-0.1.0.tgz oci://ghcr.io/myorg/my-package:0.1.0 \
    --sign --key cosign.key

# Keyless (Sigstore Fulcio, OIDC).
installer push my-package-0.1.0.tgz oci://ghcr.io/myorg/my-package:0.1.0 \
    --sign --keyless
```

If your package will be consumed by people you don't know, sign every
release. The cost is small; the trust story for your operators is
substantial.

## Versioning + upgrade considerations

Operators upgrade with `installer upgrade <work-dir> <new-ref>`. The
upgrade machinery does a schema-diff between the prior install's
package and yours, and behaves accordingly. As an author, this gives
you a few rules of the road:

### Adding inputs

- **New input with a default**: silently adopted on upgrade. Safe.
- **New required input without a default**: surfaces as an
  interactive prompt; in non-interactive mode, the upgrade fails fast
  naming the new input. Avoid this when you can — provide a sensible
  default and let the operator change it later if needed.

### Removing inputs

Silently dropped from the new `inputs.yaml` on upgrade. Operators do
not need to do anything.

### Changing input types

Errors out the upgrade. The operator must re-run `installer wizard`
to re-answer. Avoid type changes; if you need one, ship a new input
under a different name and remove the old in a later release.

### Adding components

- **New component with `default: true`**: adopted on upgrade _only_
  if the operator's prior selection matched the old package's
  default preset exactly. Otherwise the component is available but
  not enabled by default — operators discover it via `installer doc`
  or the wizard.
- **New component without `default: true`**: invisible until the
  operator picks it.

### Removing components

Silently dropped from selection if previously selected. Operators do
not need to do anything; their next render simply omits the
removed component.

### Changing the function chain

Any change in `functionChainTemplate` re-runs against the new render
on upgrade. Render is deterministic; the next plan shows the
resulting diff, and operators can review before applying.

### Image transforms

If your package declares an `images:` block, operators may have
applied `--set-image` overrides that are now stored in their
`Inputs.Spec.ImageOverrides`. Those carry forward on upgrade
automatically — your new release does not need to do anything special
for them.

### Bumping `metadata.version`

Use SemVer. The version is what shows up in the `installer-upgrade-…`
ChangeSet name and in operator-facing diagnostics; honest
versioning makes operator audit trails readable.

### Bumping `kubeVersion` / `installerVersion`

Tighten these as you start using newer features. The installer
refuses to render against incompatible cluster + installer versions.

## Common author tasks

### Add an opt-in component

1. `mkdir components/<name>`, drop a kustomize Component
   (`kind: Component`) `kustomization.yaml` + the resources that
   layer in.
2. Add an entry under `spec.components`. Set `default: true` if the
   component should be part of the recommended install.
3. Declare any preconditions in `externalRequires`.
4. If the component depends on another component in your package,
   list it under `requires:`.

### Add an input

1. Add an entry under `spec.inputs` with `name`, `type`, and a
   `default:` whenever you can name one.
2. Reference the value from `functionChainTemplate` invocations as
   `{{ .Inputs.<name> }}`, or rely on a kustomize transformer
   reading it (e.g., via a generator).

### Depend on another installer package

1. Add an entry under `spec.dependencies` with `name`, `package`
   (OCI ref without tag), `version` (SemVer range).
2. Optionally pre-answer the dep's wizard via `selection:` and
   `inputs:`.
3. If the dep covers an `externalRequires` of yours, list it under
   `satisfies:` so the resolver marks the requirement satisfied.

### Cut a release

1. Bump `metadata.version` per SemVer.
2. `installer package ./my-package -o my-package-X.Y.Z.tgz` — record
   the printed sha256.
3. `installer push my-package-X.Y.Z.tgz oci://.../my-package:X.Y.Z
   --sign` (or `--key` / `--keyless`).
4. Optionally `installer tag oci://.../my-package:X.Y.Z latest` if
   you publish a moving `latest` tag (most don't — a SemVer tag is
   reproducible).

### Help operators audit your package

Ship `validation/` with `command.yaml` / `env.schema.json` /
`runtime.yaml` — generated from the workload itself, committed in
the package. `installer doc <ref>` surfaces them. AI agents and
humans both benefit.

## Next steps

- The [tutorial](./author-tutorial.md) walks through building a small
  package end to end.
- [Consumer guide](./consumer-guide.md) — what your operators see.
- [Principles](./principles.md) — the doctrine the installer is
  anchored to.
- The full design lives in [package-management.md](./package-management.md)
  and [lifecycle.md](./lifecycle.md), if you want to know _why_ a
  thing works the way it does.

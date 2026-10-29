# Package Author Guide

Reference for authoring an installer package — file layout, the
`installer.yaml` schema, what each declaration becomes at install time,
publishing, signing, and version-to-version evolution.

If you're new to the installer, read [the consumer
guide](./consumer-guide.md) first to understand what an operator does
with your package, then come back here. For a hands-on walkthrough of
authoring a small package from scratch, see [the author
tutorial](./author-tutorial.md).

The CLI provides four authoring shortcuts:

- `installer init <dir>` — scaffold a new package with the
  recommended defaults, optionally starting its base from an
  existing kustomization (`--source`).
- `installer new <kind> <name>` — clone a Kubernetes resource
  template from the bootstrapped `kubernetes-resources` package.
- `installer edit add/remove/set input|component|dependency` —
  mutate `installer.yaml` fields (no hand-editing YAML).
- `installer vet <work-dir>` — run the package's validators against
  the existing render without re-rendering.

These all operate on package source on disk; there's no ConfigHub
interaction except `installer new`, which fetches templates from
the bootstrapped kubernetes-resources Space.

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

The fastest way to get this layout is:

```bash
installer init ./my-package
```

`installer init` scaffolds the manifest (with the recommended
validator chain — see [Validators](#specvalidators) below), an
empty `bases/default/`, an empty `components/`, and an empty
`validation/`. From there, drop your kustomize resources into
`bases/default/` and either hand-author them or use `installer new
<kind> <name>` to clone canonical templates from the
[`kubernetes-resources`](#kubernetes-resources-package) package. If
you already have a kustomization, `installer init --source` starts
`bases/default/` as a copy of it; see [Import an existing
kustomization](#import-an-existing-kustomization).

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
installerMetadata:
  version: 0.1.0          # SemVer
  kubeVersion: ">= 1.28"  # SemVer range; empty means unconstrained
  installerVersion: ">= 0.2.0"
spec:
  ...
```

`metadata.name` and `installerMetadata.version` are required. The two
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
- `transformers:` and `validators:` are component-scoped mixins. Same
  shape as `spec.transformers` / `spec.validators` (see below), but
  the groups only run when this component is selected. They're
  appended to the package-wide chain in the order components appear
  in `spec.components`, so re-render is deterministic regardless of
  which order the operator passes `--select`. All groups still run
  in a single in-process executor pass — adding a component-scoped
  transformer costs nothing extra at render time.

  Example: a `monitoring` component that adds a ServiceMonitor and
  wants to pin its scrape interval per-namespace can keep that
  mutation co-located with the component declaration:

  ```yaml
  components:
    - name: monitoring
      path: components/monitoring
      transformers:
        - toolchain: Kubernetes/YAML
          whereResource: "ConfigHub.ResourceType = 'monitoring.coreos.com/v1/ServiceMonitor'"
          invocations:
            - name: set-yq
              args: ['.spec.endpoints[].interval = "{{ .Inputs.scrape_interval }}"']
  ```

### `spec.componentGroups`

Alternatives: components that implement one thing differently, such as
the GitOps controller a bot drives, and can't be installed together.

```yaml
spec:
  components:
    - name: argocd
      path: components/argocd
      default: true
    - name: flux
      path: components/flux
    - name: sveltos
      path: components/sveltos
  componentGroups:
    - name: controller
      description: The GitOps controller to drive.
      components: [argocd, flux, sveltos]
      required: true
```

- Selecting two members of a group is an error at solve time, so the
  members need no `conflicts:` with one another.
- `required: true` means exactly one member, not at most one. When
  nothing selects a member — the `minimal` preset, or `--select` of
  other components — the solver selects the member with
  `default: true`, and what it `requires:`. A required group without a
  default member makes the operator pick one.
- The `all` preset takes only one member of each group: the default
  member, or the first.
- The interactive wizard's `selected` mode asks one question per
  group, then offers the other components as a multi-select.
- A component belongs to at most one group, and at most one member of
  a group may set `default: true`.

### `spec.inputs`

Wizard prompts. Referenced in `spec.transformers` (and
`spec.validators`) as `{{ .Inputs.<name> }}`.

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

### `spec.validators`

A list of validating-function invocation groups, run automatically
at the end of every `installer render` against the post-mutation
output. Each group has the same shape as
[`transformers`](#specfunctionchaintemplate) — a toolchain,
optional `whereResource` filter, ordered `invocations` — but every
named function must be a Validating function (`Mutating: false`,
`Validating: true`). Validators do not modify the rendered
manifests; render fails fast if any returns `Passed=false`.

```yaml
spec:
  validators:
    - toolchain: Kubernetes/YAML
      description: Default validators applied at the end of every render.
      invocations:
        - name: vet-schemas
        - name: vet-merge-keys
        - name: vet-format
```

`installer init` seeds `vet-schemas`, `vet-merge-keys`, and
`vet-format` by default. `vet-placeholders` is intentionally NOT
seeded — packages that ship cloneable bases (with
`confighubplaceholder` fields) would fail it.

Edit the list with `installer edit add/remove validator <name>`
(coming in a follow-up) or by hand. The full list of available
validators:

```bash
cub function list --where "Validating = TRUE" --toolchain Kubernetes/YAML
```

Frequently used:

| Validator | Catches |
| --- | --- |
| `vet-schemas` | Resources that don't validate against their Kubernetes schema. |
| `vet-merge-keys` | Duplicate strategic-merge-patch keys (e.g., two containers with the same name). |
| `vet-format` | YAML format issues — anchors/aliases, empty values, duplicate keys, truthy values, old-style octals. |
| `vet-placeholders` | `confighubplaceholder`-bearing fields — install-time placeholder leaking into output. |
| `vet-images` | Container images that fail an allow/deny filter. |
| `vet-cel` | Custom CEL expressions per resource. |
| `vet-celexpr` | Custom CEL expression across all resources. |
| `vet-jsonschema` | Resources that don't validate against a custom JSONSchema. |
| `vet-starlark` | Custom Starlark validation. |
| `vet-values` | Field values outside an allowed set. |

When the list changes, run `installer vet <work-dir>` against an
existing render to check the new validators without re-rendering.

`whereResource` filters in `validators[].whereResource` use the same
qualified path syntax as
[`transformers`](#specfunctionchaintemplate) —
`ConfigHub.ResourceType = 'apps/v1/Deployment'`, etc.

### `spec.transformers`

A list of function-invocation groups. At render time the template is
resolved against the operator's answers (via Go `text/template`,
with `.Inputs`, `.Selection`, `.Package`, `.Namespace`, `.Facts` in
scope), then each group is executed by the ConfigHub function
executor SDK. The output of each invocation feeds the next
invocation; the output of each group feeds the next group.

```yaml
spec:
  transformers:
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
  this group to a subset of resources. Empty = all. **Path
  qualifier required**: use `ConfigHub.ResourceType` (with prefix),
  not bare `ResourceType`. Bare `ResourceType` parses but matches
  zero resources, so the function silently no-ops. Example:
  `ConfigHub.ResourceType IN ('apps/v1/Deployment', 'apps/v1/StatefulSet')`.
- `invocations` — ordered. Each invocation names a function and its
  string args (templated).

Common functions: `set-namespace`, `set-name`, `set-replicas`,
`set-container-image`, `set-container-image-reference`,
`set-container-resources`, `set-env`, `set-hostname`, `set-yq`,
`set-string-path`, `delete-path`. The full list is in `cub function
list` against a running ConfigHub.

**When to use the function chain vs kustomize itself**: kustomize
handles structural composition (resources, components, overlays,
patches). The function chain handles install-time mutations driven by
operator answers. Image overrides for occasional patch-level bumps
are a special case — see "Image overrides" below.

### Namespace guarantee

The rendered output is **guaranteed to contain a `Namespace` resource**
when an install namespace is set (the wizard's `--namespace`). If your
package's manifests already declare a `Namespace` (e.g. you vendored one,
or your base ships it), that one is used as-is. If they don't — many
upstreams (Argo CD's `install.yaml`, bare kustomize bases) omit it and
rely on `kubectl apply -n` — the transform pass injects a minimal

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: <install-namespace>
```

**before your function chain runs**, so it's mutated by the same chain as
everything else: `set-namespace` finds it (a no-op rename, since it
already carries the install namespace), and a `set-pod-security-defaults`
group will stamp PSA labels onto it. You therefore don't need to author a
placeholder `Namespace` just so `set-namespace` has something to rename,
and you don't need to tell operators to `kubectl create namespace` first.

The check is "is there already a `v1/Namespace`?" — exactly one Namespace
ends up in the output either way; the injector never adds a duplicate. The
guarantee is disabled only when no install namespace is supplied (a
package with no namespace input has opted out). Cluster-scoped-only
packages (CRDs, ClusterRoles) that legitimately install nothing namespaced
should leave `--namespace` unset.

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
install-time facts (server URL, image tag, etc.) and to
materialize sensitive material as `.env.secret` files consumed by
kustomize secretGenerator.

```yaml
spec:
  collector:
    command: collector/collect.sh
    args: []
    description: Discover the active cub server URL + generate an app token.
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
`out/record/facts.yaml`. The map's keys become available in chain
templates as `{{ .Facts.<key> }}`. The collector may also write
`.env.secret` files inside the package working copy at paths its
kustomize secretGenerator references; the installer never reads or
uploads those files. Rendered Secret resources are routed to
`out/secrets/` and never uploaded as Units.

**Re-run on every setup**. `installer setup` re-runs the collector
on each invocation (whether or not `--pull` was passed). Facts are
not assumed to survive a version bump or a cluster-state change.

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
`out/record/lock.yaml` pinning each dependency to a manifest digest.

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

2. **`installer setup`** (or `installer wizard <ref>` + `installer
   render` granularly) — loads `installer.yaml`, runs the selection
   solver (closure of `requires:`, conflict / `validForBases` checks),
   runs your collector if present, writes
   `out/record/{selection,inputs,facts}.yaml`. For multi-package
   installs, automatically runs `deps update` to resolve the
   dependency DAG and write `out/record/lock.yaml`. Then composes a
   synthetic top-level kustomization under `out/compose/` that
   references your chosen base + components, resolves your
   `spec.transformers` / `spec.validators` against `.Inputs` /
   `.Selection` / `.Package` / `.Namespace` / `.Facts`, writes them as
   KRM function configs, and builds it with the kustomize built into
   the installer, as `kustomize build --enable-exec
   --enable-alpha-plugins` would. Kustomize invokes the `installer transformer` subcommand as an exec plugin
   to run each function group in process; the result lands as one
   resource per file in `out/manifests/`. For multi-package installs,
   each dep is rendered into its own subtree under `out/<dep-name>/`.

3. **`installer upload`** — sends each package (parent + each locked
   dep) to ConfigHub's upload API, one Space per package. Every rendered
   resource becomes its own Unit, and each Space gets an untargeted
   `installer` record Unit carrying your `installer.yaml` and the record of
   the render, so a freshly cloned work-dir is recoverable from ConfigHub.
   The parent's Space records its dependencies in a `DependsOn`
   annotation. Every upload is create-or-update: later uploads merge what
   changed and empty what the render dropped.

4. **`installer plan`** — day-2 dry run of what the next `installer
   upload` would change in ConfigHub. The
   operator's job, but several things you author shape how it
   behaves: your `images:` block enables `--set-image`; your
   `default: true` components are adopted by `default`-preset
   re-renders; your input schema diff (across versions) drives what
   `setup --pull <new-ref>` prompts for vs carries forward silently.

## kubernetes-resources package

`packages/kubernetes-resources/` (in this repo) ships eleven
canonical Kubernetes resource templates — Namespace, Deployment +
Service, StatefulSet + headless Service, DaemonSet, Job, CronJob,
Ingress, NetworkPolicy pair, RBAC bundle, HPA, PDB — each with the
recommended defaults pre-applied via per-type ConfigHub functions
(resource requests, probes, securityContext,
automountServiceAccountToken=false, pod-security labels on
Namespace, etc.). Resources select one another with the Kubernetes
[recommended label](https://kubernetes.io/docs/concepts/overview/working-with-objects/common-labels/)
`app.kubernetes.io/name` rather than a bare `app` label. The
Ingress's `ingressClassName` is `confighubplaceholder`, since the
right class depends on the controller each cluster runs; set it
before the Ingress is released.

Once installed in your ConfigHub organization, `installer new
<kind> <name>` clones any of these templates into your package's
`bases/default/` with operator customizations applied (renamed,
optional `--image` / `--port` / `--replicas`, namespace replaced
with `confighubplaceholder` so your package's own `set-namespace`
function rewrites it at install time).

Bootstrap the package once per organization:

```bash
installer setup \
    --pull ./packages/kubernetes-resources \
    --work-dir /tmp/k8s-res \
    --non-interactive --namespace kubernetes-resources
installer upload --work-dir /tmp/k8s-res --space kubernetes-resources
```

The `installer upload` step records the install in
`~/.confighub/installer/state.yaml`; subsequent `installer new`
calls find it automatically.

After bootstrap, scaffold a Deployment into your package:

```bash
installer new deployment my-app \
    --image myorg/my-app:1.0.0 --port 8080 --replicas 3
```

Supported kinds: `cronjob`, `daemonset`, `deployment`, `hpa`
(alias for `horizontalpodautoscaler`), `ingress`, `job`,
`namespace`, `networkpolicy`, `pdb` (alias for
`poddisruptionbudget`), `role`, `rolebinding`, `service`,
`serviceaccount`, `statefulset`.

`installer new --update-kustomization` (default true) appends the
new file to your package's `bases/default/kustomization.yaml`'s
resources list.

## Application configuration files (AppConfig)

When your workload reads a properties file, an env file, a TOML
config, or any of the other supported application-configuration
formats, you have two choices: hardcode it into a `ConfigMap`
manifest as a literal `data:` map, or keep it in its native format
and let kustomize's `configMapGenerator` bake it into a ConfigMap.

The installer prefers the second path: the config stays in its
native format (a real `.properties`, `.toml`, `.env`, etc. file in
your package tree) so per-format ConfigHub functions can mutate it
correctly, ConfigHub can validate against a schema, and the upload
step can split the rendered output into an AppConfig Unit + a
`render-configmap` Invocation so day-2 edits stay in the source
format too.

### Author contract

Annotate the `configMapGenerator` entry with
`installer.confighub.com/toolchain` pointing at the matching
AppConfig toolchain. The installer reads the annotation off the
rendered ConfigMap (kustomize copies generator annotations onto the
output), so the contract is "tag once, here":

```yaml
# bases/default/kustomization.yaml
configMapGenerator:
  - name: app-config
    files:
      - application.properties
    options:
      disableNameSuffixHash: true
      annotations:
        installer.confighub.com/toolchain: AppConfig/Properties

  - name: app-env
    envs:
      - .env
    options:
      disableNameSuffixHash: true
      annotations:
        installer.confighub.com/toolchain: AppConfig/Env
```

Supported toolchain values:

- `AppConfig/Properties` — Java-style `.properties`
- `AppConfig/Env` — `.env` / `KEY=value`
- `AppConfig/TOML`, `AppConfig/INI`, `AppConfig/YAML`,
  `AppConfig/JSON`, `AppConfig/Text` — match the file extension

### What the installer does with it

At render time, the `installer transformer` plugin (which kustomize
invokes as an exec transformer) recognizes ConfigMaps carrying the
toolchain annotation and:

- Derives the carrier shape (`installer.confighub.com/appconfig-mode`:
  `file` if the generator used `files:`, `env` if it used `envs:`) by
  inspecting `data:`. For file mode it also records
  `installer.confighub.com/appconfig-source-key` — the `data:` key
  whose value is the raw config file body. And it records
  `installer.confighub.com/appconfig-mutable`: `false` when the
  generator hashes the ConfigMap's name, `true` when it doesn't (see
  [disableNameSuffixHash](#disablenamesuffixhash)). kustomize appends
  the hash after the transformer runs, and marks a ConfigMap it will
  hash with `internal.config.kubernetes.io/needsHashSuffix: enabled`,
  which is what the transformer reads. For such a ConfigMap it also
  records `installer.confighub.com/appconfig-name`, the name before
  the hash. Authors can pre-set the mode, source key and mutability
  annotations to override the inference.
- Routes any `AppConfig/*` function group in `spec.transformers` to
  the matching ConfigMap: extract the raw config from `data:`, run
  the function in the AppConfig toolchain, write the mutated content
  back into `data:`. Other kustomize transformers
  (`commonLabels`, `namespace`, `namePrefix`, `replacements`)
  continue to decorate the carrier ConfigMap normally.

At upload time, an annotated ConfigMap becomes a four-piece bundle,
named after the ConfigMap's name before any hash: an AppConfig Unit
(toolchain from the annotation, data = the extracted raw file), a
`render-configmap` Invocation `<name>-render`, a placeholder
Kubernetes/YAML ConfigMap Unit `<name>-rendered`, and an `Upsert`
link from the placeholder to the AppConfig Unit with the Invocation
as its TransformInvocation. On resolution the function renders the
ConfigMap into the placeholder. A workload that references the
ConfigMap is linked to the placeholder Unit. When ConfigHub renders
the ConfigMap under a name other than the one the workload's
reference holds, the upload replaces the reference with a named
placeholder for the AppConfig Unit, such as
`confighubplaceholderapp_env` for `app-env`, and ConfigHub fills it
with the rendered name.

Rendering runs as a built-in function on the server — no Target or
apply step is involved.

### disableNameSuffixHash

Whether the generator hashes the ConfigMap's name decides how
ConfigHub renders it after upload, and how a change to it reaches
the pods.

**Stable name (`disableNameSuffixHash: true`).** The installer marks
the ConfigMap mutable, and the upload's Invocation runs
`render-configmap --immutable=false`. ConfigHub renders one
ConfigMap, named like the generator, and updates it in place on
every change to the AppConfig Unit. Workloads reference it by that
name. The rendered ConfigMap carries a `confighub.com/Hash`
annotation with its content's hash. To roll the pods when the
content changes, give the pod template a `confighub.com/Hash`
annotation set to `confighubplaceholder`. ConfigHub fills it with
the ConfigMap's hash, and fills it again on every change:

```yaml
# bases/default/deployment.yaml
spec:
  template:
    metadata:
      annotations:
        confighub.com/Hash: confighubplaceholder
    spec:
      containers:
        - name: app
          envFrom:
            - configMapRef:
                name: app-env
```

Without the annotation, the ConfigMap is still updated in place,
but the pods don't restart: ones that read it as environment
variables keep the values they started with. A pod template holds one
`confighub.com/Hash`, so it rolls on changes to one rendered
ConfigMap.

**Hashed name (the kustomize default).** The installer marks the
ConfigMap immutable and records its name before the hash, and the
upload's Invocation runs `render-configmap --immutable=true`.
`out/manifests/` keeps kustomize's output, hashed name and all, so
it still rolls the pods when deployed without ConfigHub. In
ConfigHub, the AppConfig Unit takes the name before the hash, so a
change to the content updates it rather than adding another. Each
change renders a new ConfigMap named after the Unit with a hash of
ConfigHub's own, beside the ones before it, which the pods still
running may use. The upload turns the workload's reference into a
named placeholder for the AppConfig Unit, and ConfigHub fills it
with the newest rendered name, so the pods roll on every change.

Re-render with `installer render --clean` (or `setup --clean`) after
changing the content. Without it, the ConfigMap under its previous
hashed name stays in `out/manifests/` beside the new one, and the
upload refuses the AppConfig as defined twice.

Old renders accumulate until a `prune-configmaps` Mutation Trigger
in the Space retires them. The Trigger is also what moves workloads
to a new render: it withdraws the older renders from Needs/Provides,
and until it does, a workload's reference stays on the render it
first named. The upload doesn't create the Trigger.

```sh
cub trigger create --space <space> prune-configmaps Mutation Kubernetes/YAML \
    -- prune-configmaps --revision-history-limit=2
```

Hashed names need a ConfigHub server newer than v0.9.10. An older
one names the Units after the hashed name and leaves the workload's
reference holding a name no rendered ConfigMap has.

ConfigHub's [AppConfig guide](https://docs.confighub.com/guide/app-config/#deploying-as-a-kubernetes-configmap)
describes both modes in full.

### secretGenerator

`secretGenerator` is not tagged with installer annotations. Secrets
flow through the existing sensitive-resource pathway: rendered to
`out/secrets/`, never uploaded as Units. The roadmap for a better
secrets story is tracked separately.

## Authoring best practices

These are the prescriptive ones. The full doctrine is
[principles.md](./principles.md); read it.

### Defaults everywhere

Inputs without defaults force prompts. Components without `default:
true` are invisible to the `default` preset. Operators of a
well-authored package can install with `installer setup --pull <ref>
--namespace foo` and walk away. Aim for that.

### Declare an `images:` block

Put a kustomize `images:` block in your chosen base's
`kustomization.yaml`, with `newName` / `newTag` matching what's
hard-coded in your manifests. This enables `installer setup
--set-image name=ref` for operators. Without it, `--set-image`
fails fast with a message pointing at this guide. Cost: three lines
of YAML; benefit: image mirroring + patch-version bumps without
your operator hand-editing your package tree.

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
`transformers` instead. Reach for that only if the
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
either declare an input + a `spec.transformers` mutation, or expose
it as a kustomize image / replicas / patch transformer.

### Design for re-render

Every install state (selection, inputs, facts, dependency lock) is
captured in `out/record/`. Your render must be deterministic from
those files plus your package source — same package + same spec +
same facts = byte-identical Unit bodies. If you have a non-determinism
(timestamps, random IDs), push it into the collector's facts so it's
captured once and replayed thereafter.

### Don't add inputs an operator could change post-install

Container images, replica counts, env vars — anything ConfigHub has a
function for (`set-container-image`, `set-replicas`, `set-env`) — is
post-install ConfigHub mutation territory. Don't add an input and
`spec.transformers` step for these unless the value is install-time-only
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

After `installer push`, attach a cosign signature with
`installer sign <ref>`. Keyless mode (Sigstore Fulcio + Rekor) is
the default; pass `--key <ref>` for keyed mode. Operators verify
with `installer verify` or by configuring
`~/.config/installer/policy.yaml` to require signatures matching
trusted keys / identities — when that policy exists, `installer
pull` and `installer deps update` enforce verification on every
fetch. Requires the `cosign` binary on PATH (override via
`INSTALLER_COSIGN_BIN`).

```bash
# Keyless (Sigstore Fulcio + Rekor — interactive OIDC flow).
# Add --yes to skip the cosign confirmation prompt in CI.
installer sign oci://ghcr.io/myorg/my-package:0.1.0 --yes

# Keyed.
installer sign oci://ghcr.io/myorg/my-package:0.1.0 --key cosign.key
```

If your package will be consumed by people you don't know, sign every
release. The cost is small; the trust story for your operators is
substantial.

## Versioning + upgrade considerations

Operators upgrade with `installer setup --pull <new-ref>` against an
existing work-dir. The setup auto-detects prior state and runs the
schema-diff machinery between the prior install's package and yours.
As an author, this gives you a few rules of the road:

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

Errors out setup. The operator must re-run `installer setup`
interactively to re-answer. Avoid type changes; if you need one, ship
a new input under a different name and remove the old in a later
release.

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

Any change in `transformers` re-runs against the new render
on upgrade. Render is deterministic; the next plan shows the
resulting diff, and operators can review before applying.

### Image transforms

If your package declares an `images:` block, operators may have
applied `--set-image` overrides that are now stored in their
`Inputs.Spec.ImageOverrides`. Those carry forward on upgrade
automatically — your new release does not need to do anything special
for them.

### Bumping `installerMetadata.version`

Use SemVer. The version is what shows up in the `installer-upgrade-…`
ChangeSet name and in operator-facing diagnostics; honest
versioning makes operator audit trails readable.

### Bumping `installerMetadata.kubeVersion` / `installerMetadata.installerVersion`

Tighten these as you start using newer features. The installer
refuses to render against incompatible cluster + installer versions.

## Common author tasks

### Start a new package

```bash
installer init ./my-package
```

Scaffolds `installer.yaml` (with the recommended validators),
`bases/default/kustomization.yaml`, `bases/default/`,
`components/`, `validation/`. Drop your kustomize resources into
`bases/default/`, then add inputs / components / dependencies via
`installer edit add` or hand-edit `installer.yaml`.

### Import an existing kustomization

```bash
installer init ./my-package --source ~/src/my-app/deploy/overlays/prod
```

Starts `bases/default/` as a copy of an existing kustomization
directory, made with
[`kustomize localize`](https://kubectl.docs.kubernetes.io/references/kustomize/cmd/localize/):
every file the source references is copied, and remote resources
(URLs and git references) are downloaded into the package, so the
package builds without network access. The copy is the package's
from then on; nothing ties it back to the source.

Point `--source` at the kustomization you deploy. A package
installs one shape of your application, and the differences between
environments come later, as [variants](https://docs.confighub.com/guide/variants/),
so import a single overlay (or the base) rather than one per
environment.

When the source references files outside its own directory, as an
overlay's `../../base` does, those files must be inside
`--source-scope`, which defaults to the git repository the source
is in. `bases/default/` then mirrors the directories of the files the
source references, and its `kustomization.yaml` is a generated one
that includes the source:

```
bases/default/
├── kustomization.yaml       # resources: [overlays/prod], plus images:
├── base/
│   ├── kustomization.yaml
│   └── deployment.yaml
└── overlays/
    └── prod/
        ├── kustomization.yaml
        └── replicas.yaml
```

A source that references nothing outside its directory is copied
straight into `bases/default/`.

Either way, `bases/default/kustomization.yaml` gets an `images:`
block naming every container image the base runs, at the tag it
already has, so operators can override them with `--set-image` (see
[Declare an `images:` block](#declare-an-images-block)). A source that
declares its own `images:` keeps it unchanged.

The import is refused, and nothing is written, when:

- the source inflates Helm charts (`helmCharts:`); `installer render`
  does not run Helm, so render the chart with `helm template` and
  reference the output instead.
- the source produces Secrets with data, from a `secretGenerator` or
  a Secret manifest; the package would publish them. Remove them, and
  have a [collector](#speccollector) write `*.env.secret` files for a
  `secretGenerator`.
- the copy does not build to the same output as the source, as
  happens when the source uses KRM function or exec plugins, which
  localize does not copy.
- `bases/default/` already has files in it, even with `--force`.

The copied kustomization files lose their comments, and list their
fields alphabetically. Resource files are copied unchanged.

### Add a Kubernetes resource

```bash
installer new deployment my-app --image myorg/my-app:1.0 --port 8080
```

Clones the `kubernetes-resources` Deployment template (defaults
already applied) into `bases/default/deployment-my-app.yaml` and
appends it to `bases/default/kustomization.yaml`. See
[kubernetes-resources package](#kubernetes-resources-package) for
the full kind list and prerequisites.

For resource shapes the template doesn't cover, hand-author the
YAML and add it to `bases/default/kustomization.yaml`.

### Add an opt-in component

```bash
mkdir -p components/monitoring
# drop kustomization.yaml + resources into components/monitoring/
installer edit add component monitoring \
    --path components/monitoring \
    --default \
    --description "Adds a ServiceMonitor for Prometheus scraping."
```

`installer edit add component` writes the entry under
`spec.components` and re-validates the manifest. Use `--requires`,
`--conflicts`, `--valid-for-bases` (each repeatable) for
constraints.

### Add an input

```bash
installer edit add input replicas \
    --type int --default 2 \
    --prompt "Number of replicas"
```

Then reference it from `transformers` invocations as
`{{ .Inputs.replicas }}`. Use `installer edit set input <name>` to
modify, `installer edit remove input <name>` to drop.

### Depend on another installer package

```bash
installer edit add dependency gateway-api \
    --package-ref oci://ghcr.io/myorg/gateway-api \
    --version "^1.2.0"
# Optionally:
installer edit set dependency gateway-api --when-component ingress
```

For more nuanced fields (`selection`, `inputs`, `satisfies`), edit
`spec.dependencies` directly — they have nested structure that
`installer edit` doesn't model in v1.

### Re-vet after editing the validator list

```bash
installer vet <work-dir>
```

Runs `spec.validators` against the existing `out/manifests/`
without re-running kustomize. Useful when you
added a new validator (e.g., `vet-images`) and want to check the
existing render before re-rendering. (Render auto-runs validators
too; `vet` is for the validator-list-changed-but-render-unchanged
case.)

### Cut a release

1. Bump `installerMetadata.version` per SemVer.
2. `installer package ./my-package -o my-package-X.Y.Z.tgz` — record
   the printed sha256.
3. `installer push my-package-X.Y.Z.tgz oci://.../my-package:X.Y.Z`.
4. `installer sign oci://.../my-package:X.Y.Z --yes` (keyless) or
   `installer sign oci://.../my-package:X.Y.Z --key cosign.key`
   (keyed).
5. Optionally `installer tag oci://.../my-package:X.Y.Z latest` if
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

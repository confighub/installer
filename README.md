# installer

Kubernetes off-the-shelf component installer using
[configuration as data](https://docs.confighub.com/background/config-as-data/).

This tool is intended to play the role of an
[install wizard](https://www.revenera.com/install/products/installshield/installshield-tips-tricks/what-is-an-installation-wizard)
and a [package dependency manager](https://medium.com/@sdboyer/so-you-want-to-write-a-package-manager-4ae9c17d9527).

This installer aims to present only the minimal number of high-level decisions, such as
which components to install and where to install them. It is recommended to set reasonable
defaults as much as possible.

There's a local "fact collection" extension point. This can be used to collect details about
the workload being deployed, verify existence of dependencies, and/or to retrieve or generate
secrets and convert them to Kubernetes secrets. For cases where installation decisions depend
on hardware, operating system, networking, or other details of the target cluster, Target Facts
can be used, and can also be collected (assuming permission and access to the cluster) using
`cub k8s collect <target>`.

As with installer wizards for systems other than Kubernetes, changes to detailed default
settings are deferred until after installation. Configuration as data makes this possible
by storing the configuration data rather than re-rendering from scratch. That decouples
[configuration authoring](https://docs.confighub.com/guide/authoring-config/) from
[configuration editing](https://itnext.io/configuration-editing-is-imperative-fa9db379fbe4).
Changes [can be merged](https://docs.confighub.com/guide/variants/#merging-external-sources-into-confighub).

More explanation of the tool's approach can be found in [Design principles](docs/principles.md).

With the configuration-as-data approach, code that operates on configuration is separate from
the data, which is plain YAML (and other formats in the future). The "code" lives in the installer.

This tool leverages kustomize for its foundational composition and customization. It adds a Package manifest
named `installer.yaml` that declares bases, available components, dependencies, and inputs. It also contains
a sequence of ConfigHub functions executed at install (render) time locally via the SDK.

Output from the rendering process goes to plain YAML files that can be uploaded to ConfigHub for delivery
via ArgoCD or Flux, or could be committed to git to be deployed by ArgoCD, Flux, or other Kubernetes
deployment tool.

For post-installation customization, [ConfigHub's function suite](https://docs.confighub.com/guide/functions/#frequently-used-functions) includes functions for changing commonly changed Kubernetes resource properties,
such as `set-container-image`, `set-container-resources`, `set-replicas`, `set-env`, and `set-hostname`,
and general-purpose editing functions, such as `yq-i`, `set-string-path`, `delete-path`, and `set-starlark`.

Why not just [kustomize](https://kustomize.io), or [kpt](https://kpt.dev)? Neither tool
was really designed to be a full-blown installer, and package management was explicitly
[out of scope](https://github.com/kubernetes/design-proposals-archive/blob/main/architecture/scope.md)
for kustomize and kubectl. A lot was learned from kustomize and kpt, but starting afresh made it
easier to experiment with different design choices.

## Status

Working:

- **Package authoring + distribution.** `install package` (deterministic
  bundle), `push` / `pull` / `inspect` / `list` / `tag` /
  `login` / `logout` (OCI artifacts), `sign` / `verify` (cosign keyed +
  keyless) with a `~/.config/installer/policy.yaml` trust policy that
  gates `pull` and `deps update`.
- **Dependencies.** SemVer resolver + lock (`deps update`, `deps tree`),
  multi-package render into per-dep subtrees, upload into per-dep Spaces
  with cross-Space Links.
- **Install lifecycle.** `install setup` (the one-shot consumer entry
  point: optional pull, wizard with high-level component presets
  `minimal` / `default` / `all` / `selected`, render). Interactive +
  non-interactive. Prior-state re-entry from ConfigHub (via the
  persisted `installer-record` Unit) or local `out/spec/`,
  organization + server sanity-check against the active cub context.
- **Day-2 lifecycle.** `install setup` and `install upload`
  auto-detect first-install vs upgrade vs reconcile via the presence
  of prior spec files / `out/spec/upload.yaml`. Re-running `setup`
  with `--pull <new-ref>` runs the schema-diff machinery (carry
  forward existing values, adopt new defaults, prompt for new
  required-without-default). Re-running `upload` against an already-
  uploaded work-dir opens a ChangeSet and reconciles updates / adds /
  deletes. `install plan` previews the reconcile diff read-only.
  `--merge-external-source` is the change predicate, so post-install
  ConfigHub edits survive re-render.
- **Image overrides.** `install setup --set-image` applies
  `kustomize edit set image` before render. Overrides round-trip via
  `Inputs.Spec.ImageOverrides` and carry forward across re-renders /
  upgrades. The chosen base must declare an `images:` block; render
  fails fast otherwise.

Stubbed: `install preflight` — cluster-side constraint checks.

## Build

```bash
make build       # writes bin/install
```

The binary is named `install` so the cub plugin protocol exposes it as
`cub install ...` (cub names plugins after the entrypoint basename in
`cub-plugin.yaml`). `install` shells out to `kustomize` for
`kustomize build`. Install it from
[kubernetes-sigs/kustomize](https://kubectl.docs.kubernetes.io/installation/kustomize/)
or `brew install kustomize`.

## Quick start

End to end against the included example, no ConfigHub server required:

```bash
# 1. Inspect what's in the package.
bin/install doc ./examples/hello-app

# 2. Setup: pull (here: a local dir), pick base + components, supply
#    inputs, and render. One command replaces pull + wizard + render.
mkdir -p /tmp/hello && cd /tmp/hello
bin/install setup \
  --pull ./examples/hello-app \
  --non-interactive \
  --select monitoring --select ingress \
  --namespace demo

# 3. Upload to ConfigHub. Records the destination Space(s) in
#    out/spec/upload.yaml so subsequent commands re-enter the same
#    Space without re-typing.
bin/install upload --space my-greeter

# 4. Day-2: edit a rendered file, see what upload would do, apply.
$EDITOR out/manifests/deployment-demo-hello-app.yaml
bin/install plan                       # read-only diff vs ConfigHub
bin/install upload --yes               # reconcile (ChangeSet-wrapped)

# 5. Upgrade: re-pull (atomic), re-render via setup, then upload.
bin/install setup --pull ./examples/hello-app \
  --set-image nginxdemos/hello=nginxdemos/hello:plain-text-v2
bin/install upload --yes
```

The wizard's `--select` is closed under each component's `requires:` list, so
selecting `ingress-tls` automatically pulls in `ingress`. Conflicts and
`validForBases` are enforced at solve time.

`setup` auto-detects whether the work-dir is a fresh install (no
`out/spec/`) or a re-entry (prior state present, possibly with a
newer package). On re-entry it runs the schema-diff machinery:
silently carry prior values, adopt new defaults, drop removed inputs,
prompt or fail-fast on newly-required inputs.

The granular commands (`pull`, `wizard`, `render`) are still available
when you want step-by-step control — see
[consumer-guide.md](docs/consumer-guide.md#granular-commands).

## Working directory layout

After `setup` (or `wizard` + `render`), the working dir looks like:

```
<work-dir>/
├── package/                  # what 'pull' fetched
│   ├── installer.yaml
│   ├── bases/
│   └── components/
└── out/
    ├── manifests/            # per-resource YAML, ready to upload
    │   ├── deployment-<ns>-<name>.yaml
    │   ├── service-<ns>-<name>.yaml
    │   └── ...
    ├── compose/              # the synthesized kustomization driving render
    │   ├── kustomization.yaml    # references the chosen base + components
    │   ├── transformers.yaml     # resolved ConfigHubTransformers (chain)
    │   ├── validators.yaml       # resolved ConfigHubValidators (if any)
    │   └── installer-transformer.sh   # exec wrapper kustomize invokes
    └── spec/                 # the "installer record" (also uploadable as Units)
        ├── selection.yaml    # base + closure-resolved components
        ├── inputs.yaml       # validated wizard answers
        ├── function-chain.yaml   # the resolved chain that ran (audit copy)
        └── manifest-index.yaml   # filename → kind/name/namespace
```

`out/compose/` is durable, not a temp dir. You can `cd out/compose &&
kustomize build --enable-exec --enable-alpha-plugins .` to reproduce
the render byte-for-byte outside the installer.

The two spec docs (`selection.yaml`, `inputs.yaml`) are the load-bearing inputs
to re-render: edit them, re-run `install render`, get a deterministic new set
of manifests.

## Package format

A package is a kustomize tree wrapped with an `installer.yaml`:

```yaml
apiVersion: installer.confighub.com/v1alpha1
kind: Package
metadata:
  name: my-package
  version: 0.1.0
spec:
  bases: # alternative top-level kustomize trees
    - { name: default, path: bases/default, default: true }
  components: # opt-in kustomize Components (kind: Component)
    - { name: monitoring, path: components/monitoring }
    - { name: ingress, path: components/ingress }
    - name: ingress-tls
      path: components/ingress-tls
      requires: [ingress]
      externalRequires:
        - {
            kind: WebhookCertProvider,
            name: cert-manager,
            issuerKind: ClusterIssuer,
          }
  externalRequires: [] # cluster preconditions not provided by this package
  provides: [] # cluster-scope resources this package installs (CRDs, etc.)
  clusterSingleton: [] # leader-election leases this package claims
  externalManifests: [] # remote release-tarball manifests to fetch + merge
  inputs: [] # wizard prompts
  transformers: # one or more groups of function invocations
    - toolchain: Kubernetes/YAML
      whereResource: ""
      invocations:
        - name: set-namespace
          args: ["{{ .Namespace }}"]
```

See `examples/hello-app/` for a complete working package.

The `transformers:` list is resolved with Go `text/template` syntax —
`{{ .Namespace }}`, `{{ .Inputs.* }}`, `{{ .Selection.* }}`,
`{{ .Facts.* }}`, `{{ .Package.* }}` — then emitted as a
`ConfigHubTransformers` KRM function config that kustomize invokes
through the `install transformer` exec plugin. Each group's
`toolchain` and `whereResource` are applied per-group, so a single
chain can mutate raw Kubernetes manifests with `Kubernetes/YAML` and
AppConfig-carried files (`AppConfig/Properties`, `AppConfig/Env`, …)
in the same render. Components can carry their own `transformers:`
and `validators:` lists; they append to the package-wide chain only
when the component is selected.

## Plugin install

The binary doubles as a `cub` plugin. After publishing a release that includes
a platform binary at the path `bin/install`, install with:

```bash
cub plugin install confighub/installer
```

The `cub-plugin.yaml` at the repo root tells cub the entry point. Once
installed, the same commands work via `cub install ...`.

## Layout

```
.
├── cmd/installer/main.go       # CLI entry point
├── internal/
│   ├── cli/                    # cobra subcommands
│   ├── pkg/                    # package load + OCI pull (oras-go)
│   ├── bundle/                 # deterministic tarball for `install package`
│   ├── selection/              # required-deps closure + conflict detection
│   ├── wizard/                 # interactive + non-interactive answer collection,
│   │                           # prior-state load, schema diff for upgrades
│   ├── collector/              # in-package fact collectors run by the wizard
│   ├── render/                 # kustomize compose + chain execution + split,
│   │                           # --set-image (kustomize edit) + image extraction
│   ├── deps/                   # SemVer resolver + lock writer
│   ├── upload/                 # discover Spaces, build/split installer-record,
│   │                           # write upload.yaml, intra-Space link inference
│   ├── diff/                   # plan compute (cub list + dry-run mutations) +
│   │                           # apply (with ChangeSet) + image footer
│   ├── changeset/              # cub changeset open + restore-command formatter
│   ├── cubctx/                 # active cub context (org / server) + sanity check
│   └── sign/                   # cosign sign + verify
├── pkg/api/                    # Package, Selection, Inputs, FunctionChain,
│                               # Lock, Upload schemas
├── packages/                   # "published" packages bundled in this repo
│   ├── kubernetes-resources/   # 11 canonical resource templates with
│   │                           # per-type defaults (used by `install new`)
│   └── worker/                 # ConfigHub bridge worker
├── examples/                   # test fixtures for the e2e + unit tests
│   ├── hello-app/              # single-package end-to-end test package
│   ├── example-base/           # multi-package: shared base
│   └── example-stack/          # multi-package: depends on example-base
├── docs/                       # design + implementation plans (see below)
└── cub-plugin.yaml             # cub plugin manifest
```

The `packages/` subdirectory holds packages that are intended for
publication; we'll move them to a separate repo as the catalog
grows. `examples/` stays in this repo as test fixtures the e2e and
unit tests exercise.

## User docs

For people using the installer (writing packages, installing them, or
managing day-2 changes). These are how-to and reference; the design
docs below explain _why_ the installer works the way it does.

- [Author guide](docs/author-guide.md) — for package authors. Schema
  reference for `installer.yaml`, file organization, how the install
  pipeline consumes your declarations, authoring best practices,
  publishing, signing, and version-to-version evolution.
- [Author tutorial](docs/author-tutorial.md) — hands-on walkthrough
  of building a small package from an empty directory to a signed OCI
  artifact. ~30 minutes. Read this before the author guide if you
  prefer learning by example.
- [Consumer guide](docs/consumer-guide.md) — for operators consuming
  packages. Find a package, install it, make day-2 changes, upgrade,
  revert. Plus a troubleshooting section for the common errors.

## Design docs

For contributors to the installer itself. If you're using or
authoring packages, the user docs above are what you want.

- [Design principles](docs/principles.md) — the seven principles the
  installer is anchored to (package files are read-only; spec is the
  round-trippable source of truth; two layers of override; optimize
  for the zero-override case; image management; defer to ConfigHub
  for what ConfigHub does well; configuration as data, not templates).
- [Package and dependency management](docs/package-management.md) +
  [implementation plan](docs/package-management-plan.md) — spec and
  phased build plan for bundling, OCI publish, dependency declaration
  and resolution, and signing. Phases 0–8 shipped.
- [Day-2 lifecycle: interactive wizard, plan, update, upgrade](docs/lifecycle.md)
  - [implementation plan](docs/lifecycle-plan.md) — spec and phased
    build plan for the interactive wizard, prior-state re-entry, plan
    vs ConfigHub, ChangeSet-wrapped update, staged upgrade with
    schema-diff, and `--set-image` overrides. Phases A–E shipped.
- [Kustomize transformer plugin and AppConfig support](docs/transformer.md)
  — design for the `install transformer` exec plugin (folds the
  function chain into kustomize), durable `out/compose/`,
  component-scoped function-chain mixins, and AppConfig support via
  `configMapGenerator` round-trip. Not yet implemented.

## Multi-package example

`examples/example-stack` depends on `examples/example-base`. To exercise
the full pipeline locally:

```bash
test/e2e/package-and-deps.sh
```

That script starts `registry:2`, pushes `example-base`, runs
`wizard → deps update → render`, asserts the output layout + digest
stability, and (when `INSTALLER_E2E_CONFIGHUB=1`) drives the full
day-2 flow against the live server:
`upload → plan (clean) → edit → plan (diff) → update → update (no-op) →
upgrade (edit) → upgrade-apply → upgrade (carry-forward) →
upgrade --set-image → upgrade (override carries forward) →
upgrade --set-image preflight rejection`. Spaces created with the
`installer-e2e-*` prefix are cleaned on exit.

## Roadmap

- Better Secrets support (currently we generate secrets during fact collection).
- `install preflight` — evaluate `externalRequires` against a live cluster.
- Automatic apply ordering (CRDs before custom resources, Namespace before
  namespaced resources, etc.) inferred from resource kind plus the existing
  link graph — no per-package phase declarations.
- Real packages: ArgoCD, Flux, llm-d, KServe, vLLM production stack
  (KubeRay and Gateway API Inference Extension shipped — see `examples/`).
- TBD: Hooks, in-cluster and local.
- TBD: variant creation and promotion.
- TBD: support deploying via ArgoCD and Flux directly.

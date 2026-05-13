# installer

Kubernetes off-the-shelf component installer using
[configuration as data](https://docs.confighub.com/background/config-as-data/).

This tool is intended to play the role of an
[installer wizard](https://www.revenera.com/install/products/installshield/installshield-tips-tricks/what-is-an-installation-wizard)
and a [package dependency manager](https://medium.com/@sdboyer/so-you-want-to-write-a-package-manager-4ae9c17d9527).

As with installer wizards for systems other than Kubernetes, changes to detailed default
settings are deferred until after installation. Configuration as data makes this possible
by storing the configuration data rather than re-rendering from scratch. That decouples
[configuration authoring](https://docs.confighub.com/guide/authoring-config/) from
[configuration editing](https://itnext.io/configuration-editing-is-imperative-fa9db379fbe4).

An installer should only present the minimal number of high-level decisions, such as
which components to install and where to install them. To simplify the component decision,
it is recommended to offer a working default selection of components. In general, it is
recommended to set reasonable defaults as much as possible.

For cases where installation decisions depend on hardware, operating system, networking,
or other details of the deployment Target, we plan to add a mechanism for retrieving discovered
Target facts.

This tool renders kustomize-based packages — wrapped with an `installer.yaml`
manifest that declares components, dependencies, inputs, and a function chain —
into per-resource Kubernetes YAML, customized at install time with ConfigHub
functions executed locally via the SDK. Output goes to plain YAML files that
can be uploaded to ConfigHub for delivery via ArgoCD, Flux, or direct apply.

The "code" lives in the installer (kustomize composition + ConfigHub function
chain). The "config" stays as data (literal YAML in ConfigHub Units). For post-installation
customization, [ConfigHub's function suite](https://docs.confighub.com/guide/functions/#frequently-used-functions) includes functions for changing
commonly changed Kubernetes resource properties, such as `set-container-image`,
`set-container-resources`, `set-replicas`, and `set-env-var`, and general-purpose
editing functions, such as `yq-i`, `set-string-path`, `delete-path`, and `set-starlark`.

Why not just [kustomize](https://kustomize.io), or [kpt](https://kpt.dev)? Neither tool
was really designed to be an installer. A lot was learned from kustomize and kpt, but
starting afresh made it easier to experiment with different design choices.

## Status

First-iteration wedge. Working: package format, local + OCI pull, non-
interactive wizard, kustomize composition, function chain execution, per-
resource splitting, deterministic output. Stubbed: interactive wizard,
preflight (cluster-side constraint checks), plan (diff vs. previous render or
ConfigHub), upload (currently shells out to `cub unit create` per file; the
cleaner `cub unit create -f <dir>` bulk mode is a planned cub addition).

## Build

```bash
go build -o bin/installer ./cmd/installer
```

`installer` shells out to `kustomize` for `kustomize build`. Install it from
[kubernetes-sigs/kustomize](https://kubectl.docs.kubernetes.io/installation/kustomize/)
or `brew install kustomize`.

## Quick start

End to end against the included example, no ConfigHub server required:

```bash
# 1. Inspect what's in the package.
bin/installer doc ./examples/hello-app

# 2. Wizard: pick base + components, supply inputs. Writes
#    /tmp/hello/out/spec/{selection,inputs}.yaml.
bin/installer wizard ./examples/hello-app \
  --work-dir /tmp/hello \
  --non-interactive \
  --select monitoring --select ingress \
  --input namespace=demo \
  --input image=nginxdemos/hello:latest \
  --input hostname=greet.example.com

# 3. Render: composes the kustomization, runs kustomize, runs the function
#    chain, writes one file per resource to /tmp/hello/out/manifests/.
bin/installer render /tmp/hello

# 4. (later) Upload to ConfigHub. Today this requires the cub CLI on PATH
#    and creates one Unit per file. A future bulk mode will batch this.
bin/installer upload /tmp/hello --space my-greeter
```

The wizard's `--select` is closed under each component's `requires:` list, so
selecting `ingress-tls` automatically pulls in `ingress`. Conflicts and
`validForBases` are enforced at solve time.

## Working directory layout

After `wizard` and `render`, the working dir looks like:

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
    └── spec/                 # the "installer record" (also uploadable as Units)
        ├── selection.yaml    # base + closure-resolved components
        ├── inputs.yaml       # validated wizard answers
        ├── function-chain.yaml   # the resolved chain that ran
        └── manifest-index.yaml   # filename → kind/name/namespace
```

The two spec docs (`selection.yaml`, `inputs.yaml`) are the load-bearing inputs
to re-render: edit them, re-run `installer render`, get a deterministic new set
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
  inputs: # wizard prompts
    - { name: namespace, type: string, default: my-app }
    - { name: app_name, type: string, required: true }
  functionChainTemplate: # one or more groups of function invocations
    - toolchain: Kubernetes/YAML
      whereResource: "ConfigHub.ResourceType = 'v1/Namespace'"
      invocations:
        - name: set-name
          args: ["{{ .Inputs.namespace }}"]
    - toolchain: Kubernetes/YAML
      whereResource: ""
      invocations:
        - name: ensure-namespaces
        - name: set-namespace
          args: ["{{ .Inputs.namespace }}"]
    - toolchain: Kubernetes/YAML
      whereResource: "ConfigHub.ResourceType = 'apps/v1/Deployment'"
      invocations:
        - name: set-container-image
          args: ["app", "{{ .Inputs.image }}"]
```

See `examples/hello-app/` for a complete working package.

The function chain template uses Go `text/template` syntax with `.Inputs`,
`.Selection`, and `.Package` in scope. Each group's `toolchain` and
`whereResource` are applied per-group (different groups can target different
toolchains — e.g., `Kubernetes/YAML` for manifests, `AppConfig/Properties` for
shipped config files).

## Plugin install

The binary doubles as a `cub` plugin. After publishing a release that includes
a platform binary at the path `bin/installer`, install with:

```bash
cub plugin install confighubai/installer
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
│   ├── selection/              # required-deps closure + conflict detection
│   ├── wizard/                 # non-interactive answer collection
│   └── render/                 # kustomize compose + chain execution + split
├── pkg/api/                    # Package, Selection, Inputs, FunctionChain schemas
├── examples/
│   └── hello-app/              # end-to-end test package
└── cub-plugin.yaml             # cub plugin manifest
```

## Roadmap

- Bundling
- Secrets
- Use for worker installation
- Interactive wizard (e.g., `survey`).
- `installer plan` — diff next render vs. previous render and vs. ConfigHub.
- `installer preflight` — evaluate `externalRequires` against a live cluster.
- Automatic apply ordering (CRDs before custom resources, Namespace before
  namespaced resources, etc.) inferred from resource kind plus the existing
  link graph — no per-package phase declarations.
- Real packages: llm-d, KServe, vLLM production stack
  (KubeRay and Gateway API Inference Extension shipped — see `examples/`).
- AppConfig support.
- Change of selections and inputs
- TBD: Hooks
- TBD: upgrade
- TBD: variant creation

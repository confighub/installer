# Kustomize transformer plugin and AppConfig support — Design

Status: design. Not implemented. Phased plan at the bottom; task tracking
lives outside this doc.

Companion to [`principles.md`](./principles.md) (configuration as data,
two layers of override) and [`lifecycle.md`](./lifecycle.md) (the
plan / update / upgrade loop). This doc covers two changes:

1. Folding the ConfigHub function chain into kustomize as an exec
   transformer plugin instead of running it as a post-`kustomize build`
   step.
2. Supporting `AppConfig/*` toolchains in installer packages by routing
   them through kustomize's `configMapGenerator`.

The two are intertwined: AppConfig only makes sense if the function
chain can operate on it from inside the kustomize pipeline.

## Goals

- Run the function chain and validators as a kustomize transformer
  plugin. The plugin is a real, non-hidden subcommand of `installer`
  so anyone can use it standalone against raw kustomize.
- Make `out/compose/` durable, not a temp dir. Operators can
  reproduce a render byte-for-byte with `cd out/compose && kustomize
  build`.
- Let components carry their own function chains (mixins) in the
  kustomize-native way: a KRM function config listed under the
  component's own `kustomization.yaml` `transformers:`.
- Support `AppConfig/*` toolchains in the function chain without
  introducing a new KRM wrapper kind. AppConfig content stays in
  ConfigMaps in the rendered tree; the transformer reverses the
  ConfigMap → AppConfig content boundary in-process, mutates, and
  writes back.
- At upload, materialize annotated ConfigMaps into the right
  ConfigHub triple: AppConfig Unit + ConfigMapRenderer Target +
  Kubernetes/YAML Unit, linked via existing intra-Space link
  inference.

## Non-goals

- Generalized templating. The variable surface stays restricted to
  `.Namespace`, `.Inputs`, `.Selection`, `.Facts`, `.Package` with
  `missingkey=error`. Same constraint everywhere we accept
  templated text.
- A custom KRM kind to ferry non-Kubernetes config through KRM
  function pipelines. See [kpt issue
  3118](https://github.com/kptdev/kpt/issues/3118) — the kustomize
  community never settled the question. We sidestep it by piggybacking
  on `configMapGenerator`.
- Secrets. The current `out/secrets/` opt-out path stays; secrets
  are never uploaded. A better secrets story is on the roadmap.

## Background

Today `internal/render/render.go` shells out to `kustomize build`,
then runs the function chain in-process via
`funcimpl.NewStandardExecutor`. Authors declare the chain in
`installer.yaml.spec.transformers` and validators in
`installer.yaml.spec.validators`. Both are resolved against installer
state with a restricted Go template pass (`resolveChainTemplate`,
`resolveValidatorTemplate`) before execution.

Three constraints from the current setup carry into the new design:

- The function chain is part of the render contract — re-rendering
  must produce identical output for identical inputs. This rules out
  general templating.
- The chain runs as one in-process executor. Each group's output
  feeds the next. Subprocess fan-out per group would be a regression.
- `whereResource` is a ConfigHub filter expression (not CEL) —
  evaluated by the function executor against resources, with field
  references like `ConfigHub.ResourceType = 'apps/v1/Deployment'`.
  See [filter concepts](https://docs.confighub.com/markdown/background/concepts/filters.md).

## Design

### `installer transformer` subcommand

First-class CLI verb. Reads a KRM `ResourceList` from stdin, joins
`items` into a YAML doc stream, runs the chain encoded in
`functionConfig.spec.groups`, splits the result back into `items`,
writes the `ResourceList` to stdout. Validator failures emit
`ResourceList.results` with `severity: error` so kustomize fails the
build.

Two `functionConfig` kinds:

- `ConfigHubTransformers` — mutating; all groups run in one process
  call (preserves today's in-process executor pattern).
- `ConfigHubValidators` — non-mutating; rejects mutating functions
  before any invocation runs (mirrors current `runValidators`).

Both share the same shape:

```yaml
apiVersion: installer.confighub.com/v1alpha1
kind: ConfigHubTransformers   # or ConfigHubValidators
metadata:
  name: transformers
  annotations:
    config.kubernetes.io/function: |
      exec:
        path: ./installer-transformer.sh
spec:
  groups:
    - toolchain: Kubernetes/YAML
      whereResource: "ConfigHub.ResourceType = 'apps/v1/Deployment'"
      invocations:
        - name: set-container-image
          args: ["kuberay-operator", "quay.io/kuberay/operator:v1.6.1"]
```

`whereResource` is ConfigHub's filter syntax — passed through to the
function executor's `FunctionInvocationOptions.WhereResource`
unchanged.

The same binary is usable outside the installer. Drop a
`ConfigHubTransformers` config into any kustomization, reference it
from `transformers:`, and as long as `installer-transformer.sh` is
reachable from kustomize's working directory (PATH, a shipped
wrapper, or a symlink) it works against raw kustomize.

### Kustomize-driven render in installer

`compose.go` writes everything kustomize needs into a durable
`out/compose/` instead of a temp dir:

```
<work-dir>/out/
├── manifests/                 # per-resource non-sensitive output
├── secrets/                   # per-resource sensitive output (unchanged)
├── spec/                      # selection, inputs, facts, function-chain, manifest-index
└── compose/
    ├── kustomization.yaml     # synthesized: resources, transformers, validators, images
    ├── transformers.yaml             # resolved package-level ConfigHubTransformers
    ├── validators.yaml        # resolved ConfigHubValidators
    ├── installer-transformer.sh  # exec wrapper (one line, chmod +x)
    └── components/<name>/...  # pre-resolved component-scoped configs (if any)
```

`Render` rewrites `out/compose/` on every render. Same render is
reproducible by anyone with `installer-transformer.sh` on PATH (or
present alongside the kustomization in `out/compose/`):

```
cd <work-dir>/out/compose
kustomize build --enable-exec --enable-alpha-plugins
```

The exec wrapper exists because the KRM
`config.kubernetes.io/function` annotation's `exec.path` accepts no
args. Wrapper is one line:

```sh
#!/bin/sh
exec "$INSTALLER_BIN" transformer
```

`$INSTALLER_BIN` is baked in at render time pointing at the
currently-running installer binary, so wrapper and binary always
match. For users running raw kustomize without the installer, ship
the wrapper alongside `installer` in release artifacts.

#### Why all groups in one transformer pass

A subprocess per group would mean N round-trips of YAML parse +
serialize for the same in-process executor work we do today. One
`ConfigHubTransformers` with `spec.groups: [...]` keeps the
existing data flow: one subprocess, one parse, N executor invocations
sharing in-process state, one serialize. Groups still feed each
other through `ConfigData`, same as today's `runChain`.

#### Built-in transformer ordering

Kustomize runs its built-ins (`namePrefix`, `nameSuffix`,
`commonLabels`, `commonAnnotations`, `namespace`, `replacements`,
`configMapGenerator`, `secretGenerator`, `images:`) before
user-listed transformers, unless they're explicitly placed in the
`transformers:` list ([plugin orchestration
docs](https://kubectl.docs.kubernetes.io/guides/extending_kustomize/#plugin-orchestration)).
The installer relies on this: by default our `ConfigHubTransformers`
runs last, so it sees the post-built-in resources — including the
ConfigMaps that `configMapGenerator` produced. Authors who need to
interleave (e.g. mutate before `commonLabels` decorates) can
explicitly list our transformer earlier in their component's
`transformers:`.

### Hybrid templating

The package surface stays familiar; components get a kustomize-native
mixin path.

**Package-wide chain** stays in
`installer.yaml.spec.transformers`. Existing tests,
existing author muscle memory. Resolved by today's
`resolveChainTemplate`, emitted as `out/compose/transformers.yaml`.

**Component-scoped mixins.** Each component declaration in
`installer.yaml.spec.components` can carry optional `transformers:` and
`validators:` fields. They take the same FunctionGroup shape as the
package-wide list and only run when the component is selected.
Components contribute their groups in the order they appear in
`spec.components` so re-render is deterministic regardless of which
order the operator passed `--select`. All groups — package-wide and
component-scoped — are resolved together and emitted as a single
`out/compose/transformers.yaml`, so the in-process executor still
runs them in one kustomize subprocess.

```yaml
# packages/<pkg>/installer.yaml
spec:
  components:
    - name: observability
      path: components/observability
      transformers:
        - toolchain: Kubernetes/YAML
          whereResource: "ConfigHub.ResourceType = 'monitoring.coreos.com/v1/ServiceMonitor'"
          invocations:
            - name: yq-i
              args: ['.spec.namespaceSelector.matchNames = ["{{ .Namespace }}"]']
  transformers:
    - toolchain: Kubernetes/YAML
      invocations:
        - {name: set-namespace, args: ["{{ .Namespace }}"]}
```

Authors who'd rather use the kustomize-native pattern — declaring a
KRM function config inside the component directory and referencing
it from the component's own `kustomization.yaml` `transformers:`
list — can do that too against raw kustomize, pointing
`exec.path` at `installer-transformer.sh`. The installer doesn't
manage those files; they're the user's surface area and bypass the
installer's template-resolution pre-pass.

**Built-in `namespace:` transformer.** Not yet implemented — would
let authors who prefer kustomize's built-in `namespace:` field over
the `set-namespace` function write `namespace: "{{ .Namespace }}"`
directly in `kustomization.yaml`. That requires a templating
pre-pass over package `kustomization.yaml` files, with resolved
copies written to `out/compose/` (the package tree stays
read-only). Deferred; for now, package authors use
`spec.transformers` with `set-namespace`.

### AppConfig

#### Author contract

Authors use standard `configMapGenerator` and annotate it once with
the toolchain:

```yaml
configMapGenerator:
  - name: app-config
    files:
      - application.properties
    options:
      annotations:
        installer.confighub.com/toolchain: "AppConfig/Properties"

  - name: app-env
    envs:
      - .env
    options:
      disableNameSuffixHash: true
      annotations:
        installer.confighub.com/toolchain: "AppConfig/Env"
```

The annotation key is generalized to
`installer.confighub.com/toolchain`, not `appconfig-toolchain`, so
the same mechanism can roundtrip future non-Kubernetes toolchains
that have a natural ConfigMap (or equivalent) carrier.

Kustomize copies `options.annotations` from a `configMapGenerator`
entry onto the rendered ConfigMap, so the `toolchain` annotation
is what reaches the installer transformer (running as an exec
plugin) and the upload step (reading `out/manifests/`).

The installer transformer infers three more annotations from the
rendered carrier and injects them back onto the ConfigMap so
downstream consumers (upload, drift) see one canonical contract:

- `installer.confighub.com/appconfig-mode: file | env` — derived
  from the `data:` shape. `AppConfig/Env` always implies env mode
  unless the data looks file-shaped; everything else implies file
  mode.
- `installer.confighub.com/appconfig-source-key: <name>` —
  file-mode only. The `data:` key whose value is the raw config
  body. Skipped when there's a single `data:` key (the unambiguous
  case).
- `installer.confighub.com/appconfig-mutable: true | false` —
  derived from the rendered ConfigMap's name pattern. Kustomize
  appends a `-<10-char hash>` suffix iff `disableNameSuffixHash`
  is false (the generator default), so the regex
  `-[a-z0-9]{10}$` is a reliable signal. Matched → immutable
  (versioned ConfigMaps, each content change rolls a new one);
  unmatched → mutable (stable name, hash annotation on the
  consuming workload triggers restarts). The upload step reads
  this to set `RevisionHistoryLimit=0` on the renderer Target in
  the mutable case.

Authors don't write the mode, source-key, or mutable annotations
themselves; the transformer infers and injects them. Authors who
need to override the inference (rare: file-shaped `.env` content,
an ambiguous `data:` shape, or a basename that happens to end in 10
lowercase-alphanumeric chars) can set the annotation explicitly on
the generator entry and the transformer respects it.

We deliberately don't modify the package's `kustomization.yaml`
files — that would conflict with the read-only-package principle.
Injection at the transformer stage means the resolved annotations
ride into `out/manifests/` cleanly and the package tree stays
untouched.

#### Transformer round-trip

When a `FunctionGroup` declares `toolchain: AppConfig/*`, the
transformer:

1. Pre-filters `ResourceList.items` to ConfigMaps whose
   `installer.confighub.com/toolchain` annotation matches the
   group's `toolchain`. The group's `whereResource` then filters
   further over the carrier ConfigMap — kind, name, labels — using
   standard ConfigHub filter syntax. Intra-content filtering is
   the function's own job, via its arguments.
2. For each match, extracts the raw AppConfig content from `data:`:
   - file-mode: the value at `appconfig-source-key`.
   - env-mode: re-emit a `.env`-shaped doc from the flat key/value
     map.
3. Invokes the executor against that content as a single doc, with
   the group's invocations.
4. Writes the mutated content back into `data:`:
   - file-mode: replace the source-key value.
   - env-mode: re-parse the mutated `.env` into key/value pairs
     and replace the ConfigMap's `data:`.

Other kustomize transformers (`commonLabels`, `namespace`,
`namePrefix`, `replacements`) continue to decorate the ConfigMap
normally — the data round-trip is invisible to them.

Validators on AppConfig run the same way: extract, validate, no
write-back; failures become `ResourceList.results`.

#### Upload split

At upload, ConfigMaps with `installer.confighub.com/toolchain` are
split into four ConfigHub objects:

- **AppConfig Unit** (slug = `<carrier-name>`, matching the
  kustomize-generated ConfigMap name). Toolchain from the annotation.
  Data = the extracted raw file body. Day-2 source of truth in the
  native format. The ConfigMapRenderer bridge uses the Unit slug as
  the rendered ConfigMap's `metadata.name`, so the slug deliberately
  has no suffix — workloads can reference the carrier by its
  kustomize-generated name (e.g. `envFrom.configMapRef.name`) and the
  rendered ConfigMap will match.
- **ConfigMapRenderer Target** (slug `<carrier-name>-renderer`).
  Provider `ConfigMapRenderer`, toolchain from the annotation,
  livestate-type `Kubernetes/YAML`. Attached to the AppConfig Unit
  so applying it renders a ConfigMap in the cluster. Worker is
  `<space>/<--appconfig-worker>` (default `server-worker`). Options:
  - `AsKeyValue=true` iff mode=env AND toolchain=AppConfig/Env. The
    bridge ignores it for non-Env toolchains; we set it only where
    it's meaningful.
  - `RevisionHistoryLimit=0` iff `appconfig-mutable=true` (the
    transformer pre-pass infers this from kustomize's hash-suffix
    convention; see "Author contract" above). Mutable ConfigMaps
    update in place; immutable ones (the kustomize default) leave
    `RevisionHistoryLimit` at the bridge default so cub retains
    a few revisions for rollback.
- **Apply the AppConfig Unit** (`cub unit apply --wait`) right after
  creating it. The renderer Target's worker produces the rendered
  ConfigMap as the AppConfig Unit's live state. Doing the apply
  before the link is created (next step) means the link's initial
  MergeUnits pulls real content into the placeholder's Data — which
  link inference at the end of `uploadOnePackage` then reads to wire
  workload references (envFrom, volumes, etc.) into the placeholder.
- **Placeholder Kubernetes/YAML ConfigMap Unit** (slug =
  `<carrier-name>-rendered`). Body is empty at creation; populated by
  the live-merge link below using the live state from the
  just-applied AppConfig Unit. Inherits the upload-wide `--target`
  flag (typically a Kubernetes namespace target) so it applies into
  the same place as every other rendered manifest. The `-rendered`
  suffix avoids colliding with the AppConfig Unit (which owns the
  bare carrier name). Workloads still resolve to the rendered
  ConfigMap by `metadata.name` (= `<carrier-name>`, set by the bridge
  from the AppConfig Unit's slug), so intra-Space link inference
  (`internal/upload/links.go`) wires them into this placeholder via
  the merged content.
- **Live-merge link** (server-assigned slug via `-`).
  `--use-live-state --auto-update --update-type MergeUnits`. Pulls
  the rendered ConfigMap from the AppConfig Unit's live state into
  the placeholder's Data so the runtime ConfigMap name (with its
  hash suffix) flows through to the workload's
  `volumeMounts` / `envFrom` reference.
- **`cub function do set-namespace`** on the placeholder Unit, using
  the wizard's `Inputs.Spec.Namespace`. The ConfigMapRenderer bridge
  stamps `metadata.namespace=confighubplaceholder` on its live state
  (the placeholder is meant to be resolved by a namespace link at
  apply time), but the intra-Space link inference matches by
  `metadata.namespace` and a placeholder value never resolves. Stamping
  the real namespace onto the placeholder Unit here unblocks the
  inference so Deployments referencing the carrier by name link to it
  cleanly.

Known follow-up: `get-references` doesn't currently surface
`spec.template.spec.containers[*].envFrom.configMapRef.name` /
`envFrom.secretRef.name`, so a workload that consumes the AppConfig
solely through `envFrom` (no `volumes`/`volumeMounts`) won't get an
auto-inferred link to the placeholder Unit even with the namespace
fix above. Tracked separately.

The rendered ConfigMap YAML in `out/manifests/` is NOT uploaded as a
Unit — the renderer Target re-derives the ConfigMap from the
AppConfig Unit's content at apply time, and the placeholder + link
pair handles the namespace/reference wiring.

The `manifest-index.yaml` schema can later gain fields recording the
AppConfig source-key and toolchain so `update` / `upgrade` can
reason about ConfigMaps that have split provenance; deferred to a
follow-up.

#### Why not a custom wrapper kind

We considered defining a new KRM kind (e.g.
`installer.confighub.com/v1alpha1, kind: AppConfig`) that would
carry the raw file body through the kustomize pipeline, with the
ConfigMap synthesized at the end. Three problems:

1. Built-in kustomize transformers (`commonLabels`, `namespace`,
   `replacements`) only decorate Kubernetes resources by GVK. Our
   wrapper kind would be invisible to them, so any author wanting
   common-labels behavior on their AppConfig would have to
   re-implement it. ConfigMap-as-carrier gets the decoration for
   free.
2. Downstream consumers (other transformers, validators, KRM
   functions written elsewhere) would need to learn our kind.
   That's the kpt 3118 problem.
3. Two sources of truth: the wrapper and the synthesized
   ConfigMap. Diffing, upgrade, drift all get harder.

ConfigMap-as-carrier + reverse-on-extract pays a small cost
(re-parsing data on every AppConfig function group invocation) and
buys full kustomize compatibility.

### Built-in transformer interactions

We test all of these against AppConfig-bearing ConfigMaps:

- `images:` — already driven by `installer --set-image`. Works
  unchanged; ConfigHub functions see the post-image-set Deployment.
- `namespace:` — supported either via the `set-namespace` function
  in the chain or via the kustomize built-in with `{{ .Namespace }}`
  templating in `kustomization.yaml`.
- `commonLabels:` / `commonAnnotations:` — applied before the
  ConfigHub transformer. Transformer sees decorated resources.
  AppConfig ConfigMaps still extract cleanly because `data:` is
  untouched by label/annotation transforms.
- `namePrefix:` / `nameSuffix:` — applied to the rendered ConfigMap.
  The AppConfig Unit slug is derived from the
  `configMapGenerator.name` (the pre-prefix logical name), so the
  AppConfig slug stays stable across rename mutations.
- `replacements:` — applied before our transformer when in the
  default slot.
- `configMapGenerator:` / `secretGenerator:` — run before
  transformers, so AppConfig hashing and data-merging happen before
  the transformer extracts. `secretGenerator` is not annotated;
  secrets continue to flow into `out/secrets/` and are never
  uploaded.

## Phasing

1. **Lift chain execution. ✅** Moved `runChain`, `runValidators`,
   `parseFunctionArguments`, `ValidatorFailure`,
   `FormatValidatorFailures`, `decodeValidatorFailures` from
   `internal/render/chain.go` into a new `internal/chainexec/`
   package. Render and `installer vet` import it.
2. **`installer transformer` subcommand. ✅** Standalone-usable
   KRM Functions exec plugin. Two functionConfig kinds:
   `ConfigHubTransformers` and `ConfigHubValidators`.
3. **Kustomize-driven render with durable `out/compose/`. ✅**
   Single path: `Render` generates `out/compose/{kustomization,
   transformers, validators}.yaml` and a one-line
   `installer-transformer.sh` wrapper, then invokes `kustomize
   build --enable-exec --enable-alpha-plugins`. No legacy
   fallback; the in-process chain code path is gone.
4. **Component-scoped transformer mixins. ✅** Added optional
   `transformers:` and `validators:` to each entry in
   `spec.components`. Resolved alongside the package-wide chain in
   declaration order, emitted as a single `out/compose/transformers.yaml`.
5. **AppConfig annotation contract. ✅** Documented in
   author-guide.md; the transformer reads
   `installer.confighub.com/toolchain` from kustomize-copied
   generator annotations.
6. **AppConfig transformer round-trip + annotation injection. ✅**
   `installer transformer` runs a pre-pass that derives and injects
   `appconfig-mode` (file/env, from data: shape) and
   `appconfig-source-key`. Function groups with toolchain
   `AppConfig/*` extract from `data:`, invoke, write back. Both
   mutating and validating paths supported.
7. **AppConfig upload split. ✅** Detected ConfigMaps become a
   four-piece bundle: renderer Target, AppConfig Unit, placeholder
   Kubernetes/YAML ConfigMap Unit (slug matches the carrier name so
   intra-Space link inference works), and a live-state MergeUnits
   link from placeholder → AppConfig Unit. Behind
   `--appconfig-worker` (default `server-worker`).
8. **Cleanup.** Remove the AppConfig roadmap bullet from
   README.md once 5–7 land.

## Open questions

- `RevisionHistoryLimit` default for the immutable (hashed-name)
  case. 10? 5? Match the cub default. Decide before phase 7.
- Whether component-supplied transformer configs should be
  template-resolved at compose time (current plan) or via a small
  vars-KRM function injected upstream in the transformers list.
  Compose-time is simpler and preserves the principle of
  read-only package files; revisit if components ever need
  per-resource variable substitution that can't be hoisted.
- Whether to surface a CLI hint when an annotated `configMapGenerator`
  with `disableNameSuffixHash: false` is used by a workload that
  doesn't have `envFrom`/`volume` plumbed — i.e. catch the common
  "I forgot to wire the ConfigMap into the Pod" mistake at render
  time. Defer.

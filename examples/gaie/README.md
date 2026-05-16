# gaie

Gateway API Inference Extension (GAIE) CRDs for the ConfigHub installer.

## Upstream

CRDs are vendored from
[kubernetes-sigs/gateway-api-inference-extension](https://github.com/kubernetes-sigs/gateway-api-inference-extension)
at tag **v1.5.0**:

- `bases/default/crds/` — `config/crd/bases/`

The release artifact at
`gateway-api-inference-extension/releases/download/v1.5.0/manifests.yaml`
is the same four CRDs concatenated; we vendor the per-CRD files for
readability.

The EPP (Endpoint Picker), HTTPRoute, Gateway, and supporting RBAC live
in a Helm chart with sub-chart dependencies (`config/charts/inferencepool`)
and are NOT vendored here. Wire those up either by layering the upstream
Helm chart on top of these CRDs, or with a future ConfigHub installer
package that templates the chart at install time.

## Components

| name | what it adds | external requirements |
| --- | --- | --- |
| (base) | the four CRDs | — |
| `sample-pool` | minimal `InferencePool` + `InferenceObjective` | EPP `Service` named `sample-pool-epp`; model-server pods with label `app: sample-model-server` |

## Inputs

| name | default | notes |
| --- | --- | --- |
| `namespace` | `inference` | Only used by `sample-pool`; the base is cluster-scoped CRDs only. |

## Quick start

```bash
bin/install wizard ./examples/gaie \
  --work-dir /tmp/gaie \
  --non-interactive \
  --select sample-pool \
  --input namespace=inference

bin/install render /tmp/gaie
ls /tmp/gaie/out/manifests/
```

## Refresh

```bash
TAG=v1.5.0
BASE=https://raw.githubusercontent.com/kubernetes-sigs/gateway-api-inference-extension/$TAG/config/crd/bases
for f in inference.networking.k8s.io_inferencepools.yaml \
         inference.networking.x-k8s.io_inferencemodelrewrites.yaml \
         inference.networking.x-k8s.io_inferenceobjectives.yaml \
         inference.networking.x-k8s.io_inferencepoolimports.yaml; do
  curl -sfL "$BASE/$f" -o "bases/default/crds/$f"
done
```

Bump `installer.yaml`'s `metadata.version` to match.

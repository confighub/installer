# kuberay

KubeRay operator package for the ConfigHub installer.

## Upstream

Manifests are vendored from
[ray-project/kuberay](https://github.com/ray-project/kuberay) at tag **v1.6.1**:

- `bases/default/crds/` — `ray-operator/config/crd/bases/`
- `bases/default/operator/` — `ray-operator/config/{manager,rbac}/`
- `components/sample-cluster/raycluster.yaml` — `ray-operator/config/samples/ray-cluster.sample.yaml`

The only addition to the upstream files is `bases/default/namespace.yaml` (a
`Namespace` resource at the top of the kustomization so the operator's
namespace is created on apply). ConfigHub's `set-namespace` function renames
that `Namespace`, sets `metadata.namespace` on every namespaced resource, and
upserts `subjects[*].namespace` on the `ServiceAccount` subjects in the two
binding files — so the upstream YAML can be vendored without any further
edits.

## Components

| name | what it adds | external requirements |
| --- | --- | --- |
| (base) | operator Deployment, Service, RBAC, four CRDs, target Namespace | — |
| `sample-cluster` | a minimal `RayCluster` (1 head + 1 worker on `rayproject/ray:2.52.0`) | — |

## Inputs

| name | default | notes |
| --- | --- | --- |
| `namespace` | `kuberay-system` | Operator namespace. Also names the `Namespace` resource. |

The operator image is hardcoded to `quay.io/kuberay/operator:v1.6.1` in
`installer.yaml`'s function chain (upstream `manager.yaml` ships `image:
kuberay/operator` without a registry or tag, and the kuberay project doesn't
push to Docker Hub). Fork the package to point at a private mirror.

## Quick start

```bash
bin/install wizard ./examples/kuberay \
  --work-dir /tmp/kuberay \
  --non-interactive \
  --input namespace=kuberay-system

bin/install render /tmp/kuberay
ls /tmp/kuberay/out/manifests/
```

## Refresh

To pull a newer KubeRay release:

```bash
TAG=v1.6.1
BASE=https://raw.githubusercontent.com/ray-project/kuberay/$TAG/ray-operator/config
curl -sfL $BASE/crd/bases/ray.io_rayclusters.yaml -o bases/default/crds/ray.io_rayclusters.yaml
curl -sfL $BASE/crd/bases/ray.io_raycronjobs.yaml -o bases/default/crds/ray.io_raycronjobs.yaml
curl -sfL $BASE/crd/bases/ray.io_rayjobs.yaml -o bases/default/crds/ray.io_rayjobs.yaml
curl -sfL $BASE/crd/bases/ray.io_rayservices.yaml -o bases/default/crds/ray.io_rayservices.yaml
for f in manager.yaml service.yaml; do
  curl -sfL $BASE/manager/$f -o bases/default/operator/$f
done
for f in role.yaml role_binding.yaml leader_election_role.yaml \
         leader_election_role_binding.yaml service_account.yaml; do
  curl -sfL $BASE/rbac/$f -o bases/default/operator/$f
done
curl -sfL $BASE/samples/ray-cluster.sample.yaml \
  -o components/sample-cluster/raycluster.yaml
```

After refreshing, update `installer.yaml`'s `metadata.version` and the
hardcoded operator image tag in the `set-container-image` invocation to match
the new release. The vendored YAML doesn't need any post-fetch edits.

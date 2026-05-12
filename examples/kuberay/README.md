# kuberay

KubeRay operator package for the ConfigHub installer.

## Upstream

Manifests are vendored from
[ray-project/kuberay](https://github.com/ray-project/kuberay) at tag **v1.6.1**:

- `bases/default/crds/` — `ray-operator/config/crd/bases/`
- `bases/default/operator/` — `ray-operator/config/{manager,rbac}/`
- `components/sample-cluster/raycluster.yaml` — `ray-operator/config/samples/ray-cluster.sample.yaml`

Local edits to the vendored YAML:

- The `namespace: system` placeholder was stripped from `manager.yaml`; the
  function chain sets `metadata.namespace` from the wizard's `namespace`
  input.
- The `ServiceAccount` subjects in `role_binding.yaml` (ClusterRoleBinding)
  and `leader_election_role_binding.yaml` (RoleBinding) gained a placeholder
  `namespace: kuberay-system`. ConfigHub's `set-namespace` function only
  rewrites existing `subjects[*].namespace` paths — it doesn't add the field —
  so without this seed the chain would leave RBAC subjects unscoped.
- A `Namespace` resource was added at the top of the kustomization so the
  operator's namespace is created on apply.

## Components

| name | what it adds | external requirements |
| --- | --- | --- |
| (base) | operator Deployment, Service, RBAC, four CRDs, target Namespace | — |
| `sample-cluster` | a minimal `RayCluster` (1 head + 1 worker on `rayproject/ray:2.52.0`) | — |

## Inputs

| name | default | notes |
| --- | --- | --- |
| `namespace` | `kuberay-system` | Operator namespace. Also names the `Namespace` resource. |
| `operator_image` | `quay.io/kuberay/operator:v1.6.1` | Tag should match the CRDs vendored above. |

## Quick start

```bash
bin/installer wizard ./examples/kuberay \
  --work-dir /tmp/kuberay \
  --non-interactive \
  --input namespace=kuberay-system

bin/installer render /tmp/kuberay
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

After refreshing, reapply the local edits listed in "Upstream" above (strip
`namespace: system` from `manager.yaml`, add `namespace: kuberay-system` to
the `ServiceAccount` subjects in both binding files), and update
`installer.yaml`'s `metadata.version` and the default for `operator_image`.

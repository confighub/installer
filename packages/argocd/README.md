# argocd

[Argo CD](https://argo-cd.readthedocs.io/) installer package for the ConfigHub
installer.

## Upstream

Manifests are vendored from
[argoproj/argo-cd](https://github.com/argoproj/argo-cd) at tag **v3.4.2**
under `upstream/`:

| vendored | upstream |
| --- | --- |
| `upstream/base/` | `manifests/base/` |
| `upstream/cluster-rbac/` | `manifests/cluster-rbac/` |
| `upstream/crds/` | `manifests/crds/` |
| `upstream/ha/` | `manifests/ha/` |

`upstream/` is read verbatim — no patches, no in-place edits. The four
`bases/*/kustomization.yaml` files in this package each compose those
upstream trees the same way upstream's own `cluster-install/`,
`namespace-install/`, `ha/cluster-install/`, `ha/namespace-install/`
kustomizations do, but they also declare the version-pinned `images:`
block so `install setup --set-image` works. Resource counts match
upstream's pre-rendered `install.yaml` / `namespace-install.yaml` /
`ha/install.yaml` / `ha/namespace-install.yaml` byte-for-byte (modulo
namespace and image).

## Critical customization decisions

These are the small set of choices an operator makes at install time;
everything else is post-install ConfigHub mutation (`set-replicas`,
`set-container-image`, `set-env`, `yq-i` on `argocd-cm`, …).

| decision | how |
| --- | --- |
| install scope (cluster-admin vs namespace-only) | base selection: `cluster-install` vs `namespace-install` |
| HA or single-replica | base selection: `ha-cluster-install` / `ha-namespace-install` vs the non-HA pair |
| target namespace | `install setup --namespace <name>` (defaults to `argocd` per upstream convention) |
| image / version override | `install setup --set-image quay.io/argoproj/argocd=<ref>` |
| include CRDs | implicit in base choice — the `cluster-install` bases include them, the `namespace-install` bases don't |

Decisions explicitly deferred to post-install:

- `argocd-cm` contents (RBAC, repos, clusters, SSO, ignoreResourceUpdates)
- `argocd-cmd-params-cm` runtime flags (server insecure, log format, …)
- Per-component resource requests / limits, replica counts
- Dex, ApplicationSet, Notifications, Commit-server enable/disable
  (all are part of the upstream `base/` and ship enabled — disable by
  scaling the Deployment to 0 or deleting the Unit post-install)

## Bases

| name | resources | use when |
| --- | --- | --- |
| `cluster-install` (default) | 59 | ArgoCD deploys to the cluster it runs in |
| `namespace-install` | 50 | ArgoCD deploys only to external clusters via inputted creds |
| `ha-cluster-install` | 70 | production, same-cluster deploys (needs ≥3 nodes) |
| `ha-namespace-install` | 61 | production, external-cluster only (needs ≥3 nodes) |

The HA bases bundle [DandyDeveloper/charts redis-ha](https://github.com/DandyDeveloper/charts)
4.34.11 rendered as plain YAML (vendored as
`upstream/ha/redis-ha/chart/upstream.yaml`).

## Inputs

| name | default | notes |
| --- | --- | --- |
| (namespace) | `argocd` | passed via `install setup --namespace argocd` |

No declared inputs — namespace + the four-base choice + the optional
`--set-image` flag cover the install-time decisions. Operators who
want to tune anything else should `install upload` first and then
edit the resulting ConfigHub Units.

## Quick start

```bash
# Render against the default cluster-install base.
mkdir -p /tmp/argocd && cd /tmp/argocd
install setup \
  --pull ~/ConfigHub/installer/packages/argocd \
  --non-interactive \
  --namespace argocd

# Deploy.
kubectl create namespace argocd --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -n argocd -f out/manifests/

# Watch it come up.
kubectl -n argocd get pods -w
```

For HA on a multi-node cluster:

```bash
install setup --pull ~/ConfigHub/installer/packages/argocd \
  --base ha-cluster-install --namespace argocd --non-interactive
```

To pin a different image (e.g. mirror or patch version):

```bash
install setup --pull ~/ConfigHub/installer/packages/argocd \
  --non-interactive --namespace argocd \
  --set-image quay.io/argoproj/argocd=quay.io/argoproj/argocd:v3.4.2
```

## Refresh upstream

The vendored tree is a straight `cp` from a checkout of the upstream
repo at the desired tag. To refresh to a new release:

```bash
TAG=v3.4.2
SRC=~/ConfigHub/argo-cd                  # local clone of argoproj/argo-cd
DST=~/ConfigHub/installer/packages/argocd

(cd $SRC && git fetch --tags && git checkout $TAG -- manifests/)
rm -rf $DST/upstream
mkdir -p $DST/upstream
cp -R $SRC/manifests/base         $DST/upstream/
cp -R $SRC/manifests/ha           $DST/upstream/
cp -R $SRC/manifests/cluster-rbac $DST/upstream/
cp -R $SRC/manifests/crds         $DST/upstream/

# Bump the pinned image tag in each base:
for b in cluster-install namespace-install ha-cluster-install ha-namespace-install; do
  sed -i '' "s/newTag: v.*/newTag: $TAG/" $DST/bases/$b/kustomization.yaml
done

# Bump metadata.version in installer.yaml to match.
```

After refresh, re-run `kustomize build` against each base to confirm
the upstream tree still composes cleanly:

```bash
for b in cluster-install namespace-install ha-cluster-install ha-namespace-install; do
  kustomize build $DST/bases/$b > /dev/null && echo "$b ok"
done
```

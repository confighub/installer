# Author Tutorial: Build a Package From Scratch

Walks you through authoring a small installer package — a status
page service called `statusboard` — from an empty directory to a
signed OCI artifact. Takes ~30 minutes. Mirrors the shape of
`examples/hello-app` but is built fresh so you see every decision.

By the end you'll have:

- A working package with a base, two opt-in components, an input,
  and a function-chain template.
- An `images:` block so operators can override container tags
  without editing your source.
- A bundled `.tgz` ready to push to an OCI registry.

For the complete schema reference, see [author-guide.md](./author-guide.md).
For the doctrine the installer is anchored to, see
[principles.md](./principles.md).

## Prerequisites

- `installer` binary on PATH (or invoke `bin/installer` from a clone).
- `kustomize` on PATH.
- A scratch directory: `mkdir -p ~/scratch/statusboard && cd
  ~/scratch/statusboard`.

## Step 1: the minimum viable package

A package must have:

- `installer.yaml` (the manifest).
- One base directory containing a `kustomization.yaml`.

Create the manifest:

```bash
cat > installer.yaml <<'YAML'
apiVersion: installer.confighub.com/v1alpha1
kind: Package
metadata:
  name: statusboard
  version: 0.1.0
spec:
  bases:
    - name: default
      path: bases/default
      default: true
      description: A minimal status page service.
YAML
```

Create the base. We'll start with a single Namespace + Deployment +
Service:

```bash
mkdir -p bases/default
cat > bases/default/namespace.yaml <<'YAML'
apiVersion: v1
kind: Namespace
metadata:
  name: confighubplaceholder
YAML

cat > bases/default/deployment.yaml <<'YAML'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: statusboard
  namespace: confighubplaceholder
spec:
  replicas: 1
  selector:
    matchLabels: { app: statusboard }
  template:
    metadata:
      labels: { app: statusboard }
    spec:
      containers:
        - name: app
          image: nginxdemos/hello:plain-text
          ports:
            - containerPort: 80
YAML

cat > bases/default/service.yaml <<'YAML'
apiVersion: v1
kind: Service
metadata:
  name: statusboard
  namespace: confighubplaceholder
spec:
  selector: { app: statusboard }
  ports:
    - port: 80
      targetPort: 80
YAML

cat > bases/default/kustomization.yaml <<'YAML'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - namespace.yaml
  - deployment.yaml
  - service.yaml
YAML
```

Two notes:

- The Namespace name is `confighubplaceholder` — a sentinel. The
  function-chain template (which we'll add in step 3) rewrites it
  to whatever the operator passes via `--namespace`.
- The image is hard-coded to `nginxdemos/hello:plain-text`. We'll
  enable operator overrides via a kustomize `images:` block in
  step 4.

Verify the package parses:

```bash
installer doc .
```

You should see `statusboard 0.1.0` with the `default` base and no
components. Now let's exercise the install path locally without a
ConfigHub server:

```bash
installer wizard ./. \
  --work-dir /tmp/statusboard \
  --non-interactive \
  --namespace demo

installer render /tmp/statusboard
ls /tmp/statusboard/out/manifests/
# deployment-confighubplaceholder-statusboard.yaml
# namespace-confighubplaceholder.yaml
# service-confighubplaceholder-statusboard.yaml
```

Notice the manifests still say `confighubplaceholder`. We haven't
added the namespace-rewriting function yet.

## Step 2: a function chain that rewrites the namespace

The wizard accepts `--namespace`; we want the rendered manifests to
use that value instead of `confighubplaceholder`.

Add `spec.functionChainTemplate` to `installer.yaml`:

```yaml
# append to installer.yaml
  functionChainTemplate:
    - toolchain: Kubernetes/YAML
      whereResource: ""
      description: Set the namespace on every namespaced resource.
      invocations:
        - name: set-namespace
          args: ["{{ .Namespace }}"]
```

Re-render:

```bash
rm -rf /tmp/statusboard
installer wizard ./. --work-dir /tmp/statusboard --non-interactive --namespace demo
installer render /tmp/statusboard
ls /tmp/statusboard/out/manifests/
# deployment-demo-statusboard.yaml
# namespace-demo.yaml
# service-demo-statusboard.yaml
```

The filenames now reflect the chosen namespace, and the manifest
contents do too:

```bash
grep namespace /tmp/statusboard/out/manifests/deployment-demo-statusboard.yaml
#  namespace: demo
```

The chain template is Go `text/template` — `.Namespace` is the
value passed to `--namespace`, `.Inputs.<name>` are answers to your
declared inputs, `.Selection` is the resolved base + components, and
`.Facts` is the collector's output map (we don't have one yet).

## Step 3: a wizard input

Add a `replicas` input so operators can size the deployment without
editing the package:

```yaml
# append to installer.yaml under spec:
  inputs:
    - name: replicas
      type: int
      default: 1
      prompt: "Number of replicas"
      description: "How many statusboard pods to run."
```

Wire it into the function chain:

```yaml
# extend the existing functionChainTemplate group
  functionChainTemplate:
    - toolchain: Kubernetes/YAML
      whereResource: ""
      description: Set the namespace + replicas.
      invocations:
        - name: set-namespace
          args: ["{{ .Namespace }}"]
        - name: set-replicas
          args: ["{{ .Inputs.replicas }}"]
```

Re-render with a non-default value:

```bash
rm -rf /tmp/statusboard
installer wizard ./. --work-dir /tmp/statusboard --non-interactive --namespace demo --input replicas=3
installer render /tmp/statusboard
grep replicas /tmp/statusboard/out/manifests/deployment-demo-statusboard.yaml
#  replicas: 3
```

And confirm the default takes when `--input replicas=…` is absent:

```bash
rm -rf /tmp/statusboard
installer wizard ./. --work-dir /tmp/statusboard --non-interactive --namespace demo
installer render /tmp/statusboard
grep replicas /tmp/statusboard/out/manifests/deployment-demo-statusboard.yaml
#  replicas: 1
```

> **Why a `default:`?** Because operators of a well-authored package
> shouldn't have to answer prompts that have a sensible answer.
> [Principle 4](./principles.md#4-optimize-for-the-zero-override-case)
> — _optimize for the zero-override case._

## Step 4: declare an `images:` block for operator overrides

Operators frequently want to pin a specific image tag, mirror to an
internal registry, or bump a patch version — without hand-editing
your source. Declare a kustomize `images:` block in your base, and
they get `installer wizard --set-image` and `installer upgrade
--set-image` for free.

Edit `bases/default/kustomization.yaml`:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - namespace.yaml
  - deployment.yaml
  - service.yaml
# images: declares the names overridable by --set-image. The default
# values match what's hard-coded in deployment.yaml.
images:
  - name: nginxdemos/hello
    newName: nginxdemos/hello
    newTag: plain-text
```

Test with an override:

```bash
rm -rf /tmp/statusboard
installer wizard ./. \
  --work-dir /tmp/statusboard \
  --non-interactive --namespace demo \
  --set-image nginxdemos/hello=nginxdemos/hello:plain-text-v2
installer render /tmp/statusboard
grep image /tmp/statusboard/out/manifests/deployment-demo-statusboard.yaml
#  - image: nginxdemos/hello:plain-text-v2
```

The override is recorded in `out/spec/inputs.yaml` under
`spec.imageOverrides`, so it round-trips across upgrades:

```bash
grep -A 1 imageOverrides /tmp/statusboard/out/spec/inputs.yaml
# imageOverrides:
#   nginxdemos/hello: nginxdemos/hello:plain-text-v2
```

If you don't declare an `images:` block and an operator passes
`--set-image`, render fails fast with a useful message — `kustomize
edit set image` against an undeclared image would silently inject a
new block, which contradicts your contract as the package author.

## Step 5: an opt-in component

Components are kustomize Components (`kind: Component`) layered on
top of the base. We'll add a `monitoring` component that adds a
`ServiceMonitor`.

```bash
mkdir -p components/monitoring
cat > components/monitoring/servicemonitor.yaml <<'YAML'
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: statusboard
  namespace: confighubplaceholder
spec:
  selector:
    matchLabels: { app: statusboard }
  endpoints:
    - port: web
YAML

cat > components/monitoring/kustomization.yaml <<'YAML'
apiVersion: kustomize.config.k8s.io/v1alpha1
kind: Component
resources:
  - servicemonitor.yaml
YAML
```

Add it to `installer.yaml`:

```yaml
# add under spec:
  components:
    - name: monitoring
      path: components/monitoring
      description: Adds a ServiceMonitor for Prometheus scraping.
      default: true
      externalRequires:
        - kind: CRD
          name: servicemonitors.monitoring.coreos.com
          suggestedSource: oci://ghcr.io/.../kube-prometheus-stack
```

Two things to notice:

- `default: true` makes monitoring part of the wizard's `default`
  preset. Operators who say "I trust your defaults" get monitoring
  automatically.
- `externalRequires` declares that the cluster needs the
  ServiceMonitor CRD before this component can apply. The wizard
  surfaces this; `installer preflight` (when shipped) will probe
  the cluster.

Test:

```bash
rm -rf /tmp/statusboard
installer wizard ./. --work-dir /tmp/statusboard --non-interactive \
  --namespace demo --select monitoring
installer render /tmp/statusboard
ls /tmp/statusboard/out/manifests/
# adds: servicemonitor-demo-statusboard.yaml
```

Or via the `default` preset:

```bash
rm -rf /tmp/statusboard
installer wizard ./. --work-dir /tmp/statusboard --non-interactive \
  --namespace demo --components default
installer render /tmp/statusboard
ls /tmp/statusboard/out/manifests/
# also adds servicemonitor-demo-statusboard.yaml
```

## Step 6: a second component with a `requires:`

We'll add an `ingress` component, then an `ingress-tls` component
that depends on it.

```bash
mkdir -p components/ingress
cat > components/ingress/ingress.yaml <<'YAML'
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: statusboard
  namespace: confighubplaceholder
spec:
  rules:
    - host: statusboard.example.com
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: statusboard
                port: { number: 80 }
YAML

cat > components/ingress/kustomization.yaml <<'YAML'
apiVersion: kustomize.config.k8s.io/v1alpha1
kind: Component
resources:
  - ingress.yaml
YAML

mkdir -p components/ingress-tls
cat > components/ingress-tls/patch.yaml <<'YAML'
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: statusboard
  namespace: confighubplaceholder
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
spec:
  tls:
    - hosts: [statusboard.example.com]
      secretName: statusboard-tls
YAML

cat > components/ingress-tls/kustomization.yaml <<'YAML'
apiVersion: kustomize.config.k8s.io/v1alpha1
kind: Component
patches:
  - path: patch.yaml
YAML
```

Update `installer.yaml`:

```yaml
  components:
    - name: monitoring
      path: components/monitoring
      ...
    - name: ingress
      path: components/ingress
      description: Expose the service via an Ingress.
    - name: ingress-tls
      path: components/ingress-tls
      description: Annotate the Ingress to request a cert from cert-manager.
      requires: [ingress]
      externalRequires:
        - kind: WebhookCertProvider
          name: cert-manager
          issuerKind: ClusterIssuer
```

`requires: [ingress]` means selecting `ingress-tls` automatically
pulls in `ingress`. The wizard's solver does this closure and
errors on conflicts. Test:

```bash
rm -rf /tmp/statusboard
installer wizard ./. --work-dir /tmp/statusboard --non-interactive \
  --namespace demo --select ingress-tls
cat /tmp/statusboard/out/spec/selection.yaml
#   components: [ingress, ingress-tls]   # both selected
installer render /tmp/statusboard
```

## Step 7: try the interactive wizard

Everything we've done so far has been non-interactive (scripted via
flags). Try it interactively to see what an operator sees:

```bash
rm -rf /tmp/statusboard
installer wizard ./.
# Prompts:
#   Components: [minimal/default/all/selected]
#   Kubernetes namespace: <text>
#   Tweak any other inputs? [y/N]
```

The interactive wizard skips inputs that have a default and aren't
required, unless the operator answers yes to "Tweak any other
inputs?". This is Principle 4 in action — well-defaulted packages
ask one or two questions and ship.

## Step 8: package and publish

Build a deterministic tarball:

```bash
installer package ./. -o statusboard-0.1.0.tgz
# Wrote statusboard-0.1.0.tgz (sha256:bc4e...)
```

Two builds on two machines from the same source produce the same
sha256. Publish to a registry:

```bash
installer push statusboard-0.1.0.tgz oci://ghcr.io/myorg/statusboard:0.1.0
```

Operators can now:

```bash
installer inspect oci://ghcr.io/myorg/statusboard:0.1.0
installer pull oci://ghcr.io/myorg/statusboard:0.1.0 ./statusboard-pulled
```

For production releases, sign with cosign — keyed:

```bash
cosign generate-key-pair
installer push statusboard-0.1.0.tgz oci://ghcr.io/myorg/statusboard:0.1.0 \
    --sign --key cosign.key
```

Or keyless (Sigstore Fulcio + OIDC):

```bash
installer push statusboard-0.1.0.tgz oci://ghcr.io/myorg/statusboard:0.1.0 \
    --sign --keyless
```

Operators can configure `~/.config/installer/policy.yaml` to require
signatures on every pull.

## Step 9: release a 0.2.0

When you change the package, bump `metadata.version` to 0.2.0,
re-package, re-push. Operators upgrade with:

```bash
installer upgrade <work-dir> oci://ghcr.io/myorg/statusboard:0.2.0
installer upgrade-apply <work-dir>
```

The upgrade machinery diffs your old vs new schema:

- New input with default → silently adopted.
- New required input without default → operator prompted (or
  fail-fast in non-interactive mode).
- Removed input → silently dropped.
- Changed input type → upgrade fails, operator must re-run wizard.
- New `default: true` component → adopted only if operator's prior
  selection matched the old default preset.

See [author-guide.md → Versioning + upgrade
considerations](./author-guide.md#versioning--upgrade-considerations)
for the complete rules.

## What you have

```
~/scratch/statusboard/
├── installer.yaml                # the manifest
├── bases/
│   └── default/
│       ├── deployment.yaml
│       ├── kustomization.yaml    # with images: block
│       ├── namespace.yaml
│       └── service.yaml
└── components/
    ├── ingress/
    │   ├── ingress.yaml
    │   └── kustomization.yaml
    ├── ingress-tls/
    │   ├── kustomization.yaml
    │   └── patch.yaml
    └── monitoring/
        ├── kustomization.yaml
        └── servicemonitor.yaml
```

A ~150-line `installer.yaml` + a small kustomize tree. Operators
can install with one command, override images without editing your
source, choose components by name or by preset, and upgrade to your
next release with the schema-diff machinery handling the carry-
forward.

## Where to go next

- [author-guide.md](./author-guide.md) — schema reference for every
  field you didn't touch in this tutorial: `dependencies`,
  `provides`, `clusterSingleton`, `externalManifests`, `collector`,
  `validation`, `conflicts`, `replaces`, `bundleExamples`.
- [consumer-guide.md](./consumer-guide.md) — what operators do with
  what you ship. Read it once before you publish your first release.
- [principles.md](./principles.md) — the doctrine the installer is
  anchored to. Worth re-reading after you've shipped a few packages.

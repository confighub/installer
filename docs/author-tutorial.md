# Author Tutorial: Build a Package From Scratch

Walks you through authoring a small installer package — a status
page service called `statusboard` — from an empty directory to a
signed OCI artifact. Takes ~20 minutes. Built from the new author
shortcuts (`installer init` / `new` / `edit` / `vet`) so you author
~50 lines of YAML by the end instead of ~150.

By the end you'll have:

- A working package with a base, two opt-in components, an input,
  a `transformers:` chain, and the recommended validator chain.
- An `images:` block so operators can override container tags
  without editing your source.
- A bundled `.tgz` ready to push to an OCI registry.

For the complete schema reference, see [author-guide.md](./author-guide.md).
For the doctrine the installer is anchored to, see
[principles.md](./principles.md).

## Prerequisites

- `install` binary on PATH (or invoke `bin/install` from a clone).
- `kustomize` on PATH.
- `cub` on PATH and signed in (`cub auth login`).
- The `kubernetes-resources` package bootstrapped in your
  organization (one-time setup, see below).
- A scratch directory: `mkdir -p ~/scratch/statusboard-pkg && cd
  ~/scratch/statusboard-pkg`.

### One-time: bootstrap kubernetes-resources

`installer new` clones canonical resource templates from the
`kubernetes-resources` package in ConfigHub. Install it once per
organization:

```bash
cd <path-to-installer-repo>
installer wizard ./packages/kubernetes-resources \
    --work-dir /tmp/k8s-res \
    --non-interactive --namespace kubernetes-resources
installer render /tmp/k8s-res
installer upload /tmp/k8s-res --space kubernetes-resources
# Recorded kubernetes-resources install in ~/.confighub/installer/state.yaml
```

After this, `installer new` knows where to find templates without
asking. The bootstrap is per-organization; if you switch ConfigHub
contexts, re-bootstrap there.

## Step 1: scaffold the package

```bash
installer init .
# Initialized package "statusboard-pkg" at /Users/.../statusboard-pkg
#   - installer.yaml
#   - bases/default/
#   - components/
#   - validation/
```

`installer init` writes the manifest with one default base, a
`set-namespace` group under `spec.transformers`, and the recommended
validator chain (`vet-schemas`, `vet-merge-keys`, `vet-format`).
We'll fix the package name in a moment. Have a look at what was
created:

```bash
cat installer.yaml
# apiVersion: installer.confighub.com/v1alpha1
# kind: Package
# metadata:
#   name: statusboard-pkg
#   version: 0.1.0
# spec:
#   bases:
#     - name: default
#       path: bases/default
#       default: true
#   transformers:
#     - toolchain: Kubernetes/YAML
#       invocations:
#         - name: set-namespace
#           args: ['{{ .Namespace }}']
#   validators:
#     - toolchain: Kubernetes/YAML
#       invocations:
#         - name: vet-schemas
#         - name: vet-merge-keys
#         - name: vet-format
```

Rename the package and let `init` re-write the manifest with
`--force`:

```bash
installer init . --name statusboard --force > /dev/null
```

Now scaffold the resources. We want a Namespace, a Deployment, and
a Service. `installer new` clones each from the
`kubernetes-resources` package with operator customizations
applied:

```bash
installer new namespace statusboard
installer new deployment statusboard --image nginxdemos/hello:plain-text --port 80 --replicas 1
installer new service statusboard --port 80
```

Each writes a file under `bases/default/` and updates
`bases/default/kustomization.yaml`'s resources list. Have a look:

```bash
ls bases/default/
# deployment-statusboard.yaml
# kustomization.yaml
# namespace-statusboard.yaml
# service-statusboard.yaml

cat bases/default/kustomization.yaml
# resources:
#   - deployment-statusboard.yaml
#   - namespace-statusboard.yaml
#   - service-statusboard.yaml
```

Render and verify:

```bash
installer wizard ./. --work-dir /tmp/statusboard --non-interactive --namespace demo
installer render /tmp/statusboard
ls /tmp/statusboard/out/manifests/
# deployment-demo-statusboard.yaml
# namespace-demo.yaml
# service-demo-statusboard.yaml
```

The default validators ran during render — the output passed
`vet-schemas`, `vet-merge-keys`, and `vet-format`.

Two things to notice:

- Each resource file uses `confighubplaceholder` for `namespace`
  (a sentinel that `set-namespace` rewrites at install time —
  hence `namespace-demo.yaml` after render).
- The Deployment has all the recommended defaults baked in
  (resource requests, readiness/liveness/startup probes,
  `securityContext`, `automountServiceAccountToken: false`)
  because they were applied by the kubernetes-resources package's
  `transformers` chain at template-creation time.

## Step 2: a wizard input

Add a `replicas` input so operators can size the deployment without
editing the package. Use `installer edit add input`:

```bash
installer edit add input replicas \
    --type int --default 1 \
    --prompt "Number of replicas" \
    --description "How many statusboard pods to run."
```

Wire it into `spec.transformers`. The chain already has
`set-namespace` from `installer init`; we want to add `set-replicas`
after it. There's no `installer edit` for transformer entries yet,
so hand-edit `installer.yaml`:

```yaml
  transformers:
    - toolchain: Kubernetes/YAML
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

## Step 3: declare an `images:` block for operator overrides

Operators frequently want to pin a specific image tag, mirror to an
internal registry, or bump a patch version — without hand-editing
your source. Declare a kustomize `images:` block in your base, and
they get `installer wizard --set-image` and `installer upgrade
--set-image` for free.

Edit `bases/default/kustomization.yaml` to add an `images:` block.
`installer init` already left a comment showing the shape; add:

```yaml
images:
  - name: nginxdemos/hello
    newName: nginxdemos/hello
    newTag: plain-text
```

Or use `kustomize edit set image` from the base directory:

```bash
( cd bases/default && kustomize edit set image nginxdemos/hello=nginxdemos/hello:plain-text )
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

## Step 4: an opt-in component

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

Register it via `installer edit add component`:

```bash
installer edit add component monitoring \
    --path components/monitoring --default \
    --description "Adds a ServiceMonitor for Prometheus scraping."
```

Two things to notice:

- `--default` makes monitoring part of the wizard's `default`
  preset. Operators who say "I trust your defaults" get monitoring
  automatically.
- For the `externalRequires` declaration ("cluster needs the
  ServiceMonitor CRD before this component can apply") hand-edit
  `installer.yaml` and append it to the `monitoring` component
  entry — `installer edit` doesn't model nested
  externalRequires today.

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

## Step 5: a second component with `requires:`

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

Register both via `installer edit add component`:

```bash
installer edit add component ingress \
    --path components/ingress \
    --description "Expose the service via an Ingress."

installer edit add component ingress-tls \
    --path components/ingress-tls \
    --description "Annotate the Ingress to request a cert from cert-manager." \
    --requires ingress
```

(For the `externalRequires` on `ingress-tls`, hand-edit
`installer.yaml`.)

`--requires ingress` means selecting `ingress-tls` automatically
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

## Step 6: vet the package

The default validator chain (`vet-schemas`, `vet-merge-keys`,
`vet-format`) ran during render in steps 1–5. To re-run validators
against the existing render — without re-running kustomize — use
`installer vet`:

```bash
installer vet /tmp/statusboard
# Vetting N resource(s) under /tmp/statusboard/out/manifests against 1 validator group(s)...
# All validators passed.
```

This is the right command after you edit the validator list (e.g.,
add `vet-images` to enforce an image registry) and want to check
the existing render before re-rendering.

You can also tighten the validator list now. For example, add
`vet-placeholders` to fail render if any `confighubplaceholder`
sentinel survives. Hand-edit `spec.validators` in `installer.yaml`
or use the cub function list to discover the full menu:

```bash
cub function list --where "Validating = TRUE" --toolchain Kubernetes/YAML
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

For production releases, sign with `installer sign` after push.
Keyless (Sigstore Fulcio + Rekor) is the default — an interactive
OIDC flow opens a browser; `--yes` skips cosign's TTY confirmation
prompt for CI:

```bash
installer sign oci://ghcr.io/myorg/statusboard:0.1.0 --yes
```

Or keyed:

```bash
cosign generate-key-pair
installer sign oci://ghcr.io/myorg/statusboard:0.1.0 --key cosign.key
```

Requires the `cosign` binary on PATH (override via
`INSTALLER_COSIGN_BIN`). Operators can configure
`~/.config/installer/policy.yaml` to require signatures on every
pull.

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
~/scratch/statusboard-pkg/
├── installer.yaml                # the manifest
├── bases/
│   └── default/
│       ├── deployment-statusboard.yaml   # cloned from kubernetes-resources
│       ├── kustomization.yaml            # with images: block
│       ├── namespace-statusboard.yaml    # cloned from kubernetes-resources
│       └── service-statusboard.yaml      # cloned from kubernetes-resources
├── components/
│   ├── ingress/
│   │   ├── ingress.yaml
│   │   └── kustomization.yaml
│   ├── ingress-tls/
│   │   ├── kustomization.yaml
│   │   └── patch.yaml
│   └── monitoring/
│       ├── kustomization.yaml
│       └── servicemonitor.yaml
└── validation/                  # left empty in this tutorial
```

A ~50-line `installer.yaml` + a small kustomize tree. The
Deployment came pre-loaded with resource requests, three probes,
securityContext, and `automountServiceAccountToken: false` from
`kubernetes-resources` — you didn't author or copy any of that.
Operators can install with one command, override images without
editing your source, choose components by name or by preset, and
upgrade to your next release with the schema-diff machinery handling
the carry-forward.

## Where to go next

- [author-guide.md](./author-guide.md) — schema reference for every
  field you didn't touch in this tutorial: `dependencies`,
  `provides`, `clusterSingleton`, `externalManifests`, `collector`,
  `validation`, `conflicts`, `replaces`, `bundleExamples`.
- [consumer-guide.md](./consumer-guide.md) — what operators do with
  what you ship. Read it once before you publish your first release.
- [principles.md](./principles.md) — the doctrine the installer is
  anchored to. Worth re-reading after you've shipped a few packages.

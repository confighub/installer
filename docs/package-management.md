# Package and Dependency Management — Design

Status: design — not yet implemented. The current installer ships with `pull`
(local + OCI) and a wizard/render/upload pipeline. This document covers
_bundling_, _publishing_, and _dependency resolution_ for installer packages,
none of which are implemented yet beyond `pull`.

## Goals

- Distribute installer packages as immutable OCI artifacts that round-trip
  through `package` → `push` → `pull` → `wizard` → `render` without ever
  re-rendering at distribution time.
- Let one package declare hard dependencies on other installer packages, with
  SemVer constraints, conflicts, and provides — resolved into a lock that
  pins each dep to a digest.
- Render a dependency tree into one ConfigHub Space per dependency, so each
  package keeps its own ownership and apply-time identity.
- Make every install reproducible: same source + same lock + same inputs =
  byte-identical rendered output.

## Non-goals

- **Hooks and triggers.** Deferred. We expect to need two kinds eventually:
  locally executed extensions (fact collection, secret materialization — the
  current `collector` hook is the seed of this) and in-cluster hook jobs
  similar to Helm's `pre-install` / `post-install`. The schema below leaves
  room for both but does not define them.
- **Helm chart interop as a dependency type.** Helm cannot itself drive this
  installer because Helm packages have no way to specify a post-renderer, so
  there is no symmetric path. We can still _pull_ a chart with `installer pull`
  as a convenience (existing behavior), but `dependencies:` entries cannot
  point at Helm charts.
- **Kustomizer-style render-on-push.** Kustomizer's `push` renders first; ours
  must not, because the function chain _is_ part of the package and runs at
  wizard time against user-supplied inputs.
- **Variants.** The Space layout has converged on one Space per component per
  variant/environment, but the variant problem itself is out of scope here.
  Bases + components + inputs cover today's needs.
- **Server-side search/catalog.** OCI `_catalog` support is uneven across
  registries. Discovery is convention + `installer list`. Curated indexes can
  be added later if needed.

## What a package is

A package is the _source tree_ the wizard and renderer read from:

```
my-pkg/
├── installer.yaml        # the Package manifest (pkg/api.Package)
├── bases/
├── components/
├── validation/           # optional, surfaced by `installer doc`
├── collector             # optional executable; produces facts + secrets
└── examples/
```

It is **never** rendered before distribution. Bundling is a deterministic tar
of the source tree. This is the same shape as a Helm chart source — different
from Kustomizer artifacts, which are post-render YAML.

## OCI artifact format

Push one OCI artifact per published version, with a media type that is
unmistakably ours so other tools (Helm, Kustomizer, generic OCI clients) do
not misinterpret it.

| Slot           | Value                                                                                                                                                                                  |
| -------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `artifactType` | `application/vnd.confighub.installer.package.v1+json`                                                                                                                                  |
| Config blob    | Computed metadata document (see below). `mediaType: application/vnd.confighub.installer.package.config.v1+json`                                                                        |
| Single layer   | `package.tar.gz` of the source tree. `mediaType: application/vnd.confighub.installer.package.tar+gzip`                                                                                 |
| Annotations    | `org.opencontainers.image.title`, `…version`, plus `installer.confighub.com/{name,version,kubeVersion,installerVersion,provides,dependencies}` summaries for cheap registry-listing UX |

The config blob is the parsed `installer.yaml` plus a header section computed
at bundle time:

```yaml
# computed header (not authored)
bundle:
  installerVersion: 0.4.0 # the installer CLI that produced this
  createdAt: 2026-05-13T17:02:00Z
  files:
    - { path: installer.yaml, sha256: ... }
    - { path: bases/default/..., sha256: ... }
    # one entry per file in the tar
  digest: sha256:... # of package.tar.gz
manifest: <the parsed installer.yaml>
```

Having the manifest in the config blob lets `installer inspect <ref>` and the
resolver read metadata (dependencies, provides, kubeVersion) without pulling
the layer.

### Pull-time discrimination

`installer pull` already handles a Helm-OCI shape heuristically (single `.tgz`
layer named like a chart). Native installer artifacts take a deterministic
path based on `artifactType`; the Helm fallback remains for convenience but
must not be exercised when the artifact identifies itself as ours.

### Determinism

The bundler MUST produce byte-identical tars from byte-identical source trees:

- Entries sorted by path.
- `mtime`, `uid`, `gid`, `uname`, `gname` zeroed; mode bits canonicalized.
- gzip with no original-filename and zeroed mtime.

Without this, signing and digest-pinning give weak guarantees.

## CLI surface

```
installer package <dir> [-o file.tgz]
installer push    <pkg.tgz|dir> oci://registry/repo:tag
installer pull    <ref>                                # exists; harden + add --digest
installer inspect <ref>                                # reads config blob only
installer list    oci://registry/repo                  # tags via /tags/list
installer tag     <src-ref> <dst-tag>
installer sign    <ref>                                # cosign keyed or keyless
installer verify  <ref>
installer login   <registry>
installer logout  <registry>
installer deps update <dir>                            # write installer.lock to out/spec/
installer deps build  <dir>                            # pre-fetch locked deps for offline render
installer deps tree   <dir>                            # render the resolved DAG
```

`installer search` is omitted in v1.

`pull` accepts an optional `@sha256:...` digest after the tag and fails the
fetch if the registry returns a different digest. Used by the resolver and by
operators who want pinned installs.

## Versioning and channels

- `metadata.version` is required and parsed as SemVer 2.0. Tag-per-version
  artifacts are immutable.
- Channel tags (`stable`, `latest`, `0`, `0.3`) are allowed by convention only.
  No registry-side mechanism enforces them; they are mutable aliases that
  authors maintain.
- The resolver always pins a channel to the digest it resolved to and records
  that digest in the lock, so re-renders are reproducible even if the channel
  later moves.

## Dependency management

The package today already has two "needs" concepts:

1. **Cluster-side preconditions** — `externalRequires` (CRDs, GatewayClass,
   WebhookCertProvider, Operator, …). Evaluated at preflight against a live
   cluster. This stays as is.
2. **Package-side dependencies** — other installer packages this package
   composes with. This is new.

The two are linked: a `dependencies:` entry can declare which of _this_
package's `externalRequires` it satisfies, and the dep's own `provides:` must
be compatible. If the user accepts the dep, those `externalRequires` are
considered met without a separate cluster probe.

### Why side-by-side, not merged

Each dependency renders into its **own** ConfigHub Space (one Space per dep,
matching the broader convergence on Space-per-component-per-variant/env). The
installer does not melt deps into the parent's manifests. Reasons:

- Preserves config-as-data: each rendered Unit has one provenance.
- Matches Debian's behavior — apt installs a tree of packages; each stays
  distinct.
- Upgrade ergonomics: a dep can be re-rendered without touching the parent's
  Units, and vice versa.
- ConfigHub Links express the cross-package relationships explicitly.

### Schema additions to `installer.yaml`

```yaml
spec:
  dependencies:
    - name: gateway-api # local handle
      package: oci://ghcr.io/confighubai/gateway-api # OCI ref, no tag
      version: "^1.2.0" # SemVer range
      selection: # pre-answers the dep's wizard
        base: default
        components: [crds, kgateway]
      inputs:
        namespace: gateway-system
      optional: false # default false
      whenComponent: ingress # only follow if parent component selected
      satisfies: # which of *this* package's externalRequires this dep covers
        - { kind: CRD, name: gateway.networking.k8s.io }
        - { kind: GatewayClass, capability: ext-proc }

  conflicts: # hard exclusion
    - package: oci://ghcr.io/foo/old-gateway
      version: "*"

  replaces: # smooth rename / supersession
    - package: oci://ghcr.io/foo/old-name
      version: "< 2.0.0"
```

`provides:` already exists and gains a second role: in addition to detecting
double-install conflicts at apply time, it is matched against parent
`externalRequires` during resolution so the resolver knows a dep covers a
precondition.

### Lock file

The lock is not stored in git. Per the broader design, all of `out/spec/*` —
`selection.yaml`, `inputs.yaml`, `function-chain.yaml`, `manifest-index.yaml`,
the new `lock.yaml`, and the package's `installer.yaml` itself — are combined
into a single multi-document YAML stream and uploaded as one
`Kubernetes/YAML` Unit (the "installer record" Unit). That Unit has **no
Target** because it is not deployed: it exists to reproduce a render. The
renderer reads from `out/spec/` locally; the upload step also writes the
record Unit alongside the rendered Units.

Lock contents:

```yaml
apiVersion: installer.confighub.com/v1alpha1
kind: Lock
metadata:
  name: <package name>
spec:
  package:
    name: llm-stack
    version: 0.3.0
    digest: sha256:...
  resolved:
    - name: gateway-api
      ref: oci://ghcr.io/confighubai/gateway-api:1.4.2
      digest: sha256:bbbb...
      requestedBy: [root]
      version: 1.4.2
      selection: { base: default, components: [crds, kgateway] }
    - name: cert-manager
      ref: oci://ghcr.io/confighubai/cert-manager:1.15.3
      digest: sha256:cccc...
      requestedBy: [gateway-api]
      version: 1.15.3
```

Workflow:

- `installer deps update <dir>` resolves the DAG, writes `out/spec/lock.yaml`.
- `installer render` requires a lock; if absent or stale (root manifest's
  declared deps disagree with the lock), it errors and instructs the user to
  re-run `installer deps update`.
- `installer deps build <dir>` pre-fetches each locked dep into a local
  `out/vendor/<name>@<version>/` so a subsequent render is fully offline.

### Resolver

Inspired by Cargo's resolver and apt's coherent-set behavior:

- One version per package name in the resolution set. Two parents asking for
  incompatible ranges of the same package → resolution failure with a clear
  report (chain → constraint → conflict, à la `cargo` errors).
- `optional: true` + `whenComponent: <name>` makes a dep follow only when the
  named component is selected in the parent's `Selection`. This is the same
  shape as Helm's subchart `condition:` and Debian's `Recommends`.
- `conflicts:` is hard: the resolver fails if any conflicting package appears
  anywhere in the DAG.
- `replaces:` lets a renamed package satisfy a request for the old name (used
  during migrations).
- Cycles are an error.

### Apply ordering across packages

ConfigHub Links + automatic apply-ordering (Roadmap item in the README) handle
ordering inside a Space. Across Spaces, the dep tree implies an order: a Space
is applied only after the Spaces of packages it depends on have converged. The
installer's upload step records this in the installer-record Unit and in
inter-Space Links; cross-Space ApplyGates enforce it at apply time.

### Cluster-side `externalRequires` still apply

Anything in `externalRequires` not satisfied by some dep's `provides` remains
a cluster precondition checked by `installer preflight`. This is what keeps
the model honest: declaring a dep is a _promise_ that the dep provides the
precondition; preflight verifies leftovers.

## Bundling rules

`installer package` MUST refuse to bundle:

- `*.env.secret` files anywhere in the tree (collector output; never published).
- Anything under `out/` (rendered artifacts; not source).
- Anything matched by `.installerignore` (optional, mirrors `.helmignore`).

`installer package` MUST include:

- `installer.yaml`, all referenced `bases/` and `components/` trees,
  `validation/`, the `collector` executable if declared, and any files
  reachable by kustomization references.

`externalManifests` (remote release-tarball CRDs already pinned by digest in
the manifest) are **not** embedded in the package tarball. They are fetched
at render time and verified against the declared digest. Embedding would
bloat artifacts and duplicate immutable upstream blobs.

## Compatibility metadata

```yaml
metadata:
  name: my-stack
  version: 0.3.0
  kubeVersion: ">= 1.28" # SemVer range, checked at resolve time
  installerVersion: ">= 0.2.0" # range against the CLI invoking render
  annotations:
    confighub.com/category: gateway
    confighub.com/maintainers: ...
```

The two `*Version` fields are evaluated at resolve time and at render time;
mismatch is an error with an upgrade hint. Annotations are advisory and
surfaced in `installer inspect` and `installer doc`.

## Signing

`installer push --sign` and `installer verify` use cosign (keyed or keyless).
When a local policy file is present (`~/.config/installer/policy.yaml`),
`pull` and `deps update` enforce verification before trusting an artifact's
digest.

## What this design does _not_ answer (yet)

- **Mirroring / air-gap workflows.** `installer deps build` produces a
  vendor tree, but a `installer mirror oci://src oci://dst` for moving an
  entire dep closure between registries is left for later.
- **Multi-arch packages.** Installer packages are platform-independent (they
  contain YAML and an optional `collector` script). The `collector` binary
  case is the only one where multi-arch might matter, and we can address it
  by shipping `collector` as a remote-fetched layer when needed.
- **Yanking.** Removing a published version. OCI registries vary in support;
  we will document "deprecate via annotations on a new tag" as the v1 story.
- **Hooks / triggers.** See Non-goals. Reserved for a follow-on design.

## Implementation order (suggested)

1. Deterministic bundler + `installer package` (no network).
2. `installer push` / `inspect` / `list` / `tag` against an OCI registry,
   reusing oras-go.
3. `installer.yaml` schema additions (`dependencies`, `conflicts`,
   `replaces`, satisfies-link on existing fields) — parse-only.
4. Resolver + `installer deps update` writing `out/spec/lock.yaml`.
5. Render wired to read the lock, fetch deps (cached via `deps build`), and
   render each into its own output subtree (`out/<dep-name>/manifests/`,
   `out/<dep-name>/spec/`).
6. Upload step extended to create one Space per dep and one installer-record
   Unit per package, with cross-Space Links for the dep relationships.
7. Signing.

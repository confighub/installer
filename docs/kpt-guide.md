# Post-install changes with kpt

The installer renders fully-materialized Kubernetes manifests to
`out/manifests/`. The recommended day-2 path is to upload them to
ConfigHub and reconcile with `installer upload` + `cub unit apply`:
ConfigHub's `--merge-external-source` preserves your post-install edits
across re-renders (see [Post-install ConfigHub
mutations](./consumer-guide.md#3-post-install-confighub-mutations)).

[kpt](https://kpt.dev) is an open-source alternative for the same job,
using git instead of ConfigHub. This guide is for two audiences:

- you already use kpt and want to manage the rendered output with it;
- you want to make post-install changes **without** standing up
  ConfigHub first — an alternative to `installer upload` and `cub unit
  apply`.

## What kpt gives you

kpt is a configuration-as-data tool that stores configuration in git.
The unit of distribution is a **package** — a directory of YAML with a
`Kptfile`. Fetching a package (`kpt pkg get`) clones it and records its
upstream; pulling a new version (`kpt pkg update`) does a structured,
schema-aware **3-way merge** (`resource-merge`) between the new upstream,
the original upstream, and your local edits.

That merge is the whole point here: it's the git-based analogue of
ConfigHub's `--merge-external-source`. You keep your post-install edits
in a separate clone, and when you re-render the base for an upgrade, kpt
reconciles the new render with your edits.

See the kpt package operations reference:
<https://kpt.dev/book/03-packages/>.

## The model

Three pieces, all in one git repo (your work-dir):

- **Base package** — `out/manifests/`, turned into a kpt package with a
  `Kptfile`. Every `installer render` rewrites it. This is the upstream.
- **Post-install clone** — `out/post-install/manifests/`, a kpt clone of
  the base. **You make your edits here**, never in the base.
- **Upgrade** — re-render the base, commit, then `kpt pkg update` in the
  clone merges the new render with your edits.

Because your edits live in a separate clone, re-rendering the base never
touches them; kpt merges the two on update.

## Setup (local — no remote needed)

`kpt pkg update` only needs the upstream's **committed** state — it
git-fetches into a temp dir and merges. It never reads your working tree
and doesn't need a *remote*, so the work-dir can be its own upstream. You
only commit; you never push.

```bash
# In the work-dir — the directory holding package/ and out/.
cd my-workdir
git init -b main
git add . && git commit -m "Initial render"

# Turn the rendered manifests into a kpt package.
cd out/manifests
kpt pkg init .                 # writes Kptfile (+ README.md, package-context.yaml)
cd ../..
git add . && git commit -m "kpt: init base package in out/manifests"

# Clone the base into out/post-install/manifests. The destination dir
# (post-install) must exist; kpt nests the package — named "manifests" —
# inside it. The ".git" in the path tells kpt where the repo ends and
# the package path begins (otherwise: "ambiguous repo/dir").
cd out
mkdir -p post-install
kpt pkg get "$(git -C .. rev-parse --show-toplevel)/.git/out/manifests" post-install
cd ..
git add . && git commit -m "kpt: clone base into out/post-install"
```

`out/.gitignore` (written by the installer) keeps `out/secrets/` out of
git; the base package is `out/manifests/` only, which never contains
rendered Secrets.

## Make post-install changes

Edit files under `out/post-install/manifests/` — by hand or with
`kpt fn eval` — and commit:

```bash
$EDITOR out/post-install/manifests/deployment-*.yaml   # e.g. scale, annotate
git add . && git commit -m "post-install: scale to 2 replicas"
```

Apply them to the cluster with `kpt live` — the kpt-native apply, which
tracks an inventory of what it applied so it can prune resources you've
since removed and report reconcile status (this is what replaces `cub
unit apply`):

```bash
kpt live init out/post-install/manifests    # once — records an inventory in the Kptfile
kpt live apply out/post-install/manifests   # apply, prune, and wait for status
```

(`kubectl apply -f out/post-install/manifests/` also works, but without
the inventory it won't prune deleted resources.) See
<https://kpt.dev/book/06-apply/>.

## Upgrade: re-render, then merge

```bash
# 1. Re-render the base (new package version, new inputs, image bump…).
#    --clean prunes resources dropped by the upgrade while PRESERVING the
#    base package's Kptfile, so out/manifests stays a valid kpt package.
installer setup --pull <new-ref> --work-dir my-workdir --clean
#    (or, to re-render in place without re-pulling: installer render --clean)
git add . && git commit -m "render <pkg>@<new-version>"

# 2. Merge the new render into your post-install clone.
cd out/post-install/manifests
kpt pkg update .               # strategy: resource-merge (3-way)
cd -
# Resolve any conflicts kpt reports, then commit the merged result.
git add . && git commit -m "post-install: merge <pkg>@<new-version>"
```

The merged clone has **both** the upgrade's changes and your post-install
edits. Re-apply it to the cluster as above.

## Versioning with tags

kpt tracks the upstream by git ref, so tag each rendered version and pull
the clone to a specific tag:

```bash
git tag v0.1.0                                  # at each rendered base version
kpt pkg update out/post-install/manifests@v0.1.0
```

kpt does **not** enforce semver on the tag, so you can append an extra
segment to version post-install iterations independently of the base —
e.g. tag your post-install commits `v0.1.0.1`, `v0.1.0.2` while the base
stays at `v0.1.0`.

## Portable / shared setup: a real git remote

The local setup records an absolute path as the package's upstream
(`upstream.repo: /abs/path/to/my-workdir/`), which only resolves on that
machine. To share the workflow — push the work-dir to a real remote and
let teammates run updates — point the upstream at the remote URL instead:

```bash
git remote add origin https://github.com/you/your-repo
git push -u origin main

cd out
mkdir -p post-install
# <pkg-dir> is the work-dir's path within the repo, if any.
kpt pkg get https://github.com/you/your-repo/<pkg-dir>/out/manifests post-install
```

Now the clone's `Kptfile` records the remote URL and `kpt pkg update`
fetches from it — so the upgrade loop becomes re-render → commit →
**push** → update. That extra push is the cost of portability; the
local setup avoids it.

## See also

- kpt docs: <https://kpt.dev>
- kpt package operations (`init` / `get` / `update`):
  <https://kpt.dev/book/03-packages/>
- kpt apply (`kpt live`): <https://kpt.dev/book/06-apply/>
- The integrated ConfigHub alternative: [Post-install ConfigHub
  mutations](./consumer-guide.md#3-post-install-confighub-mutations)

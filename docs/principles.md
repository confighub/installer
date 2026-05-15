# Design Principles

The principles the installer is designed around. Anchor decisions here when
something feels arbitrary; if a proposed change violates one of these,
either the change is wrong or the principle needs updating — name which.

## 1. Package files are read-only to consumers

A package is shipped by its author and consumed by an operator. The
operator never edits the package tree (`installer.yaml`, `bases/`,
`components/`, kustomize files). All change is driven through one of two
channels:

- **Install-time / re-render**: the wizard's generated spec files
  (`out/spec/{selection,inputs,facts}.yaml`). Editing these and
  re-rendering is the supported workflow for adjusting selections,
  inputs, and (via re-running the collector) facts.
- **Post-install**: ConfigHub mutations on the uploaded Units (e.g.,
  `cub function do set-container-image`, hand-edits via `cub unit
update`, ApplyGate-mediated approvals).

Why: a consumer-edited package tree is silently overwritten on the next
`installer pull` or `installer upgrade`. Worse, the edits are invisible
to anyone reading the package source. Routing every override through one
of the two supported channels keeps changes discoverable and survivable.

How to apply: if you find yourself reaching for `kustomize edit` against
a package directory or hand-editing a `bases/*/kustomization.yaml`,
stop. Either declare an input for it (and let the package's
`transformers` consume it), or do it post-install in ConfigHub.
The one allowed package-file mutation is `kustomize edit set image`
applied by the installer itself behind `--set-image` (see Principle 4).

## 2. Spec files are the round-trippable source of truth

Same package + same spec + same collector output = byte-identical
rendered Units. The spec layer is the only thing the installer
persists, and it must be sufficient to re-derive the rendered output
without consulting the cluster or ConfigHub.

The Upload doc (`out/spec/upload.yaml`) extends this: it records where
the spec was last uploaded so the wizard can re-enter from ConfigHub if
the local work-dir is lost. The installer-record Unit on ConfigHub
contains the full spec so a freshly cloned work-dir is recoverable.

How to apply: any new state the installer learns about an install goes
into a spec doc. Anything not in the spec must be derivable from it
plus the package.

## 3. Two layers of override, with clear lifetimes

- **Install-time** changes are made in spec files and re-render. They
  affect what the installer materializes. They are versioned with the
  installer's working directory; they survive `installer upgrade`.
- **Post-install** changes are made in ConfigHub on the materialized
  Units. They affect what ConfigHub serves to apply. They are
  preserved across re-render via `cub unit update --merge-external-source`,
  which only writes the paths that changed in the new render.

The two layers do not need to know about each other. A package author
asking "should this be an input or a post-install mutation?" should
think: is this a decision the consumer makes once at install time, or
something they will keep tuning across the install's lifetime? Post-install
is obviously the right approach for the second. Inputs could be used for the
first, but may not need to be, as discussed in the next section.

How to apply: when triaging a change request, name which layer it
belongs in. Don't add an input for something that's clearly a day-2
tuning concern; don't push every install-time decision into post-install
mutations and call the wizard simple.

## 4. Optimize for the zero-override case

We want to keep inputs as minimal as possible, similar to installing an
application on your phone or laptop. For kicking the tires, the defaults
should just work. Even for production use we want to rely mostly on defaults,
automatic discovery using the fact collection mechanism, and post-installation
customization. If there is a reasonable default for a field, and especially
if it a standard Kubernetes API field with functions available to discover and
modify the value, such as container images, then it should be just changed
post-install rather than adding an input parameter. Even application-specific
configuration properties should not necessarily be added as inputs. Provide
specifications of configurable properties instead.

Concretely:

- Inputs should declare `default:` whenever the author can name a
  reasonable one. The wizard skips defaults-having inputs in a
  presets-only flow.
- Components should declare `default: true` when the package author
  considers them part of the recommended install.
- The wizard's preset prompt (`minimal` / `default` / `all` / `selected`)
  exists to let an operator say "I trust your defaults" in one
  keystroke.
- Required inputs without defaults should be rare. If you have ten of
  them, your package needs a sensible default profile, not ten more
  prompts.

How to apply: when reviewing a package, count the prompts a typical
install hits. If the count is more than ~5, push back on the package
author to declare more defaults or fold inputs into the collector.

## 5. Image management: declare a kustomize transformer; use functions when changes are common

Container images are the most common day-2 change. The installer's
recommended pattern, in order of decreasing user friction:

1. **Package author declares a kustomize `images:` transformer** in the
   chosen base's `kustomization.yaml`. Operators override at install or
   upgrade time with `installer upgrade --set-image name=ref` (which
   runs `kustomize edit set image` against the package's working copy
   before render). Operators override post-install with `cub function
do set-container-image` on the uploaded Unit. This is the default
   recommendation.
2. **Package author declares image inputs and a `set-container-image`
   group in `transformers`** when image changes are expected
   to be common (per-component image registries, multi-arch by tag,
   image-by-URI rewrites). Inputs surface in the wizard; the function
   chain consumes them at render time.
3. **Operators use ConfigHub image functions post-install** —
   `set-container-image`, `set-container-image-reference`,
   `set-image-reference-by-uri`, `set-image-registry-by-registry` — for
   ad hoc changes that do not warrant re-render.

`installer plan` and `installer update` print a per-Space `Images:`
footer (built from `cub function do get-container-image '*'`) so the
operator can see the eventual image set without applying anything.

How to apply: package authors, default to (1). Reach for (2) only if
(1) is genuinely insufficient. Operators, prefer (1) for install/upgrade
time and (3) for post-install one-offs; only edit spec/inputs.yaml for
(2).

## 6. Defer to ConfigHub for what ConfigHub does well

The installer materializes Units. Everything downstream — apply,
ApplyGates, validation Triggers, drift reconciliation, ChangeSets,
promotion, rollback — is ConfigHub's job. The installer creates a
ChangeSet for `installer update` so updates are revertable, but it does
not run apply, does not author Triggers, and does not reconcile cluster
drift.

How to apply: when a feature request lands ("add a Trigger that blocks
:latest"), route it to the right ConfigHub skill rather than building it
into the installer. The installer's surface area should be: package +
spec → Units. Anything more is scope creep.

## 7. Configuration as data, not templates

Rendered output is literal Kubernetes YAML, one Unit per resource. The
function chain is the "code" that produces it; once produced, it does
not get re-templated. Re-render produces a new literal YAML, which
ConfigHub merges against the prior version.

This is the same doctrine as ConfigHub's
[config-as-data](https://docs.confighub.com/background/config-as-data/);
the installer is the upstream renderer for it.

How to apply: never ship templates as ConfigHub Unit bodies. Never
parameterize a Unit at apply time. If a Unit needs to vary across
environments, vary it at render time (different `spec/inputs.yaml`,
different upload Space) or post-install via a ConfigHub function.

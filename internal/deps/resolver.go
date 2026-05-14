package deps

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Masterminds/semver/v3"

	"github.com/confighubai/installer/pkg/api"
)

// Options configure a Resolve call.
type Options struct {
	// Selection is the parent's wizard selection. When non-nil, the
	// resolver uses Selection.Spec.Components to gate optional deps
	// (whenComponent). When nil, optional deps are NOT followed and are
	// listed in Result.SkippedOptional so the caller can surface them.
	Selection *api.Selection
}

// Result is what Resolve returns.
type Result struct {
	// Lock is the pinned dependency tree, ready to write to lock.yaml.
	Lock *api.Lock
	// SkippedOptional records optional deps the resolver did not follow,
	// either because no Selection was supplied or because the gating
	// component was not selected. Format: "<parent>/<dep-name>".
	SkippedOptional []string
}

// Resolve walks pkg's dependency DAG, picks versions, and returns a Lock.
//
// Resolution rules in Phase 4:
//   - One version per package repo. If two parents pick incompatible
//     constraints for the same repo, resolution fails with a chain.
//   - Optional deps are followed iff Options.Selection contains
//     WhenComponent (or WhenComponent is empty and a Selection is provided).
//   - Non-optional deps are always followed.
//   - Cycles are an error.
//   - Root-level Conflicts are enforced anywhere in the resolved DAG.
//   - Replaces are parsed but not yet acted on (TODO).
func Resolve(ctx context.Context, pkg *api.Package, src Source, opts Options) (*Result, error) {
	r := &resolver{
		src:             src,
		selected:        selectedComponents(opts.Selection),
		hasSelection:    opts.Selection != nil,
		rootConflicts:   pkg.Spec.Conflicts,
		resolved:        map[string]*resolvedEntry{},
		path:            []string{"root"},
		skippedOptional: nil,
	}
	if err := r.walk(ctx, pkg, "root"); err != nil {
		return nil, err
	}

	lock := &api.Lock{
		APIVersion: api.APIVersion,
		Kind:       api.KindLock,
		Metadata:   api.Metadata{Name: pkg.Metadata.Name, Version: pkg.Metadata.Version},
		Spec: api.LockSpec{
			Package: api.LockedPackage{
				Name:    pkg.Metadata.Name,
				Version: pkg.Metadata.Version,
			},
			Resolved: r.flatten(),
		},
	}
	return &Result{Lock: lock, SkippedOptional: r.skippedOptional}, nil
}

// resolvedEntry is the resolver's in-flight state per chosen package.
type resolvedEntry struct {
	// repo is the OCI ref without tag, e.g. "ghcr.io/o/r".
	repo string
	// name is the local handle the parent used (the first parent's).
	name string
	// version is the chosen SemVer.
	version *semver.Version
	// tag is the OCI tag corresponding to version.
	tag string
	// digest is the manifest digest sha256:...
	digest string
	// pkg is the parsed Package for recursion + sanity checks.
	pkg *api.Package
	// constraints is every constraint imposed so far, with the requester
	// chain that imposed it (for error reporting).
	constraints []constraintEntry
	// requestedBy lists the names of parents that requested this dep.
	requestedBy []string
	// selection and inputs are the merged pre-answers across requesters.
	selection *api.DependencySelection
	inputs    map[string]any
	// order is the position in flatten output (topological).
	order int
}

type constraintEntry struct {
	by   []string // requester chain, root → leaf
	expr string   // the version expression
}

type resolver struct {
	src             Source
	selected        map[string]struct{}
	hasSelection    bool
	rootConflicts   []api.ConflictRef
	resolved        map[string]*resolvedEntry // key: stripped repo ref
	path            []string                  // current DFS path of dep names
	skippedOptional []string
	orderCounter    int
}

// walk visits parent's Dependencies, resolving and recursing as needed.
func (r *resolver) walk(ctx context.Context, parent *api.Package, parentName string) error {
	for _, dep := range parent.Spec.Dependencies {
		if err := r.visit(ctx, parent, parentName, dep); err != nil {
			return err
		}
	}
	return nil
}

func (r *resolver) visit(ctx context.Context, parent *api.Package, parentName string, dep api.Dependency) error {
	// Optional gating.
	if dep.Optional {
		if !r.hasSelection {
			r.skippedOptional = append(r.skippedOptional, parentName+"/"+dep.Name+" (no selection yet)")
			return nil
		}
		if dep.WhenComponent != "" {
			if _, ok := r.selected[dep.WhenComponent]; !ok {
				r.skippedOptional = append(r.skippedOptional, parentName+"/"+dep.Name+" (component "+dep.WhenComponent+" not selected)")
				return nil
			}
		}
	}

	// Conflict check against root-level conflicts.
	if err := r.checkRootConflict(dep, parentName); err != nil {
		return err
	}

	repo := stripOCIPrefix(dep.Package)
	constraint, err := parseConstraint(dep.Version)
	if err != nil {
		return fmt.Errorf("%s/%s: %w", parentName, dep.Name, err)
	}

	if existing, ok := r.resolved[repo]; ok {
		// Same repo seen before — verify the existing pick satisfies the new
		// constraint, then merge requester metadata.
		if !constraint.Check(existing.version) {
			return r.versionConflictError(existing, parentName, dep)
		}
		existing.constraints = append(existing.constraints, constraintEntry{
			by:   append(slices.Clone(r.path), dep.Name),
			expr: dep.Version,
		})
		existing.requestedBy = appendUnique(existing.requestedBy, parentName)
		mergeSelection(existing, dep.Selection)
		mergeInputs(existing, dep.Inputs)
		return nil
	}

	// Cycle check: if dep.Name is already on the DFS path, the resolver is
	// looping. (Repos cycle == same OCI ref; for nice errors we check by
	// dep name on the path stack too.)
	if slices.Contains(r.path, dep.Name) {
		return fmt.Errorf("dependency cycle: %s -> %s", strings.Join(r.path, " -> "), dep.Name)
	}

	// Pick a version + fetch the manifest.
	tags, err := r.src.ListVersions(ctx, dep.Package)
	if err != nil {
		return fmt.Errorf("%s/%s: list versions: %w", parentName, dep.Name, err)
	}
	tag, version, err := pickBest(tags, constraint)
	if err != nil {
		return fmt.Errorf("%s/%s: %w for %s (available: %s)",
			parentName, dep.Name, err, dep.Version, strings.Join(tags, ", "))
	}
	ref := joinRef(repo, tag)
	res, err := r.src.Inspect(ctx, ref)
	if err != nil {
		return fmt.Errorf("%s/%s: inspect %s: %w", parentName, dep.Name, ref, err)
	}

	// Collectors are not supported on dependencies yet: they only run via
	// the wizard, which is not invoked per-dep. Reject at resolve time so
	// the user discovers the limit immediately rather than at render. See
	// docs/package-management-plan.md (Phase 5 limits).
	if res.Config != nil && res.Config.Manifest != nil && res.Config.Manifest.Spec.Collector != nil {
		return fmt.Errorf("%s/%s: package %s declares spec.collector; collectors are not yet supported on dependencies — pick a version without one or open an issue",
			parentName, dep.Name, ref)
	}

	entry := &resolvedEntry{
		repo:        repo,
		name:        dep.Name,
		version:     version,
		tag:         tag,
		digest:      res.ManifestDigest,
		pkg:         res.Config.Manifest,
		constraints: []constraintEntry{{by: append(slices.Clone(r.path), dep.Name), expr: dep.Version}},
		requestedBy: []string{parentName},
		selection:   cloneDependencySelection(dep.Selection),
		inputs:      cloneMap(dep.Inputs),
		order:       r.nextOrder(),
	}
	r.resolved[repo] = entry

	// Recurse.
	r.path = append(r.path, dep.Name)
	defer func() { r.path = r.path[:len(r.path)-1] }()
	return r.walk(ctx, entry.pkg, dep.Name)
}

func (r *resolver) nextOrder() int {
	r.orderCounter++
	return r.orderCounter
}

// checkRootConflict fails if dep names a package listed in the root's
// Conflicts. Phase 4 only checks root-level conflicts; transitive ones can
// come later.
func (r *resolver) checkRootConflict(dep api.Dependency, parentName string) error {
	if len(r.rootConflicts) == 0 {
		return nil
	}
	depRepo := stripOCIPrefix(dep.Package)
	for _, c := range r.rootConflicts {
		if stripOCIPrefix(c.Package) != depRepo {
			continue
		}
		// Phase 4: treat version-less or "*" as match-all; otherwise we
		// would need the resolved version to fully evaluate. Since the
		// conflict points at a *repo* the user wants excluded, repo match
		// is enough — the user can refine with a range later.
		reason := c.Reason
		if reason == "" {
			reason = "explicit conflict"
		}
		return fmt.Errorf("%s/%s conflicts with root: %s (%s)", parentName, dep.Name, dep.Package, reason)
	}
	return nil
}

func (r *resolver) versionConflictError(existing *resolvedEntry, parent string, dep api.Dependency) error {
	var b strings.Builder
	fmt.Fprintf(&b, "incompatible constraints on %s:\n", existing.repo)
	for _, c := range existing.constraints {
		fmt.Fprintf(&b, "  %s wants %s (via %s)\n", strings.Join(c.by, " -> "), niceConstraint(c.expr), c.by[len(c.by)-1])
	}
	fmt.Fprintf(&b, "  %s/%s wants %s, but %s was already picked", parent, dep.Name, niceConstraint(dep.Version), existing.version)
	return errors.New(b.String())
}

func niceConstraint(expr string) string {
	if expr == "" {
		return "*"
	}
	return expr
}

func (r *resolver) flatten() []api.LockedDependency {
	if len(r.resolved) == 0 {
		return nil
	}
	out := make([]*resolvedEntry, 0, len(r.resolved))
	for _, e := range r.resolved {
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b *resolvedEntry) int { return a.order - b.order })
	locked := make([]api.LockedDependency, 0, len(out))
	for _, e := range out {
		locked = append(locked, api.LockedDependency{
			Name:        e.name,
			Ref:         joinRef(e.repo, e.tag),
			Digest:      e.digest,
			Version:     e.version.String(),
			RequestedBy: e.requestedBy,
			Selection:   e.selection,
			Inputs:      e.inputs,
		})
	}
	return locked
}

func selectedComponents(sel *api.Selection) map[string]struct{} {
	if sel == nil {
		return nil
	}
	out := make(map[string]struct{}, len(sel.Spec.Components))
	for _, c := range sel.Spec.Components {
		out[c] = struct{}{}
	}
	return out
}

func appendUnique(slice []string, v string) []string {
	for _, s := range slice {
		if s == v {
			return slice
		}
	}
	return append(slice, v)
}

func mergeSelection(e *resolvedEntry, sel *api.DependencySelection) {
	if sel == nil {
		return
	}
	if e.selection == nil {
		e.selection = cloneDependencySelection(sel)
		return
	}
	if sel.Base != "" && e.selection.Base == "" {
		e.selection.Base = sel.Base
	}
	for _, c := range sel.Components {
		if !slices.Contains(e.selection.Components, c) {
			e.selection.Components = append(e.selection.Components, c)
		}
	}
}

func mergeInputs(e *resolvedEntry, inputs map[string]any) {
	if len(inputs) == 0 {
		return
	}
	if e.inputs == nil {
		e.inputs = make(map[string]any, len(inputs))
	}
	for k, v := range inputs {
		if _, exists := e.inputs[k]; !exists {
			e.inputs[k] = v
		}
	}
}

func cloneDependencySelection(sel *api.DependencySelection) *api.DependencySelection {
	if sel == nil {
		return nil
	}
	out := *sel
	out.Components = slices.Clone(sel.Components)
	return &out
}

func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

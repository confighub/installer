package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/internal/selection"
	"github.com/confighub/installer/pkg/api"
)

// Fetcher resolves a locked dependency to a local directory the renderer
// can load. Implementations are responsible for digest verification.
//
// destDir is the parent-chosen cache slot for this dependency. The function
// returns the absolute path to the package root (containing installer.yaml).
type Fetcher func(ctx context.Context, ref, digest, destDir string) (string, error)

// DefaultFetcher pulls a locked dependency via ipkg.Pull. The ref is
// digest-pinned (oci://...:tag@sha256:...) so the registry's response is
// verified against the lock. If destDir already contains a package, the
// fetch is skipped — the caller's cache hit.
func DefaultFetcher(ctx context.Context, ref, digest, destDir string) (string, error) {
	pkgRoot := filepath.Join(destDir, "package")
	if _, err := os.Stat(filepath.Join(pkgRoot, "installer.yaml")); err == nil {
		return pkgRoot, nil
	}
	pinned := ref + "@" + digest
	return ipkg.Pull(ctx, pinned, destDir)
}

// DepsOptions configures RenderDependencies.
type DepsOptions struct {
	// Lock is the resolved dependency tree, typically read from
	// <work-dir>/out/spec/lock.yaml.
	Lock *api.Lock

	// ParentInputs carries the parent's namespace. Used as the default
	// namespace for dependencies whose lock entry does not provide one.
	ParentInputs *api.Inputs

	// WorkDir is the parent's working directory. Vendor cache lives at
	// <WorkDir>/out/vendor/<name>@<version>/, dep render outputs go to
	// <WorkDir>/out/<dep-name>/.
	WorkDir string

	// Fetcher resolves a locked dep's ref+digest to a local package root.
	// If nil, DefaultFetcher is used.
	Fetcher Fetcher

	// TransformerBinary is propagated to each per-dep Render call. Defaults
	// to os.Executable() inside Render when empty.
	TransformerBinary string
}

// DepResult records the outcome of one dependency render.
type DepResult struct {
	Name        string
	OutDir      string
	Manifests   []File
	Secrets     []File
	PackageRoot string // where the dep's source tree was materialized
}

// RenderDependencies renders each entry in opts.Lock into its own subtree
// under <WorkDir>/out/<dep-name>/. The lock's Selection and Inputs are used
// as wizard pre-answers; selection closure is run on top so the dep's own
// Requires/Conflicts/ValidForBases rules are honored.
//
// Returns one DepResult per resolved dependency, in lock order.
func RenderDependencies(ctx context.Context, opts DepsOptions) ([]DepResult, error) {
	if opts.Lock == nil {
		return nil, fmt.Errorf("RenderDependencies: Lock is required")
	}
	if opts.WorkDir == "" {
		return nil, fmt.Errorf("RenderDependencies: WorkDir is required")
	}
	if opts.ParentInputs == nil {
		return nil, fmt.Errorf("RenderDependencies: ParentInputs is required")
	}
	fetch := opts.Fetcher
	if fetch == nil {
		fetch = DefaultFetcher
	}

	vendorDir := filepath.Join(opts.WorkDir, "out", "vendor")
	if err := os.MkdirAll(vendorDir, 0o755); err != nil {
		return nil, err
	}

	var out []DepResult
	for _, d := range opts.Lock.Spec.Resolved {
		dest := filepath.Join(vendorDir, vendorSlug(d.Name, d.Version))
		pkgRoot, err := fetch(ctx, d.Ref, d.Digest, dest)
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", d.Ref, err)
		}
		loaded, err := ipkg.Load(pkgRoot)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", pkgRoot, err)
		}

		sel, inputs, err := buildDepWizardAnswers(loaded.Package, d, opts.ParentInputs)
		if err != nil {
			return nil, fmt.Errorf("dep %s: %w", d.Name, err)
		}

		depOut := filepath.Join(opts.WorkDir, "out", d.Name)
		if err := os.MkdirAll(depOut, 0o755); err != nil {
			return nil, err
		}
		res, err := Render(ctx, Options{
			Loaded:            loaded,
			Selection:         sel,
			Inputs:            inputs,
			TransformerBinary: opts.TransformerBinary,
		}, depOut)
		if err != nil {
			return nil, fmt.Errorf("render dep %s: %w", d.Name, err)
		}
		out = append(out, DepResult{
			Name:        d.Name,
			OutDir:      depOut,
			Manifests:   res.Manifests,
			Secrets:     res.Secrets,
			PackageRoot: pkgRoot,
		})
	}
	return out, nil
}

// vendorSlug returns the dirname used for a cached dependency in
// out/vendor/. Falls back to the dep name when no version is recorded.
func vendorSlug(name, version string) string {
	if version == "" {
		return name
	}
	return name + "@" + version
}

// buildDepWizardAnswers turns a LockedDependency + the dep's installer.yaml
// into the Selection + Inputs render needs. The lock's Selection drives the
// closure-resolved selection; the lock's Inputs map is split between the
// special "namespace" key (which becomes Inputs.Spec.Namespace) and the
// declared input values.
func buildDepWizardAnswers(pkg *api.Package, d api.LockedDependency, parent *api.Inputs) (*api.Selection, *api.Inputs, error) {
	base := ""
	components := []string{}
	if d.Selection != nil {
		base = d.Selection.Base
		components = d.Selection.Components
	}
	sel, err := selection.Resolve(pkg, base, components)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve selection: %w", err)
	}
	// Carry the locked dep's identity onto Selection so re-render is keyed
	// to the right package.
	sel.Spec.Package = pkg.Metadata.Name
	sel.Spec.PackageVersion = pkg.Metadata.Version

	namespace := parent.Spec.Namespace
	values := map[string]any{}
	for k, v := range d.Inputs {
		if k == "namespace" {
			if s, ok := v.(string); ok && s != "" {
				namespace = s
			}
			continue
		}
		values[k] = v
	}
	// Apply declared defaults for inputs not overridden by the lock.
	for i := range pkg.Spec.Inputs {
		in := &pkg.Spec.Inputs[i]
		if _, present := values[in.Name]; present {
			continue
		}
		if in.Default != nil {
			values[in.Name] = in.Default
		}
	}

	inputs := &api.Inputs{
		APIVersion: api.APIVersion,
		Kind:       api.KindInputs,
		Metadata:   api.Metadata{Name: pkg.Metadata.Name + "-inputs"},
		Spec: api.InputsSpec{
			Package:        pkg.Metadata.Name,
			PackageVersion: pkg.Metadata.Version,
			Namespace:      namespace,
			Values:         values,
		},
	}
	return sel, inputs, nil
}

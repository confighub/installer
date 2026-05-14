package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	ipkg "github.com/confighubai/installer/internal/pkg"
	"github.com/confighubai/installer/pkg/api"
)

// Options controls Render. All fields are required except Facts.
type Options struct {
	Loaded    *ipkg.Loaded
	Selection *api.Selection
	Inputs    *api.Inputs
	// Facts is the parsed facts.yaml. Nil when the package has no collector
	// or the wizard has not been re-run after one was added.
	Facts *api.Facts
}

// Result is what Render produces.
type Result struct {
	// OutDir is the directory written to (out/manifests + out/secrets + out/spec
	// underneath).
	OutDir string
	// Manifests is the per-resource non-sensitive output, ordered by Slug.
	Manifests []File
	// Secrets is the per-resource sensitive output (Kubernetes Secrets),
	// written to out/secrets/ and never uploaded as Units.
	Secrets []File
	// Chain is the resolved FunctionChain that was executed (also persisted to spec/).
	Chain *api.FunctionChain
}

// Render reads the package + selection + inputs, drives kustomize, runs the
// function chain, and writes per-resource files plus the spec docs to outDir.
//
// outDir is created if missing. Existing files in outDir/manifests are
// overwritten; files not produced by this render are NOT removed (callers
// who want a clean slate should remove manifests/ first).
func Render(ctx context.Context, opts Options, outDir string) (*Result, error) {
	if opts.Loaded == nil {
		return nil, fmt.Errorf("Render: Loaded is required")
	}
	if opts.Selection == nil {
		return nil, fmt.Errorf("Render: Selection is required")
	}
	if opts.Inputs == nil {
		return nil, fmt.Errorf("Render: Inputs is required")
	}

	// 0. Apply per-image overrides (if any) by running `kustomize edit
	//    set image` against the chosen base's kustomization.yaml. The
	//    chosen base must declare an `images:` block; render fails fast
	//    otherwise.
	if err := applyImageOverrides(opts.Loaded, opts.Selection, opts.Inputs.Spec.ImageOverrides); err != nil {
		return nil, err
	}

	// 1. Compose the top-level kustomization in a temp dir, run kustomize build.
	composeDir, err := composeKustomization(opts.Loaded, opts.Selection)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(composeDir)

	rendered, err := runKustomize(composeDir)
	if err != nil {
		return nil, err
	}

	// 2. Resolve the function chain template against the inputs and facts.
	chain, err := resolveChainTemplate(opts.Loaded.Package, opts.Inputs, opts.Selection, opts.Facts)
	if err != nil {
		return nil, err
	}

	// 3. Run the chain. Output of each group feeds the next.
	mutated := rendered
	if len(chain.Spec.Groups) > 0 {
		mutated, err = runChain(ctx, chain, rendered)
		if err != nil {
			return nil, err
		}
	}

	// 4. Split into per-resource files with deterministic naming.
	files, err := splitForUnits(mutated)
	if err != nil {
		return nil, err
	}

	// 5. Split sensitive resources off into out/secrets/, write the rest to
	// out/manifests/.
	var manifests, secrets []File
	for _, f := range files {
		if f.Sensitive {
			secrets = append(secrets, f)
		} else {
			manifests = append(manifests, f)
		}
	}

	manifestsDir := filepath.Join(outDir, "manifests")
	secretsDir := filepath.Join(outDir, "secrets")
	specDir := filepath.Join(outDir, "spec")
	if err := os.MkdirAll(manifestsDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(specDir, 0o755); err != nil {
		return nil, err
	}
	if len(secrets) > 0 {
		if err := os.MkdirAll(secretsDir, 0o700); err != nil {
			return nil, err
		}
	}

	for _, f := range manifests {
		path := filepath.Join(manifestsDir, f.Filename)
		if err := os.WriteFile(path, f.Body, 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", path, err)
		}
	}
	for _, f := range secrets {
		path := filepath.Join(secretsDir, f.Filename)
		if err := os.WriteFile(path, f.Body, 0o600); err != nil {
			return nil, fmt.Errorf("write %s: %w", path, err)
		}
	}

	// Persist spec docs alongside the rendered output: selection, inputs,
	// optional facts, the resolved function-chain, and a manifest index for
	// downstream tools.
	if err := writeYAML(filepath.Join(specDir, "selection.yaml"), opts.Selection); err != nil {
		return nil, err
	}
	if err := writeYAML(filepath.Join(specDir, "inputs.yaml"), opts.Inputs); err != nil {
		return nil, err
	}
	if opts.Facts != nil {
		if err := writeYAML(filepath.Join(specDir, "facts.yaml"), opts.Facts); err != nil {
			return nil, err
		}
	}
	if err := writeYAML(filepath.Join(specDir, "function-chain.yaml"), chain); err != nil {
		return nil, err
	}
	// The manifest index records only non-sensitive files; secrets are tracked
	// separately and never uploaded.
	if err := writeManifestIndex(filepath.Join(specDir, "manifest-index.yaml"), opts.Loaded.Package, manifests); err != nil {
		return nil, err
	}

	return &Result{
		OutDir:    outDir,
		Manifests: manifests,
		Secrets:   secrets,
		Chain:     chain,
	}, nil
}

func writeYAML(path string, v any) error {
	data, err := api.MarshalYAML(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// manifestIndex is a flat record of what was rendered, intended for the
// upload step (so it can pass --label per file) and for tooling.
//
// Phase assignment is left for a follow-up: the schema declares phases via
// whereResource expressions, but evaluating them per-resource here would
// require pulling in the SDK's filter machinery just to label files. Until
// upload is implemented, the index records kind/name/namespace and leaves
// phase empty.
type manifestIndex struct {
	APIVersion string              `yaml:"apiVersion"`
	Kind       string              `yaml:"kind"`
	Metadata   api.Metadata        `yaml:"metadata"`
	Spec       manifestIndexSpec   `yaml:"spec"`
}

type manifestIndexSpec struct {
	Package        string              `yaml:"package"`
	PackageVersion string              `yaml:"packageVersion,omitempty"`
	Files          []manifestIndexFile `yaml:"files"`
}

type manifestIndexFile struct {
	Filename   string `yaml:"filename"`
	Slug       string `yaml:"slug"`
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Name       string `yaml:"name"`
	Namespace  string `yaml:"namespace,omitempty"`
	Phase      string `yaml:"phase,omitempty"`
}

func writeManifestIndex(path string, pkg *api.Package, files []File) error {
	idx := manifestIndex{
		APIVersion: api.APIVersion,
		Kind:       "ManifestIndex",
		Metadata:   api.Metadata{Name: pkg.Metadata.Name + "-manifest-index"},
		Spec: manifestIndexSpec{
			Package:        pkg.Metadata.Name,
			PackageVersion: pkg.Metadata.Version,
		},
	}
	for _, f := range files {
		idx.Spec.Files = append(idx.Spec.Files, manifestIndexFile{
			Filename:   f.Filename,
			Slug:       f.Slug,
			APIVersion: f.APIVersion,
			Kind:       f.Kind,
			Name:       f.Name,
			Namespace:  f.Namespace,
		})
	}
	return writeYAML(path, idx)
}

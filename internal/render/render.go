package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	ipkg "github.com/confighubai/installer/internal/pkg"
	"github.com/confighubai/installer/pkg/api"
)

// Options controls Render. All fields are optional except Loaded.
type Options struct {
	Loaded    *ipkg.Loaded
	Selection *api.Selection
	Inputs    *api.Inputs
}

// Result is what Render produces.
type Result struct {
	// OutDir is the directory written to (out/manifests + out/spec underneath).
	OutDir string
	// Files is the per-resource output, ordered by Slug.
	Files []File
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

	// 2. Resolve the function chain template against the inputs.
	chain, err := resolveChainTemplate(opts.Loaded.Package, opts.Inputs, opts.Selection)
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

	// 5. Write everything out.
	manifestsDir := filepath.Join(outDir, "manifests")
	specDir := filepath.Join(outDir, "spec")
	if err := os.MkdirAll(manifestsDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(specDir, 0o755); err != nil {
		return nil, err
	}

	for _, f := range files {
		path := filepath.Join(manifestsDir, f.Filename)
		if err := os.WriteFile(path, f.Body, 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", path, err)
		}
	}

	// Persist spec docs alongside the rendered output: selection, inputs,
	// the resolved function-chain, and a manifest index for downstream tools.
	if err := writeYAML(filepath.Join(specDir, "selection.yaml"), opts.Selection); err != nil {
		return nil, err
	}
	if err := writeYAML(filepath.Join(specDir, "inputs.yaml"), opts.Inputs); err != nil {
		return nil, err
	}
	if err := writeYAML(filepath.Join(specDir, "function-chain.yaml"), chain); err != nil {
		return nil, err
	}
	if err := writeManifestIndex(filepath.Join(specDir, "manifest-index.yaml"), opts.Loaded.Package, files); err != nil {
		return nil, err
	}

	return &Result{
		OutDir: outDir,
		Files:  files,
		Chain:  chain,
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

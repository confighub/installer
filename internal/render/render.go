package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/pkg/api"
)

// Options controls Render. All fields are required except Facts and
// TransformerBinary.
type Options struct {
	Loaded    *ipkg.Loaded
	Selection *api.Selection
	Inputs    *api.Inputs
	// Facts is the parsed facts.yaml. Nil when the package has no collector
	// or the wizard has not been re-run after one was added.
	Facts *api.Facts
	// TransformerBinary is the absolute path baked into the
	// out/compose/installer-transformer wrapper script. Defaults to
	// os.Executable() — the binary currently running Render. Tests inject a
	// freshly-built installer binary so the go test binary (which doesn't
	// implement the `transformer` subcommand) isn't invoked by kustomize.
	TransformerBinary string
}

// Result is what Render produces.
type Result struct {
	// OutDir is the directory written to (out/manifests + out/secrets + out/spec
	// + out/compose underneath).
	OutDir string
	// Manifests is the per-resource non-sensitive output, ordered by Slug.
	Manifests []File
	// Secrets is the per-resource sensitive output (Kubernetes Secrets),
	// written to out/secrets/ and never uploaded as Units.
	Secrets []File
	// Chain is the resolved FunctionChain that was executed (also persisted
	// to spec/ and to compose/chain.yaml).
	Chain *api.FunctionChain
}

// Render reads the package + selection + inputs, drives kustomize (which
// invokes `installer transformer` as an exec plugin to apply the function
// chain and validators), and writes per-resource files plus the spec docs
// to outDir.
//
// outDir is created if missing. Existing files in outDir/manifests are
// overwritten; files not produced by this render are NOT removed (callers
// who want a clean slate should remove manifests/ first). out/compose/ is
// always cleared and rewritten so the on-disk kustomization tree reflects
// the current render exactly.
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

	transformerBin := opts.TransformerBinary
	if transformerBin == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("locate installer binary: %w", err)
		}
		transformerBin = exe
	}

	// 0. Apply per-image overrides (if any) by running `kustomize edit
	//    set image` against the chosen base's kustomization.yaml. The
	//    chosen base must declare an `images:` block; render fails fast
	//    otherwise.
	if err := applyImageOverrides(opts.Loaded, opts.Selection, opts.Inputs.Spec.ImageOverrides); err != nil {
		return nil, err
	}

	// 1. Resolve the transformer chain. Validators run in-process after
	//    kustomize (see step 4) so the rendered manifests can be fed to
	//    chainexec.RunValidators without being filtered through the
	//    kustomize validators: slot — that slot enforces byte-level item
	//    equality which yaml.v3 round-trip violates, and the transformers:
	//    slot silently swallows severity=error results.
	chain, err := resolveChainTemplate(opts.Loaded.Package, opts.Inputs, opts.Selection, opts.Facts, opts.Loaded.Root)
	if err != nil {
		return nil, err
	}
	validators, err := resolveValidatorTemplate(opts.Loaded.Package, opts.Inputs, opts.Selection, opts.Facts, opts.Loaded.Root)
	if err != nil {
		return nil, err
	}

	// 2. Compose out/compose/ and run kustomize against it. Kustomize
	//    invokes the wrapper, which execs `installer transformer`,
	//    which runs each transformer group through chainexec in-process.
	composeDir := filepath.Join(outDir, "compose")
	if err := composeKustomization(composeInputs{
		Loaded:            opts.Loaded,
		Selection:         opts.Selection,
		Chain:             chain,
		TransformerBinary: transformerBin,
	}, composeDir); err != nil {
		return nil, err
	}

	rendered, err := runKustomize(composeDir)
	if err != nil {
		return nil, err
	}

	// 3. Run validators in-process against kustomize's output. Each
	//    AppConfig/* group is dispatched per-carrier; everything else
	//    runs against the full stream.
	if len(validators) > 0 {
		if err := runValidatorsAfterKustomize(ctx, validators, rendered); err != nil {
			return nil, err
		}
	}

	// 4. Split into per-resource files with deterministic naming.
	files, err := splitForUnits(rendered)
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
	APIVersion string            `yaml:"apiVersion"`
	Kind       string            `yaml:"kind"`
	Metadata   api.Metadata      `yaml:"metadata"`
	Spec       manifestIndexSpec `yaml:"spec"`
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

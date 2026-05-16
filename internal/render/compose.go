// Package render takes a loaded package + Selection + Inputs and produces
// rendered Kubernetes manifests by:
//
//  1. Synthesizing a top-level kustomization.yaml under out/compose/ that
//     references the chosen base + Components and wires the ConfigHub
//     transformers and validators as kustomize transformer / validator
//     plugins.
//  2. Resolving the package's Transformers + Validators against the
//     Inputs (Go templates) and writing them to
//     out/compose/{transformers,validators}.yaml as KRM function configs.
//  3. Shelling out to `kustomize build --enable-exec --enable-alpha-plugins`,
//     which invokes `installer transformer` (via a wrapper script in
//     out/compose/) to run each function group in process.
//  4. Splitting the resulting multi-doc YAML into per-resource files with
//     deterministic naming, written to out/manifests/.
//  5. Persisting the resolved FunctionChain alongside selection.yaml and
//     inputs.yaml in out/spec/ so re-render is reproducible and the exact
//     transforms applied are inspectable.
package render

import (
	"fmt"
	"os"
	"path/filepath"

	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/pkg/api"
	"gopkg.in/yaml.v3"
)

// composeInputs is what composeKustomization needs to produce the on-disk
// compose tree. All paths in Loaded must already be symlink-resolved upstream
// (Render does this).
type composeInputs struct {
	Loaded            *ipkg.Loaded
	Selection         *api.Selection
	Chain             *api.FunctionChain // resolved; may have zero groups
	TransformerBinary string             // absolute path to the installer binary
}

// composeKustomization writes a synthesized kustomization tree under
// composeDir (created if missing, cleared if it already exists). The
// kustomization references the chosen base + components by relative path,
// wires transformers.yaml as a transformer and validators.yaml as a
// validator (when each is non-empty), and writes a one-line
// installer-transformer.sh wrapper script that execs the running installer
// binary's transformer subcommand.
//
// composeDir is the only directory written; callers persist it under
// <work-dir>/out/compose/ so `cd out/compose && kustomize build` reproduces
// the render byte-for-byte.
func composeKustomization(in composeInputs, composeDir string) error {
	pkg := in.Loaded.Package
	baseDir, err := basePathForName(pkg, in.Selection.Spec.Base)
	if err != nil {
		return err
	}

	if err := os.RemoveAll(composeDir); err != nil {
		return err
	}
	if err := os.MkdirAll(composeDir, 0o755); err != nil {
		return err
	}

	// kustomize rejects absolute paths in resources/components; compute
	// relative paths from the compose dir to each referenced directory.
	// Both paths must be symlink-resolved or kustomize's own EvalSymlinks
	// step produces nonsense (notably on macOS, where /var → /private/var).
	composeReal, err := filepath.EvalSymlinks(composeDir)
	if err != nil {
		return err
	}
	rootReal, err := filepath.EvalSymlinks(in.Loaded.Root)
	if err != nil {
		return err
	}
	rel := func(target string) (string, error) {
		return filepath.Rel(composeReal, filepath.Join(rootReal, target))
	}

	baseRel, err := rel(baseDir)
	if err != nil {
		return err
	}
	composed := composedKustomization{
		APIVersion: "kustomize.config.k8s.io/v1beta1",
		Kind:       "Kustomization",
		Resources:  []string{baseRel},
	}
	for _, name := range in.Selection.Spec.Components {
		path, err := componentPathForName(pkg, name)
		if err != nil {
			return err
		}
		r, err := rel(path)
		if err != nil {
			return err
		}
		composed.Components = append(composed.Components, r)
	}

	// Write transformers.yaml + validators.yaml + the exec wrapper when needed,
	// and link them into the top-level kustomization. Empty groups are
	// elided so trivial packages don't pay for an exec subprocess they
	// don't use.
	needsWrapper := false
	if in.Chain != nil && len(in.Chain.Spec.Groups) > 0 {
		if err := writeKRMFunctionConfig(composeDir, "transformers.yaml", kindConfigHubTransformers,
			pkg.Metadata.Name+"-transformers", in.Chain.Spec.Groups); err != nil {
			return err
		}
		composed.Transformers = append(composed.Transformers, "transformers.yaml")
		needsWrapper = true
	}
	// Validators do NOT go into the synthesized kustomization. Two
	// reasons make running them in-process after kustomize build
	// strictly better than running them as a kustomize validator:
	//   - Kustomize's validators: slot enforces byte-level item
	//     pass-through; yaml.v3 round-trip subtly reformats unchanged
	//     items and trips the check. Avoiding that without pulling in
	//     kyaml is expensive.
	//   - Kustomize's transformers: slot ignores severity=error
	//     results, so moving validators there silently swallows
	//     failures.
	// The installer reads pkg.Spec.Validators (plus components')
	// directly and runs them via chainexec.RunValidators against the
	// kustomize output. Standalone kustomize users who want ConfigHub
	// validators can wire ConfigHubValidators into their own
	// kustomization's validators: list — `installer transformer`
	// supports it.
	if needsWrapper {
		if err := writeTransformerWrapper(composeDir, in.TransformerBinary); err != nil {
			return err
		}
	}

	body, err := yaml.Marshal(composed)
	if err != nil {
		return err
	}
	// Lead with the exact incantation needed to reproduce this render — the
	// two flags are mandatory for the exec transformer/validator to fire, and
	// neither is the kustomize default. yaml.Marshal won't emit comments, so
	// we prefix the header manually.
	header := []byte("# Reproduce this render with:\n" +
		"#   kustomize build --enable-exec --enable-alpha-plugins .\n")
	return os.WriteFile(filepath.Join(composeDir, "kustomization.yaml"), append(header, body...), 0o644)
}

// krmFunctionConfig is the shape we emit into transformers.yaml / validators.yaml.
// The config.kubernetes.io/function annotation tells kustomize which
// executable to invoke for this transformer/validator; we always point at
// ./installer-transformer.sh (relative to composeDir) so the kustomization is
// self-contained.
type krmFunctionConfig struct {
	APIVersion string                `yaml:"apiVersion"`
	Kind       string                `yaml:"kind"`
	Metadata   krmFunctionConfigMeta `yaml:"metadata"`
	Spec       struct {
		Groups []api.FunctionGroup `yaml:"groups"`
	} `yaml:"spec"`
}

type krmFunctionConfigMeta struct {
	Name        string            `yaml:"name"`
	Annotations map[string]string `yaml:"annotations"`
}

// kindConfigHubTransformers names the KRM functionConfig kind our exec
// plugin recognizes for the kustomization's transformers: list. The
// matching ConfigHubValidators kind exists in internal/cli/transformer.go
// for standalone-kustomize users wiring it into their own validators:
// list — `installer render` runs validators in-process after kustomize
// (see render.go and post_validate.go) and doesn't emit a ConfigHubValidators
// config into the synthesized kustomization.
const kindConfigHubTransformers = "ConfigHubTransformers"

func writeKRMFunctionConfig(composeDir, filename, kind, name string, groups []api.FunctionGroup) error {
	cfg := krmFunctionConfig{
		APIVersion: api.APIVersion,
		Kind:       kind,
		Metadata: krmFunctionConfigMeta{
			Name: name,
			Annotations: map[string]string{
				"config.kubernetes.io/function": "exec:\n  path: ./installer-transformer.sh\n",
			},
		},
	}
	cfg.Spec.Groups = groups
	body, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", filename, err)
	}
	return os.WriteFile(filepath.Join(composeDir, filename), body, 0o644)
}

// writeTransformerWrapper writes a one-line shell wrapper that execs the
// installer binary's transformer subcommand. The absolute path is baked in
// at render time so `cd out/compose && kustomize build` reproduces without
// any extra env setup; users can edit the wrapper to point elsewhere if the
// binary moves.
func writeTransformerWrapper(composeDir, installerBin string) error {
	body := fmt.Sprintf("#!/bin/sh\nexec %q transformer\n", installerBin)
	return os.WriteFile(filepath.Join(composeDir, "installer-transformer.sh"), []byte(body), 0o755)
}

func basePathForName(pkg *api.Package, name string) (string, error) {
	for _, b := range pkg.Spec.Bases {
		if b.Name == name {
			return b.Path, nil
		}
	}
	return "", fmt.Errorf("base %q not found in package %q", name, pkg.Metadata.Name)
}

func componentPathForName(pkg *api.Package, name string) (string, error) {
	for _, c := range pkg.Spec.Components {
		if c.Name == name {
			return c.Path, nil
		}
	}
	return "", fmt.Errorf("component %q not found in package %q", name, pkg.Metadata.Name)
}

type composedKustomization struct {
	APIVersion   string   `yaml:"apiVersion"`
	Kind         string   `yaml:"kind"`
	Resources    []string `yaml:"resources,omitempty"`
	Components   []string `yaml:"components,omitempty"`
	Transformers []string `yaml:"transformers,omitempty"`
	Validators   []string `yaml:"validators,omitempty"`
}

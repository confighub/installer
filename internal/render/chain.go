package render

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/confighub/installer/internal/chainexec"
	"github.com/confighub/installer/pkg/api"
	"gopkg.in/yaml.v3"
)

// templateFuncs returns the FuncMap used to resolve installer.yaml's
// spec.transformers / spec.validators templates. Keep the set deliberately
// small — broad helpers turn a config-as-data field into a templating
// language. Today there's exactly one helper, loadJSON, which is the
// minimum needed to wire vet-jsonschema's schema-map arg from a file
// shipped in the package.
//
// packageRoot is the directory the helpers are anchored to. All file
// arguments must resolve to paths INSIDE packageRoot (no absolute paths,
// no path-escape via ..) — the templates are author-controlled but the
// resolved files might be referenced by any rendered output, so we
// constrain the surface to the package boundary.
func templateFuncs(packageRoot string) template.FuncMap {
	resolveUnder := func(name, path string) (string, error) {
		if path == "" {
			return "", fmt.Errorf("%s: path is required", name)
		}
		if filepath.IsAbs(path) {
			return "", fmt.Errorf("%s: %q must be relative to the package root", name, path)
		}
		clean := filepath.Clean(path)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("%s: %q escapes the package root", name, path)
		}
		return filepath.Join(packageRoot, clean), nil
	}
	return template.FuncMap{
		// loadJSON reads a JSON file under the package root, re-marshals
		// it in compact form (no embedded newlines), and returns it as a
		// string suitable for dropping inside a YAML single-quoted
		// scalar — that's the natural quoting style yaml.v3 picks when
		// the author writes `args: ['... {{ loadJSON "..." }} ...']`,
		// and apostrophes inside the loaded JSON would otherwise
		// terminate the YAML scalar early. Single quotes in the result
		// are doubled so the YAML re-parse decodes back to the original
		// JSON byte-for-byte. Intended for wiring vet-jsonschema's
		// schema-map argument from a versioned file like
		// validation/<x>.schema.json.
		"loadJSON": func(path string) (string, error) {
			full, err := resolveUnder("loadJSON", path)
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(full)
			if err != nil {
				return "", fmt.Errorf("loadJSON: %w", err)
			}
			var v any
			if err := json.Unmarshal(data, &v); err != nil {
				return "", fmt.Errorf("loadJSON %s: %w", path, err)
			}
			out, err := json.Marshal(v)
			if err != nil {
				return "", fmt.Errorf("loadJSON %s: %w", path, err)
			}
			return strings.ReplaceAll(string(out), "'", "''"), nil
		},
	}
}

// resolveChainTemplate expands Go template expressions inside the package's
// Transformers against the resolved Inputs, Selection, and Facts.
// Returns the materialized FunctionChain ready to execute. Empty arg strings
// after resolution are kept (they may legitimately encode "set this field
// empty"); callers that want to skip empty groups should filter post-resolution.
//
// Template context:
//
//	{{ .Namespace }}      — value of `installer wizard --namespace`
//	{{ .Inputs.<name> }}  — value from inputs.yaml
//	{{ .Facts.<name> }}   — value from facts.yaml (nil if no collector ran)
//	{{ .Selection.* }}    — chosen base + components
//	{{ .Package.* }}      — package metadata (name, version, labels, ...)
func resolveChainTemplate(pkg *api.Package, inputs *api.Inputs, sel *api.Selection, facts *api.Facts, packageRoot string) (*api.FunctionChain, error) {
	// Aggregate package-wide transformers + each selected component's
	// transformers in declaration order. Components add transformers in
	// the order they appear in installer.yaml.spec.components (not the
	// order the wizard selected them), so re-rendering is deterministic.
	groups := gatherGroups(pkg, sel, func(p *api.Package) []api.FunctionGroup { return p.Spec.Transformers },
		func(c *api.Component) []api.FunctionGroup { return c.Transformers })
	if len(groups) == 0 {
		return &api.FunctionChain{
			APIVersion: api.APIVersion,
			Kind:       api.KindFunctionChain,
			Metadata:   api.Metadata{Name: pkg.Metadata.Name + "-function-chain"},
			Spec: api.FunctionChainSpec{
				Package:        pkg.Metadata.Name,
				PackageVersion: pkg.Metadata.Version,
			},
		}, nil
	}

	// Marshal the template to YAML, run text/template over the bytes, then
	// re-parse. This lets the template author use {{ .Inputs.foo }} anywhere
	// a string appears in the chain (function args, whereResource, even
	// toolchain), without us having to recurse through every field.
	srcBytes, err := yaml.Marshal(groups)
	if err != nil {
		return nil, fmt.Errorf("marshal template: %w", err)
	}

	tmpl, err := template.New("chain").Option("missingkey=error").Funcs(templateFuncs(packageRoot)).Parse(string(srcBytes))
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	factValues := map[string]any{}
	if facts != nil {
		factValues = facts.Spec.Values
	}
	ctx := map[string]any{
		"Namespace": inputs.Spec.Namespace,
		"Inputs":    inputs.Spec.Values,
		"Facts":     factValues,
		"Selection": sel.Spec,
		"Package":   pkg.Metadata,
	}
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}

	var resolved []api.FunctionGroup
	if err := yaml.Unmarshal(buf.Bytes(), &resolved); err != nil {
		return nil, fmt.Errorf("re-parse resolved chain: %w\n----\n%s\n----", err, buf.String())
	}

	return &api.FunctionChain{
		APIVersion: api.APIVersion,
		Kind:       api.KindFunctionChain,
		Metadata: api.Metadata{
			Name: pkg.Metadata.Name + "-function-chain",
		},
		Spec: api.FunctionChainSpec{
			Package:        pkg.Metadata.Name,
			PackageVersion: pkg.Metadata.Version,
			Groups:         resolved,
		},
	}, nil
}

// gatherGroups concatenates package-wide groups with each selected
// component's groups in installer.yaml declaration order. The picker
// functions let one helper handle both Transformers and Validators without
// reflection.
func gatherGroups(
	pkg *api.Package,
	sel *api.Selection,
	fromPackage func(*api.Package) []api.FunctionGroup,
	fromComponent func(*api.Component) []api.FunctionGroup,
) []api.FunctionGroup {
	selected := map[string]bool{}
	for _, name := range sel.Spec.Components {
		selected[name] = true
	}
	out := append([]api.FunctionGroup(nil), fromPackage(pkg)...)
	for i := range pkg.Spec.Components {
		c := &pkg.Spec.Components[i]
		if !selected[c.Name] {
			continue
		}
		out = append(out, fromComponent(c)...)
	}
	return out
}

// resolveValidatorTemplate is the parallel of resolveChainTemplate for the
// package's spec.validators field. Returns the resolved FunctionGroup list
// ready to feed chainexec.RunValidators.
func resolveValidatorTemplate(pkg *api.Package, inputs *api.Inputs, sel *api.Selection, facts *api.Facts, packageRoot string) ([]api.FunctionGroup, error) {
	groups := gatherGroups(pkg, sel, func(p *api.Package) []api.FunctionGroup { return p.Spec.Validators },
		func(c *api.Component) []api.FunctionGroup { return c.Validators })
	if len(groups) == 0 {
		return nil, nil
	}
	srcBytes, err := yaml.Marshal(groups)
	if err != nil {
		return nil, fmt.Errorf("marshal validators: %w", err)
	}
	tmpl, err := template.New("validators").Option("missingkey=error").Funcs(templateFuncs(packageRoot)).Parse(string(srcBytes))
	if err != nil {
		return nil, fmt.Errorf("parse validators template: %w", err)
	}
	factValues := map[string]any{}
	if facts != nil {
		factValues = facts.Spec.Values
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, map[string]any{
		"Namespace": inputs.Spec.Namespace,
		"Inputs":    inputs.Spec.Values,
		"Facts":     factValues,
		"Selection": sel.Spec,
		"Package":   pkg.Metadata,
	}); err != nil {
		return nil, fmt.Errorf("execute validators template: %w", err)
	}
	var resolved []api.FunctionGroup
	if err := yaml.Unmarshal(buf.Bytes(), &resolved); err != nil {
		return nil, fmt.Errorf("re-parse resolved validators: %w\n----\n%s\n----", err, buf.String())
	}
	return resolved, nil
}

// RunValidators is the public entry point used by `installer vet`. Resolves
// the package's spec.validators template against inputs + selection + facts
// and runs each group against data (the concatenated rendered manifests).
// packageRoot is the directory loadJSON/other template helpers anchor
// relative paths against. Returns a list of failures, or nil on full
// success.
func RunValidators(ctx context.Context, pkg *api.Package, sel *api.Selection, inputs *api.Inputs, facts *api.Facts, packageRoot string, data []byte) ([]chainexec.ValidatorFailure, error) {
	groups, err := resolveValidatorTemplate(pkg, inputs, sel, facts, packageRoot)
	if err != nil {
		return nil, err
	}
	return chainexec.RunValidators(ctx, groups, data)
}

// FormatValidatorFailures re-exports chainexec.FormatValidatorFailures so
// callers that already import render don't have to add a second import.
func FormatValidatorFailures(failures []chainexec.ValidatorFailure) string {
	return chainexec.FormatValidatorFailures(failures)
}

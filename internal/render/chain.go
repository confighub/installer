package render

import (
	"bytes"
	"context"
	"fmt"
	"text/template"

	"github.com/confighub/installer/internal/chainexec"
	"github.com/confighub/installer/pkg/api"
	"gopkg.in/yaml.v3"
)

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
func resolveChainTemplate(pkg *api.Package, inputs *api.Inputs, sel *api.Selection, facts *api.Facts) (*api.FunctionChain, error) {
	// Marshal the template to YAML, run text/template over the bytes, then
	// re-parse. This lets the template author use {{ .Inputs.foo }} anywhere
	// a string appears in the chain (function args, whereResource, even
	// toolchain), without us having to recurse through every field.
	srcBytes, err := yaml.Marshal(pkg.Spec.Transformers)
	if err != nil {
		return nil, fmt.Errorf("marshal template: %w", err)
	}

	tmpl, err := template.New("chain").Option("missingkey=error").Parse(string(srcBytes))
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

	var groups []api.FunctionGroup
	if err := yaml.Unmarshal(buf.Bytes(), &groups); err != nil {
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
			Groups:         groups,
		},
	}, nil
}

// resolveValidatorTemplate is the parallel of resolveChainTemplate for the
// package's spec.validators field. Returns the resolved FunctionGroup list
// ready to feed chainexec.RunValidators.
func resolveValidatorTemplate(pkg *api.Package, inputs *api.Inputs, sel *api.Selection, facts *api.Facts) ([]api.FunctionGroup, error) {
	if len(pkg.Spec.Validators) == 0 {
		return nil, nil
	}
	srcBytes, err := yaml.Marshal(pkg.Spec.Validators)
	if err != nil {
		return nil, fmt.Errorf("marshal validators: %w", err)
	}
	tmpl, err := template.New("validators").Option("missingkey=error").Parse(string(srcBytes))
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
	var groups []api.FunctionGroup
	if err := yaml.Unmarshal(buf.Bytes(), &groups); err != nil {
		return nil, fmt.Errorf("re-parse resolved validators: %w\n----\n%s\n----", err, buf.String())
	}
	return groups, nil
}

// RunValidators is the public entry point used by `installer vet`. Resolves
// the package's spec.validators template against inputs + selection + facts
// and runs each group against data (the concatenated rendered manifests).
// Returns a list of failures, or nil on full success.
func RunValidators(ctx context.Context, pkg *api.Package, sel *api.Selection, inputs *api.Inputs, facts *api.Facts, data []byte) ([]chainexec.ValidatorFailure, error) {
	groups, err := resolveValidatorTemplate(pkg, inputs, sel, facts)
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

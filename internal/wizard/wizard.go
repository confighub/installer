// Package wizard collects user answers (currently non-interactive only),
// validates them against the package's Inputs schema, runs the package's
// optional Collector to discover install-time facts, and emits Selection +
// Inputs + Facts documents into a working directory.
package wizard

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/confighubai/installer/internal/collector"
	"github.com/confighubai/installer/internal/selection"
	"github.com/confighubai/installer/pkg/api"
)

// RawAnswers are CLI-flag-shaped: parsed but not yet validated against the
// package's Inputs schema.
type RawAnswers struct {
	// Inputs is the raw map from --input k=v, where v is a string.
	Inputs map[string]string
	// SelectedComponents is the list from --select (raw user picks; closure
	// happens later in the solver).
	SelectedComponents []string
	// BaseName is the chosen base from --base; "" means use the package default.
	BaseName string
	// Namespace is the Kubernetes namespace from --namespace. Persisted to
	// inputs.yaml and exposed to function-chain templates as {{ .Namespace }}.
	Namespace string
}

// Result bundles the documents the wizard produces. Facts is nil if the
// package declares no Collector.
type Result struct {
	Selection *api.Selection
	Inputs    *api.Inputs
	Facts     *api.Facts
}

// Run validates answers against the package, runs the selection solver,
// invokes the package's Collector (if any), and writes selection.yaml +
// inputs.yaml + facts.yaml into outDir/spec.
//
// packageDir is the absolute path to the loaded package working copy; the
// collector runs with that as its working directory.
func Run(ctx context.Context, pkg *api.Package, raw RawAnswers, packageDir, outDir string) (*Result, error) {
	if raw.Namespace == "" {
		return nil, fmt.Errorf("namespace is required and must be non-empty")
	}
	values, err := coerceInputs(pkg, raw.Inputs)
	if err != nil {
		return nil, err
	}

	sel, err := selection.Resolve(pkg, raw.BaseName, raw.SelectedComponents)
	if err != nil {
		return nil, fmt.Errorf("resolve selection: %w", err)
	}

	inputs := &api.Inputs{
		APIVersion: api.APIVersion,
		Kind:       api.KindInputs,
		Metadata: api.Metadata{
			Name: pkg.Metadata.Name + "-inputs",
		},
		Spec: api.InputsSpec{
			Package:        pkg.Metadata.Name,
			PackageVersion: pkg.Metadata.Version,
			Namespace:      raw.Namespace,
			Values:         values,
		},
	}

	workDir := filepath.Dir(outDir)
	factsValues, err := collector.Run(ctx, pkg, collector.Inputs{
		PackageDir:         packageDir,
		WorkDir:            workDir,
		Namespace:          raw.Namespace,
		Base:               sel.Spec.Base,
		SelectedComponents: sel.Spec.Components,
		InputValues:        values,
	})
	if err != nil {
		return nil, fmt.Errorf("collector: %w", err)
	}

	var facts *api.Facts
	if factsValues != nil {
		facts = &api.Facts{
			APIVersion: api.APIVersion,
			Kind:       api.KindFacts,
			Metadata: api.Metadata{
				Name: pkg.Metadata.Name + "-facts",
			},
			Spec: api.FactsSpec{
				Package:        pkg.Metadata.Name,
				PackageVersion: pkg.Metadata.Version,
				Values:         factsValues,
			},
		}
	}

	specDir := filepath.Join(outDir, "spec")
	if err := os.MkdirAll(specDir, 0o755); err != nil {
		return nil, err
	}

	if err := writeYAML(filepath.Join(specDir, "selection.yaml"), sel); err != nil {
		return nil, err
	}
	if err := writeYAML(filepath.Join(specDir, "inputs.yaml"), inputs); err != nil {
		return nil, err
	}
	if facts != nil {
		if err := writeYAML(filepath.Join(specDir, "facts.yaml"), facts); err != nil {
			return nil, err
		}
	}

	return &Result{Selection: sel, Inputs: inputs, Facts: facts}, nil
}

// coerceInputs validates raw string values against the declared Input schema
// and applies defaults. Returns the typed map keyed by input name.
//
// WhenExternalRequire-gated inputs are skipped if the package does not declare
// a matching ExternalRequire kind.
func coerceInputs(pkg *api.Package, raw map[string]string) (map[string]any, error) {
	declared := make(map[string]*api.Input, len(pkg.Spec.Inputs))
	for i := range pkg.Spec.Inputs {
		declared[pkg.Spec.Inputs[i].Name] = &pkg.Spec.Inputs[i]
	}

	for k := range raw {
		if _, ok := declared[k]; !ok {
			return nil, fmt.Errorf("unknown input %q", k)
		}
	}

	values := map[string]any{}
	for i := range pkg.Spec.Inputs {
		in := &pkg.Spec.Inputs[i]
		if in.WhenExternalRequire != "" && !packageHasExternalRequireKind(pkg, in.WhenExternalRequire) {
			continue
		}
		got, supplied := raw[in.Name]
		if !supplied {
			if in.Default != nil {
				values[in.Name] = in.Default
				continue
			}
			if in.Required {
				return nil, fmt.Errorf("required input %q not provided", in.Name)
			}
			continue
		}
		coerced, err := coerce(in, got)
		if err != nil {
			return nil, fmt.Errorf("input %q: %w", in.Name, err)
		}
		values[in.Name] = coerced
	}
	return values, nil
}

func coerce(in *api.Input, raw string) (any, error) {
	switch in.Type {
	case "", "string":
		return raw, nil
	case "int":
		return strconv.Atoi(raw)
	case "bool":
		return strconv.ParseBool(raw)
	case "enum":
		for _, opt := range in.Options {
			if opt == raw {
				return raw, nil
			}
		}
		return nil, fmt.Errorf("value %q not in enum options %v", raw, in.Options)
	case "list":
		// Comma-separated for the non-interactive wizard.
		parts := strings.Split(raw, ",")
		out := make([]any, 0, len(parts))
		for _, p := range parts {
			out = append(out, strings.TrimSpace(p))
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported input type %q", in.Type)
	}
}

func packageHasExternalRequireKind(pkg *api.Package, k api.ExternalRequireKind) bool {
	for _, r := range pkg.Spec.ExternalRequires {
		if r.Kind == k {
			return true
		}
	}
	for _, b := range pkg.Spec.Bases {
		for _, r := range b.ExternalRequires {
			if r.Kind == k {
				return true
			}
		}
	}
	for _, c := range pkg.Spec.Components {
		for _, r := range c.ExternalRequires {
			if r.Kind == k {
				return true
			}
		}
	}
	return false
}

func writeYAML(path string, v any) error {
	data, err := api.MarshalYAML(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ParseInputFlags parses --input k=v repeated occurrences into a map.
func ParseInputFlags(flags []string) (map[string]string, error) {
	out := map[string]string{}
	for _, f := range flags {
		eq := strings.IndexByte(f, '=')
		if eq < 0 {
			return nil, fmt.Errorf("--input %q must be key=value", f)
		}
		out[f[:eq]] = f[eq+1:]
	}
	return out, nil
}

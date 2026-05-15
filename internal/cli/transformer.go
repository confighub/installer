package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/confighub/installer/internal/chainexec"
	"github.com/confighub/installer/pkg/api"
)

// KRM Functions ResourceList — https://github.com/kubernetes-sigs/kustomize/blob/master/cmd/config/docs/api-conventions/functions-spec.md.
// We accept config.kubernetes.io/v1 and kpt's older io.k8s.cli/v1alpha2 shapes
// transparently: the difference is just the apiVersion string.
const (
	resourceListAPIVersion = "config.kubernetes.io/v1"
	resourceListKind       = "ResourceList"

	kindConfigHubTransformers = "ConfigHubTransformers"
	kindConfigHubValidators   = "ConfigHubValidators"
)

// resourceList mirrors the KRM Functions ResourceList shape. Items and
// FunctionConfig are yaml.Node so we round-trip with reasonable fidelity
// (comments, key order, scalar styles) — the function executor only sees
// re-marshalled bytes anyway, but kustomize-level annotations on items
// (config.kubernetes.io/path, config.kubernetes.io/index) ride through
// untouched.
type resourceList struct {
	APIVersion string      `yaml:"apiVersion"`
	Kind       string      `yaml:"kind"`
	Items      []yaml.Node `yaml:"items"`
	// FunctionConfig is yaml.Node (not *yaml.Node) — yaml.v3 only
	// special-cases the value form for capturing a subtree; the pointer form
	// falls through to field-by-field decoding and chokes on yaml.Node's own
	// Kind field (which is the enum, not a string).
	FunctionConfig yaml.Node   `yaml:"functionConfig,omitempty"`
	Results        []krmResult `yaml:"results,omitempty"`
}

type krmResult struct {
	Message     string          `yaml:"message"`
	Severity    string          `yaml:"severity,omitempty"`
	ResourceRef *krmResourceRef `yaml:"resourceRef,omitempty"`
}

type krmResourceRef struct {
	APIVersion string `yaml:"apiVersion,omitempty"`
	Kind       string `yaml:"kind,omitempty"`
	Name       string `yaml:"name,omitempty"`
	Namespace  string `yaml:"namespace,omitempty"`
}

// functionConfigHeader is the slim view of the functionConfig we need to
// dispatch on Kind and to lift Groups out of Spec. Everything else
// (annotations, the config.kubernetes.io/function exec annotation kustomize
// uses to find this binary) is ignored.
type functionConfigHeader struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Groups []api.FunctionGroup `yaml:"groups"`
	} `yaml:"spec"`
}

func newTransformerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "transformer",
		Short: "Run ConfigHub functions as a kustomize exec transformer (reads ResourceList from stdin)",
		Long: `Transformer is a KRM Functions exec plugin for kustomize. It reads a
ResourceList from stdin, runs the ConfigHub function chain or validators
encoded in the ResourceList's functionConfig against the items, and writes
the resulting ResourceList to stdout.

Two functionConfig kinds are recognized:

  ConfigHubTransformers — mutating. Goes under kustomization.transformers:.
                          spec.groups is a list of FunctionGroups whose
                          output feeds the next.
  ConfigHubValidators   — non-mutating. Goes under kustomization.validators:.
                          spec.groups is a list of validator groups.
                          Failures are emitted as ResourceList.results with
                          severity=error; items are not modified. Kustomize
                          fails the build when any result has severity=error.

Each group's whereResource uses ConfigHub filter syntax (e.g.
"ConfigHub.ResourceType = 'apps/v1/Deployment'"), not CEL — the expression
is passed through verbatim to the function executor's WhereResource option.

Usable standalone with raw kustomize: reference this subcommand from your
kustomization's transformers: list via a KRM function config whose
config.kubernetes.io/function annotation points at a wrapper that runs
'installer transformer'.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			in, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return fmt.Errorf("read stdin: %w", err)
			}
			out, err := runTransformer(ctx, in)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(out)
			return err
		},
	}
}

// runTransformer is the testable core: takes a ResourceList YAML, returns
// the result ResourceList YAML. Function-execution errors (malformed input,
// unsupported toolchain) are returned as Go errors → non-zero exit; validator
// failures land in ResourceList.results with severity=error → zero exit, but
// kustomize fails the build based on the results.
func runTransformer(ctx context.Context, in []byte) ([]byte, error) {
	var list resourceList
	if err := yaml.Unmarshal(in, &list); err != nil {
		return nil, fmt.Errorf("parse ResourceList: %w", err)
	}
	if list.FunctionConfig.Kind == 0 {
		return nil, fmt.Errorf("ResourceList has no functionConfig — nothing to do")
	}

	var header functionConfigHeader
	if err := list.FunctionConfig.Decode(&header); err != nil {
		return nil, fmt.Errorf("decode functionConfig: %w", err)
	}

	// AppConfig annotation pre-pass runs regardless of which kind we're
	// dispatching, so the canonical contract (toolchain + mode + source-key)
	// is set on every annotated ConfigMap before any function group fires.
	if err := injectAppConfigAnnotations(&list); err != nil {
		return nil, err
	}

	switch header.Kind {
	case kindConfigHubTransformers:
		if err := runChainTransform(ctx, &list, header); err != nil {
			return nil, err
		}
	case kindConfigHubValidators:
		if err := runValidatorTransform(ctx, &list, header); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported functionConfig kind %q (want %s or %s)",
			header.Kind, kindConfigHubTransformers, kindConfigHubValidators)
	}

	if list.APIVersion == "" {
		list.APIVersion = resourceListAPIVersion
	}
	if list.Kind == "" {
		list.Kind = resourceListKind
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&list); err != nil {
		return nil, fmt.Errorf("encode ResourceList: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func runChainTransform(ctx context.Context, list *resourceList, header functionConfigHeader) error {
	if len(list.Items) == 0 || len(header.Spec.Groups) == 0 {
		return nil
	}
	// Groups are dispatched one at a time so AppConfig/* groups can take the
	// per-carrier round-trip path while Kubernetes/YAML groups (and anything
	// else top-level) run via chainexec.RunChain on the joined item stream.
	// Each group's output mutates list.Items in place, so the data-feeds-
	// forward semantic still holds across mixed-toolchain chains.
	for _, group := range header.Spec.Groups {
		if isAppConfigToolchain(group.Toolchain) {
			if err := runAppConfigGroup(ctx, list, group, true, nil); err != nil {
				return err
			}
			continue
		}
		stream, err := encodeItemsAsStream(list.Items)
		if err != nil {
			return err
		}
		mutated, err := chainexec.RunChain(ctx, &api.FunctionChain{
			APIVersion: api.APIVersion,
			Kind:       api.KindFunctionChain,
			Metadata:   api.Metadata{Name: chainNameOr(header, "chain")},
			Spec:       api.FunctionChainSpec{Groups: []api.FunctionGroup{group}},
		}, stream)
		if err != nil {
			return err
		}
		items, err := decodeStreamAsItems(mutated)
		if err != nil {
			return err
		}
		list.Items = items
	}
	return nil
}

func runValidatorTransform(ctx context.Context, list *resourceList, header functionConfigHeader) error {
	if len(list.Items) == 0 || len(header.Spec.Groups) == 0 {
		return nil
	}
	for _, group := range header.Spec.Groups {
		if isAppConfigToolchain(group.Toolchain) {
			if err := runAppConfigGroup(ctx, list, group, false, &list.Results); err != nil {
				return err
			}
			continue
		}
		stream, err := encodeItemsAsStream(list.Items)
		if err != nil {
			return err
		}
		failures, err := chainexec.RunValidators(ctx, []api.FunctionGroup{group}, stream)
		if err != nil {
			return err
		}
		for _, f := range failures {
			prefix := f.FunctionName
			if f.UnitSlug != "" {
				prefix = f.UnitSlug + "/" + prefix
			}
			if len(f.Details) == 0 {
				list.Results = append(list.Results, krmResult{
					Message:  fmt.Sprintf("%s: failed", prefix),
					Severity: "error",
				})
				continue
			}
			for _, d := range f.Details {
				list.Results = append(list.Results, krmResult{
					Message:  fmt.Sprintf("%s: %s", prefix, d),
					Severity: "error",
				})
			}
		}
	}
	return nil
}

func chainNameOr(h functionConfigHeader, fallback string) string {
	if h.Metadata.Name != "" {
		return h.Metadata.Name
	}
	return fallback
}

// encodeItemsAsStream marshals each item as a separate YAML document so the
// chainexec executor sees a `---`-separated multi-doc stream — the same shape
// it gets from `kustomize build` output today.
func encodeItemsAsStream(items []yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	for i := range items {
		if err := enc.Encode(&items[i]); err != nil {
			return nil, fmt.Errorf("encode item %d: %w", i, err)
		}
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decodeStreamAsItems splits the multi-doc YAML stream returned by chainexec
// back into yaml.Nodes suitable for ResourceList.items. DocumentNode wrappers
// are unwrapped to their content; empty docs are dropped.
func decodeStreamAsItems(stream []byte) ([]yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(stream))
	var out []yaml.Node
	for {
		var node yaml.Node
		err := dec.Decode(&node)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode item: %w", err)
		}
		if node.Kind == yaml.DocumentNode {
			if len(node.Content) == 0 {
				continue
			}
			out = append(out, *node.Content[0])
			continue
		}
		if node.Kind == 0 {
			continue
		}
		out = append(out, node)
	}
	return out, nil
}

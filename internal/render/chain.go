package render

import (
	"bytes"
	"context"
	"fmt"
	"text/template"

	"github.com/confighubai/installer/pkg/api"
	fapi "github.com/confighub/sdk/core/function/api"
	"github.com/confighub/sdk/core/workerapi"
	funcimpl "github.com/confighub/sdk/function-impl"
	"gopkg.in/yaml.v3"
)

// resolveChainTemplate expands Go template expressions inside the package's
// FunctionChainTemplate against the resolved Inputs and Selection. Returns
// the materialized FunctionChain ready to execute. Empty arg strings after
// resolution are kept (they may legitimately encode "set this field empty");
// callers that want to skip empty groups should filter post-resolution.
func resolveChainTemplate(pkg *api.Package, inputs *api.Inputs, sel *api.Selection) (*api.FunctionChain, error) {
	// Marshal the template to YAML, run text/template over the bytes, then
	// re-parse. This lets the template author use {{ .Inputs.foo }} anywhere
	// a string appears in the chain (function args, whereResource, even
	// toolchain), without us having to recurse through every field.
	srcBytes, err := yaml.Marshal(pkg.Spec.FunctionChainTemplate)
	if err != nil {
		return nil, fmt.Errorf("marshal template: %w", err)
	}

	tmpl, err := template.New("chain").Option("missingkey=error").Parse(string(srcBytes))
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	ctx := map[string]any{
		"Inputs":    inputs.Spec.Values,
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

// runChain executes the resolved FunctionChain against rendered manifests.
// Each group fires one FunctionInvocationRequest; the response's ConfigData
// feeds the next group. Mirrors invokeLocalFunctions in cub's worker_install.
func runChain(ctx context.Context, chain *api.FunctionChain, input []byte) ([]byte, error) {
	executor := funcimpl.NewStandardExecutor(nil, true)
	registered := executor.RegisteredFunctions()

	data := input
	for i, group := range chain.Spec.Groups {
		toolchain := workerapi.ToolchainType(group.Toolchain)
		fns, ok := registered[toolchain]
		if !ok {
			return nil, fmt.Errorf("group %d: no functions registered for toolchain %q", i, group.Toolchain)
		}

		invs := make(fapi.FunctionInvocationList, 0, len(group.Invocations))
		for _, inv := range group.Invocations {
			if _, exists := fns[inv.Name]; !exists {
				return nil, fmt.Errorf("group %d: function %q not registered for toolchain %q",
					i, inv.Name, group.Toolchain)
			}
			args := make([]fapi.FunctionArgument, 0, len(inv.Args))
			for _, a := range inv.Args {
				args = append(args, fapi.FunctionArgument{Value: a})
			}
			invs = append(invs, fapi.FunctionInvocation{
				FunctionName: inv.Name,
				Arguments:    args,
			})
		}

		req := &fapi.FunctionInvocationRequest{
			FunctionContext: fapi.FunctionContext{ToolchainType: toolchain},
			ConfigData:      data,
			FunctionInvocationOptions: fapi.FunctionInvocationOptions{
				WhereResource: group.WhereResource,
				StopOnError:   true,
			},
			FunctionInvocations: invs,
		}
		resp, err := executor.Invoke(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("group %d (%s): %w", i, group.Toolchain, err)
		}
		if !resp.Success {
			return nil, fmt.Errorf("group %d (%s): %v", i, group.Toolchain, resp.ErrorMessages)
		}
		if len(resp.ConfigData) > 0 {
			data = resp.ConfigData
		}
	}
	return data, nil
}

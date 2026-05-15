// Package chainexec executes ConfigHub FunctionChain and validator groups
// against config data. It's the shared core consumed by `installer render`
// (after template resolution) and by `installer transformer` (which receives
// already-resolved groups from a KRM ResourceList functionConfig). Template
// resolution itself lives in internal/render — it depends on installer state
// that's irrelevant to the kustomize-side caller.
package chainexec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/confighub/installer/pkg/api"
	fapi "github.com/confighub/sdk/core/function/api"
	"github.com/confighub/sdk/core/workerapi"
	funcimpl "github.com/confighub/sdk/function-impl"
)

// RunChain executes the resolved FunctionChain against the input YAML stream.
// Each group fires one FunctionInvocationRequest; the response's ConfigData
// feeds the next group. Mirrors invokeLocalFunctions in cub's worker_install.
func RunChain(ctx context.Context, chain *api.FunctionChain, input []byte) ([]byte, error) {
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
			args, err := ParseFunctionArguments(inv.Args)
			if err != nil {
				return nil, fmt.Errorf("group %d, function %q: %w", i, inv.Name, err)
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

// ValidatorFailure is one failing validation result. Returned by
// RunValidators; formatted into operator-facing error messages via
// FormatValidatorFailures.
type ValidatorFailure struct {
	Group        int
	FunctionName string
	UnitSlug     string
	Details      []string
}

// RunValidators executes validator groups against data. Each group is invoked
// with StopOnError=false so every validator runs and the operator sees a
// complete report. Returns nil on full success.
//
// Validators must be Validating functions (Mutating=false). Mutating functions
// in the validators list are rejected before any invocation runs — the
// validators list is a contract, not a generic chain.
func RunValidators(ctx context.Context, groups []api.FunctionGroup, data []byte) ([]ValidatorFailure, error) {
	if len(groups) == 0 {
		return nil, nil
	}
	executor := funcimpl.NewStandardExecutor(nil, true)
	registered := executor.RegisteredFunctions()

	var failures []ValidatorFailure
	for i, group := range groups {
		toolchain := workerapi.ToolchainType(group.Toolchain)
		fns, ok := registered[toolchain]
		if !ok {
			return nil, fmt.Errorf("validators group %d: no functions registered for toolchain %q", i, group.Toolchain)
		}
		invs := make(fapi.FunctionInvocationList, 0, len(group.Invocations))
		for _, inv := range group.Invocations {
			sig, exists := fns[inv.Name]
			if !exists {
				return nil, fmt.Errorf("validators group %d: function %q not registered for toolchain %q",
					i, inv.Name, group.Toolchain)
			}
			if sig.Mutating || !sig.Validating {
				return nil, fmt.Errorf("validators group %d: function %q is not a validating function — declare it under transformers instead",
					i, inv.Name)
			}
			args, err := ParseFunctionArguments(inv.Args)
			if err != nil {
				return nil, fmt.Errorf("validators group %d, function %q: %w", i, inv.Name, err)
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
				StopOnError:   false,
			},
			FunctionInvocations: invs,
		}
		resp, err := executor.Invoke(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("validators group %d (%s): %w", i, group.Toolchain, err)
		}
		failures = append(failures, decodeValidatorFailures(i, group, resp)...)
	}
	return failures, nil
}

// decodeValidatorFailures walks the executor response's Outputs map and pulls
// out failing ValidationResults. Outputs is keyed by OutputType with
// JSON-encoded bodies; validators emit OutputTypeValidationResult or
// OutputTypeValidationResultList. A non-empty resp.ErrorMessages also surfaces
// as a synthetic failure so the operator sees executor-level errors
// (StopOnError=false only stops the group, not individual invocation errors).
func decodeValidatorFailures(groupIdx int, group api.FunctionGroup, resp *fapi.FunctionInvocationResponse) []ValidatorFailure {
	var failures []ValidatorFailure
	for _, msg := range resp.ErrorMessages {
		failures = append(failures, ValidatorFailure{
			Group:    groupIdx,
			UnitSlug: resp.UnitSlug,
			Details:  []string{msg},
		})
	}
	for outType, body := range resp.Outputs {
		if outType != fapi.OutputTypeValidationResult && outType != fapi.OutputTypeValidationResultList {
			continue
		}
		var list fapi.ValidationResultList
		if err := json.Unmarshal(body, &list); err != nil {
			var single fapi.ValidationResult
			if err2 := json.Unmarshal(body, &single); err2 != nil {
				failures = append(failures, ValidatorFailure{
					Group:        groupIdx,
					FunctionName: "(decode)",
					UnitSlug:     resp.UnitSlug,
					Details:      []string{fmt.Sprintf("could not decode validator output: %v", err)},
				})
				continue
			}
			list = fapi.ValidationResultList{single}
		}
		for _, r := range list {
			if !r.Passed {
				failures = append(failures, validatorFailureFor(groupIdx, group, resp.UnitSlug, r))
			}
		}
	}
	return failures
}

func validatorFailureFor(groupIdx int, group api.FunctionGroup, unitSlug string, r fapi.ValidationResult) ValidatorFailure {
	name := r.FunctionName
	if name == "" && r.Index < len(group.Invocations) {
		name = group.Invocations[r.Index].Name
	}
	details := append([]string(nil), r.Details...)
	for _, iss := range r.Issues {
		details = append(details, iss.Message)
	}
	for _, av := range r.FailedAttributes {
		details = append(details, fmt.Sprintf("%s: %v", av.Path, av.Value))
	}
	return ValidatorFailure{
		Group:        groupIdx,
		FunctionName: name,
		UnitSlug:     unitSlug,
		Details:      details,
	}
}

// FormatValidatorFailures renders a list of failures as a multi-line error
// message suitable for the operator. Empty slice → empty string.
func FormatValidatorFailures(failures []ValidatorFailure) string {
	if len(failures) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d validator failure(s):\n", len(failures))
	for _, f := range failures {
		prefix := f.FunctionName
		if f.UnitSlug != "" {
			prefix = f.UnitSlug + "/" + prefix
		}
		if len(f.Details) == 0 {
			fmt.Fprintf(&b, "  - %s: failed\n", prefix)
			continue
		}
		for _, d := range f.Details {
			fmt.Fprintf(&b, "  - %s: %s\n", prefix, d)
		}
	}
	return b.String()
}

// ParseFunctionArguments converts cub-CLI-shaped arg strings into the
// FunctionArgument form the executor expects. An arg of the form
// `--name=value` becomes a named argument with ParameterName=name; a bare arg
// becomes a positional argument with Value=<arg>.
//
// Mirrors the parser in public/cmd/cub/function_do.go so chain templates can
// use the same `--liveness-path=/internal/ok` style as worker_install.go.
func ParseFunctionArguments(args []string) ([]fapi.FunctionArgument, error) {
	out := make([]fapi.FunctionArgument, 0, len(args))
	namedMode := false
	for _, a := range args {
		if strings.HasPrefix(a, "--") && strings.Contains(a, "=") {
			namedMode = true
			parts := strings.SplitN(a, "=", 2)
			name := strings.TrimPrefix(parts[0], "--")
			out = append(out, fapi.FunctionArgument{ParameterName: name, Value: parts[1]})
			continue
		}
		if namedMode {
			return nil, fmt.Errorf("positional argument %q cannot follow named arguments", a)
		}
		out = append(out, fapi.FunctionArgument{Value: a})
	}
	return out, nil
}

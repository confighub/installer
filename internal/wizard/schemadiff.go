package wizard

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/confighubai/installer/pkg/api"
)

// InputDiff is the result of comparing the input schemas of an old
// and a new package against a set of prior values. The four buckets
// are mutually exclusive — every prior key + every new declared input
// lands in exactly one of them.
type InputDiff struct {
	// Carry are values that exist in both packages with a compatible
	// type and a recorded prior value. They flow through to the new
	// inputs.yaml verbatim.
	Carry map[string]any
	// AdoptedDefaults are inputs new in this version that have a
	// declared default. The default is silently used.
	AdoptedDefaults map[string]any
	// NewRequired are inputs new in this version that are required and
	// have no default. They must be answered (interactive prompt or
	// fail-fast in non-interactive mode).
	NewRequired []api.Input
	// Dropped are prior keys whose input was removed in the new
	// package. Operator does not need to do anything; the value is
	// silently omitted from the new inputs.yaml.
	Dropped []string
	// TypeChanged are inputs whose declared Type differs between old
	// and new. The operator must re-answer because coercion may
	// silently misinterpret the value.
	TypeChanged []api.Input
}

// DiffInputs walks oldPkg's inputs and newPkg's inputs against a set
// of prior values (typically prior.Inputs.Spec.Values), bucketing each
// declared input. Inputs gated by WhenExternalRequire that the new
// package does not actually require are silently skipped (mirrors
// coerceInputs's gate).
func DiffInputs(oldPkg, newPkg *api.Package, prior map[string]any) InputDiff {
	out := InputDiff{
		Carry:           map[string]any{},
		AdoptedDefaults: map[string]any{},
	}

	oldByName := indexInputs(oldPkg)
	newByName := indexInputs(newPkg)

	// Walk new inputs first to fill Carry / AdoptedDefaults / NewRequired
	// / TypeChanged.
	for _, in := range newPkg.Spec.Inputs {
		if in.WhenExternalRequire != "" && !packageHasExternalRequireKind(newPkg, in.WhenExternalRequire) {
			continue
		}
		oldIn, hadBefore := oldByName[in.Name]
		priorVal, hadValue := prior[in.Name]

		// Type-changed: existed before with a different type. Operator
		// must re-answer.
		if hadBefore && !typesCompatible(oldIn.Type, in.Type) {
			out.TypeChanged = append(out.TypeChanged, in)
			continue
		}
		if hadValue {
			out.Carry[in.Name] = priorVal
			continue
		}
		if in.Default != nil {
			out.AdoptedDefaults[in.Name] = in.Default
			continue
		}
		if in.Required {
			out.NewRequired = append(out.NewRequired, in)
		}
		// Otherwise: optional, no prior, no default — leave unset.
	}

	// Dropped: keys in the prior map that are not in newByName at all.
	for k := range prior {
		if _, present := newByName[k]; !present {
			out.Dropped = append(out.Dropped, k)
		}
	}
	sort.Strings(out.Dropped)
	return out
}

// SelectionDiff is the result of comparing component selections.
type SelectionDiff struct {
	// Components is the resolved new selection — the operator's
	// effective component list under the new package's component
	// graph.
	Components []string
	// PreservedFromPrior is a copy of Components for callers that want
	// to know how many entries were carried forward from the prior
	// selection.
	PreservedFromPrior []string
	// AdoptedNewDefaults names components new in this version that
	// were silently selected because the prior install used the
	// default preset and the components are flagged default: true.
	AdoptedNewDefaults []string
	// RemovedFromPrior names prior selections whose component no
	// longer exists in the new package. Silently dropped.
	RemovedFromPrior []string
	// DefaultPresetDetected is true when prior matches old package's
	// `default` preset exactly — the upgrade re-runs the default
	// preset against the new package rather than carrying the
	// (possibly stale) component list verbatim.
	DefaultPresetDetected bool
}

// DiffComponents merges priorSelection's Components into the new
// package's component graph, honoring the prior preset. If the prior
// selection matches the old package's default preset (every
// `default: true` component, nothing else), the upgrade adopts the
// new package's default preset — so a new default-flagged component
// flows in automatically. Otherwise the prior list is filtered to
// components that still exist.
//
// priorSelection may be nil (treated as empty selection).
func DiffComponents(oldPkg, newPkg *api.Package, prior []string) SelectionDiff {
	out := SelectionDiff{}
	priorSet := stringSet(prior)
	newNames := stringSet(componentNames(newPkg))

	if matchesDefaultPreset(oldPkg, prior) {
		out.DefaultPresetDetected = true
		newDefaults := defaultComponentNames(newPkg)
		out.Components = append([]string(nil), newDefaults...)
		out.PreservedFromPrior = append([]string(nil), out.Components...)
		// Among the new defaults, anything not in the prior set is a
		// "newly adopted" component.
		for _, c := range newDefaults {
			if _, was := priorSet[c]; !was {
				out.AdoptedNewDefaults = append(out.AdoptedNewDefaults, c)
			}
		}
		// Anything in prior that isn't in the new graph is dropped.
		for _, p := range prior {
			if _, ok := newNames[p]; !ok {
				out.RemovedFromPrior = append(out.RemovedFromPrior, p)
			}
		}
		sort.Strings(out.RemovedFromPrior)
		return out
	}

	// Non-default-preset path: filter prior to components that still
	// exist in the new package, in deterministic order.
	for _, p := range prior {
		if _, ok := newNames[p]; ok {
			out.Components = append(out.Components, p)
			out.PreservedFromPrior = append(out.PreservedFromPrior, p)
		} else {
			out.RemovedFromPrior = append(out.RemovedFromPrior, p)
		}
	}
	sort.Strings(out.RemovedFromPrior)
	return out
}

func indexInputs(pkg *api.Package) map[string]api.Input {
	m := make(map[string]api.Input, len(pkg.Spec.Inputs))
	for _, in := range pkg.Spec.Inputs {
		m[in.Name] = in
	}
	return m
}

// typesCompatible treats empty / "string" as equivalent (Input.Type
// defaults to string when omitted). Other types must match exactly.
func typesCompatible(a, b string) bool {
	if a == "" {
		a = "string"
	}
	if b == "" {
		b = "string"
	}
	return a == b
}

func componentNames(pkg *api.Package) []string {
	out := make([]string, 0, len(pkg.Spec.Components))
	for _, c := range pkg.Spec.Components {
		out = append(out, c.Name)
	}
	return out
}

func defaultComponentNames(pkg *api.Package) []string {
	var out []string
	for _, c := range pkg.Spec.Components {
		if c.Default {
			out = append(out, c.Name)
		}
	}
	return out
}

// matchesDefaultPreset reports whether selection matches the set of
// `default: true` components in pkg (set equality, ignoring order).
// Empty selection matches a package with no default components.
func matchesDefaultPreset(pkg *api.Package, selection []string) bool {
	want := stringSet(defaultComponentNames(pkg))
	got := stringSet(selection)
	return reflect.DeepEqual(want, got)
}

func stringSet(ss []string) map[string]struct{} {
	out := make(map[string]struct{}, len(ss))
	for _, s := range ss {
		out[s] = struct{}{}
	}
	return out
}

// FormatNewRequiredHint renders a multiline message naming each new
// required input — used by `installer upgrade` in non-interactive
// mode to fail fast with actionable detail.
func FormatNewRequiredHint(workDir string, ins []api.Input) string {
	if len(ins) == 0 {
		return ""
	}
	msg := fmt.Sprintf("the new package adds %d required input(s) that the prior install did not answer:\n", len(ins))
	for _, in := range ins {
		prompt := in.Prompt
		if prompt == "" {
			prompt = in.Description
		}
		if prompt == "" {
			msg += fmt.Sprintf("  - %s (%s)\n", in.Name, displayType(in))
		} else {
			msg += fmt.Sprintf("  - %s (%s): %s\n", in.Name, displayType(in), prompt)
		}
	}
	msg += fmt.Sprintf("re-run interactively or run `installer wizard %s` to answer them, then re-run `installer upgrade`.", workDir)
	return msg
}

func displayType(in api.Input) string {
	if in.Type == "" {
		return "string"
	}
	return in.Type
}

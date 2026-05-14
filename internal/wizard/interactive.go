package wizard

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/AlecAivazis/survey/v2"
	"github.com/AlecAivazis/survey/v2/terminal"

	"github.com/confighubai/installer/pkg/api"
)

// Component-set presets the wizard offers as the high-level shortcut to
// component selection. Strings here are also the user-facing prompt
// values; keep them stable across releases.
const (
	PresetMinimal  = "minimal"
	PresetDefault  = "default"
	PresetAll      = "all"
	PresetSelected = "selected"
)

// AllPresets is the menu order shown by the wizard.
var AllPresets = []string{PresetMinimal, PresetDefault, PresetAll, PresetSelected}

// AskOptions tunes the interactive flow.
type AskOptions struct {
	// Stdio overrides the I/O streams; nil uses the real terminal.
	// Tests pass scripted streams via survey/v2's WithStdio.
	Stdio *terminal.Stdio
}

// Ask runs the interactive wizard against pkg, optionally pre-filling
// every prompt from prior. Returns RawAnswers ready to feed Run().
//
// On Ctrl+C / EOF the function returns ErrAborted so the caller can
// distinguish operator cancellation from a real failure.
func Ask(pkg *api.Package, prior *PriorState, opts AskOptions) (RawAnswers, error) {
	out := RawAnswers{Inputs: map[string]string{}}

	if prior != nil && (prior.Selection != nil || prior.Inputs != nil) {
		reuse := true
		if err := askConfirm("Re-use last choices?", true, &reuse, opts); err != nil {
			return out, err
		}
		if reuse {
			return RawAnswersFromPrior(pkg, prior), nil
		}
	}

	base, err := askBase(pkg, prior, opts)
	if err != nil {
		return out, err
	}
	out.BaseName = base

	preset, components, err := askComponents(pkg, prior, opts)
	if err != nil {
		return out, err
	}
	out.SelectedComponents = components
	_ = preset // recorded into Selection.spec by Run via the selection solver

	ns, err := askNamespace(prior, opts)
	if err != nil {
		return out, err
	}
	out.Namespace = ns

	if err := askInputs(pkg, prior, &out, opts); err != nil {
		return out, err
	}
	return out, nil
}

// RawAnswersFromPrior turns a PriorState into a RawAnswers as if the
// operator had just re-typed the same answers. Used for the "re-use
// last choices" fast path, the --reuse CLI flag, and upgrade's
// non-interactive carry-over.
func RawAnswersFromPrior(pkg *api.Package, prior *PriorState) RawAnswers {
	out := RawAnswers{Inputs: map[string]string{}}
	if prior.Selection != nil {
		out.BaseName = prior.Selection.Spec.Base
		out.SelectedComponents = append([]string(nil), prior.Selection.Spec.Components...)
	}
	if prior.Inputs != nil {
		out.Namespace = prior.Inputs.Spec.Namespace
		for k, v := range prior.Inputs.Spec.Values {
			out.Inputs[k] = stringifyAny(v)
		}
	}
	_ = pkg
	return out
}

// ResolvePreset turns a preset name into a component slice, without
// touching the filesystem or the terminal. Used by the non-interactive
// CLI to support `--components <preset>`.
func ResolvePreset(pkg *api.Package, preset string) ([]string, error) {
	switch preset {
	case PresetMinimal:
		return nil, nil
	case PresetDefault:
		return defaultComponents(pkg), nil
	case PresetAll:
		return allComponents(pkg), nil
	case PresetSelected:
		return nil, fmt.Errorf("preset %q requires interactive mode or explicit --select flags", preset)
	default:
		return nil, fmt.Errorf("unknown preset %q (want one of: %s)", preset, strings.Join(AllPresets, ", "))
	}
}

func askBase(pkg *api.Package, prior *PriorState, opts AskOptions) (string, error) {
	if len(pkg.Spec.Bases) == 0 {
		return "", fmt.Errorf("package declares no bases")
	}
	if len(pkg.Spec.Bases) == 1 {
		return pkg.Spec.Bases[0].Name, nil
	}
	names := make([]string, 0, len(pkg.Spec.Bases))
	descrs := map[string]string{}
	for _, b := range pkg.Spec.Bases {
		names = append(names, b.Name)
		descrs[b.Name] = b.Description
	}
	def := defaultBaseName(pkg, prior)
	var choice string
	q := &survey.Select{
		Message: "Base:",
		Options: names,
		Default: def,
		Description: func(value string, _ int) string {
			return descrs[value]
		},
	}
	if err := ask(q, &choice, opts); err != nil {
		return "", err
	}
	return choice, nil
}

func defaultBaseName(pkg *api.Package, prior *PriorState) string {
	if prior != nil && prior.Selection != nil && prior.Selection.Spec.Base != "" {
		for _, b := range pkg.Spec.Bases {
			if b.Name == prior.Selection.Spec.Base {
				return b.Name
			}
		}
	}
	for _, b := range pkg.Spec.Bases {
		if b.Default {
			return b.Name
		}
	}
	return pkg.Spec.Bases[0].Name
}

// askComponents resolves the preset prompt, then either returns the
// preset's component set directly (minimal/default/all) or drops into
// a multi-select for `selected`. Returns the preset string for caller
// audit and the resolved component name list.
func askComponents(pkg *api.Package, prior *PriorState, opts AskOptions) (string, []string, error) {
	if len(pkg.Spec.Components) == 0 {
		return PresetMinimal, nil, nil
	}
	def := defaultPreset(prior)
	var preset string
	q := &survey.Select{
		Message: "Components:",
		Options: AllPresets,
		Default: def,
		Description: func(value string, _ int) string {
			switch value {
			case PresetMinimal:
				return "just the base; nothing optional"
			case PresetDefault:
				return "components the package marks default"
			case PresetAll:
				return "every component"
			case PresetSelected:
				return "pick individually"
			}
			return ""
		},
	}
	if err := ask(q, &preset, opts); err != nil {
		return "", nil, err
	}
	switch preset {
	case PresetMinimal:
		return preset, nil, nil
	case PresetDefault:
		return preset, defaultComponents(pkg), nil
	case PresetAll:
		return preset, allComponents(pkg), nil
	case PresetSelected:
		picked, err := askComponentMultiSelect(pkg, prior, opts)
		if err != nil {
			return "", nil, err
		}
		return preset, picked, nil
	}
	return "", nil, fmt.Errorf("unknown preset %q", preset)
}

func defaultPreset(prior *PriorState) string {
	if prior != nil && prior.Selection != nil {
		// If the prior selection had any components, default to
		// `selected` so the user can edit; otherwise minimal.
		if len(prior.Selection.Spec.Components) > 0 {
			return PresetSelected
		}
		return PresetMinimal
	}
	return PresetDefault
}

func defaultComponents(pkg *api.Package) []string {
	out := []string{}
	for _, c := range pkg.Spec.Components {
		if c.Default {
			out = append(out, c.Name)
		}
	}
	return out
}

func allComponents(pkg *api.Package) []string {
	out := make([]string, 0, len(pkg.Spec.Components))
	for _, c := range pkg.Spec.Components {
		out = append(out, c.Name)
	}
	return out
}

func askComponentMultiSelect(pkg *api.Package, prior *PriorState, opts AskOptions) ([]string, error) {
	names := allComponents(pkg)
	descrs := map[string]string{}
	for _, c := range pkg.Spec.Components {
		descrs[c.Name] = c.Description
	}
	def := []string{}
	if prior != nil && prior.Selection != nil {
		// Preserve only those that still exist in the new package.
		valid := map[string]struct{}{}
		for _, n := range names {
			valid[n] = struct{}{}
		}
		for _, n := range prior.Selection.Spec.Components {
			if _, ok := valid[n]; ok {
				def = append(def, n)
			}
		}
	} else {
		def = defaultComponents(pkg)
	}
	var picked []string
	q := &survey.MultiSelect{
		Message: "Pick components:",
		Options: names,
		Default: def,
		Description: func(value string, _ int) string {
			return descrs[value]
		},
	}
	if err := ask(q, &picked, opts); err != nil {
		return nil, err
	}
	sort.Strings(picked)
	return picked, nil
}

func askNamespace(prior *PriorState, opts AskOptions) (string, error) {
	def := ""
	if prior != nil && prior.Inputs != nil {
		def = prior.Inputs.Spec.Namespace
	}
	var ns string
	q := &survey.Input{
		Message: "Kubernetes namespace:",
		Default: def,
	}
	if err := ask(q, &ns, opts, survey.WithValidator(survey.Required)); err != nil {
		return "", err
	}
	return strings.TrimSpace(ns), nil
}

// askInputs prompts for any input that is required and lacks a default
// (and prior value), then optionally walks the operator through every
// other input under a "tweak inputs?" gate. Inputs gated by
// WhenExternalRequire that the package does not actually require are
// skipped silently.
func askInputs(pkg *api.Package, prior *PriorState, out *RawAnswers, opts AskOptions) error {
	priorVals := map[string]any{}
	if prior != nil && prior.Inputs != nil {
		priorVals = prior.Inputs.Spec.Values
	}

	required := []*api.Input{}
	optional := []*api.Input{}
	for i := range pkg.Spec.Inputs {
		in := &pkg.Spec.Inputs[i]
		if in.WhenExternalRequire != "" && !packageHasExternalRequireKind(pkg, in.WhenExternalRequire) {
			continue
		}
		_, hasPrior := priorVals[in.Name]
		hasDefault := in.Default != nil || hasPrior
		if in.Required && !hasDefault {
			required = append(required, in)
		} else {
			optional = append(optional, in)
		}
	}

	for _, in := range required {
		if err := promptInput(in, priorVals[in.Name], out, opts); err != nil {
			return err
		}
	}

	if len(optional) == 0 {
		return nil
	}
	tweak := false
	if err := askConfirm("Tweak any other inputs?", false, &tweak, opts); err != nil {
		return err
	}
	if !tweak {
		// Carry prior values forward verbatim — Ask's Run-time call
		// merges these into Inputs.spec.values.
		for _, in := range optional {
			if v, ok := priorVals[in.Name]; ok {
				out.Inputs[in.Name] = stringifyAny(v)
			}
		}
		return nil
	}
	for _, in := range optional {
		if err := promptInput(in, priorVals[in.Name], out, opts); err != nil {
			return err
		}
	}
	return nil
}

func promptInput(in *api.Input, prior any, out *RawAnswers, opts AskOptions) error {
	def := ""
	if prior != nil {
		def = stringifyAny(prior)
	} else if in.Default != nil {
		def = stringifyAny(in.Default)
	}
	msg := in.Prompt
	if msg == "" {
		msg = in.Name + ":"
	}

	switch in.Type {
	case "bool":
		bv := false
		if def != "" {
			b, err := strconv.ParseBool(def)
			if err == nil {
				bv = b
			}
		}
		if err := askConfirm(msg, bv, &bv, opts); err != nil {
			return err
		}
		out.Inputs[in.Name] = strconv.FormatBool(bv)
	case "enum":
		var v string
		q := &survey.Select{Message: msg, Options: in.Options, Default: def}
		if err := ask(q, &v, opts); err != nil {
			return err
		}
		out.Inputs[in.Name] = v
	default:
		// string, int, list — coerce in coerceInputs at Run time.
		var v string
		q := &survey.Input{Message: msg, Default: def, Help: in.Description}
		validators := []survey.AskOpt{}
		if in.Required {
			validators = append(validators, survey.WithValidator(survey.Required))
		}
		if err := ask(q, &v, opts, validators...); err != nil {
			return err
		}
		out.Inputs[in.Name] = strings.TrimSpace(v)
	}
	return nil
}

func askConfirm(msg string, def bool, target *bool, opts AskOptions) error {
	q := &survey.Confirm{Message: msg, Default: def}
	return ask(q, target, opts)
}

func ask(p survey.Prompt, response any, opts AskOptions, extra ...survey.AskOpt) error {
	askOpts := []survey.AskOpt{}
	if opts.Stdio != nil {
		askOpts = append(askOpts, survey.WithStdio(opts.Stdio.In, opts.Stdio.Out, opts.Stdio.Err))
	}
	askOpts = append(askOpts, extra...)
	return survey.AskOne(p, response, askOpts...)
}

// stringifyAny renders an input value in the form the non-interactive
// path expects: every value is a string and coerceInputs at Run time
// re-types it from in.Type.
func stringifyAny(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		// yaml.v3 decodes plain numbers into int when possible; this
		// branch handles inputs declared with type: int that came in
		// via JSON or via a default.
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case []any:
		parts := make([]string, 0, len(x))
		for _, e := range x {
			parts = append(parts, stringifyAny(e))
		}
		return strings.Join(parts, ",")
	default:
		return fmt.Sprintf("%v", v)
	}
}

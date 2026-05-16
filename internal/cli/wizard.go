package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/confighub/installer/internal/cubctx"
	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/internal/wizard"
	"github.com/confighub/installer/pkg/api"
)

func newWizardCmd() *cobra.Command {
	var (
		workDir        string
		baseName       string
		namespace      string
		selectFlags    []string
		inputFlags     []string
		nonInteractive bool
		reuse          bool
		preset         string
		setImage       []string
	)
	cmd := &cobra.Command{
		Use:   "wizard <package-ref>",
		Short: "Pick base + components, answer inputs, and collect facts",
		Long: `Wizard turns the user's high-level intent (which base, which components, what
namespace, etc.) into ConfigHub-bound documents inside the working dir:

  <work-dir>/spec/selection.yaml   chosen base + components (closure-resolved)
  <work-dir>/spec/inputs.yaml      validated input values (+ namespace)
  <work-dir>/spec/facts.yaml       facts emitted by the package's collector,
                                   if it declares one

These documents are the inputs to render. Edit them later and re-run render
to add or remove components without re-running the wizard.

--namespace is a top-level flag rather than a per-package input: packages
reference it from chain templates as {{ .Namespace }} and don't need to
declare their own namespace input.

If the package declares spec.collector, the wizard runs that command with
the package root as the working directory. It receives INSTALLER_PACKAGE_DIR,
INSTALLER_WORK_DIR, INSTALLER_NAMESPACE, INSTALLER_BASE, INSTALLER_SELECTED,
INSTALLER_INPUT_<NAME>, and the parent environment. Its stdout is parsed as
a YAML map and persisted to facts.yaml. The collector may also write
.env.secret files inside the package working copy — those are consumed by
a kustomize secretGenerator at render time and the resulting Secret is
routed to out/secrets/ (never uploaded as a Unit).

When stdin is a TTY the wizard runs interactively, prompting for base,
component preset (minimal / default / all / selected), namespace, and any
required inputs without defaults. If a prior install is recorded in the
work-dir (out/spec/upload.yaml or out/spec/*.yaml), the wizard offers to
re-use the prior choices, otherwise pre-fills every prompt with the prior
value.

Pass --non-interactive to script the wizard with --base, --components,
--select, --input, and --namespace.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := args[0]
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			if workDir == "" {
				workDir = "."
			}
			absWork, err := filepath.Abs(workDir)
			if err != nil {
				return err
			}

			interactive := !nonInteractive && term.IsTerminal(int(os.Stdin.Fd()))

			pkgDir, err := ipkg.Pull(ctx, ref, filepath.Join(absWork, "package"))
			if err != nil {
				return err
			}
			loaded, err := ipkg.Load(pkgDir)
			if err != nil {
				return err
			}

			prior, source, err := wizard.LoadPriorState(ctx, absWork, func(msg string) {
				fmt.Fprintln(os.Stderr, "warning:", msg)
			})
			if err != nil {
				return fmt.Errorf("load prior state: %w", err)
			}
			if prior != nil && prior.Upload != nil {
				if err := cubctx.CheckMatches(ctx, prior.Upload.Spec.OrganizationID, prior.Upload.Spec.Server); err != nil {
					return err
				}
			}
			if source != wizard.SourceNone {
				fmt.Printf("Loaded prior install state from %s.\n", source)
			}

			raw, err := buildRawAnswers(loaded.Package, prior, interactive, reuse, preset, baseName, namespace, selectFlags, inputFlags)
			if err != nil {
				return err
			}
			imgOverrides, err := wizard.ParseSetImageFlags(setImage)
			if err != nil {
				return err
			}
			raw.ImageOverrides = imgOverrides
			if prior != nil && prior.Inputs != nil {
				raw.PriorImageOverrides = prior.Inputs.Spec.ImageOverrides
			}

			outDir := filepath.Join(absWork, "out")
			res, err := wizard.Run(ctx, loaded.Package, raw, loaded.Root, outDir)
			if err != nil {
				return err
			}
			fmt.Printf("Wizard wrote %s/spec/selection.yaml and inputs.yaml\n", outDir)
			if res.Facts != nil {
				fmt.Printf("Collector produced %d fact(s) in %s/spec/facts.yaml\n", len(res.Facts.Spec.Values), outDir)
			}
			fmt.Printf("Base: %s; components: %v\n", res.Selection.Spec.Base, res.Selection.Spec.Components)
			fmt.Printf("Namespace: %s\n", raw.Namespace)
			fmt.Printf("Next: %s render %s\n", InvocationName(), absWork)
			return nil
		},
	}
	cmd.Flags().StringVar(&workDir, "work-dir", ".", "working directory (gets ./package and ./out subdirs)")
	cmd.Flags().StringVar(&baseName, "base", "", "base name (default: package's default base)")
	cmd.Flags().StringVar(&namespace, "namespace", "", "Kubernetes namespace for the install (exposed to chain templates as {{ .Namespace }}). Required in non-interactive mode.")
	cmd.Flags().StringSliceVar(&selectFlags, "select", nil, "component to select (repeatable; required-deps closed automatically). Mutually exclusive with --components.")
	cmd.Flags().StringSliceVar(&inputFlags, "input", nil, "input value as key=value (repeatable)")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "do not prompt; require --input/--select for everything")
	cmd.Flags().BoolVar(&reuse, "reuse", false, "skip prompts and re-use the prior install's selection + inputs (requires prior state)")
	cmd.Flags().StringVar(&preset, "components", "", "component preset: minimal | default | all | selected. Mutually exclusive with --select.")
	cmd.Flags().StringSliceVar(&setImage, "set-image", nil, "image override as name=ref (repeatable); applied via `kustomize edit set image` against the chosen base before render. The base's kustomization.yaml must declare an `images:` block.")
	return cmd
}

// buildRawAnswers assembles wizard.RawAnswers from the available
// sources, in priority order: --reuse → interactive prompts → CLI
// flags. Validates flag combinations.
func buildRawAnswers(
	pkg *api.Package,
	prior *wizard.PriorState,
	interactive bool,
	reuse bool,
	preset, baseName, namespace string,
	selectFlags, inputFlags []string,
) (wizard.RawAnswers, error) {
	if preset != "" && len(selectFlags) > 0 {
		return wizard.RawAnswers{}, fmt.Errorf("--components and --select are mutually exclusive")
	}
	if reuse {
		if prior == nil || (prior.Selection == nil && prior.Inputs == nil) {
			return wizard.RawAnswers{}, fmt.Errorf("--reuse requires a prior install — none found in this work-dir")
		}
		return wizard.RawAnswersFromPrior(pkg, prior), nil
	}

	if interactive {
		raw, err := wizard.Ask(pkg, prior, wizard.AskOptions{})
		if err != nil {
			return wizard.RawAnswers{}, err
		}
		// Allow CLI flags to override interactive answers (useful when
		// the operator runs interactively but pins a value via flag).
		if baseName != "" {
			raw.BaseName = baseName
		}
		if namespace != "" {
			raw.Namespace = namespace
		}
		if len(selectFlags) > 0 {
			raw.SelectedComponents = selectFlags
		}
		flagInputs, err := wizard.ParseInputFlags(inputFlags)
		if err != nil {
			return wizard.RawAnswers{}, err
		}
		for k, v := range flagInputs {
			raw.Inputs[k] = v
		}
		return raw, nil
	}

	// Non-interactive path: namespace is required.
	if namespace == "" {
		return wizard.RawAnswers{}, fmt.Errorf("--namespace is required in non-interactive mode")
	}
	flagInputs, err := wizard.ParseInputFlags(inputFlags)
	if err != nil {
		return wizard.RawAnswers{}, err
	}
	raw := wizard.RawAnswers{
		Inputs:             flagInputs,
		SelectedComponents: selectFlags,
		BaseName:           baseName,
		Namespace:          namespace,
	}
	if preset != "" {
		picked, err := wizard.ResolvePreset(pkg, preset)
		if err != nil {
			return wizard.RawAnswers{}, err
		}
		raw.SelectedComponents = picked
	}
	return raw, nil
}

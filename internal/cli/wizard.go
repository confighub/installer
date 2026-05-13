package cli

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	ipkg "github.com/confighubai/installer/internal/pkg"
	"github.com/confighubai/installer/internal/wizard"
)

func newWizardCmd() *cobra.Command {
	var (
		workDir        string
		baseName       string
		namespace      string
		selectFlags    []string
		inputFlags     []string
		nonInteractive bool
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

Only --non-interactive mode is implemented today: pass --base, --select, and
--input k=v repeatedly to script the wizard.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !nonInteractive {
				return fmt.Errorf("interactive wizard not yet implemented; pass --non-interactive with --input/--select flags")
			}
			ref := args[0]
			ctx := context.Background()

			if workDir == "" {
				workDir = "."
			}
			absWork, err := filepath.Abs(workDir)
			if err != nil {
				return err
			}

			pkgDir, err := ipkg.Pull(ctx, ref, filepath.Join(absWork, "package"))
			if err != nil {
				return err
			}
			loaded, err := ipkg.Load(pkgDir)
			if err != nil {
				return err
			}

			inputs, err := wizard.ParseInputFlags(inputFlags)
			if err != nil {
				return err
			}

			raw := wizard.RawAnswers{
				Inputs:             inputs,
				SelectedComponents: selectFlags,
				BaseName:           baseName,
				Namespace:          namespace,
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
			if namespace != "" {
				fmt.Printf("Namespace: %s\n", namespace)
			}
			fmt.Printf("Next: installer render %s\n", absWork)
			return nil
		},
	}
	cmd.Flags().StringVar(&workDir, "work-dir", ".", "working directory (gets ./package and ./out subdirs)")
	cmd.Flags().StringVar(&baseName, "base", "", "base name (default: package's default base)")
	cmd.Flags().StringVar(&namespace, "namespace", "", "Kubernetes namespace for the install (exposed to chain templates as {{ .Namespace }})")
	cmd.Flags().StringSliceVar(&selectFlags, "select", nil, "component to select (repeatable; required-deps closed automatically)")
	cmd.Flags().StringSliceVar(&inputFlags, "input", nil, "input value as key=value (repeatable)")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "do not prompt; require --input/--select for everything")
	return cmd
}

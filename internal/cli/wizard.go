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
		workDir         string
		baseName        string
		selectFlags     []string
		inputFlags      []string
		nonInteractive  bool
	)
	cmd := &cobra.Command{
		Use:   "wizard <package-ref>",
		Short: "Pick base + components and answer inputs; emit selection.yaml + inputs.yaml",
		Long: `Wizard turns the user's high-level intent (which base, which components, what
namespace, etc.) into two ConfigHub-bound documents inside the working dir:

  <work-dir>/spec/selection.yaml   chosen base + components (closure-resolved)
  <work-dir>/spec/inputs.yaml      validated input values

These two documents are the inputs to render. Edit them later and re-run
render to add or remove components without re-running the wizard.

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
			}
			outDir := filepath.Join(absWork, "out")
			sel, _, err := wizard.Run(loaded.Package, raw, outDir)
			if err != nil {
				return err
			}
			fmt.Printf("Wizard wrote %s/spec/selection.yaml and inputs.yaml\n", outDir)
			fmt.Printf("Base: %s; components: %v\n", sel.Spec.Base, sel.Spec.Components)
			fmt.Printf("Next: installer render %s\n", absWork)
			return nil
		},
	}
	cmd.Flags().StringVar(&workDir, "work-dir", ".", "working directory (gets ./package and ./out subdirs)")
	cmd.Flags().StringVar(&baseName, "base", "", "base name (default: package's default base)")
	cmd.Flags().StringSliceVar(&selectFlags, "select", nil, "component to select (repeatable; required-deps closed automatically)")
	cmd.Flags().StringSliceVar(&inputFlags, "input", nil, "input value as key=value (repeatable)")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "do not prompt; require --input/--select for everything")
	return cmd
}

package cli

import (
	"context"

	"github.com/spf13/cobra"
)

func newWizardCmd() *cobra.Command {
	var opts flowOptions
	cmd := &cobra.Command{
		Use:   "wizard <package-ref>",
		Short: "Pull a package, pick base + components, answer inputs, and (by default) render",
		Long: `Wizard turns the user's high-level intent (which base, which components,
what namespace, etc.) into ConfigHub-bound documents inside the working
dir:

  <work-dir>/spec/selection.yaml   chosen base + components (closure-resolved)
  <work-dir>/spec/inputs.yaml      validated input values (+ namespace)
  <work-dir>/spec/facts.yaml       facts emitted by the package's collector,
                                   if it declares one

By default, wizard also runs render after the Q&A. Pass --render=false
to write only the spec docs.

For most installs use the higher-level setup command, which combines
wizard with optional pull and supports the same flags. wizard is the
narrow form that always pulls (positional ref required) and is useful
when you want to be explicit about the Q&A step.

--namespace is a top-level flag rather than a per-package input:
packages reference it from chain templates as {{ .Namespace }} and
don't need to declare their own namespace input.

If the package declares spec.collector, the wizard runs that command
with the package root as the working directory. It receives
INSTALLER_PACKAGE_DIR, INSTALLER_WORK_DIR, INSTALLER_NAMESPACE,
INSTALLER_BASE, INSTALLER_SELECTED, INSTALLER_INPUT_<NAME>, and the
parent environment. Its stdout is parsed as a YAML map and persisted to
facts.yaml. The collector may also write .env.secret files inside the
package working copy — those are consumed by a kustomize
secretGenerator at render time and the resulting Secret is routed to
out/secrets/ (never uploaded as a Unit).

When stdin is a TTY the wizard runs interactively, prompting for base,
component preset (minimal / default / all / selected), namespace, and
any required inputs without defaults. If a prior install is recorded
in the work-dir (out/spec/upload.yaml or out/spec/*.yaml), wizard
re-enters from those choices using the same schema-diff logic setup
uses.

Pass --non-interactive to script the wizard with --base, --components,
--select, --input, and --namespace.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			opts.pullRef = args[0]
			return runFlow(ctx, opts)
		},
	}
	cmd.Flags().StringVar(&opts.workDir, "work-dir", ".", "working directory (gets ./package and ./out subdirs)")
	cmd.Flags().StringVar(&opts.baseName, "base", "", "base name (default: package's default base)")
	cmd.Flags().StringVar(&opts.namespace, "namespace", "", "Kubernetes namespace for the install (exposed to chain templates as {{ .Namespace }}). Required in non-interactive mode.")
	cmd.Flags().StringSliceVar(&opts.selectFlags, "select", nil, "component to select (repeatable; required-deps closed automatically). Mutually exclusive with --components.")
	cmd.Flags().StringSliceVar(&opts.inputFlags, "input", nil, "input value as key=value (repeatable)")
	cmd.Flags().BoolVar(&opts.nonInteractive, "non-interactive", false, "do not prompt; require --input/--select for everything")
	cmd.Flags().BoolVar(&opts.reuse, "reuse", false, "skip prompts and re-use the prior install's selection + inputs (requires prior state)")
	cmd.Flags().StringVar(&opts.preset, "components", "", "component preset: minimal | default | all | selected. Mutually exclusive with --select.")
	cmd.Flags().StringSliceVar(&opts.setImage, "set-image", nil, "image override as name=ref (repeatable); applied via `kustomize edit set image` against the chosen base before render. The base's kustomization.yaml must declare an `images:` block.")
	cmd.Flags().BoolVar(&opts.runRender, "render", true, "render after the wizard writes spec docs; pass --render=false to skip and run install render later")
	cmd.Flags().BoolVar(&opts.clean, "clean", false, "remove previously rendered out/manifests/ (preserving any kpt Kptfile) and out/secrets/ before rendering")
	return cmd
}

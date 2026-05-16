// Package cli wires the installer's cobra commands. It is invoked by
// cmd/installer/main.go and works identically whether run as `installer ...`
// standalone or as `cub install ...` via the cub plugin protocol — the only
// difference is cosmetic (the plugin sets CUB_PLUGIN=1 in the env, which we
// surface in `installer doc` output).
package cli

import (
	"os"

	"github.com/spf13/cobra"
)

// invokedAsPlugin reports whether the binary was launched by `cub install`.
func invokedAsPlugin() bool {
	return os.Getenv("CUB_PLUGIN") == "1"
}

// InvocationName is the command name to print in user-facing follow-up
// instructions ("Next: ... upload <dir>"). It returns "cub install" when
// the binary was launched via the cub plugin protocol so the operator
// can copy-paste the suggestion back into their shell; otherwise it
// returns os.Args[0] verbatim (e.g. "bin/install", "install",
// "/usr/local/bin/install") so the suggestion matches what they
// actually typed. Falls back to "installer" if os.Args is empty.
func InvocationName() string {
	if invokedAsPlugin() {
		return "cub install"
	}
	if len(os.Args) > 0 && os.Args[0] != "" {
		return os.Args[0]
	}
	return "installer"
}

// NewRoot builds the root cobra command with all subcommands attached.
func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "installer",
		Short: "Render and install Kubernetes config-as-data packages",
		Long: `Installer renders kustomize-based packages — wrapped with an installer.yaml
manifest — into per-resource Kubernetes YAML, customized with ConfigHub functions.

Output is plain YAML files that can be uploaded to ConfigHub for delivery via
ArgoCD, Flux, or direct Kubernetes apply.`,
		SilenceUsage: true,
	}
	root.AddCommand(
		// Author
		newInitCmd(),
		newNewCmd(),
		newEditCmd(),
		newVetCmd(),
		// Inspect / discover
		newDocCmd(),
		newPullCmd(),
		newInspectCmd(),
		newListCmd(),
		// Registry auth
		newLoginCmd(),
		newLogoutCmd(),
		// Install lifecycle
		newWizardCmd(),
		newDepsCmd(),
		newRenderCmd(),
		newPlanCmd(),
		newUpdateCmd(),
		newUpgradeCmd(),
		newUpgradeApplyCmd(),
		newPreflightCmd(),
		newUploadCmd(),
		// Publish
		newPackageCmd(),
		newPushCmd(),
		newTagCmd(),
		// Trust
		newSignCmd(),
		newVerifyCmd(),
		// Kustomize integration
		newTransformerCmd(),
	)
	return root
}

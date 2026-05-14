package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newDepsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deps",
		Short: "(not yet implemented) Manage installer package dependencies",
		Long: `Deps resolves the dependency DAG declared in installer.yaml against an OCI
registry, writes <work-dir>/out/spec/lock.yaml, and optionally vendors
locked dependencies for offline render.

See docs/package-management-plan.md (Phase 4).`,
	}
	cmd.AddCommand(newDepsUpdateCmd(), newDepsBuildCmd(), newDepsTreeCmd())
	return cmd
}

func newDepsUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update <work-dir>",
		Short: "(not yet implemented) Resolve the dependency DAG and write out/spec/lock.yaml",
		Long: `Update walks the dependency DAG declared in
<work-dir>/package/installer.yaml, resolves SemVer constraints against the
OCI registry by fetching each dep's manifest + config blob (no layer pull),
and writes <work-dir>/out/spec/lock.yaml pinning every dependency to a
digest. Conflicts and replaces are honored.

See docs/package-management-plan.md (Phase 4).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("deps update not yet implemented; see docs/package-management-plan.md (Phase 4)")
		},
	}
}

func newDepsBuildCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "build <work-dir>",
		Short: "(not yet implemented) Pre-fetch locked dependencies into out/vendor/",
		Long: `Build fetches every dependency listed in <work-dir>/out/spec/lock.yaml into
<work-dir>/out/vendor/<name>@<version>/ so that a subsequent installer
render runs without network access.

See docs/package-management-plan.md (Phase 4).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("deps build not yet implemented; see docs/package-management-plan.md (Phase 4)")
		},
	}
}

func newDepsTreeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tree <work-dir>",
		Short: "(not yet implemented) Print the resolved dependency DAG",
		Long: `Tree prints the resolved dependency DAG from
<work-dir>/out/spec/lock.yaml, showing parent → child relationships,
versions, and which optional deps were followed based on the current
selection.

See docs/package-management-plan.md (Phase 4).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("deps tree not yet implemented; see docs/package-management-plan.md (Phase 4)")
		},
	}
}

package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/confighub/installer/internal/deps"
	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/pkg/api"
)

func newDepsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deps",
		Short: "Manage install package dependencies",
		Long: `Deps resolves the dependency DAG declared in installer.yaml against an OCI
registry, writes <work-dir>/out/spec/lock.yaml, and optionally vendors
locked dependencies for offline render.`,
	}
	cmd.AddCommand(newDepsUpdateCmd(), newDepsBuildCmd(), newDepsTreeCmd())
	return cmd
}

func newDepsUpdateCmd() *cobra.Command {
	var workDir string
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Resolve the dependency DAG and write out/spec/lock.yaml",
		Long: `Update walks the dependency DAG declared in
<work-dir>/package/installer.yaml, resolves SemVer constraints against the
OCI registry by fetching each dep's manifest + config blob (no layer pull),
and writes <work-dir>/out/spec/lock.yaml pinning every dependency to a
manifest digest. Conflicts are honored.

If <work-dir>/out/spec/selection.yaml exists, the resolver honors optional
deps gated by whenComponent. If it does not, optional deps are skipped and
listed in the output.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			absWork, err := filepath.Abs(workDir)
			if err != nil {
				return err
			}
			pkgDir := filepath.Join(absWork, "package")
			loaded, err := ipkg.Load(pkgDir)
			if err != nil {
				return fmt.Errorf("load package: %w", err)
			}
			sel, err := loadSelectionOptional(filepath.Join(absWork, "out", "spec", "selection.yaml"))
			if err != nil {
				return err
			}

			ctx := context.Background()
			res, err := deps.Resolve(ctx, loaded.Package, deps.OCISource{}, deps.Options{Selection: sel})
			if err != nil {
				return err
			}
			if err := deps.WriteLock(absWork, loaded.Package, res.Lock); err != nil {
				return err
			}
			fmt.Printf("Locked %d dependency(ies) to %s\n", len(res.Lock.Spec.Resolved), deps.LockPath(absWork))
			for _, d := range res.Lock.Spec.Resolved {
				fmt.Printf("  %s  %s  (%s)\n", d.Name, d.Ref, d.Digest)
			}
			for _, s := range res.SkippedOptional {
				fmt.Printf("  skipped optional: %s\n", s)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workDir, "work-dir", ".", "working directory")
	return cmd
}

func newDepsBuildCmd() *cobra.Command {
	var workDir string
	cmd := &cobra.Command{
		Use:   "build",
		Short: "(not yet implemented) Pre-fetch locked dependencies into out/vendor/",
		Long: `Build fetches every dependency listed in <work-dir>/out/spec/lock.yaml into
<work-dir>/out/vendor/<name>@<version>/ so that a subsequent installer
render runs without network access.

Not yet wired in Phase 4 (resolver + lock only). Tracked under
docs/package-management-plan.md (Phase 4 cross-cutting cache work).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = workDir
			return fmt.Errorf("deps build not yet implemented; see docs/package-management-plan.md (Phase 4)")
		},
	}
	cmd.Flags().StringVar(&workDir, "work-dir", ".", "working directory")
	return cmd
}

func newDepsTreeCmd() *cobra.Command {
	var workDir string
	cmd := &cobra.Command{
		Use:   "tree",
		Short: "Print the resolved dependency DAG",
		Long: `Tree prints the resolved dependency DAG from
<work-dir>/out/spec/lock.yaml, with each entry's pinned ref + manifest
digest and the requester chain. Run deps update first.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			absWork, err := filepath.Abs(workDir)
			if err != nil {
				return err
			}
			pkgDir := filepath.Join(absWork, "package")
			loaded, err := ipkg.Load(pkgDir)
			if err != nil {
				return fmt.Errorf("load package: %w", err)
			}
			lock, err := deps.ReadLock(absWork)
			if err != nil {
				return err
			}
			if lock == nil {
				return fmt.Errorf("no lock at %s — run `%s deps update --work-dir %s` first", deps.LockPath(absWork), InvocationName(), absWork)
			}
			if deps.IsStale(lock, loaded.Package) {
				fmt.Fprintf(os.Stderr, "WARNING: lock is stale (installer.yaml's dependencies have changed) — re-run `%s deps update`\n\n", InvocationName())
			}
			fmt.Printf("%s@%s\n", lock.Spec.Package.Name, lock.Spec.Package.Version)
			for _, d := range lock.Spec.Resolved {
				fmt.Printf("  %s -> %s\n", d.Name, d.Ref)
				fmt.Printf("    digest:       %s\n", d.Digest)
				fmt.Printf("    requested by: %s\n", joinList(d.RequestedBy))
				if d.Selection != nil && (d.Selection.Base != "" || len(d.Selection.Components) > 0) {
					if d.Selection.Base != "" {
						fmt.Printf("    base:         %s\n", d.Selection.Base)
					}
					if len(d.Selection.Components) > 0 {
						fmt.Printf("    components:   %s\n", joinList(d.Selection.Components))
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workDir, "work-dir", ".", "working directory")
	return cmd
}

func loadSelectionOptional(path string) (*api.Selection, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return api.ParseSelection(data)
}

func joinList(ss []string) string {
	if len(ss) == 0 {
		return "(none)"
	}
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

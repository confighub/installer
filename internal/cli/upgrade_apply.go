package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/confighubai/installer/internal/changeset"
	"github.com/confighubai/installer/internal/cubctx"
	"github.com/confighubai/installer/internal/deps"
	"github.com/confighubai/installer/internal/diff"
	ipkg "github.com/confighubai/installer/internal/pkg"
	"github.com/confighubai/installer/internal/upload"
	"github.com/confighubai/installer/pkg/api"
)

func newUpgradeApplyCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "upgrade-apply <work-dir>",
		Short: "Promote a staged upgrade and execute the plan in ConfigHub",
		Long: `Upgrade-apply atomically promotes <work-dir>/.upgrade/ over the
working tree and runs 'installer update' against the resulting state.

Steps:
  1. Verify <work-dir>/.upgrade/{package,out} exist (i.e., a previous
     'installer upgrade' staged successfully).
  2. Archive the prior <work-dir>/{package,out} into
     <work-dir>/.upgrade-prev/ (overwriting any prior archive — kept
     for one rollback step).
  3. Move .upgrade/package → package and .upgrade/out → out.
  4. Run 'installer update' with a ChangeSet slug
     installer-upgrade-<from>-to-<to>-<timestamp>, where <from> and
     <to> are the prior and new package versions.

The resulting ChangeSet is named distinctly from a routine update so
upgrade reverts are visible at a glance in 'cub changeset list'.
Pass --yes to allow the resulting update to delete Units without
per-delete confirmation.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workDir, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			if _, err := exec.LookPath("cub"); err != nil {
				return fmt.Errorf("cub CLI not found on PATH: %w", err)
			}

			// Load the prior package metadata BEFORE the swap so we can
			// name the ChangeSet "<from>-to-<to>".
			priorPkg, _ := loadPackageOptional(filepath.Join(workDir, "package"))
			newPkg, err := loadPackageRequired(filepath.Join(workDir, upgradeStageDir, "package"))
			if err != nil {
				return err
			}
			return runUpgradeApply(ctx, workDir, newPkg, yes, priorPkg)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip confirmation for deletes (forwarded to the resulting `installer update`)")
	return cmd
}

// runUpgradeApply is the shared implementation called from both the
// `installer upgrade-apply` command and `installer upgrade --apply`.
// priorPkg may be nil if the work-dir lacks a package/ tree (e.g.,
// upgrade was the first install) — the ChangeSet slug then omits the
// from-version.
func runUpgradeApply(ctx context.Context, workDir string, newPkg *api.Package, yes bool, priorPkg *api.Package) error {
	stageDir := filepath.Join(workDir, upgradeStageDir)
	prevDir := filepath.Join(workDir, upgradePrevDir)

	stagedPkg := filepath.Join(stageDir, "package")
	stagedOut := filepath.Join(stageDir, "out")
	if !exists(stagedPkg) || !exists(stagedOut) {
		return fmt.Errorf("nothing to apply: %s missing %s and/or %s — run `installer upgrade %s <ref>` first",
			stageDir, "package", "out", workDir)
	}

	uploadDoc, err := readUploadDoc(stageDir)
	if err != nil {
		return err
	}
	if err := cubctx.CheckMatches(ctx, uploadDoc.Spec.OrganizationID, uploadDoc.Spec.Server); err != nil {
		return err
	}

	if err := os.RemoveAll(prevDir); err != nil {
		return fmt.Errorf("clear %s: %w", prevDir, err)
	}
	if err := os.MkdirAll(prevDir, 0o755); err != nil {
		return err
	}
	if err := moveIfExists(filepath.Join(workDir, "package"), filepath.Join(prevDir, "package")); err != nil {
		return err
	}
	if err := moveIfExists(filepath.Join(workDir, "out"), filepath.Join(prevDir, "out")); err != nil {
		return err
	}
	if err := os.Rename(stagedPkg, filepath.Join(workDir, "package")); err != nil {
		return fmt.Errorf("promote staged package: %w", err)
	}
	if err := os.Rename(stagedOut, filepath.Join(workDir, "out")); err != nil {
		return fmt.Errorf("promote staged out: %w", err)
	}
	if err := os.RemoveAll(stageDir); err != nil {
		return fmt.Errorf("clean %s: %w", stageDir, err)
	}
	fmt.Printf("Promoted .upgrade/ over %s; prior tree archived in %s\n", workDir, prevDir)

	// Re-load the now-promoted package + lock for the update step.
	loaded, err := ipkg.Load(filepath.Join(workDir, "package"))
	if err != nil {
		return err
	}
	var lock *api.Lock
	if len(loaded.Package.Spec.Dependencies) > 0 {
		lock, err = deps.ReadLock(workDir)
		if err != nil {
			return err
		}
		if lock == nil {
			return fmt.Errorf("staged package declares deps but lock is missing")
		}
	}
	pattern := uploadDoc.Spec.SpacePattern
	if pattern == "" {
		pattern = "{{.PackageName}}"
	}
	packages, err := upload.Discover(upload.DiscoverInput{
		WorkDir:       workDir,
		SpacePattern:  pattern,
		ParentPackage: loaded.Package,
		Lock:          lock,
	})
	if err != nil {
		return err
	}
	plan, err := diff.Compute(ctx, packages)
	if err != nil {
		return err
	}
	diff.Print(os.Stdout, plan)
	if !plan.HasChanges() {
		fmt.Println("\nNo changes to apply.")
		return nil
	}
	csSlug := upgradeChangeSetSlug(priorPkg, newPkg, time.Now())
	res, err := diff.Apply(ctx, plan, diff.ApplyOptions{
		Yes:                  yes,
		ChangeSetSlug:        csSlug,
		ChangeSetDescription: upgradeChangeSetDescription(priorPkg, newPkg),
		PostSpaceHook:        refreshInstallerRecordHook(packages),
	})
	if err != nil {
		return err
	}
	fmt.Printf("\nApplied: %d created, %d updated, %d deleted.\n", res.Created, res.Updated, res.Deleted)
	if len(res.ChangeSetsOpened) > 0 {
		fmt.Println("Updates revertable via:")
		for _, cs := range res.ChangeSetsOpened {
			fmt.Printf("  %s\n", changeset.RestoreCommand(cs.Space, cs.Slug, cs.UpdatedSlugs))
		}
	}
	return nil
}

// upgradeChangeSetSlug names a ChangeSet so an upgrade revert is
// visually distinct from a routine `installer update` revert in
// `cub changeset list`. Format:
//
//	installer-upgrade-<from>-to-<to>-<YYYYMMDD-HHMMSS>
//
// where <from> defaults to "init" if no prior package was present.
func upgradeChangeSetSlug(prior, next *api.Package, t time.Time) string {
	from := "init"
	if prior != nil && prior.Metadata.Version != "" {
		from = sanitizeVersion(prior.Metadata.Version)
	}
	to := "unknown"
	if next != nil && next.Metadata.Version != "" {
		to = sanitizeVersion(next.Metadata.Version)
	}
	return fmt.Sprintf("installer-upgrade-%s-to-%s-%s", from, to, t.UTC().Format("20060102-150405"))
}

func upgradeChangeSetDescription(prior, next *api.Package) string {
	from := "(none)"
	if prior != nil {
		from = fmt.Sprintf("%s@%s", prior.Metadata.Name, prior.Metadata.Version)
	}
	to := "(unknown)"
	if next != nil {
		to = fmt.Sprintf("%s@%s", next.Metadata.Name, next.Metadata.Version)
	}
	return fmt.Sprintf("installer upgrade from %s to %s", from, to)
}

// sanitizeVersion replaces characters not allowed in ConfigHub slugs
// with hyphens. SemVer dots and pluses are the common offenders.
func sanitizeVersion(v string) string {
	out := []byte(v)
	for i, c := range out {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '-' {
			continue
		}
		out[i] = '-'
	}
	return string(out)
}

func loadPackageOptional(dir string) (*api.Package, error) {
	if !exists(filepath.Join(dir, "installer.yaml")) {
		return nil, nil
	}
	loaded, err := ipkg.Load(dir)
	if err != nil {
		return nil, err
	}
	return loaded.Package, nil
}

func loadPackageRequired(dir string) (*api.Package, error) {
	loaded, err := ipkg.Load(dir)
	if err != nil {
		return nil, fmt.Errorf("load package %s: %w", dir, err)
	}
	return loaded.Package, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// moveIfExists renames src → dst when src exists; no-op when src is
// missing (the prior tree may genuinely not exist on a first install).
func moveIfExists(src, dst string) error {
	if !exists(src) {
		return nil
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("archive %s → %s: %w", src, dst, err)
	}
	return nil
}

package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/confighubai/installer/internal/cubctx"
	"github.com/confighubai/installer/internal/deps"
	"github.com/confighubai/installer/internal/diff"
	ipkg "github.com/confighubai/installer/internal/pkg"
	"github.com/confighubai/installer/internal/upload"
	"github.com/confighubai/installer/pkg/api"
)

func newPlanCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plan <work-dir>",
		Short: "Show what installer update would change in ConfigHub",
		Long: `Plan diffs the work-dir's rendered output against the corresponding
ConfigHub Spaces and prints a terraform-style summary of adds, updates,
and deletes per Space, plus the post-render image set per Space.

Plan is read-only — it does not mutate ConfigHub. Use 'installer
update' to execute the plan.

Plan reads <work-dir>/out/spec/upload.yaml to locate the Spaces; if
upload.yaml is missing, run 'installer upload' first. The active cub
organization and server are sanity-checked against the recorded
values; mismatch fails fast.

The diff is computed by:
  - Listing Units in each Space filtered by the Component=<package>
    label (written by upload).
  - Bucketing into adds (rendered but not in cub), deletes (in cub
    but not rendered, excluding the installer-record Unit), and
    updates (in both).
  - For each update, running 'cub unit update --merge-external-source
    <basename> --dry-run -o mutations' and showing the resulting
    diff. Empty mutations means no change.

The Images: footer per Space is built locally from the rendered
manifests, so it shows the eventual image set whether or not the
plan actually changes anything.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := exec.LookPath("cub"); err != nil {
				return fmt.Errorf("cub CLI not found on PATH: %w", err)
			}
			workDir, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			loaded, err := ipkg.Load(filepath.Join(workDir, "package"))
			if err != nil {
				return fmt.Errorf("load package: %w", err)
			}

			uploadDoc, err := readUploadDoc(workDir)
			if err != nil {
				return err
			}
			if err := cubctx.CheckMatches(ctx, uploadDoc.Spec.OrganizationID, uploadDoc.Spec.Server); err != nil {
				return err
			}

			lock, err := loadLockIfNeeded(workDir, loaded.Package)
			if err != nil {
				return err
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
			return nil
		},
	}
	return cmd
}

// readUploadDoc reads <work-dir>/out/spec/upload.yaml and returns the
// parsed Upload. Errors with a useful "run upload first" hint when the
// file is missing.
func readUploadDoc(workDir string) (*api.Upload, error) {
	path := filepath.Join(workDir, "out", "spec", upload.UploadDocFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf(
				"%s not found — run `installer upload %s` first to record where this work-dir was uploaded",
				path, workDir,
			)
		}
		return nil, err
	}
	return api.ParseUpload(data)
}

// loadLockIfNeeded mirrors upload.go's lock-handling logic: a package
// without dependencies has no lock; a package with dependencies must
// have an up-to-date lock or the plan would target the wrong dep
// versions.
func loadLockIfNeeded(workDir string, pkg *api.Package) (*api.Lock, error) {
	if len(pkg.Spec.Dependencies) == 0 {
		return nil, nil
	}
	lock, err := deps.ReadLock(workDir)
	if err != nil {
		return nil, err
	}
	if lock == nil {
		return nil, fmt.Errorf("package declares dependencies but %s does not exist; run `installer deps update %s` and `installer render %s` first",
			deps.LockPath(workDir), workDir, workDir)
	}
	if deps.IsStale(lock, pkg) {
		return nil, fmt.Errorf("lock at %s is stale; run `installer deps update %s` and `installer render %s` again",
			deps.LockPath(workDir), workDir, workDir)
	}
	return lock, nil
}

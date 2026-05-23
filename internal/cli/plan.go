// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/confighub/installer/internal/cubctx"
	"github.com/confighub/installer/internal/deps"
	"github.com/confighub/installer/internal/diff"
	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/internal/upload"
	"github.com/confighub/installer/pkg/api"
)

func newPlanCmd() *cobra.Command {
	var workDir string
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Show what installer upload would change in ConfigHub",
		Long: `Plan diffs the work-dir's rendered output against the corresponding
ConfigHub Spaces and prints a terraform-style summary of adds, updates,
and deletes per Space, plus the post-render image set per Space.

Plan is read-only — it does not mutate ConfigHub. Use 'installer
upload' to execute the plan.

Plan reads <work-dir>/out/spec/upload.yaml to locate the Spaces; if
upload.yaml is missing, run 'installer upload --space <slug>' first.
The active cub organization and server are sanity-checked against the
recorded values; mismatch fails fast.

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
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := exec.LookPath("cub"); err != nil {
				return fmt.Errorf("cub CLI not found on PATH: %w", err)
			}
			absWork, err := filepath.Abs(workDir)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			loaded, err := ipkg.Load(filepath.Join(absWork, "package"))
			if err != nil {
				return fmt.Errorf("load package: %w", err)
			}

			uploadDoc, err := readUploadDoc(absWork)
			if err != nil {
				return err
			}
			if err := cubctx.CheckMatches(ctx, uploadDoc.Spec.OrganizationID, uploadDoc.Spec.Server); err != nil {
				return err
			}

			lock, err := loadLockIfNeeded(absWork, loaded.Package)
			if err != nil {
				return err
			}

			pattern := uploadDoc.Spec.SpacePattern
			if pattern == "" {
				pattern = "{{.PackageName}}"
			}
			packages, err := upload.Discover(upload.DiscoverInput{
				WorkDir:       absWork,
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
	cmd.Flags().StringVar(&workDir, "work-dir", ".", "working directory")
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
				"%s not found — run `%s upload --work-dir %s --space <slug>` first to record where this work-dir was uploaded",
				path, InvocationName(), workDir,
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
		return nil, fmt.Errorf("package declares dependencies but %s does not exist; run `%s deps update --work-dir %s` and `%s render --work-dir %s` first",
			deps.LockPath(workDir), InvocationName(), workDir, InvocationName(), workDir)
	}
	if deps.IsStale(lock, pkg) {
		return nil, fmt.Errorf("lock at %s is stale; run `%s deps update --work-dir %s` and `%s render --work-dir %s` again",
			deps.LockPath(workDir), InvocationName(), workDir, InvocationName(), workDir)
	}
	return lock, nil
}

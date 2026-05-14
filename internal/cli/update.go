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
	"github.com/confighubai/installer/internal/diff"
	ipkg "github.com/confighubai/installer/internal/pkg"
	"github.com/confighubai/installer/internal/upload"
)

func newUpdateCmd() *cobra.Command {
	var (
		yes           bool
		changeSetSlug string
		target        string
		annotations   []string
		labels        []string
	)
	cmd := &cobra.Command{
		Use:   "update <work-dir>",
		Short: "Apply the installer plan to ConfigHub (creates a ChangeSet for updates)",
		Long: `Update re-computes the same plan as 'installer plan' and executes it
in ConfigHub:

  - cub unit create  for adds (new Component-labeled Units)
  - cub unit update --merge-external-source --changeset <slug>  for
    updates (recorded against a per-Space ChangeSet so updates are
    revertable)
  - cub unit delete  for deletes (gated on --yes)
  - link reconciliation runs after Unit changes (idempotent)

Update on an unchanged work-dir is a no-op — no ChangeSet is opened.

ChangeSet revert: only updates are revertable via 'cub unit update
--restore Before:ChangeSet:<slug>'. Creates and deletes from this
update are not reverted by ChangeSet restore — to undo a create,
delete the Unit; to undo a delete, re-render and re-run update. The
revert command is printed to stdout when the ChangeSet opens.

--target, --annotation, and --label are forwarded to 'cub unit
create' on adds. They are NOT applied to existing Units on update —
that would clobber post-install metadata edits in ConfigHub.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := exec.LookPath("cub"); err != nil {
				return fmt.Errorf("cub CLI not found on PATH: %w", err)
			}
			if err := validateKeyValueFlags("--annotation", annotations); err != nil {
				return err
			}
			if err := validateKeyValueFlags("--label", labels); err != nil {
				return err
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

			if !plan.HasChanges() {
				return nil
			}

			if changeSetSlug == "" {
				changeSetSlug = changeset.DefaultSlug(time.Now())
			}
			res, err := diff.Apply(ctx, plan, diff.ApplyOptions{
				Yes:                  yes,
				ChangeSetSlug:        changeSetSlug,
				ChangeSetDescription: fmt.Sprintf("installer update from %s@%s", loaded.Package.Metadata.Name, loaded.Package.Metadata.Version),
				Target:               target,
				Annotations:          annotations,
				Labels:               labels,
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
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip confirmation for deletes (required when stdin is not a TTY and the plan contains deletes)")
	cmd.Flags().StringVar(&changeSetSlug, "changeset", "", "ChangeSet slug to open per Space (default: installer-update-<timestamp>)")
	cmd.Flags().StringVar(&target, "target", "", "target slug; forwarded to cub unit create on adds")
	cmd.Flags().StringSliceVar(&annotations, "annotation", nil, "annotation key=value to set on created Units (repeatable)")
	cmd.Flags().StringSliceVar(&labels, "label", nil, "label key=value to set on created Units (repeatable)")
	return cmd
}

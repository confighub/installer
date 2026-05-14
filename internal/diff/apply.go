package diff

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/confighub/installer/internal/changeset"
	"github.com/confighub/installer/internal/upload"
)

// PackageRefresher is the hook Apply calls after each Space's Unit
// changes (and link reconcile) to give the caller a chance to refresh
// per-package state in ConfigHub — notably the installer-record Unit
// whose body must stay in sync with the local spec/. Returning an
// error fails Apply.
type PackageRefresher func(ctx context.Context, sp SpacePlan) error

// ApplyOptions tunes Apply's behavior. Zero value is the safe default
// (interactive confirmation for deletes; no extra Unit flags).
type ApplyOptions struct {
	// Yes skips per-delete interactive confirmation. Required when
	// stdin is not a TTY and the plan contains deletes.
	Yes bool
	// ChangeSetSlug is the slug used when opening a per-Space
	// ChangeSet for updates. Defaults to
	// changeset.DefaultSlug(time.Now()) when empty.
	ChangeSetSlug string
	// ChangeSetDescription is the human-readable description on the
	// opened ChangeSet. Defaults to a generic "installer update" line
	// when empty.
	ChangeSetDescription string
	// Target is forwarded to `cub unit create --target` for adds. Empty
	// means no target binding (matching upload's default).
	Target string
	// Annotations and Labels are forwarded to `cub unit create` for
	// adds. They are NOT applied to existing Units on update — that
	// would clobber user post-install metadata edits, which Principle 1
	// (read-only-to-installer for post-install state in ConfigHub) does
	// not allow.
	Annotations []string
	Labels      []string
	// Stdout/Stderr override the I/O streams; nil uses os.Stdout/Stderr.
	// Tests pass buffers to capture output.
	Stdout io.Writer
	Stderr io.Writer
	// PostSpaceHook runs after each Space's Unit + link changes. The
	// CLI passes a hook that refreshes the installer-record Unit so
	// the cub-side spec stays in sync with local. Nil = no-op.
	PostSpaceHook PackageRefresher
}

// ApplyResult reports what Apply did, suitable for the CLI's final
// summary line. ChangeSetsOpened lists every (space, slug) ChangeSet
// the operator can revert via `cub unit update --restore
// Before:ChangeSet:<slug>`.
type ApplyResult struct {
	Created          int
	Updated          int
	Deleted          int
	ChangeSetsOpened []ChangeSetRef
}

// ChangeSetRef names one opened ChangeSet so the caller can render the
// revert command. UpdatedSlugs is the exact set of Units that were
// modified inside this ChangeSet — used to scope the revert.
type ChangeSetRef struct {
	Space        string
	Slug         string
	Component    string
	UpdatedSlugs []string
}

// Apply executes the plan inside a per-Space ChangeSet (for updates
// only — adds and deletes are not ChangeSet-revertable). Empty plan
// is a no-op.
func Apply(ctx context.Context, plan Plan, opts ApplyOptions) (ApplyResult, error) {
	res := ApplyResult{}
	if !plan.HasChanges() {
		return res, nil
	}
	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	for _, sp := range plan.Spaces {
		if len(sp.Adds) == 0 && len(sp.Updates) == 0 && len(sp.Deletes) == 0 {
			continue
		}
		fmt.Fprintf(stdout, "\n== Space %s (%s@%s) ==\n", sp.SpaceSlug, sp.Package, sp.PackageVersion)

		// Open ChangeSet only if there are updates (creates and deletes
		// are not ChangeSet-revertable).
		var changeSetSlug string
		if len(sp.Updates) > 0 {
			slug, err := changeset.Open(ctx, sp.SpaceSlug, opts.ChangeSetSlug,
				descriptionOrDefault(opts.ChangeSetDescription, sp.Package, sp.PackageVersion))
			if err != nil {
				return res, err
			}
			changeSetSlug = slug
			updatedSlugs := make([]string, 0, len(sp.Updates))
			for _, u := range sp.Updates {
				updatedSlugs = append(updatedSlugs, u.Slug)
			}
			res.ChangeSetsOpened = append(res.ChangeSetsOpened, ChangeSetRef{
				Space: sp.SpaceSlug, Slug: slug, Component: sp.Package, UpdatedSlugs: updatedSlugs,
			})
			fmt.Fprintf(stdout, "ChangeSet: %s/%s (revert: `%s`)\n",
				sp.SpaceSlug, slug, changeset.RestoreCommand(sp.SpaceSlug, slug, updatedSlugs))
		}

		for _, a := range sp.Adds {
			if err := createUnit(ctx, sp.SpaceSlug, sp.Package, a, opts, stdout, stderr); err != nil {
				return res, err
			}
			res.Created++
		}
		for _, u := range sp.Updates {
			if err := updateUnit(ctx, sp.SpaceSlug, u, changeSetSlug, stdout, stderr); err != nil {
				return res, err
			}
			res.Updated++
		}
		// Close the ChangeSet by detaching the updated units. The
		// ChangeSet itself is preserved (for revert); the units are
		// freed so the next `installer update` can open a new
		// ChangeSet on them. Without this step ConfigHub rejects all
		// subsequent updates against these units (the ChangeSet acts
		// as a lock per docs/guide/change-apply.md).
		if changeSetSlug != "" {
			if err := closeChangeSet(ctx, sp.SpaceSlug, sp.Updates, stdout, stderr); err != nil {
				return res, err
			}
		}
		if len(sp.Deletes) > 0 {
			deleted, err := deleteUnits(ctx, sp.SpaceSlug, sp.Deletes, opts, stdout, stderr)
			if err != nil {
				return res, err
			}
			res.Deleted += deleted
		}
		// Re-run link inference now that the Unit set has changed.
		if err := upload.ReconcileLinks(ctx, sp.SpaceSlug, sp.Package); err != nil {
			return res, err
		}
		if opts.PostSpaceHook != nil {
			if err := opts.PostSpaceHook(ctx, sp); err != nil {
				return res, fmt.Errorf("post-space hook for %s: %w", sp.SpaceSlug, err)
			}
		}
	}
	return res, nil
}

func descriptionOrDefault(d, pkg, ver string) string {
	if d != "" {
		return d
	}
	if ver != "" {
		return fmt.Sprintf("installer update from %s@%s", pkg, ver)
	}
	return fmt.Sprintf("installer update from %s", pkg)
}

func createUnit(ctx context.Context, space, component string, a SlugDiff, opts ApplyOptions, stdout, stderr io.Writer) error {
	args := []string{"unit", "create",
		"--space", space,
		"--merge-external-source", baseName(a.Path),
		"--label", "Component=" + component,
	}
	if opts.Target != "" {
		args = append(args, "--target", opts.Target)
	}
	for _, an := range opts.Annotations {
		args = append(args, "--annotation", an)
	}
	for _, l := range opts.Labels {
		args = append(args, "--label", l)
	}
	args = append(args, a.Slug, a.Path)
	cmd := exec.CommandContext(ctx, "cub", args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cub unit create %s in %s: %w", a.Slug, space, err)
	}
	return nil
}

func updateUnit(ctx context.Context, space string, u SlugDiff, changeSetSlug string, stdout, stderr io.Writer) error {
	args := []string{"unit", "update",
		"--space", space,
		"--merge-external-source", baseName(u.Path),
	}
	if changeSetSlug != "" {
		args = append(args, "--changeset", changeSetSlug)
	}
	args = append(args, u.Slug, u.Path)
	cmd := exec.CommandContext(ctx, "cub", args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cub unit update %s in %s: %w", u.Slug, space, err)
	}
	return nil
}

// closeChangeSet detaches each updated unit from its ChangeSet by
// patching --changeset -. The ChangeSet is preserved (start/end tags +
// recorded mutations remain) so a future `cub unit update --restore
// Before:ChangeSet:<slug>` reverts cleanly.
func closeChangeSet(ctx context.Context, space string, updates []SlugDiff, stdout, stderr io.Writer) error {
	for _, u := range updates {
		cmd := exec.CommandContext(ctx, "cub", "unit", "update",
			"--space", space, "--patch", "--quiet",
			"--changeset", "-",
			u.Slug,
		)
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("close changeset on %s/%s: %w", space, u.Slug, err)
		}
	}
	return nil
}

func deleteUnits(ctx context.Context, space string, deletes []SlugDiff, opts ApplyOptions, stdout, stderr io.Writer) (int, error) {
	if !opts.Yes {
		fmt.Fprintf(stdout, "Refusing to delete %d Unit(s) in %s without --yes:\n", len(deletes), space)
		for _, d := range deletes {
			fmt.Fprintf(stdout, "  - %s\n", d.Slug)
		}
		return 0, fmt.Errorf("re-run with --yes to delete Units")
	}
	deleted := 0
	for _, d := range deletes {
		cmd := exec.CommandContext(ctx, "cub", "unit", "delete",
			"--space", space, "--quiet", d.Slug)
		var combined bytes.Buffer
		cmd.Stdout = &combined
		cmd.Stderr = &combined
		if err := cmd.Run(); err != nil {
			io.Copy(stderr, &combined)
			return deleted, fmt.Errorf("cub unit delete %s in %s: %w", d.Slug, space, err)
		}
		fmt.Fprintf(stdout, "Deleted %s/%s\n", space, d.Slug)
		deleted++
	}
	return deleted, nil
}

func baseName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[i+1:]
		}
	}
	return path
}

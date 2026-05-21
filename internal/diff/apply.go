package diff

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"

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
	// opened ChangeSet. Defaults to a generic "install update" line
	// when empty.
	ChangeSetDescription string
	// Targets maps a Space slug to the target ref forwarded to
	// `cub unit create --target` for adds in that Space. The ref is the
	// explicit --target (slug, space/slug, or UUID) when the operator
	// passed one, otherwise the Space's recorded TargetID annotation
	// (resolved by the CLI before Apply runs). A missing or empty entry
	// means no target binding.
	Targets map[string]string
	// Annotations and Labels are the operator's --unit-annotation /
	// --unit-label values, forwarded to `cub unit create` for adds. They
	// are NOT applied to existing Units on update — that would clobber
	// user post-install metadata edits, which Principle 1
	// (read-only-to-installer for post-install state in ConfigHub) does
	// not allow. (The PackageVersion annotation is the one exception —
	// see updateUnit — because it tracks which package version last
	// touched each Unit, which the installer owns.)
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
			if err := createUnit(ctx, sp.SpaceSlug, sp.Package, sp.PackageVersion, opts.Targets[sp.SpaceSlug], a, opts, stdout, stderr); err != nil {
				return res, err
			}
			res.Created++
		}
		for _, u := range sp.Updates {
			if err := updateUnit(ctx, sp.SpaceSlug, u, sp.PackageVersion, changeSetSlug, stdout, stderr); err != nil {
				return res, err
			}
			res.Updated++
		}
		// Close the ChangeSet by detaching the updated units. The
		// ChangeSet itself is preserved (for revert); the units are
		// freed so the next `install update` can open a new
		// ChangeSet on them. Without this step ConfigHub rejects all
		// subsequent updates against these units (the ChangeSet acts
		// as a lock per docs/guide/change-apply.md).
		if changeSetSlug != "" {
			if err := closeChangeSet(ctx, sp.SpaceSlug, sp.Updates, stdout, stderr); err != nil {
				return res, err
			}
		}
		if len(sp.Deletes) > 0 {
			deleted, err := emptyUnits(ctx, sp.SpaceSlug, sp.Deletes, opts, stdout, stderr)
			if err != nil {
				return res, err
			}
			res.Deleted += deleted
		}
		// Re-run link inference now that the Unit set has changed.
		// No skip set here — `install update` doesn't have the
		// installer's rendered out/secrets/ context in hand.
		if err := upload.ReconcileLinks(ctx, sp.SpaceSlug, sp.Package, nil); err != nil {
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
		return fmt.Sprintf("install update from %s@%s", pkg, ver)
	}
	return fmt.Sprintf("install update from %s", pkg)
}

func createUnit(ctx context.Context, space, pkg, version, target string, a SlugDiff, opts ApplyOptions, stdout, stderr io.Writer) error {
	args := []string{"unit", "create",
		"--space", space,
		"--merge-external-source", baseName(a.Path),
		"--label", "Package=" + pkg,
		"--annotation", "PackageVersion=" + version,
	}
	if target != "" {
		args = append(args, "--target", target)
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

func updateUnit(ctx context.Context, space string, u SlugDiff, version, changeSetSlug string, stdout, stderr io.Writer) error {
	// For AppConfig Units, the local source is the raw env content
	// (not the rendered ConfigMap manifest). Stage it to a temp file
	// so cub reads it from disk the same way it reads regular
	// manifest paths. The merge-external-source identity matches
	// what upload used when creating the Unit (the carrier
	// manifest's basename).
	path := u.Path
	srcName := baseName(u.Path)
	if u.AppCfg != nil {
		tmp, err := os.CreateTemp("", "appconfig-update-*-"+u.Slug)
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		if _, err := tmp.Write(u.AppCfg.Content); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		path = tmp.Name()
		srcName = baseName(u.AppCfg.ManifestPath)
	}
	args := []string{"unit", "update",
		"--space", space,
		"--merge-external-source", srcName,
	}
	if changeSetSlug != "" {
		args = append(args, "--changeset", changeSetSlug)
	}
	args = append(args, u.Slug, path)
	cmd := exec.CommandContext(ctx, "cub", args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cub unit update %s in %s: %w", u.Slug, space, err)
	}
	// Stamp the package version that last touched this Unit. Done as a
	// separate --patch so it merges into the Unit's annotations without
	// disturbing the data merge above or any other annotation. This lets
	// an operator see which Units a given upload changed (e.g. to triage
	// a partial failure). Unlike --unit-annotation/--unit-label, this is
	// installer-owned bookkeeping, so refreshing it on update is allowed.
	// It must ride the same ChangeSet as the data update — while a Unit is
	// attached to a ChangeSet the server rejects updates that omit it — so
	// the version stamp reverts cleanly alongside the data change.
	pvArgs := []string{"unit", "update",
		"--space", space, "--patch", "--quiet",
		"--annotation", "PackageVersion=" + version,
	}
	if changeSetSlug != "" {
		pvArgs = append(pvArgs, "--changeset", changeSetSlug)
	}
	pvArgs = append(pvArgs, u.Slug)
	pv := exec.CommandContext(ctx, "cub", pvArgs...)
	pv.Stdout = stdout
	pv.Stderr = stderr
	if err := pv.Run(); err != nil {
		return fmt.Errorf("cub unit update --patch PackageVersion on %s in %s: %w", u.Slug, space, err)
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

// emptyUnits reconciles Units that exist in ConfigHub (under this
// package's Package label) but no longer appear in the rendered
// output. It does NOT run `cub unit delete`: upload reconciles Data
// only, never the deployed live state. Deleting the Unit record would
// orphan whatever it has deployed (the resources must be destroyed via
// `cub unit apply` first, which upload does not do), discard any
// post-install edits the operator made, and fail outright on Units
// guarded by DeleteGates.
//
// Instead it feeds empty content through a `cub unit update
// --merge-external-source`. That is a 3-way merge against the last
// external (installer) push, so it removes only the resources the
// installer contributed and leaves any post-install additions in place
// — never a blunt full-replace-to-empty. The Unit record, target
// binding, and metadata survive; the next `cub unit apply` removes the
// now-absent resources through the normal deploy path. Prior Data stays
// in revision history, so an emptying can be reverted with `cub unit
// update --restore`.
//
// Because applying an emptied Unit destroys its deployed resources,
// emptying is refused for any Unit carrying a DestroyGate — the same
// protection the server enforces on the destroy action itself. The
// whole batch is scanned first so a gated Unit is reported before
// anything is touched.
func emptyUnits(ctx context.Context, space string, deletes []SlugDiff, opts ApplyOptions, stdout, stderr io.Writer) (int, error) {
	if !opts.Yes {
		fmt.Fprintf(stdout, "Refusing to empty %d Unit(s) in %s without --yes:\n", len(deletes), space)
		for _, d := range deletes {
			fmt.Fprintf(stdout, "  - %s\n", d.Slug)
		}
		return 0, fmt.Errorf("re-run with --yes to empty Units no longer in the rendered output")
	}

	var gated []string
	for _, d := range deletes {
		gates, err := unitDestroyGates(ctx, space, d.Slug, stderr)
		if err != nil {
			return 0, err
		}
		if len(gates) > 0 {
			gated = append(gated, fmt.Sprintf("  - %s (DestroyGates: %s)", d.Slug, strings.Join(gates, ", ")))
		}
	}
	if len(gated) > 0 {
		fmt.Fprintf(stdout, "Refusing to empty %d Unit(s) in %s guarded by DestroyGates:\n", len(gated), space)
		for _, line := range gated {
			fmt.Fprintln(stdout, line)
		}
		return 0, fmt.Errorf("remove the DestroyGate(s) (e.g. `cub unit update --patch --destroy-gate <key>=-`) before these Units can be emptied")
	}

	// Stage a single empty file to feed every merge-on-update.
	empty, err := os.CreateTemp("", "installer-empty-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(empty.Name())
	if err := empty.Close(); err != nil {
		return 0, err
	}

	emptied := 0
	for _, d := range deletes {
		// --merge-external-source makes this a 3-way merge against the
		// last installer push (the server picks that base by source
		// type, so the identifier value only labels the change). Pass
		// the slug as that label; --change-desc below is what actually
		// lands on the revision.
		cmd := exec.CommandContext(ctx, "cub", "unit", "update",
			"--space", space, "--quiet",
			"--merge-external-source", d.Slug,
			"--change-desc", fmt.Sprintf("install upload: emptied %s (no longer in rendered output)", d.Slug),
			d.Slug, empty.Name())
		var combined bytes.Buffer
		cmd.Stdout = &combined
		cmd.Stderr = &combined
		if err := cmd.Run(); err != nil {
			io.Copy(stderr, &combined)
			return emptied, fmt.Errorf("cub unit update (empty) %s in %s: %w", d.Slug, space, err)
		}
		fmt.Fprintf(stdout, "Emptied %s/%s (apply to remove its deployed resources)\n", space, d.Slug)
		emptied++
	}
	return emptied, nil
}

// unitDestroyGates returns the DestroyGate keys set on a Unit, or nil if
// it has none. Used to refuse emptying a Unit whose deployed resources
// the gate is meant to protect.
func unitDestroyGates(ctx context.Context, space, slug string, stderr io.Writer) ([]string, error) {
	cmd := exec.CommandContext(ctx, "cub", "unit", "get",
		"--space", space, "-o", "json", slug)
	var stdout, errb bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		io.Copy(stderr, &errb)
		return nil, fmt.Errorf("cub unit get %s in %s: %w", slug, space, err)
	}
	var resp struct {
		Unit struct {
			DestroyGates map[string]bool `json:"DestroyGates"`
		} `json:"Unit"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("parse cub unit get %s in %s: %w", slug, space, err)
	}
	if len(resp.Unit.DestroyGates) == 0 {
		return nil, nil
	}
	gates := make([]string, 0, len(resp.Unit.DestroyGates))
	for k := range resp.Unit.DestroyGates {
		gates = append(gates, k)
	}
	sort.Strings(gates)
	return gates, nil
}

func baseName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[i+1:]
		}
	}
	return path
}

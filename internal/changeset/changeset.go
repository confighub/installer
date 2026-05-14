// Package changeset wraps the cub changeset CLI for installer use:
// per-Space ChangeSets opened by `installer update` so the resulting
// updates can be reverted as a unit.
//
// Note on revert scope (per docs/lifecycle.md): only update mutations
// recorded against the ChangeSet are revertable via `cub unit update
// --restore Before:ChangeSet:<slug>`. Creates and deletes are not
// reverted by ChangeSet restore — to undo a create, delete the Unit;
// to undo a delete, re-render and re-run `installer update`.
package changeset

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DefaultSlug returns the conventional slug for an installer update
// ChangeSet at the given time. Stable input → stable output (no
// nondeterminism); callers wanting the live timestamp pass time.Now().
//
// Format: installer-update-YYYYMMDD-HHMMSS (UTC). Avoids RFC3339's
// colons and timezone offsets that ConfigHub slug rules disallow.
func DefaultSlug(t time.Time) string {
	return "installer-update-" + t.UTC().Format("20060102-150405")
}

// Open creates a ChangeSet in space with the given slug and
// description. Idempotent via --allow-exists: if a ChangeSet with the
// same slug already exists in the Space, Open returns the same slug
// without erroring. The caller is responsible for choosing a slug
// that's stable across retries (DefaultSlug is — within the same
// second).
func Open(ctx context.Context, space, slug, description string) (string, error) {
	cmd := exec.CommandContext(ctx, "cub", "changeset", "create",
		"--space", space, "--allow-exists", "--quiet",
		"--description", description,
		slug,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("cub changeset create %s/%s: %w\n%s", space, slug, err, stderr.String())
	}
	return slug, nil
}

// RestoreCommand returns the verbatim shell command an operator can
// run to revert the updates this ChangeSet captured. updatedSlugs is
// the exact set of Units modified during the update — scoping the
// restore to those slugs avoids partial-failure noise when other
// Units share the Component label but were not part of this
// ChangeSet. Uses --patch because bulk restore requires it.
func RestoreCommand(space, slug string, updatedSlugs []string) string {
	if len(updatedSlugs) == 0 {
		return fmt.Sprintf("cub unit update --patch --space %s --restore Before:ChangeSet:%s --where \"true\"", space, slug)
	}
	quoted := make([]string, 0, len(updatedSlugs))
	for _, s := range updatedSlugs {
		quoted = append(quoted, "'"+s+"'")
	}
	return fmt.Sprintf("cub unit update --patch --space %s --restore Before:ChangeSet:%s --where \"Slug IN (%s)\"",
		space, slug, strings.Join(quoted, ","))
}

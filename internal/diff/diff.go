// Package diff computes and renders the diff between a work-dir's
// rendered manifests and the corresponding ConfigHub Units. Used by
// `installer plan` (read-only) and `installer update` (executes the
// plan).
//
// The Component label written by upload (Component=<pkg.Name>) is what
// scopes ownership: a Space may contain Units owned by other tools or
// other packages, and we must never delete those.
package diff

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/confighubai/installer/internal/upload"
)

// Plan is the diff between a work-dir's rendered output and the live
// ConfigHub state, broken down per Space.
type Plan struct {
	Spaces []SpacePlan
}

// SpacePlan is the slice of a Plan that lives in one ConfigHub Space.
type SpacePlan struct {
	// Package is the source-package name (the value of the Component
	// label).
	Package string
	// PackageVersion is informational; rendered alongside Package.
	PackageVersion string
	// SpaceSlug is the ConfigHub Space slug.
	SpaceSlug string
	// Adds are slugs that exist in the rendered output but not in
	// ConfigHub. Path points to the rendered manifest file.
	Adds []SlugDiff
	// Updates are slugs that exist in both, with cub's dry-run
	// reporting non-empty mutations. DiffText is the cub -o mutations
	// output (ANSI-stripped).
	Updates []SlugDiff
	// Deletes are slugs that exist in ConfigHub (under the Component
	// label) but not in the rendered output. The installer-record Unit
	// is excluded.
	Deletes []SlugDiff
	// Images is the post-render image set for the footer; one entry per
	// container per workload found in rendered output.
	Images []WorkloadImage
}

// SlugDiff is one entry in Adds/Updates/Deletes.
type SlugDiff struct {
	Slug string
	// Path is the rendered manifest file. Set for Adds and Updates;
	// empty for Deletes.
	Path string
	// DiffText is the cub -o mutations human-readable diff for this
	// slug, ANSI-stripped. Set only for Updates.
	DiffText string
}

// HasChanges reports whether the plan would create, update, or delete
// anything. Used by callers to short-circuit "no changes" UX.
func (p Plan) HasChanges() bool {
	for _, s := range p.Spaces {
		if len(s.Adds) > 0 || len(s.Updates) > 0 || len(s.Deletes) > 0 {
			return true
		}
	}
	return false
}

// Counts returns the totals across all Spaces in the plan.
func (p Plan) Counts() (adds, updates, deletes int) {
	for _, s := range p.Spaces {
		adds += len(s.Adds)
		updates += len(s.Updates)
		deletes += len(s.Deletes)
	}
	return
}

// Compute walks the discovered packages and produces a Plan by
// querying ConfigHub for the current Unit set under the Component
// label, then running a dry-run merge-external-source per intersecting
// slug. ConfigHub state is the single source of truth — local-only
// state (e.g., a stale prior render) is not considered.
func Compute(ctx context.Context, packages []upload.Package) (Plan, error) {
	plan := Plan{Spaces: make([]SpacePlan, 0, len(packages))}
	for _, pkg := range packages {
		sp, err := computeOne(ctx, pkg)
		if err != nil {
			return plan, fmt.Errorf("space %s: %w", pkg.SpaceSlug, err)
		}
		plan.Spaces = append(plan.Spaces, sp)
	}
	return plan, nil
}

func computeOne(ctx context.Context, pkg upload.Package) (SpacePlan, error) {
	out := SpacePlan{
		Package:        pkg.Name,
		PackageVersion: pkg.Version,
		SpaceSlug:      pkg.SpaceSlug,
	}

	rendered, err := listRenderedSlugs(pkg.ManifestsDir)
	if err != nil {
		return out, err
	}
	current, err := listCurrentSlugs(ctx, pkg.SpaceSlug, pkg.Name)
	if err != nil {
		return out, err
	}

	// Bucket. Treat installer-record specially: it belongs to upload's
	// own bookkeeping and is never present as a rendered manifest, but
	// must not show up as a delete candidate.
	currentSet := map[string]struct{}{}
	for _, s := range current {
		if s == upload.InstallerRecordSlug {
			continue
		}
		currentSet[s] = struct{}{}
	}
	renderedSet := map[string]struct{}{}
	for slug := range rendered {
		renderedSet[slug] = struct{}{}
	}

	for _, slug := range sortedKeys(rendered) {
		path := rendered[slug]
		if _, exists := currentSet[slug]; !exists {
			out.Adds = append(out.Adds, SlugDiff{Slug: slug, Path: path})
			continue
		}
		base := filepath.Base(path)
		diff, err := dryRunMutations(ctx, pkg.SpaceSlug, slug, base, path)
		if err != nil {
			return out, fmt.Errorf("slug %s: %w", slug, err)
		}
		if diff != "" {
			out.Updates = append(out.Updates, SlugDiff{Slug: slug, Path: path, DiffText: diff})
		}
	}
	for slug := range currentSet {
		if _, kept := renderedSet[slug]; !kept {
			out.Deletes = append(out.Deletes, SlugDiff{Slug: slug})
		}
	}
	sort.Slice(out.Deletes, func(i, j int) bool { return out.Deletes[i].Slug < out.Deletes[j].Slug })

	imgs, err := ExtractImages(pkg.ManifestsDir)
	if err != nil {
		return out, fmt.Errorf("extract images: %w", err)
	}
	out.Images = imgs
	return out, nil
}

// listRenderedSlugs returns slug → absolute file path for every YAML
// file in dir (recursing one level into subdirs is unnecessary; render
// places one file per resource at the top level).
func listRenderedSlugs(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		slug := trimExt(name)
		out[slug] = filepath.Join(dir, name)
	}
	return out, nil
}

// listCurrentSlugs returns the slugs of Units in space scoped by the
// Component=<pkg> label.
func listCurrentSlugs(ctx context.Context, space, pkg string) ([]string, error) {
	where := fmt.Sprintf("Labels.Component='%s'", pkg)
	cmd := exec.CommandContext(ctx, "cub", "unit", "list",
		"--space", space, "--where", where,
		"-o", "jq=.[].Unit.Slug")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("cub unit list: %w\n%s", err, stderr.String())
	}
	var slugs []string
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		s := strings.Trim(strings.TrimSpace(line), "\"")
		if s != "" {
			slugs = append(slugs, s)
		}
	}
	return slugs, nil
}

// dryRunMutations runs `cub unit update --merge-external-source ...
// --dry-run -o mutations` and returns the cleaned diff text. Empty
// string means no changes.
//
// Mutations whose only content is ConfigHub bookkeeping (the
// confighub.com/ResourceMergeID annotation injected by every
// merge-external-source apply) are dropped — without this filter
// `installer update` does not converge on its second run, because the
// new file body lacks the annotation that cub injected on the prior
// merge.
func dryRunMutations(ctx context.Context, space, slug, sourceName, path string) (string, error) {
	cmd := exec.CommandContext(ctx, "cub", "unit", "update",
		"--space", space,
		"--merge-external-source", sourceName,
		"--dry-run", "-o", "mutations",
		slug, path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("cub unit update --dry-run: %w\n%s", err, stderr.String())
	}
	clean := stripANSI(stdout.String())
	if isNoChange(clean) {
		return "", nil
	}
	filtered := filterBookkeepingMutations(clean)
	if isNoChange(filtered) {
		return "", nil
	}
	return filtered, nil
}

func isNoChange(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" || strings.Contains(t, "No new changes") {
		return true
	}
	// After bookkeeping filtering, the diff may consist of only the
	// "New changes from update from <path>:" preamble with no
	// Resource: blocks. Treat that as no change too.
	if !strings.Contains(t, "Resource:") {
		return true
	}
	return false
}

var (
	ansiRE        = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	mutationLine  = regexp.MustCompile(`^\s*[+~-]\s*\[(?:Add|Update|Delete)\]\s`)
	resourceLine  = regexp.MustCompile(`^Resource:\s`)
	bookkeepingRE = regexp.MustCompile(`^\s*confighub\.com/`)
)

func stripANSI(s string) string {
	return ansiRE.ReplaceAllString(s, "")
}

// filterBookkeepingMutations walks the cub -o mutations output and
// drops any mutation whose body contains only confighub.com/* keys
// (currently just ResourceMergeID — see internal/models/mutation.go in
// confighub3). Resources whose mutations are all dropped are removed
// too. Format is parsed line-by-line:
//
//	Resource: <type> <name>            <- resource header
//	  ~ [Update] <path>  (#N)          <- mutation header
//	    <content lines, indented more> <- mutation body
//
// Robust against the New-changes prefix and the resource-no-changes
// case. Conservative: when in doubt, keeps the mutation.
func filterBookkeepingMutations(in string) string {
	lines := strings.Split(in, "\n")
	type block struct {
		header  string
		body    []string
		dropped bool
	}
	var (
		out         []string
		currentRes  []string
		blocks      []block
		flushRes    func()
	)
	flushBlocks := func() {
		for _, b := range blocks {
			if b.dropped {
				continue
			}
			currentRes = append(currentRes, b.header)
			currentRes = append(currentRes, b.body...)
		}
		blocks = nil
	}
	flushRes = func() {
		flushBlocks()
		// Only emit the resource header if at least one mutation
		// survived. currentRes[0] is the "Resource:" header line.
		if len(currentRes) > 1 {
			out = append(out, currentRes...)
		}
		currentRes = nil
	}
	var pendingBlock *block
	closePending := func() {
		if pendingBlock == nil {
			return
		}
		// A mutation is bookkeeping iff it has body lines AND every
		// body line is a confighub.com/ key. Empty-body mutations
		// (like "+ [Add] metadata.labels.foo") are real changes —
		// the path in the header is the diff. Bookkeeping mutations
		// also need their path to look like an annotations path
		// (a body-only diff for a non-annotations path is not
		// bookkeeping even if the body line happens to start with
		// confighub.com).
		bodyHasContent := false
		bodyAllBookkeeping := true
		for _, l := range pendingBlock.body {
			t := strings.TrimSpace(l)
			if t == "" {
				continue
			}
			bodyHasContent = true
			if !bookkeepingRE.MatchString(l) {
				bodyAllBookkeeping = false
				break
			}
		}
		pendingBlock.dropped = bodyHasContent && bodyAllBookkeeping
		blocks = append(blocks, *pendingBlock)
		pendingBlock = nil
	}
	for _, line := range lines {
		switch {
		case resourceLine.MatchString(line):
			closePending()
			flushRes()
			currentRes = []string{line}
		case mutationLine.MatchString(line):
			closePending()
			pendingBlock = &block{header: line}
		case pendingBlock != nil:
			pendingBlock.body = append(pendingBlock.body, line)
		default:
			out = append(out, line)
		}
	}
	closePending()
	flushRes()
	return strings.Join(out, "\n")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// trimExt strips the final dot-extension from name. Mirrors the
// upload package's slug derivation so plan and upload agree on
// per-file → slug mapping.
func trimExt(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			return name[:i]
		}
	}
	return name
}

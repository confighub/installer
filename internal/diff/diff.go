// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

// Package diff computes and renders the diff between a work-dir's
// rendered manifests and the corresponding ConfigHub Units. Used by
// `installer plan` (read-only) and `installer update` (executes the
// plan).
//
// The Package label written by upload (Package=<pkg.Name>) is what
// scopes ownership: a Space may contain Units owned by other tools or
// other packages (or added by the operator before cloning the Space),
// and we must never delete those.
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

	"github.com/confighub/installer/internal/upload"
)

// Plan is the diff between a work-dir's rendered output and the live
// ConfigHub state, broken down per Space.
type Plan struct {
	Spaces []SpacePlan
}

// SpacePlan is the slice of a Plan that lives in one ConfigHub Space.
type SpacePlan struct {
	// Package is the source-package name (the value of the Package
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
	// Deletes are slugs that exist in ConfigHub (under the Package
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
	// empty for Deletes. For AppConfig updates this is unused — the
	// raw env content is staged from AppCfg.Content at apply time.
	Path string
	// DiffText is the cub -o mutations human-readable diff for this
	// slug, ANSI-stripped. Set only for Updates.
	DiffText string
	// AppCfg is non-nil for the AppConfig Unit of an AppConfig
	// manifest (the carrier ConfigMap that upload split into AppConfig
	// + placeholder + renderer Target). Apply uses it to stage raw env
	// content for the merge-external-source update. Nil for regular
	// Kubernetes manifests.
	AppCfg *upload.AppConfigManifest
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
// querying ConfigHub for the current Unit set under the Package
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
		item := rendered[slug]
		// Placeholder Units exist in cub but their body is maintained
		// by the Upsert link from upload's AppConfig pathway.
		// They have no separate local source — registering them in
		// the rendered set keeps them out of delete candidates;
		// add/update is a no-op.
		if item.IsPlaceholder {
			continue
		}
		if _, exists := currentSet[slug]; !exists {
			out.Adds = append(out.Adds, SlugDiff{Slug: slug, Path: item.Path, AppCfg: item.AppCfg})
			continue
		}
		stagedPath, cleanup, err := item.stagedPath()
		if err != nil {
			return out, fmt.Errorf("slug %s: %w", slug, err)
		}
		diff, err := dryRunMutations(ctx, pkg.SpaceSlug, slug, item.Base, stagedPath)
		cleanup()
		if err != nil {
			return out, fmt.Errorf("slug %s: %w", slug, err)
		}
		if diff != "" {
			out.Updates = append(out.Updates, SlugDiff{Slug: slug, Path: item.Path, DiffText: diff, AppCfg: item.AppCfg})
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

// renderedItem describes one entry in the local rendered set.
// For regular Kubernetes manifests the file at Path is what gets
// merged. For AppConfig manifests, upload split the carrier
// ConfigMap into:
//
//   - AppConfig Unit (toolchain AppConfig/<Format>; body = raw env
//     content) — this is the "real" Unit; its content gets updated
//     from AppCfg.Content via merge-external-source, with Base
//     matching the carrier filename upload used as the source name.
//   - Placeholder ConfigMap Unit (toolchain Kubernetes/YAML; body
//     empty at create, populated via the Upsert link from the
//     AppConfig Unit) — IsPlaceholder=true; never directly updated
//     and never a delete candidate while the AppConfig manifest is
//     present.
type renderedItem struct {
	Slug          string
	Path          string // path to the local file (carrier manifest for AppConfig)
	Base          string // basename used as merge-external-source identity
	AppCfg        *upload.AppConfigManifest
	IsPlaceholder bool
}

// stagedPath returns a file path containing the merge-source body.
// For regular manifests, that's just item.Path. For AppConfig, it
// stages AppCfg.Content into a temp file (the cub CLI reads the body
// from disk via the positional arg). The returned cleanup must be
// called when the path is no longer needed; it is a no-op for non-
// AppConfig items.
func (item renderedItem) stagedPath() (string, func(), error) {
	if item.AppCfg == nil {
		return item.Path, func() {}, nil
	}
	tmp, err := os.CreateTemp("", "appconfig-diff-*-"+item.Slug)
	if err != nil {
		return "", func() {}, err
	}
	if _, err := tmp.Write(item.AppCfg.Content); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", func() {}, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", func() {}, err
	}
	return tmp.Name(), func() { os.Remove(tmp.Name()) }, nil
}

// listRenderedSlugs scans the rendered manifests directory and
// returns one renderedItem per slug. AppConfig-tagged ConfigMap
// manifests expand into TWO entries: the AppConfig Unit slug (with
// AppCfg set) and the *-rendered placeholder slug (IsPlaceholder
// true). Regular manifests emit one entry keyed by their
// filename-derived slug.
func listRenderedSlugs(dir string) (map[string]renderedItem, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	out := map[string]renderedItem{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		path := filepath.Join(dir, name)
		appCfg, err := upload.DetectAppConfigManifest(path)
		if err != nil {
			return nil, fmt.Errorf("detect AppConfig in %s: %w", name, err)
		}
		if appCfg != nil {
			out[appCfg.UnitSlug()] = renderedItem{
				Slug:   appCfg.UnitSlug(),
				Path:   path,
				Base:   filepath.Base(appCfg.ManifestPath),
				AppCfg: appCfg,
			}
			out[appCfg.PlaceholderSlug()] = renderedItem{
				Slug:          appCfg.PlaceholderSlug(),
				AppCfg:        appCfg,
				IsPlaceholder: true,
			}
			continue
		}
		slug := trimExt(name)
		out[slug] = renderedItem{Slug: slug, Path: path, Base: name}
	}
	return out, nil
}

// listCurrentSlugs returns the slugs of Units in space scoped by the
// Package=<pkg> label.
func listCurrentSlugs(ctx context.Context, space, pkg string) ([]string, error) {
	where := fmt.Sprintf("Labels.Package='%s'", pkg)
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
	ansiRE       = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	mutationLine = regexp.MustCompile(`^\s*[+~-]\s*\[(?:Add|Update|Delete)\]\s`)
	resourceLine = regexp.MustCompile(`^Resource:\s`)
	// bookkeepingRE matches a body line whose payload starts with a
	// well-known bookkeeping key. Used to drop Add / Update mutations
	// whose only content is bookkeeping cub injects on every
	// merge-external-source apply.
	//
	// Two flavors are recognized:
	//   - "confighub.com/..." (Kubernetes/YAML annotation keys)
	//   - "configHub.resourceMergeID" (AppConfig flat-key bookkeeping;
	//     "configHub.<other>" can be legitimate schema config a
	//     package author declared — e.g., configHub.configName /
	//     configHub.configSchema — so the AppConfig match is keyed
	//     on the specific bookkeeping name, not the configHub prefix
	//     in general).
	bookkeepingRE = regexp.MustCompile(`^\s*(?:confighub\.com/|configHub\.resourceMergeID(?:[\s=:]|$))`)
	// deleteOnBookkeepingHeader matches a mutation header line for a
	// [Delete] on the bookkeeping key. The body of a Delete mutation
	// holds only the deleted value (a UUID), so the body-based
	// bookkeepingRE check above can't catch it — the signal is in
	// the path. Two formats:
	//   - Kubernetes annotation, "." as separator, "~1" as the escape
	//     for "." inside the key:
	//       [Delete] metadata.annotations.confighub~1com/ResourceMergeID
	//     (literal "." form is also accepted, defensively).
	//   - AppConfig flat-key form:
	//       [Delete] configHub.resourceMergeID
	deleteOnBookkeepingHeader = regexp.MustCompile(`\[Delete\]\s+(?:.*\.annotations\.confighub(?:~1com|\.com)/|configHub\.resourceMergeID(?:\s|$))`)
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
		out        []string
		currentRes []string
		blocks     []block
		flushRes   func()
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
		// Two flavors of "bookkeeping-only" mutations to drop:
		//
		// 1. [Add] / [Update] on the annotation: body holds the
		//    confighub.com/<key>: <value> pair. Caught by the
		//    body-based check below.
		// 2. [Delete] on the annotation: body holds only the deleted
		//    value (a UUID). The signal lives in the header path
		//    (".annotations.confighub.com/..."), not the body —
		//    caught by deleteOnBookkeepingHeader.
		//
		// Empty-body mutations (like "+ [Add] metadata.labels.foo")
		// are real changes — the path in the header is the diff —
		// and must not be dropped.
		if deleteOnBookkeepingHeader.MatchString(pendingBlock.header) {
			pendingBlock.dropped = true
			blocks = append(blocks, *pendingBlock)
			pendingBlock = nil
			return
		}
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

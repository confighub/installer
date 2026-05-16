package upload

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	fapi "github.com/confighub/sdk/core/function/api"
	"gopkg.in/yaml.v3"
)

// ReconcileLinks runs the standard installer intra-Space link
// inference (references, label-selectors, custom-resource → CRD pairs)
// against the Units in space, then creates only the links that are
// missing. Idempotent: existing links matching (FromUnit.Slug,
// ToUnit.Slug, ToSpace == space) are left alone.
//
// Each new link is labeled Component=<component> so it can be
// filtered alongside the package's units.
//
// skipUnmatched suppresses entries from the unmatched-references
// reminder for resources the caller already accounts for elsewhere
// (e.g., rendered Secrets the operator will apply out-of-band). Keys
// are produced by UnmatchedKey(targetType, targetName). Pass nil to
// report every unmatched reference. Built-in Kubernetes ClusterRoles
// are always filtered out — they pre-exist in every cluster and
// would otherwise dominate the reminder for any package that ships a
// RoleBinding.
//
// Used by `installer upload` (after the per-package Unit creation
// loop) and by `installer update` (after Apply mutates the Unit set).
// The two paths share an implementation so behavior cannot drift.
func ReconcileLinks(ctx context.Context, space, component string, skipUnmatched map[string]struct{}) error {
	resources, err := loadResources(ctx, space)
	if err != nil {
		return err
	}
	refs, err := loadReferences(ctx, space)
	if err != nil {
		return err
	}
	labels, err := loadWorkloadLabels(ctx, space)
	if err != nil {
		return err
	}
	crds, err := loadCRDIndex(ctx, space)
	if err != nil {
		return err
	}
	edges, unmatched := planLinks(resources, refs, labels, crds)

	existing, err := loadExistingIntraSpaceLinks(ctx, space)
	if err != nil {
		return err
	}

	created := 0
	for _, e := range edges {
		key := linkKey(e.FromUnit, e.ToUnit)
		if _, exists := existing[key]; exists {
			continue
		}
		if err := createLink(ctx, space, component, e); err != nil {
			return err
		}
		existing[key] = struct{}{}
		created++
	}
	if created == 0 && len(edges) == 0 {
		fmt.Println("No links to create.")
	}
	reportUnmatchedReferences(unmatched, skipUnmatched)
	return nil
}

// UnmatchedKey produces the canonical key for the skipUnmatched set
// passed to ReconcileLinks. The shape matches what reportUnmatchedReferences
// computes per row internally.
func UnmatchedKey(targetType, targetName string) string {
	return targetType + "\x00" + targetName
}

// reportUnmatchedReferences prints a non-fatal reminder for references in
// uploaded workload Units that didn't match any Unit in the Space. Most
// common reason: the referenced resource lives in the cluster but isn't
// managed by ConfigHub (e.g., a Secret created out-of-band, a Namespace
// owned by the platform team, a ServiceAccount provided by a base
// install). The installer can't verify those from here — we don't assume
// the operator running `installer upload` has cluster access — so we
// surface the list for the operator to confirm rather than failing.
//
// Two classes of references are always suppressed:
//   - Built-in Kubernetes ClusterRoles (cluster-admin/admin/edit/view
//     and anything under the system: prefix) — they pre-exist in every
//     cluster and would otherwise dominate the reminder for packages
//     that ship a RoleBinding.
//   - Anything whose UnmatchedKey is in skipUnmatched, which the caller
//     uses to suppress targets it already mentions in a separate
//     reminder (e.g., rendered-but-not-uploaded Secrets that the
//     operator will apply out-of-band).
func reportUnmatchedReferences(unmatched []UnmatchedReference, skipUnmatched map[string]struct{}) {
	var visible []UnmatchedReference
	for _, u := range unmatched {
		if u.TargetType == "rbac.authorization.k8s.io/v1/ClusterRole" && IsBuiltInClusterRole(u.TargetName) {
			continue
		}
		if _, ok := skipUnmatched[UnmatchedKey(u.TargetType, u.TargetName)]; ok {
			continue
		}
		visible = append(visible, u)
	}
	if len(visible) == 0 {
		return
	}
	fmt.Println()
	fmt.Println("Note: the following references didn't resolve to any Unit in this Space.")
	fmt.Println("That's expected when the referenced resource lives in the cluster but")
	fmt.Println("isn't managed by ConfigHub (e.g., a Secret created out-of-band, a")
	fmt.Println("Namespace owned by the platform team). Verify each against your cluster")
	fmt.Println("before applying.")
	for _, u := range visible {
		fmt.Printf("  - %s -> %s %q\n", u.FromUnit, u.TargetType, u.TargetName)
	}
}

// LinkEdge is one inferred edge between two Units in the same Space.
// Exposed so callers (e.g., a future installer plan extension) can
// inspect the inference output without re-creating the links.
type LinkEdge struct {
	FromUnit string
	ToUnit   string
	// Reason is human-readable; surfaced in upload/update logs.
	Reason string
}

// UnmatchedReference is a `get-references` result that didn't match any
// Unit in the Space — workload Unit FromUnit references a resource of
// type TargetType named TargetName, but no Unit holds that target.
// Typically a sign that the target lives in the cluster (out-of-band
// Secret, platform-team Namespace, etc.) rather than a bug.
type UnmatchedReference struct {
	FromUnit   string
	TargetType string
	TargetName string
}

func createLink(ctx context.Context, space, component string, e LinkEdge) error {
	slug := "-"
	cmd := exec.CommandContext(ctx, "cub", "link", "create",
		"--space", space, "--quiet",
		"--label", "Component="+component,
		slug, e.FromUnit, e.ToUnit,
	)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cub link create %s (%s -> %s): %w", slug, e.FromUnit, e.ToUnit, err)
	}
	fmt.Printf("Linked %s -> %s (%s)\n", e.FromUnit, e.ToUnit, e.Reason)
	return nil
}

// loadExistingIntraSpaceLinks returns the set of (fromSlug, toSlug)
// keys for links inside space (ToSpace == space). Cross-Space links
// are intentionally excluded — the caller is reconciling intra-Space
// links only; cross-Space dep links are managed via PlanCrossSpaceLinks.
func loadExistingIntraSpaceLinks(ctx context.Context, space string) (map[string]struct{}, error) {
	out, err := runCubJSON(ctx, "link", "list",
		"--space", space, "-o", "json",
	)
	if err != nil {
		return nil, fmt.Errorf("cub link list: %w", err)
	}
	type entry struct {
		FromUnit struct{ Slug string }
		ToUnit   struct{ Slug string }
		ToSpace  struct{ Slug string }
	}
	var rows []entry
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("decode cub link list: %w", err)
	}
	set := map[string]struct{}{}
	for _, r := range rows {
		if r.ToSpace.Slug != space {
			continue
		}
		set[linkKey(r.FromUnit.Slug, r.ToUnit.Slug)] = struct{}{}
	}
	return set, nil
}

func linkKey(from, to string) string { return from + "\x00" + to }

// --- Inference internals (moved from internal/cli/upload.go) ---

// cub function-do emits one JSON object per unit on stdout, separated
// by whitespace. The envelope carries identity (SpaceSlug, UnitSlug,
// ...) and nests the function's typed output under "Output". Output is
// decoded into SDK types from public/core/function/api.
type funcEnvelope[T any] struct {
	Output     T               `json:"Output"`
	OutputType fapi.OutputType `json:"OutputType"`
	SpaceSlug  string          `json:"SpaceSlug"`
	UnitSlug   string          `json:"UnitSlug"`
}

// resourceIndex is the set of resources contained in each unit and a
// lookup from (ResourceType, ResourceNameWithoutScope) → unit slugs.
type resourceIndex struct {
	resourcesByUnit map[string][]fapi.Resource
	unitsByTypeName map[string][]string // key = ResourceType + "\x00" + ResourceNameWithoutScope
}

func resourceLookupKey(rt fapi.ResourceType, name fapi.ResourceName) string {
	return string(rt) + "\x00" + string(name)
}

func loadResources(ctx context.Context, space string) (*resourceIndex, error) {
	out, err := runCubJSON(ctx, "function", "do",
		"--space", space, "--quiet", "--show", "output", "-o", "json",
		"get-resources", "none")
	if err != nil {
		return nil, fmt.Errorf("get-resources: %w", err)
	}
	idx := &resourceIndex{
		resourcesByUnit: map[string][]fapi.Resource{},
		unitsByTypeName: map[string][]string{},
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var rec funcEnvelope[fapi.ResourceList]
		if err := dec.Decode(&rec); err != nil {
			return nil, fmt.Errorf("decode get-resources output: %w", err)
		}
		idx.resourcesByUnit[rec.UnitSlug] = append(idx.resourcesByUnit[rec.UnitSlug], rec.Output...)
		for _, r := range rec.Output {
			k := resourceLookupKey(r.ResourceType, r.ResourceNameWithoutScope)
			idx.unitsByTypeName[k] = appendUnique(idx.unitsByTypeName[k], rec.UnitSlug)
		}
	}
	return idx, nil
}

type referenceEntry struct {
	targetType fapi.ResourceType
	targetName fapi.ResourceName
}

func loadReferences(ctx context.Context, space string) (map[string][]referenceEntry, error) {
	out, err := runCubJSON(ctx, "function", "do",
		"--space", space, "--quiet", "--show", "output", "-o", "json",
		"get-references")
	if err != nil {
		return nil, fmt.Errorf("get-references: %w", err)
	}
	byUnit := map[string][]referenceEntry{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var rec funcEnvelope[fapi.AttributeValueList]
		if err := dec.Decode(&rec); err != nil {
			return nil, fmt.Errorf("decode get-references output: %w", err)
		}
		for _, av := range rec.Output {
			if av.Details == nil {
				continue
			}
			target := av.Details.NeededRequired["ResourceType"]
			if target == "" {
				continue
			}
			val, ok := av.Value.(string)
			if !ok || val == "" {
				continue
			}
			byUnit[rec.UnitSlug] = append(byUnit[rec.UnitSlug], referenceEntry{
				targetType: fapi.ResourceType(target),
				targetName: fapi.ResourceName(val),
			})
		}
	}
	return byUnit, nil
}

type workloadLabels struct {
	selectors []map[string]string
	templates []map[string]string
}

func loadWorkloadLabels(ctx context.Context, space string) (map[string]*workloadLabels, error) {
	out, err := runCubJSON(ctx, "function", "do",
		"--space", space, "--quiet", "--show", "output", "-o", "json",
		"get-workload-labels")
	if err != nil {
		return nil, fmt.Errorf("get-workload-labels: %w", err)
	}
	byUnit := map[string]*workloadLabels{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var rec funcEnvelope[fapi.AttributeValueList]
		if err := dec.Decode(&rec); err != nil {
			return nil, fmt.Errorf("decode get-workload-labels output: %w", err)
		}
		for _, av := range rec.Output {
			yamlStr, ok := av.Value.(string)
			if !ok || strings.TrimSpace(yamlStr) == "" {
				continue
			}
			m := map[string]string{}
			if err := yaml.Unmarshal([]byte(yamlStr), &m); err != nil {
				return nil, fmt.Errorf("decode workload-label YAML at %s on %s: %w", av.Path, rec.UnitSlug, err)
			}
			if len(m) == 0 {
				continue
			}
			wl := byUnit[rec.UnitSlug]
			if wl == nil {
				wl = &workloadLabels{}
				byUnit[rec.UnitSlug] = wl
			}
			if isSelectorPath(av.Path) {
				wl.selectors = append(wl.selectors, m)
			} else {
				wl.templates = append(wl.templates, m)
			}
		}
	}
	return byUnit, nil
}

func isSelectorPath(path fapi.ResolvedPath) bool {
	s := string(path)
	return strings.Contains(s, "selector") || strings.Contains(s, "Selector")
}

func loadCRDIndex(ctx context.Context, space string) (map[string]string, error) {
	out, err := runCubJSON(ctx, "function", "do",
		"--space", space, "--quiet", "--show", "output", "-o", "json",
		"--resource-type", "apiextensions.k8s.io/v1/CustomResourceDefinition",
		"yq", `.spec.group + "/" + .spec.names.kind`)
	if err != nil {
		return nil, fmt.Errorf("yq on CRDs: %w", err)
	}
	idx := map[string]string{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var rec funcEnvelope[fapi.YAMLPayload]
		if err := dec.Decode(&rec); err != nil {
			return nil, fmt.Errorf("decode CRD yq output: %w", err)
		}
		payload := strings.TrimSpace(rec.Output.Payload)
		if payload == "" || payload == "/" {
			continue
		}
		payload = strings.Trim(payload, "\"'\n")
		idx[payload] = rec.UnitSlug
	}
	return idx, nil
}

func planLinks(
	resources *resourceIndex,
	refs map[string][]referenceEntry,
	labels map[string]*workloadLabels,
	crds map[string]string,
) ([]LinkEdge, []UnmatchedReference) {
	seen := map[string]LinkEdge{}
	add := func(from, to, reason string) {
		if from == to || from == "" || to == "" {
			return
		}
		key := linkKey(from, to)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = LinkEdge{FromUnit: from, ToUnit: to, Reason: reason}
	}
	// Dedup unmatched refs on (fromUnit, targetType, targetName) so a
	// reference that's repeated across paths (e.g., a Secret named in
	// envFrom AND volumes) doesn't get reported twice.
	unmatchedSeen := map[string]struct{}{}
	var unmatched []UnmatchedReference
	for fromUnit, entries := range refs {
		for _, r := range entries {
			k := resourceLookupKey(r.targetType, r.targetName)
			matches := resources.unitsByTypeName[k]
			if len(matches) == 0 {
				key := fromUnit + "\x00" + string(r.targetType) + "\x00" + string(r.targetName)
				if _, dup := unmatchedSeen[key]; !dup {
					unmatchedSeen[key] = struct{}{}
					unmatched = append(unmatched, UnmatchedReference{
						FromUnit:   fromUnit,
						TargetType: string(r.targetType),
						TargetName: string(r.targetName),
					})
				}
				continue
			}
			for _, toUnit := range matches {
				add(fromUnit, toUnit, "reference:"+string(r.targetType))
			}
		}
	}
	for fromUnit, wl := range labels {
		if len(wl.selectors) == 0 {
			continue
		}
		for toUnit, candidate := range labels {
			if toUnit == fromUnit || len(candidate.templates) == 0 {
				continue
			}
			if anySelectorMatches(wl.selectors, candidate.templates) {
				add(fromUnit, toUnit, "selector")
			}
		}
	}
	for fromUnit, list := range resources.resourcesByUnit {
		for _, r := range list {
			if r.ResourceType == crdResourceType {
				continue
			}
			groupKind, ok := groupKindFromResourceType(r.ResourceType)
			if !ok {
				continue
			}
			toUnit := crds[groupKind]
			if toUnit == "" {
				continue
			}
			add(fromUnit, toUnit, "crd")
		}
	}
	out := make([]LinkEdge, 0, len(seen))
	for _, e := range seen {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FromUnit != out[j].FromUnit {
			return out[i].FromUnit < out[j].FromUnit
		}
		if out[i].ToUnit != out[j].ToUnit {
			return out[i].ToUnit < out[j].ToUnit
		}
		return out[i].Reason < out[j].Reason
	})
	sort.Slice(unmatched, func(i, j int) bool {
		if unmatched[i].FromUnit != unmatched[j].FromUnit {
			return unmatched[i].FromUnit < unmatched[j].FromUnit
		}
		if unmatched[i].TargetType != unmatched[j].TargetType {
			return unmatched[i].TargetType < unmatched[j].TargetType
		}
		return unmatched[i].TargetName < unmatched[j].TargetName
	})
	return out, unmatched
}

func anySelectorMatches(selectors, templates []map[string]string) bool {
	for _, s := range selectors {
		if len(s) == 0 {
			continue
		}
		for _, t := range templates {
			if isSubsetOf(s, t) {
				return true
			}
		}
	}
	return false
}

func isSubsetOf(s, t map[string]string) bool {
	for k, v := range s {
		if tv, ok := t[k]; !ok || tv != v {
			return false
		}
	}
	return true
}

const crdResourceType = fapi.ResourceType("apiextensions.k8s.io/v1/CustomResourceDefinition")

func groupKindFromResourceType(rt fapi.ResourceType) (string, bool) {
	parts := strings.Split(string(rt), "/")
	if len(parts) != 3 {
		return "", false
	}
	if !strings.Contains(parts[0], ".") {
		return "", false
	}
	return parts[0] + "/" + parts[2], true
}

func appendUnique(slice []string, v string) []string {
	for _, s := range slice {
		if s == v {
			return slice
		}
	}
	return append(slice, v)
}

func runCubJSON(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "cub", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("cub %s: %w\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.Bytes(), nil
}

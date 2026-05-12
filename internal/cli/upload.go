package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	fapi "github.com/confighub/sdk/core/function/api"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newUploadCmd() *cobra.Command {
	var space string
	cmd := &cobra.Command{
		Use:   "upload <work-dir>",
		Short: "Upload rendered manifests to a ConfigHub space",
		Long: `Upload sends <work-dir>/out/manifests/*.yaml to a ConfigHub space as one
Unit per file. After upload, the command analyzes the space with the
get-resources, get-references, and get-workload-labels functions and creates
NeedsProvides links between units when:

  - a unit references another unit's resource by name (e.g., a Deployment
    referencing a Namespace, ConfigMap, or ServiceAccount),
  - a unit's pod label-selector matches another unit's pod-template labels
    (e.g., a Service selecting the pods produced by a Deployment), or
  - a unit contains a custom resource whose CustomResourceDefinition lives
    in another unit.

Units are never linked to themselves; duplicate edges are collapsed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if space == "" {
				return fmt.Errorf("--space is required")
			}
			if _, err := exec.LookPath("cub"); err != nil {
				return fmt.Errorf("cub CLI not found on PATH: %w", err)
			}
			workDir, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			manifestsDir := filepath.Join(workDir, "out", "manifests")
			entries, err := os.ReadDir(manifestsDir)
			if err != nil {
				return fmt.Errorf("read %s: %w", manifestsDir, err)
			}
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				slug := trimExt(e.Name())
				path := filepath.Join(manifestsDir, e.Name())
				ccmd := exec.Command("cub", "unit", "create", "--space", space, slug, path)
				ccmd.Stdout = os.Stdout
				ccmd.Stderr = os.Stderr
				if err := ccmd.Run(); err != nil {
					return fmt.Errorf("cub unit create %s: %w", slug, err)
				}
			}
			return createInferredLinks(space)
		},
	}
	cmd.Flags().StringVar(&space, "space", "", "destination ConfigHub space slug")
	return cmd
}

// cub function-do emits one JSON object per unit on stdout, separated by
// whitespace. The envelope carries identity (SpaceSlug, UnitSlug, ...) and
// nests the function's typed output under "Output". Output is decoded into
// SDK types from public/core/function/api (fapi.ResourceList,
// fapi.AttributeValueList, fapi.YAMLPayload).

type funcEnvelope[T any] struct {
	Output     T               `json:"Output"`
	OutputType fapi.OutputType `json:"OutputType"`
	SpaceSlug  string          `json:"SpaceSlug"`
	UnitSlug   string          `json:"UnitSlug"`
}

// --- Link inference ------------------------------------------------------

type linkEdge struct {
	fromUnit string
	toUnit   string
	reason   string // human-readable why; used in slug
}

// createInferredLinks runs get-resources, get-references, and
// get-workload-labels on the just-uploaded space, plus a yq call against any
// CRDs to learn their group/Kind, then creates links for every reference,
// selector match, and custom-resource → CRD pair.
func createInferredLinks(space string) error {
	resources, err := loadResources(space)
	if err != nil {
		return err
	}
	refs, err := loadReferences(space)
	if err != nil {
		return err
	}
	labels, err := loadWorkloadLabels(space)
	if err != nil {
		return err
	}
	crds, err := loadCRDIndex(space)
	if err != nil {
		return err
	}

	edges := planLinks(resources, refs, labels, crds)
	if len(edges) == 0 {
		fmt.Println("No links to create.")
		return nil
	}

	for _, e := range edges {
		slug := "-"
		ccmd := exec.Command("cub", "link", "create", "--space", space, "--quiet", slug, e.fromUnit, e.toUnit)
		ccmd.Stderr = os.Stderr
		if err := ccmd.Run(); err != nil {
			return fmt.Errorf("cub link create %s (%s -> %s): %w", slug, e.fromUnit, e.toUnit, err)
		}
		fmt.Printf("Linked %s -> %s (%s)\n", e.fromUnit, e.toUnit, e.reason)
	}
	return nil
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

func loadResources(space string) (*resourceIndex, error) {
	out, err := runCubJSON("function", "do",
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
	targetType fapi.ResourceType // e.g., "v1/Namespace"
	targetName fapi.ResourceName // raw value at the reference path
}

func loadReferences(space string) (map[string][]referenceEntry, error) {
	out, err := runCubJSON("function", "do",
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
	// selectors are pod label-selectors — they target pods elsewhere.
	selectors []map[string]string
	// templates are pod-template labels — they describe pods this unit
	// produces and that others may select.
	templates []map[string]string
}

func loadWorkloadLabels(space string) (map[string]*workloadLabels, error) {
	out, err := runCubJSON("function", "do",
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

// isSelectorPath classifies a workload-labels path as a selector (matches
// pods elsewhere) versus a pod-template labels path (describes the pods this
// unit produces). The registered selector paths all contain the literal
// substring "selector"; template-label paths do not.
func isSelectorPath(path fapi.ResolvedPath) bool {
	s := string(path)
	return strings.Contains(s, "selector") || strings.Contains(s, "Selector")
}

// loadCRDIndex maps "<group>/<Kind>" to the unit slug that contains the CRD.
// Empty if there are no CRDs in the space.
func loadCRDIndex(space string) (map[string]string, error) {
	out, err := runCubJSON("function", "do",
		"--space", space, "--quiet", "--show", "output", "-o", "json",
		"--resource-type", "apiextensions.k8s.io/v1/CustomResourceDefinition",
		"yq", `.spec.group + "/" + .spec.names.kind`)
	if err != nil {
		// If there are no CRDs in the space the function-do call still
		// succeeds with empty output; only treat real errors as fatal.
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
		// yq may emit document separators or quotes; strip them.
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
) []linkEdge {
	seen := map[string]linkEdge{}
	add := func(from, to, reason string) {
		if from == to || from == "" || to == "" {
			return
		}
		key := from + "\x00" + to
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = linkEdge{fromUnit: from, toUnit: to, reason: reason}
	}

	// References: source unit → unit that contains the referenced resource.
	for fromUnit, entries := range refs {
		for _, r := range entries {
			k := resourceLookupKey(r.targetType, r.targetName)
			for _, toUnit := range resources.unitsByTypeName[k] {
				add(fromUnit, toUnit, "reference:"+string(r.targetType))
			}
		}
	}

	// Workload label-selectors: source unit's selector ⊆ target unit's
	// pod-template labels.
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

	// Custom resources → CRD unit.
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

	// Deterministic order.
	out := make([]linkEdge, 0, len(seen))
	for _, e := range seen {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].fromUnit != out[j].fromUnit {
			return out[i].fromUnit < out[j].fromUnit
		}
		if out[i].toUnit != out[j].toUnit {
			return out[i].toUnit < out[j].toUnit
		}
		return out[i].reason < out[j].reason
	})
	return out
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

// groupKindFromResourceType parses a ResourceType into a "<group>/<Kind>"
// string used as the lookup key into the CRD index. ResourceType has the
// form "<group>/<version>/<Kind>" for non-core APIs or "<version>/<Kind>"
// for the core group; only the former can be matched to a CRD.
func groupKindFromResourceType(rt fapi.ResourceType) (string, bool) {
	parts := strings.Split(string(rt), "/")
	if len(parts) != 3 {
		return "", false
	}
	if !strings.Contains(parts[0], ".") {
		// Built-in (non-core) group like "apps", "batch" — no CRD link.
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

func runCubJSON(args ...string) ([]byte, error) {
	cmd := exec.Command("cub", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("cub %s: %w\n%s", strings.Join(args, " "), err, stderr.String())
	}
	data, err := io.ReadAll(&stdout)
	if err != nil {
		return nil, err
	}
	return data, nil
}

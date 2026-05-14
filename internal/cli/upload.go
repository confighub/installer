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

	"github.com/confighubai/installer/internal/deps"
	ipkg "github.com/confighubai/installer/internal/pkg"
	"github.com/confighubai/installer/internal/upload"
	"github.com/confighubai/installer/pkg/api"
)

func newUploadCmd() *cobra.Command {
	var (
		space        string
		spacePattern string
		target       string
		annotations  []string
		labels       []string
	)
	cmd := &cobra.Command{
		Use:   "upload <work-dir>",
		Short: "Upload rendered manifests to ConfigHub Space(s)",
		Long: `Upload sends rendered manifests to ConfigHub. The shape depends on whether
the package declares dependencies:

Single-package (no deps):
  --space chooses the Space; one Unit per file in <work-dir>/out/manifests.

Multi-package (parent declares dependencies):
  --space-pattern is a Go template evaluated per package (vars:
  .PackageName, .PackageVersion, .Variant — Variant is empty in v1).
  Default: '{{.PackageName}}'. Each package — parent + each locked dep —
  gets its own Space. The Units for the parent come from
  <work-dir>/out/manifests; each dep's Units come from
  <work-dir>/out/<dep>/manifests.

In both modes, every Space additionally receives one untargeted
"installer-record" Unit holding installer.yaml + that package's spec docs
(selection, inputs, function-chain, manifest-index, plus the lock for the
parent). The record Unit makes a Space self-describing — it can be
re-rendered from its own ConfigHub state.

Cross-Space NeedsProvides Links are created from the parent's record Unit
to each dep's record Unit so downstream tooling can see the dependency
relationship.

Files in <work-dir>/out/secrets/ (and each <dep>/secrets/) are never
uploaded — they hold rendered Secret resources flagged as sensitive by
render.

--target, --annotation, and --label are forwarded to ` + "`cub unit create`" + ` on
each rendered manifest Unit. They do NOT apply to the installer-record
Unit (which must remain untargeted).

After per-Space upload, the existing get-resources / get-references /
get-workload-labels link-inference runs once per Space to materialize
intra-Space NeedsProvides links.`,
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

			loaded, err := ipkg.Load(filepath.Join(workDir, "package"))
			if err != nil {
				return fmt.Errorf("load package: %w", err)
			}

			// Reconcile --space vs --space-pattern.
			//
			//   no deps:   --space S        → equivalent to pattern "S".
			//              --space-pattern  → also fine.
			//              both             → error.
			//              neither          → default pattern "{{.PackageName}}".
			//   with deps: --space          → error (one slug can't host N packages).
			//              --space-pattern  → fine.
			//              neither          → default pattern "{{.PackageName}}".
			hasDeps := len(loaded.Package.Spec.Dependencies) > 0
			pattern := spacePattern
			if space != "" && spacePattern != "" {
				return fmt.Errorf("--space and --space-pattern are mutually exclusive")
			}
			if space != "" {
				if hasDeps {
					return fmt.Errorf("--space cannot be used when the package declares dependencies; use --space-pattern")
				}
				pattern = space
			}
			if pattern == "" {
				pattern = "{{.PackageName}}"
			}

			var lock *api.Lock
			if hasDeps {
				lock, err = deps.ReadLock(workDir)
				if err != nil {
					return err
				}
				if lock == nil {
					return fmt.Errorf("package declares dependencies but %s does not exist; run `installer deps update %s` and `installer render %s` first",
						deps.LockPath(workDir), workDir, workDir)
				}
				if deps.IsStale(lock, loaded.Package) {
					return fmt.Errorf("lock at %s is stale; run `installer deps update %s` and `installer render %s` again",
						deps.LockPath(workDir), workDir, workDir)
				}
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

			for _, pkg := range packages {
				if err := uploadOnePackage(pkg, target, annotations, labels); err != nil {
					return err
				}
			}

			for _, l := range upload.PlanCrossSpaceLinks(packages) {
				if err := createCrossSpaceLink(l); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&space, "space", "", "destination ConfigHub Space slug (single-package mode)")
	cmd.Flags().StringVar(&spacePattern, "space-pattern", "", "Go template for the Space slug per package (vars: .PackageName, .PackageVersion, .Variant). Default: '{{.PackageName}}'.")
	cmd.Flags().StringVar(&target, "target", "", "target slug; forwarded to cub unit create --target on every rendered Unit (not the installer-record Unit)")
	cmd.Flags().StringSliceVar(&annotations, "annotation", nil, "annotation key=value to set on every rendered Unit (repeatable)")
	cmd.Flags().StringSliceVar(&labels, "label", nil, "label key=value to set on every rendered Unit (repeatable)")
	return cmd
}

// uploadOnePackage creates pkg.SpaceSlug if missing, uploads each rendered
// manifest as a Unit (with --target/--annotation/--label applied), creates
// the untargeted installer-record Unit, and runs the intra-Space link
// inference.
func uploadOnePackage(pkg upload.Package, target string, annotations, labels []string) error {
	fmt.Printf("== %s@%s → Space %s ==\n", pkg.Name, pkg.Version, pkg.SpaceSlug)

	if err := ensureSpace(pkg.SpaceSlug); err != nil {
		return err
	}

	entries, err := os.ReadDir(pkg.ManifestsDir)
	if err != nil {
		return fmt.Errorf("read %s: %w", pkg.ManifestsDir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		slug := trimExt(e.Name())
		path := filepath.Join(pkg.ManifestsDir, e.Name())
		cubArgs := []string{"unit", "create", "--space", pkg.SpaceSlug, "--merge-external-source", e.Name()}
		if target != "" {
			cubArgs = append(cubArgs, "--target", target)
		}
		for _, a := range annotations {
			cubArgs = append(cubArgs, "--annotation", a)
		}
		for _, l := range labels {
			cubArgs = append(cubArgs, "--label", l)
		}
		cubArgs = append(cubArgs, slug, path)
		ccmd := exec.Command("cub", cubArgs...)
		ccmd.Stdout = os.Stdout
		ccmd.Stderr = os.Stderr
		if err := ccmd.Run(); err != nil {
			return fmt.Errorf("cub unit create %s in %s: %w", slug, pkg.SpaceSlug, err)
		}
	}

	if err := createInstallerRecordUnit(pkg); err != nil {
		return err
	}
	return createInferredLinks(pkg.SpaceSlug)
}

// ensureSpace creates the Space if it does not exist. Idempotent via
// --allow-exists.
func ensureSpace(slug string) error {
	ccmd := exec.Command("cub", "space", "create", "--allow-exists", "--quiet", slug)
	ccmd.Stderr = os.Stderr
	if err := ccmd.Run(); err != nil {
		return fmt.Errorf("cub space create %s: %w", slug, err)
	}
	return nil
}

// createInstallerRecordUnit builds the multi-doc YAML body and creates the
// untargeted installer-record Unit. The body file is staged in a temp
// location and passed to `cub unit create`.
func createInstallerRecordUnit(pkg upload.Package) error {
	body, err := upload.BuildInstallerRecord(pkg)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "installer-record-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()

	ccmd := exec.Command("cub", "unit", "create",
		"--space", pkg.SpaceSlug,
		"--annotation", "installer.confighub.com/role=installer-record",
		"--annotation", "installer.confighub.com/package="+pkg.Name,
		upload.InstallerRecordSlug, tmp.Name(),
	)
	ccmd.Stdout = os.Stdout
	ccmd.Stderr = os.Stderr
	if err := ccmd.Run(); err != nil {
		return fmt.Errorf("cub unit create installer-record in %s: %w", pkg.SpaceSlug, err)
	}
	return nil
}

// createCrossSpaceLink wires the parent's record Unit to a dep's record
// Unit. The 4th positional arg to `cub link create` is the target Space.
func createCrossSpaceLink(l upload.CrossSpaceLink) error {
	ccmd := exec.Command("cub", "link", "create",
		"--space", l.FromSpace, "--quiet",
		l.Slug, l.FromUnit, l.ToUnit, l.ToSpace,
	)
	ccmd.Stderr = os.Stderr
	if err := ccmd.Run(); err != nil {
		return fmt.Errorf("cub link create %s (%s/%s -> %s/%s): %w",
			l.Slug, l.FromSpace, l.FromUnit, l.ToSpace, l.ToUnit, err)
	}
	fmt.Printf("Linked %s/%s -> %s/%s (%s)\n", l.FromSpace, l.FromUnit, l.ToSpace, l.ToUnit, l.Reason)
	return nil
}

func validateKeyValueFlags(flag string, vals []string) error {
	for _, v := range vals {
		if !strings.Contains(v, "=") {
			return fmt.Errorf("%s %q must be key=value", flag, v)
		}
	}
	return nil
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

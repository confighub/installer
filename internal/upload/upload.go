// Package upload turns a rendered work-dir into the inputs the cub CLI
// needs to materialize ConfigHub Spaces, Units, and Links — without itself
// shelling out. The CLI layer in internal/cli/upload.go orchestrates the
// cub calls.
//
// Phase 6 wires this up:
//   - One Space per package (parent + each locked dep).
//   - One untargeted installer-record Unit per Space, holding installer.yaml
//     + every file in that package's out/<pkg>/spec/ (plus the lock for the
//     parent).
//   - Cross-Space NeedsProvides links from the parent's record Unit to each
//     dep's record Unit, derived from the lock.
package upload

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/confighubai/installer/pkg/api"
)

// Package is one unit-of-upload — the parent or a locked dep.
type Package struct {
	// Name is metadata.name from installer.yaml.
	Name string
	// Version is metadata.version from installer.yaml.
	Version string
	// LocalHandle is the name the parent used for this dep in its
	// installer.yaml + lock. Empty for the parent itself. Used to derive
	// link slugs and to match dep packages back to lock entries.
	LocalHandle string
	// PackageDir is the directory containing installer.yaml.
	PackageDir string
	// ManifestsDir is where rendered per-resource YAML lives.
	ManifestsDir string
	// SpecDir is where this package's spec docs live (selection.yaml etc.).
	SpecDir string
	// SecretsDir is where rendered Secret YAML lives (never uploaded).
	SecretsDir string
	// SpaceSlug is the ConfigHub Space this package's Units land in.
	SpaceSlug string
	// IsParent is true for the root package; false for every dep.
	IsParent bool
}

// Vars is the template-execution context for --space-pattern.
type Vars struct {
	PackageName    string
	PackageVersion string
	// Variant is reserved for the variant work (Phase 9+). Empty in v1.
	Variant string
}

// RenderSpaceSlug expands pattern using vars. Templates have access to
// PackageName, PackageVersion, and Variant. Returns the expanded slug,
// stripped of whitespace.
func RenderSpaceSlug(pattern string, vars Vars) (string, error) {
	if pattern == "" {
		return "", fmt.Errorf("empty --space-pattern")
	}
	tmpl, err := template.New("space").Option("missingkey=error").Parse(pattern)
	if err != nil {
		return "", fmt.Errorf("parse --space-pattern %q: %w", pattern, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("execute --space-pattern %q: %w", pattern, err)
	}
	slug := strings.TrimSpace(buf.String())
	if slug == "" {
		return "", fmt.Errorf("--space-pattern %q rendered to an empty slug for package %+v", pattern, vars)
	}
	return slug, nil
}

// DiscoverInput is what Discover needs to do its job. Caller supplies the
// parent's already-loaded Package and Lock, plus the workDir and pattern.
type DiscoverInput struct {
	WorkDir       string
	SpacePattern  string
	ParentPackage *api.Package
	Lock          *api.Lock // nil when the parent declares no Dependencies
}

// Discover walks the work-dir layout produced by Render and returns one
// Package per source — the parent first, then each locked dep in lock
// order. Each dep's installer.yaml is read from the vendor cache the
// renderer populated at out/vendor/<name>@<version>/package/.
func Discover(in DiscoverInput) ([]Package, error) {
	if in.WorkDir == "" {
		return nil, fmt.Errorf("WorkDir is required")
	}
	if in.ParentPackage == nil {
		return nil, fmt.Errorf("ParentPackage is required")
	}
	pattern := in.SpacePattern
	if pattern == "" {
		pattern = "{{.PackageName}}"
	}

	out := []Package{}

	parentSlug, err := RenderSpaceSlug(pattern, Vars{
		PackageName:    in.ParentPackage.Metadata.Name,
		PackageVersion: in.ParentPackage.Metadata.Version,
	})
	if err != nil {
		return nil, fmt.Errorf("parent space slug: %w", err)
	}
	out = append(out, Package{
		Name:         in.ParentPackage.Metadata.Name,
		Version:      in.ParentPackage.Metadata.Version,
		PackageDir:   filepath.Join(in.WorkDir, "package"),
		ManifestsDir: filepath.Join(in.WorkDir, "out", "manifests"),
		SpecDir:      filepath.Join(in.WorkDir, "out", "spec"),
		SecretsDir:   filepath.Join(in.WorkDir, "out", "secrets"),
		SpaceSlug:    parentSlug,
		IsParent:     true,
	})

	if in.Lock == nil {
		return out, nil
	}
	for _, d := range in.Lock.Spec.Resolved {
		vendor := filepath.Join(in.WorkDir, "out", "vendor", vendorSlug(d.Name, d.Version), "package")
		if _, err := os.Stat(filepath.Join(vendor, "installer.yaml")); err != nil {
			return nil, fmt.Errorf("dep %s vendor missing — run `installer render %s` first: %w", d.Name, in.WorkDir, err)
		}
		// Read just enough metadata. We pulled this dep ourselves, so the
		// lock's Name/Version are authoritative; we still re-read
		// installer.yaml so a future per-dep override (e.g., a renamed
		// upstream package) is honored.
		data, err := os.ReadFile(filepath.Join(vendor, "installer.yaml"))
		if err != nil {
			return nil, err
		}
		depPkg, err := api.ParsePackage(data)
		if err != nil {
			return nil, fmt.Errorf("parse dep %s installer.yaml: %w", d.Name, err)
		}
		slug, err := RenderSpaceSlug(pattern, Vars{
			PackageName:    depPkg.Metadata.Name,
			PackageVersion: depPkg.Metadata.Version,
		})
		if err != nil {
			return nil, fmt.Errorf("dep %s space slug: %w", d.Name, err)
		}
		out = append(out, Package{
			Name:         depPkg.Metadata.Name,
			Version:      depPkg.Metadata.Version,
			LocalHandle:  d.Name,
			PackageDir:   vendor,
			ManifestsDir: filepath.Join(in.WorkDir, "out", d.Name, "manifests"),
			SpecDir:      filepath.Join(in.WorkDir, "out", d.Name, "spec"),
			SecretsDir:   filepath.Join(in.WorkDir, "out", d.Name, "secrets"),
			SpaceSlug:    slug,
			IsParent:     false,
		})
	}
	return out, nil
}

// vendorSlug mirrors render.vendorSlug — duplicated to avoid an import
// cycle. Keep in sync.
func vendorSlug(name, version string) string {
	if version == "" {
		return name
	}
	return name + "@" + version
}

// InstallerRecordSlug is the conventional name for the per-Space Unit that
// carries installer.yaml + spec docs (no Target). One per Space.
const InstallerRecordSlug = "installer-record"

// BuildInstallerRecord builds the multi-doc YAML body for the per-Space
// installer-record Unit. The result is `installer.yaml` followed by every
// YAML doc in pkg.SpecDir (in lexicographic order), separated by `---`.
// Files outside spec/ are not included.
func BuildInstallerRecord(pkg Package) ([]byte, error) {
	paths := []string{filepath.Join(pkg.PackageDir, "installer.yaml")}
	entries, err := os.ReadDir(pkg.SpecDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", pkg.SpecDir, err)
	}
	var specFiles []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasSuffix(n, ".yaml") && !strings.HasSuffix(n, ".yml") {
			continue
		}
		specFiles = append(specFiles, filepath.Join(pkg.SpecDir, n))
	}
	sort.Strings(specFiles)
	paths = append(paths, specFiles...)

	var buf bytes.Buffer
	for i, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		// Strip the BOM-style "---\n" header some YAML emitters leave at
		// the front so the boundary marker we insert is the only one.
		trimmed := bytes.TrimSpace(data)
		trimmed = bytes.TrimPrefix(trimmed, []byte("---\n"))
		trimmed = bytes.TrimSpace(trimmed)
		if i > 0 {
			buf.WriteString("---\n")
		}
		buf.Write(trimmed)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// CrossSpaceLink describes one parent-to-dep edge to materialize as a
// ConfigHub Link spanning two Spaces. Both ends point at each Space's
// installer-record Unit.
type CrossSpaceLink struct {
	// Slug is the link's deterministic slug, derived from the dep name.
	Slug string
	// FromSpace is the parent's Space; FromUnit is the parent's
	// installer-record Unit slug.
	FromSpace string
	FromUnit  string
	// ToSpace is the dep's Space; ToUnit is the dep's installer-record
	// Unit slug.
	ToSpace string
	ToUnit  string
	// Reason is the human-readable why (the dep's Name from the lock).
	Reason string
}

// PlanCrossSpaceLinks builds the list of links to create from a discovered
// package set. Packages must include the parent first; deps are matched by
// LocalHandle.
func PlanCrossSpaceLinks(packages []Package) []CrossSpaceLink {
	if len(packages) < 2 {
		return nil
	}
	if !packages[0].IsParent {
		return nil
	}
	parent := packages[0]
	var out []CrossSpaceLink
	for _, dep := range packages[1:] {
		out = append(out, CrossSpaceLink{
			Slug:      "dep-" + dep.LocalHandle,
			FromSpace: parent.SpaceSlug,
			FromUnit:  InstallerRecordSlug,
			ToSpace:   dep.SpaceSlug,
			ToUnit:    InstallerRecordSlug,
			Reason:    "depends on " + dep.LocalHandle,
		})
	}
	return out
}

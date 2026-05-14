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
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/confighubai/installer/internal/cubctx"
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

// UploadDocFilename is the basename of the persisted Upload doc inside
// the parent's spec dir.
const UploadDocFilename = "upload.yaml"

// BuildInstallerRecord builds the multi-doc YAML body for the per-Space
// installer-record Unit. The result is `installer.yaml` followed by every
// YAML doc in pkg.SpecDir (in lexicographic order), separated by `---`.
// Files outside spec/ are not included. upload.yaml (if present) is
// included so a freshly cloned work-dir can re-derive everything,
// including where it was uploaded, from ConfigHub alone.
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

// RefreshInstallerRecord rebuilds the installer-record Unit body from
// pkg's local files and uploads it to ConfigHub. Used after
// `installer update` / `installer upgrade-apply` mutates the local
// spec so the cub-side record stays in sync — without this refresh,
// a subsequent upgrade reads stale inputs (notably ImageOverrides)
// from ConfigHub via wizard.LoadPriorState.
//
// Idempotent: cub unit update --merge-external-source upserts
// against the prior MergeExternal recorded under the same source
// name (installer-record).
func RefreshInstallerRecord(ctx context.Context, pkg Package) error {
	body, err := BuildInstallerRecord(pkg)
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
	cmd := exec.CommandContext(ctx, "cub", "unit", "update",
		"--space", pkg.SpaceSlug,
		"--merge-external-source", InstallerRecordSlug,
		InstallerRecordSlug, tmp.Name(),
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("refresh installer-record in %s: %w\n%s", pkg.SpaceSlug, err, stderr.String())
	}
	return nil
}

// SplitInstallerRecord is the inverse of BuildInstallerRecord: it
// splits a multi-doc body into one decoded value per kind. Unknown
// kinds are silently skipped — the body is forward-compatible with
// future spec docs. installer.yaml is parsed as Package; everything
// else is keyed by Kind.
type RecordContents struct {
	Package   *api.Package
	Selection *api.Selection
	Inputs    *api.Inputs
	Facts     *api.Facts
	Lock      *api.Lock
	Upload    *api.Upload
}

// SplitInstallerRecord parses a multi-doc YAML stream produced by
// BuildInstallerRecord. It is tolerant of new kinds being added later.
func SplitInstallerRecord(body []byte) (*RecordContents, error) {
	docs, err := api.SplitMultiDoc(body)
	if err != nil {
		return nil, fmt.Errorf("split installer-record: %w", err)
	}
	out := &RecordContents{}
	for i, d := range docs {
		_, kind, err := api.SniffKind(d)
		if err != nil {
			return nil, fmt.Errorf("doc %d: %w", i, err)
		}
		switch kind {
		case api.KindPackage:
			p, err := api.ParsePackage(d)
			if err != nil {
				return nil, fmt.Errorf("doc %d (Package): %w", i, err)
			}
			out.Package = p
		case api.KindSelection:
			s, err := api.ParseSelection(d)
			if err != nil {
				return nil, fmt.Errorf("doc %d (Selection): %w", i, err)
			}
			out.Selection = s
		case api.KindInputs:
			ins, err := api.ParseInputs(d)
			if err != nil {
				return nil, fmt.Errorf("doc %d (Inputs): %w", i, err)
			}
			out.Inputs = ins
		case api.KindFacts:
			f, err := api.ParseFacts(d)
			if err != nil {
				return nil, fmt.Errorf("doc %d (Facts): %w", i, err)
			}
			out.Facts = f
		case api.KindLock:
			l, err := api.ParseLock(d)
			if err != nil {
				return nil, fmt.Errorf("doc %d (Lock): %w", i, err)
			}
			out.Lock = l
		case api.KindUpload:
			u, err := api.ParseUpload(d)
			if err != nil {
				return nil, fmt.Errorf("doc %d (Upload): %w", i, err)
			}
			out.Upload = u
		default:
			// Unknown kind — ignore for forward compatibility.
		}
	}
	return out, nil
}

// WriteUploadDoc writes <work-dir>/out/spec/upload.yaml from the
// discovered package set. Reads the active cub context to record the
// organization ID and server URL alongside the resolved Space slugs.
//
// Called by the CLI at the end of a successful `installer upload`. Safe
// to call when packages contains only the parent (no deps).
func WriteUploadDoc(ctx context.Context, workDir, spacePattern string, packages []Package) error {
	if len(packages) == 0 || !packages[0].IsParent {
		return fmt.Errorf("WriteUploadDoc: packages must start with the parent")
	}
	parent := packages[0]
	cc, err := cubctx.Get(ctx)
	if err != nil {
		// Don't fail the upload — record what we have. The org/server
		// check on subsequent commands will still flag a true mismatch
		// (against an empty value the check is a no-op, which is the
		// right behavior for an installer that ran before cubctx
		// existed).
		cc = &cubctx.Context{}
	}
	doc := &api.Upload{
		APIVersion: api.APIVersion,
		Kind:       api.KindUpload,
		Metadata:   api.Metadata{Name: parent.Name + "-upload"},
		Spec: api.UploadSpec{
			Package:        parent.Name,
			PackageVersion: parent.Version,
			SpacePattern:   spacePattern,
			Spaces:         make([]api.UploadedSpace, 0, len(packages)),
			UploadedAt:     time.Now().UTC().Format(time.RFC3339),
			Server:         cc.ServerURL,
			OrganizationID: cc.OrganizationID,
		},
	}
	for _, p := range packages {
		doc.Spec.Spaces = append(doc.Spec.Spaces, api.UploadedSpace{
			Package:  p.Name,
			Version:  p.Version,
			Slug:     p.SpaceSlug,
			IsParent: p.IsParent,
		})
	}
	data, err := api.MarshalYAML(doc)
	if err != nil {
		return err
	}
	path := filepath.Join(workDir, "out", "spec", UploadDocFilename)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// CrossSpaceLink describes one parent-to-dep edge to materialize as a
// ConfigHub Link spanning two Spaces. Both ends point at each Space's
// installer-record Unit.
type CrossSpaceLink struct {
	// Slug is the link's deterministic slug, derived from the dep name.
	Slug string
	// Component is the parent package name, used as the value of the
	// "Component" label on the created link.
	Component string
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
			Component: parent.Name,
			FromSpace: parent.SpaceSlug,
			FromUnit:  InstallerRecordSlug,
			ToSpace:   dep.SpaceSlug,
			ToUnit:    InstallerRecordSlug,
			Reason:    "depends on " + dep.LocalHandle,
		})
	}
	return out
}

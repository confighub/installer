// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

// Package upload turns a rendered work-dir into ConfigHub upload requests,
// one per package: the parent and each locked dependency, each into its own
// Space. The server decides what to create, merge, or empty; this package
// only says what the bundle is and where it goes, and reports the result.
package upload

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/confighub/installer/pkg/api"
)

// Package is one unit-of-upload — the parent or a locked dep.
type Package struct {
	// Name is metadata.name from installer.yaml.
	Name string
	// Version is installerMetadata.version from installer.yaml.
	Version string
	// LocalHandle is the name the parent used for this dep in its
	// installer.yaml + lock. Empty for the parent itself. Names the dep's
	// out/<handle>/ directory.
	LocalHandle string
	// PackageDir is the directory containing installer.yaml.
	PackageDir string
	// ManifestsDir is where rendered per-resource YAML lives.
	ManifestsDir string
	// RecordDir is where this package's record of the render lives
	// (selection.yaml, inputs.yaml, and so on).
	RecordDir string
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
	// Variant is the value of the Space's Variant label.
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
// parent's already-loaded Package and Lock, plus the workDir, pattern, and
// variant the pattern is rendered with.
type DiscoverInput struct {
	WorkDir       string
	SpacePattern  string
	Variant       string
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
		PackageVersion: in.ParentPackage.InstallerMetadata.Version,
		Variant:        in.Variant,
	})
	if err != nil {
		return nil, fmt.Errorf("parent space slug: %w", err)
	}
	out = append(out, Package{
		Name:         in.ParentPackage.Metadata.Name,
		Version:      in.ParentPackage.InstallerMetadata.Version,
		PackageDir:   filepath.Join(in.WorkDir, "package"),
		ManifestsDir: filepath.Join(in.WorkDir, "out", "manifests"),
		RecordDir:    filepath.Join(in.WorkDir, "out", api.RecordDir),
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
			PackageVersion: depPkg.InstallerMetadata.Version,
			Variant:        in.Variant,
		})
		if err != nil {
			return nil, fmt.Errorf("dep %s space slug: %w", d.Name, err)
		}
		out = append(out, Package{
			Name:         depPkg.Metadata.Name,
			Version:      depPkg.InstallerMetadata.Version,
			LocalHandle:  d.Name,
			PackageDir:   vendor,
			ManifestsDir: filepath.Join(in.WorkDir, "out", d.Name, "manifests"),
			RecordDir:    filepath.Join(in.WorkDir, "out", d.Name, api.RecordDir),
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

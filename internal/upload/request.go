// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package upload

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	goclientnew "github.com/confighub/sdk/core/openapi/goclient-new"
)

// RequestOptions are the placement decisions an upload applies to every
// package: the Space labels and annotations, Unit metadata, Target, and
// ChangeSet. They come from the upload command's flags.
type RequestOptions struct {
	// Component is the parent's Component label. Empty means the parent's
	// package name. A dependency is always its own package name, so the
	// parent's DependsOn can name it.
	Component string
	// Variant is every package's Variant label.
	Variant string
	// SpaceLabels are applied to every package's Space, alongside Component
	// and Variant.
	SpaceLabels      map[string]string
	SpaceAnnotations map[string]string
	UnitLabels       map[string]string
	UnitAnnotations  map[string]string
	// TargetIDs maps a Space slug to the Target its new Units are created on.
	TargetIDs map[string]goclientnew.UUID
	// ChangeSetID is an existing ChangeSet every package's writes join, so one
	// restore reverts the whole install. Nil gives each Space its own.
	ChangeSetID *goclientnew.UUID
	// SourceRef is recorded as each Space's ExternalSourceRef.
	SourceRef     string
	ClientVersion string
}

// ComponentName is the Component label, and the upload component name, for pkg.
func (o RequestOptions) ComponentName(pkg Package) string {
	if pkg.IsParent && o.Component != "" {
		return o.Component
	}
	return pkg.Name
}

// BuildRequest returns the upload request for one package: its rendered
// manifests and its record document, placed in pkg.SpaceSlug with the install
// namespace as the release namespace. The parent's request names its
// dependencies, which are the other packages.
func BuildRequest(pkg Package, packages []Package, opts RequestOptions) (goclientnew.UploadRequest, error) {
	files, err := manifestFiles(pkg.ManifestsDir)
	if err != nil {
		return goclientnew.UploadRequest{}, err
	}
	record, err := BuildRecord(pkg)
	if err != nil {
		return goclientnew.UploadRequest{}, fmt.Errorf("build installer record for %s: %w", pkg.Name, err)
	}
	files = append(files, goclientnew.UploadRequestFile{Path: RecordFileName, Content: string(record)})

	namespace, err := Namespace(pkg)
	if err != nil {
		return goclientnew.UploadRequest{}, err
	}

	name := opts.ComponentName(pkg)
	labels := map[string]string{}
	for k, v := range opts.SpaceLabels {
		labels[k] = v
	}
	labels["Component"] = name
	labels["Variant"] = opts.Variant

	component := goclientnew.UploadComponentRequest{
		Name: name,
		// Ownership follows the package, not the Component label, so a
		// relabeled Component keeps the Units its earlier uploads wrote.
		SourceName:       pkg.Name,
		Namespace:        namespace,
		Space:            pkg.SpaceSlug,
		SpaceLabels:      labels,
		SpaceAnnotations: opts.SpaceAnnotations,
		UnitLabels:       opts.UnitLabels,
		UnitAnnotations:  opts.UnitAnnotations,
	}
	if id, ok := opts.TargetIDs[pkg.SpaceSlug]; ok {
		component.TargetID = &id
	}
	if pkg.IsParent {
		for _, dep := range packages {
			if !dep.IsParent {
				component.DependsOn = append(component.DependsOn, opts.ComponentName(dep))
			}
		}
	}

	return goclientnew.UploadRequest{
		Files:                files,
		Components:           []goclientnew.UploadComponentRequest{component},
		ChangeSetID:          opts.ChangeSetID,
		ChangeSetDescription: fmt.Sprintf("installer upload of %s@%s", pkg.Name, pkg.Version),
		ChangeDescription:    fmt.Sprintf("installer upload of %s@%s", pkg.Name, pkg.Version),
		Source: &goclientnew.UploadSourceInfo{
			Ref:           opts.SourceRef,
			Client:        "installer",
			ClientVersion: opts.ClientVersion,
		},
	}, nil
}

// manifestFiles reads every YAML file under dir, named relative to it. Secrets
// are rendered to a separate directory and so are never read here.
func manifestFiles(dir string) ([]goclientnew.UploadRequestFile, error) {
	var files []goclientnew.UploadRequestFile
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files = append(files, goclientnew.UploadRequestFile{
			Path:    "manifests/" + filepath.ToSlash(rel),
			Content: string(data),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read rendered manifests in %s — run `installer render` first: %w", dir, err)
	}
	return files, nil
}

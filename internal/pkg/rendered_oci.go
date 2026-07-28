// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package pkg

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/content/oci"

	"github.com/confighub/installer/internal/version"
	"github.com/confighub/installer/pkg/api"
)

// RenderedOCIOptions describes a rendered artifact produced by setup.
type RenderedOCIOptions struct {
	WorkDir         string
	Destination     string
	SourceReference string
	SourceDigest    string
	Package         *api.Package
	Selection       *api.Selection
	Inputs          *api.Inputs
}

// RenderedOCIResult describes a verified local write or registry push.
type RenderedOCIResult struct {
	Ref             string
	ManifestDigest  string
	LayerDigest     string
	ObjectSetDigest string
	LayerSize       int64
	ManifestCount   int
	Files           []string
	Verified        bool
}

type renderedFile struct {
	path string
	body []byte
}

// PublishRenderedOCI writes the non-secret rendered manifests as an OCI
// artifact. An oci:// destination is pushed to a registry. Any other
// destination is written as a local OCI image layout tagged "latest".
func PublishRenderedOCI(ctx context.Context, opts RenderedOCIOptions) (*RenderedOCIResult, error) {
	if opts.WorkDir == "" {
		return nil, fmt.Errorf("rendered OCI: work-dir is required")
	}
	if opts.Destination == "" {
		return nil, fmt.Errorf("rendered OCI: destination is required")
	}
	if opts.Package == nil || opts.Selection == nil || opts.Inputs == nil {
		return nil, fmt.Errorf("rendered OCI: package, selection, and inputs are required")
	}

	var (
		target oras.Target
		tag    string
		ref    string
	)
	if strings.HasPrefix(opts.Destination, "oci://") {
		repoRef, parsedTag, _, err := parseRef(opts.Destination, true)
		if err != nil {
			return nil, err
		}
		repo, err := newRepo(repoRef)
		if err != nil {
			return nil, err
		}
		target = repo
		tag = parsedTag
		ref = "oci://" + repoRef + ":" + tag
	} else {
		layoutPath, err := filepath.Abs(opts.Destination)
		if err != nil {
			return nil, err
		}
		store, err := oci.New(layoutPath)
		if err != nil {
			return nil, fmt.Errorf("create OCI layout %s: %w", layoutPath, err)
		}
		target = store
		tag = "latest"
		ref = layoutPath + ":latest"
	}

	result, err := stageAndCopyRendered(ctx, opts, tag, target)
	if err != nil {
		return nil, err
	}
	result.Ref = ref
	return result, nil
}

// ResolveManifestDigest resolves an OCI reference without pulling its layers.
func ResolveManifestDigest(ctx context.Context, ref string) (string, error) {
	if !strings.HasPrefix(ref, "oci://") {
		return "", nil
	}
	repoRef, tag, want, err := parseRef(ref, false)
	if err != nil {
		return "", err
	}
	repo, err := newRepo(repoRef)
	if err != nil {
		return "", err
	}
	resolveRef := tag
	if resolveRef == "" {
		resolveRef = want.String()
	}
	desc, err := repo.Resolve(ctx, resolveRef)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", ref, err)
	}
	if want != "" && desc.Digest != want {
		return "", fmt.Errorf("digest mismatch on %s: pinned %s but registry returned %s", ref, want, desc.Digest)
	}
	return desc.Digest.String(), nil
}

func stageAndCopyRendered(ctx context.Context, opts RenderedOCIOptions, tag string, dst oras.Target) (*RenderedOCIResult, error) {
	manifestFiles, err := collectRenderedFiles(opts.WorkDir)
	if err != nil {
		return nil, err
	}
	layerFiles := append([]renderedFile{{
		path: "kustomization.yaml",
		body: renderedKustomization(manifestFiles),
	}}, manifestFiles...)
	layerData, err := renderedTarGz(layerFiles)
	if err != nil {
		return nil, err
	}
	layerDigest := digest.FromBytes(layerData)
	layerDesc := ocispec.Descriptor{
		MediaType: api.RenderedLayerMediaType,
		Digest:    layerDigest,
		Size:      int64(len(layerData)),
		Annotations: map[string]string{
			ocispec.AnnotationTitle: api.RenderedLayerTitle,
		},
	}
	objectSetDigest := renderedObjectSetDigest(manifestFiles)

	config, err := renderedConfig(opts, manifestFiles, layerFiles, layerDigest.String(), int64(len(layerData)), objectSetDigest)
	if err != nil {
		return nil, err
	}
	configData, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	configDesc := ocispec.Descriptor{
		MediaType: api.RenderedConfigMediaType,
		Digest:    digest.FromBytes(configData),
		Size:      int64(len(configData)),
	}

	staging := memory.New()
	if err := staging.Push(ctx, layerDesc, bytes.NewReader(layerData)); err != nil {
		return nil, fmt.Errorf("stage rendered layer: %w", err)
	}
	if err := staging.Push(ctx, configDesc, bytes.NewReader(configData)); err != nil {
		return nil, fmt.Errorf("stage rendered config: %w", err)
	}
	annotations := map[string]string{
		ocispec.AnnotationCreated:      "1970-01-01T00:00:00Z",
		ocispec.AnnotationTitle:        opts.Package.Metadata.Name,
		api.AnnotationName:             opts.Package.Metadata.Name,
		api.AnnotationVersion:          opts.Package.InstallerMetadata.Version,
		api.AnnotationInstallerVersion: version.Version,
		api.AnnotationBase:             opts.Selection.Spec.Base,
		api.AnnotationObjectSetDigest:  objectSetDigest,
	}
	if opts.SourceReference != "" {
		annotations[api.AnnotationSourceReference] = opts.SourceReference
	}
	if opts.SourceDigest != "" {
		annotations[api.AnnotationSourceDigest] = opts.SourceDigest
	}
	manifestDesc, err := oras.PackManifest(ctx, staging, oras.PackManifestVersion1_1, api.RenderedArtifactType, oras.PackManifestOptions{
		Layers:              []ocispec.Descriptor{layerDesc},
		ConfigDescriptor:    &configDesc,
		ManifestAnnotations: annotations,
	})
	if err != nil {
		return nil, fmt.Errorf("pack rendered manifest: %w", err)
	}
	if err := staging.Tag(ctx, manifestDesc, tag); err != nil {
		return nil, err
	}
	if _, err := oras.Copy(ctx, staging, tag, dst, tag, oras.DefaultCopyOptions); err != nil {
		return nil, fmt.Errorf("copy rendered OCI: %w", err)
	}

	verified, err := inspectRenderedFromTarget(ctx, dst, tag)
	if err != nil {
		return nil, fmt.Errorf("verify rendered OCI: %w", err)
	}
	if verified.manifestDigest != manifestDesc.Digest.String() {
		return nil, fmt.Errorf("verify rendered OCI: manifest digest changed: wrote %s, read %s", manifestDesc.Digest, verified.manifestDigest)
	}
	if verified.objectSetDigest != objectSetDigest {
		return nil, fmt.Errorf("verify rendered OCI: object-set digest changed: wrote %s, read %s", objectSetDigest, verified.objectSetDigest)
	}

	files := make([]string, 0, len(layerFiles))
	for _, file := range layerFiles {
		files = append(files, file.path)
	}
	return &RenderedOCIResult{
		ManifestDigest:  manifestDesc.Digest.String(),
		LayerDigest:     layerDigest.String(),
		ObjectSetDigest: objectSetDigest,
		LayerSize:       int64(len(layerData)),
		ManifestCount:   len(manifestFiles),
		Files:           files,
		Verified:        true,
	}, nil
}

func collectRenderedFiles(workDir string) ([]renderedFile, error) {
	outDir := filepath.Join(workDir, "out")
	var manifestDirs []string
	root := filepath.Join(outDir, "manifests")
	if info, err := os.Stat(root); err == nil && info.IsDir() {
		manifestDirs = append(manifestDirs, root)
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		return nil, fmt.Errorf("read rendered output %s: %w", outDir, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "manifests" || entry.Name() == "vendor" {
			continue
		}
		dir := filepath.Join(outDir, entry.Name(), "manifests")
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			manifestDirs = append(manifestDirs, dir)
		}
	}
	sort.Strings(manifestDirs)

	var files []renderedFile
	for _, dir := range manifestDirs {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info.IsDir() {
				return nil
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("rendered OCI: non-regular manifest file %s", path)
			}
			ext := strings.ToLower(filepath.Ext(info.Name()))
			if ext != ".yaml" && ext != ".yml" {
				return nil
			}
			rel, err := filepath.Rel(outDir, path)
			if err != nil {
				return err
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			files = append(files, renderedFile{path: filepath.ToSlash(rel), body: body})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	if len(files) == 0 {
		return nil, fmt.Errorf("rendered OCI: no YAML manifests found under %s", outDir)
	}
	return files, nil
}

func renderedConfig(
	opts RenderedOCIOptions,
	manifestFiles []renderedFile,
	layerFiles []renderedFile,
	layerDigest string,
	layerSize int64,
	objectSetDigest string,
) (*api.RenderedConfigBlob, error) {
	selectionDigest, err := sha256Path(filepath.Join(opts.WorkDir, "out", "spec", "selection.yaml"))
	if err != nil {
		return nil, err
	}
	inputsDigest, err := sha256Path(filepath.Join(opts.WorkDir, "out", "spec", "inputs.yaml"))
	if err != nil {
		return nil, err
	}
	chainDigest, err := sha256Path(filepath.Join(opts.WorkDir, "out", "spec", "function-chain.yaml"))
	if err != nil {
		return nil, err
	}
	checks := []api.RenderedCheck{{Name: "render", Result: "passed"}}
	for _, name := range selectedValidatorNames(opts.Package, opts.Selection) {
		checks = append(checks, api.RenderedCheck{Name: name, Result: "passed"})
	}
	files := make([]string, 0, len(layerFiles))
	for _, file := range layerFiles {
		files = append(files, file.path)
	}
	return &api.RenderedConfigBlob{
		SchemaVersion:    "v1",
		InstallerVersion: version.Version,
		Source: api.RenderedSource{
			Reference:      opts.SourceReference,
			ManifestDigest: opts.SourceDigest,
			Package:        opts.Package.Metadata.Name,
			PackageVersion: opts.Package.InstallerMetadata.Version,
		},
		Render: api.RenderedContext{
			Base:                opts.Selection.Spec.Base,
			Components:          append([]string(nil), opts.Selection.Spec.Components...),
			Namespace:           opts.Inputs.Spec.Namespace,
			SelectionSHA256:     selectionDigest,
			InputsSHA256:        inputsDigest,
			FunctionChainSHA256: chainDigest,
		},
		Checks: checks,
		Output: api.RenderedBundleInfo{
			ManifestCount:   len(manifestFiles),
			ObjectSetDigest: objectSetDigest,
			LayerDigest:     layerDigest,
			LayerSize:       layerSize,
			Files:           files,
		},
	}, nil
}

func selectedValidatorNames(pkg *api.Package, selection *api.Selection) []string {
	selected := make(map[string]bool, len(selection.Spec.Components))
	for _, name := range selection.Spec.Components {
		selected[name] = true
	}
	var names []string
	appendGroups := func(groups []api.FunctionGroup) {
		for _, group := range groups {
			for _, invocation := range group.Invocations {
				names = append(names, invocation.Name)
			}
		}
	}
	appendGroups(pkg.Spec.Validators)
	for _, component := range pkg.Spec.Components {
		if selected[component.Name] {
			appendGroups(component.Validators)
		}
	}
	return names
}

func renderedKustomization(files []renderedFile) []byte {
	var b strings.Builder
	b.WriteString("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n")
	for _, file := range files {
		fmt.Fprintf(&b, "  - %s\n", file.path)
	}
	return []byte(b.String())
}

func renderedObjectSetDigest(files []renderedFile) string {
	h := sha256.New()
	for _, file := range files {
		h.Write([]byte(file.path))
		h.Write([]byte{0})
		h.Write(file.body)
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func sha256Path(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func renderedTarGz(files []renderedFile) ([]byte, error) {
	var buf bytes.Buffer
	gw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	gw.Name = ""
	gw.Comment = ""
	gw.ModTime = time.Time{}
	gw.OS = 255
	tw := tar.NewWriter(gw)
	for _, file := range files {
		header := &tar.Header{
			Name:     file.path,
			Mode:     0o644,
			Size:     int64(len(file.body)),
			Typeflag: tar.TypeReg,
			ModTime:  time.Time{},
			Uid:      0,
			Gid:      0,
			Format:   tar.FormatPAX,
		}
		if err := tw.WriteHeader(header); err != nil {
			return nil, err
		}
		if _, err := tw.Write(file.body); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type renderedInspection struct {
	manifestDigest  string
	objectSetDigest string
	config          api.RenderedConfigBlob
}

func inspectRenderedFromTarget(ctx context.Context, target oras.Target, ref string) (*renderedInspection, error) {
	desc, err := target.Resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	manifestReader, err := target.Fetch(ctx, desc)
	if err != nil {
		return nil, err
	}
	defer manifestReader.Close()
	manifestData, err := io.ReadAll(manifestReader)
	if err != nil {
		return nil, err
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, err
	}
	if manifest.ArtifactType != api.RenderedArtifactType {
		return nil, fmt.Errorf("unexpected rendered artifact type %q", manifest.ArtifactType)
	}
	if len(manifest.Layers) != 1 || manifest.Layers[0].MediaType != api.RenderedLayerMediaType {
		return nil, fmt.Errorf("rendered OCI must contain one %s layer", api.RenderedLayerMediaType)
	}
	layerReader, err := target.Fetch(ctx, manifest.Layers[0])
	if err != nil {
		return nil, err
	}
	defer layerReader.Close()
	layerData, err := io.ReadAll(layerReader)
	if err != nil {
		return nil, err
	}
	if actual := digest.FromBytes(layerData); actual != manifest.Layers[0].Digest {
		return nil, fmt.Errorf("rendered layer digest mismatch: manifest says %s, fetched %s", manifest.Layers[0].Digest, actual)
	}
	layerFiles, err := renderedFilesFromTarGz(layerData)
	if err != nil {
		return nil, err
	}
	var manifestFiles []renderedFile
	for _, file := range layerFiles {
		if file.path != "kustomization.yaml" {
			manifestFiles = append(manifestFiles, file)
		}
	}
	objectSetDigest := renderedObjectSetDigest(manifestFiles)

	configReader, err := target.Fetch(ctx, manifest.Config)
	if err != nil {
		return nil, err
	}
	defer configReader.Close()
	configData, err := io.ReadAll(configReader)
	if err != nil {
		return nil, err
	}
	var config api.RenderedConfigBlob
	if err := json.Unmarshal(configData, &config); err != nil {
		return nil, err
	}
	if config.Output.LayerDigest != manifest.Layers[0].Digest.String() {
		return nil, fmt.Errorf("rendered layer digest metadata mismatch: config says %s, manifest says %s", config.Output.LayerDigest, manifest.Layers[0].Digest)
	}
	if config.Output.LayerSize != manifest.Layers[0].Size {
		return nil, fmt.Errorf("rendered layer size metadata mismatch: config says %d, manifest says %d", config.Output.LayerSize, manifest.Layers[0].Size)
	}
	if config.Output.ManifestCount != len(manifestFiles) {
		return nil, fmt.Errorf("rendered manifest count mismatch: config says %d, layer contains %d", config.Output.ManifestCount, len(manifestFiles))
	}
	actualFiles := make([]string, 0, len(layerFiles))
	for _, file := range layerFiles {
		actualFiles = append(actualFiles, file.path)
	}
	if !sameRenderedPaths(config.Output.Files, actualFiles) {
		return nil, fmt.Errorf("rendered file list mismatch: config says %v, layer contains %v", config.Output.Files, actualFiles)
	}
	if config.Output.ObjectSetDigest != objectSetDigest {
		return nil, fmt.Errorf("rendered object-set digest mismatch: config says %s, layer contains %s", config.Output.ObjectSetDigest, objectSetDigest)
	}
	return &renderedInspection{
		manifestDigest:  desc.Digest.String(),
		objectSetDigest: objectSetDigest,
		config:          config,
	}, nil
}

func renderedFilesFromTarGz(body []byte) ([]renderedFile, error) {
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var files []renderedFile
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		clean := filepath.ToSlash(filepath.Clean(header.Name))
		if clean == "." || strings.HasPrefix(clean, "../") || filepath.IsAbs(header.Name) {
			return nil, fmt.Errorf("rendered OCI contains unsafe path %q", header.Name)
		}
		if header.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("rendered OCI contains non-regular entry %q", header.Name)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		files = append(files, renderedFile{path: clean, body: data})
	}
	return files, nil
}

func sameRenderedPaths(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

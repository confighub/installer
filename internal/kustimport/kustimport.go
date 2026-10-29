// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

// Package kustimport copies an existing kustomization into a package base,
// for `installer init --source`. The copy is made with kustomize's localizer,
// so remote resources are downloaded into the package and every file the
// source references comes along.
package kustimport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"sigs.k8s.io/kustomize/api/konfig"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/krusty/localizer"
	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"

	"github.com/confighub/installer/internal/chainexec"
	"github.com/confighub/installer/pkg/api"
)

// Result describes an imported base.
type Result struct {
	// Scope is the directory the localizer was confined to: every file the
	// source references had to be inside it.
	Scope string
	// Target is the source kustomization's directory relative to the
	// imported base. It is "." when the source referenced nothing outside
	// its own directory. Otherwise the base's kustomization.yaml is a
	// generated one that lists Target as its only resource, and the files
	// the source referenced sit beside Target as they did under Scope.
	Target string
	// Images are the entries seeded into the base's images: block, so
	// operators can override them with --set-image. Empty when the source
	// already declared images or runs no containers.
	Images []types.Image
}

// Import localizes the kustomization directory source into dest, which must
// be absent or an empty directory. scope bounds which files the source may
// reference; empty means the enclosing git repository, or source itself
// outside one.
//
// The import is refused when the source uses Helm (render does not run it)
// or produces Secrets with data (the package would publish them), and when
// the imported base does not build to the same output as the source.
// Nothing is written to dest unless the import succeeds.
func Import(source, scope, dest string) (*Result, error) {
	src, err := resolveDir(source)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	explicitScope := scope != ""
	if explicitScope {
		if scope, err = resolveDir(scope); err != nil {
			return nil, fmt.Errorf("scope: %w", err)
		}
	} else {
		scope = gitRoot(src)
	}
	rel, err := filepath.Rel(scope, src)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("source %s is not inside scope %s", src, scope)
	}
	if err := requireEmptyDir(dest); err != nil {
		return nil, err
	}

	// The temporary directory is a sibling of dest so the finished base can
	// be renamed into place.
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dest), ".import-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	out := filepath.Join(tmp, "out")
	if _, err := localizer.Run(filesys.MakeFsOnDisk(), src, scope, out); err != nil {
		if !explicitScope {
			return nil, fmt.Errorf("%w\nIf the source references files outside %s, pass --source-scope with a directory that contains all of them", err, scope)
		}
		return nil, err
	}

	// The localized tree mirrors scope. Drop the directories every file
	// shares, so a self-contained source lands flat in dest.
	root, err := commonDir(out)
	if err != nil {
		return nil, err
	}
	target, err := filepath.Rel(root, filepath.Join(out, rel))
	if err != nil {
		return nil, err
	}
	if err := rejectHelm(root); err != nil {
		return nil, err
	}

	want, err := build(src)
	if err != nil {
		return nil, fmt.Errorf("build %s: %w", src, err)
	}
	if err := rejectSecretData(want); err != nil {
		return nil, err
	}

	res := &Result{Scope: scope, Target: target}
	kustPath := filepath.Join(root, konfig.DefaultKustomizationFileName())
	if target == "." {
		// installer new and --set-image edit kustomization.yaml by that name.
		if err := normalizeKustomizationName(root); err != nil {
			return nil, err
		}
		declared, err := declaresImages(kustPath)
		if err != nil {
			return nil, err
		}
		if !declared {
			res.Images = seedImages(want)
		}
	} else {
		// root holds no kustomization of its own: one there would be an
		// ancestor of the source, which kustomize refuses as a cycle.
		if err := writeTopLevel(kustPath, target); err != nil {
			return nil, err
		}
		res.Images = seedImages(want)
	}
	if err := setImages(kustPath, res.Images); err != nil {
		return nil, err
	}

	got, err := build(root)
	if err != nil {
		return nil, fmt.Errorf("build the imported copy: %w", err)
	}
	if err := sameOutput(want, got); err != nil {
		return nil, err
	}

	if err := os.Remove(dest); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err := os.Rename(root, dest); err != nil {
		return nil, err
	}
	return res, nil
}

func resolveDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// The localizer compares symlink-resolved paths against the scope.
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", abs)
	}
	return abs, nil
}

// gitRoot returns the nearest ancestor of dir (or dir itself) holding a .git
// entry, or dir when there is none. A kustomization's references to sibling
// directories rarely leave the repository it lives in.
func gitRoot(dir string) string {
	for d := dir; ; {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return dir
		}
		d = parent
	}
}

func requireEmptyDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%s is not empty; the imported kustomization needs an empty directory", dir)
	}
	return nil
}

// commonDir returns the deepest directory under root that contains every
// file in root.
func commonDir(root string) (string, error) {
	common := ""
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		dir := filepath.Dir(path)
		if common == "" {
			common = dir
			return nil
		}
		for common != root && dir != common && !strings.HasPrefix(dir, common+string(filepath.Separator)) {
			common = filepath.Dir(common)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if common == "" {
		return "", fmt.Errorf("the localized copy in %s is empty", root)
	}
	return common, nil
}

func findKustomization(dir string) string {
	for _, name := range konfig.RecognizedKustomizationFileNames() {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return name
		}
	}
	return ""
}

func normalizeKustomizationName(dir string) error {
	name := findKustomization(dir)
	if name == "" || name == konfig.DefaultKustomizationFileName() {
		return nil
	}
	return os.Rename(filepath.Join(dir, name), filepath.Join(dir, konfig.DefaultKustomizationFileName()))
}

func loadKustomization(path string) (*types.Kustomization, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var k types.Kustomization
	if err := k.Unmarshal(data); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &k, nil
}

func declaresImages(path string) (bool, error) {
	k, err := loadKustomization(path)
	if err != nil {
		return false, err
	}
	return len(k.Images) > 0 || len(k.ImageTags) > 0, nil
}

// rejectHelm refuses a source whose kustomizations inflate Helm charts:
// render does not enable Helm, and localize leaves the charts remote.
func rejectHelm(root string) error {
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !isKustomizationFile(d.Name()) {
			return err
		}
		k, err := loadKustomization(path)
		if err != nil {
			return err
		}
		if len(k.HelmCharts) > 0 || len(k.HelmChartInflationGenerator) > 0 || k.HelmGlobals != nil {
			rel, _ := filepath.Rel(root, path)
			found = append(found, rel)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(found) > 0 {
		return fmt.Errorf("the source inflates Helm charts (%s), which installer render does not run; render the charts to YAML with `helm template` and reference that instead", strings.Join(found, ", "))
	}
	return nil
}

func isKustomizationFile(name string) bool {
	return slices.Contains(konfig.RecognizedKustomizationFileNames(), name)
}

// rejectSecretData refuses a source that produces Secrets carrying values,
// whether from secretGenerator or from Secret manifests: the files holding
// them would be copied into the package and published with it.
func rejectSecretData(m resmap.ResMap) error {
	var found []string
	for _, r := range m.Resources() {
		if r.GetKind() != "Secret" || r.GetGvk().Group != "" {
			continue
		}
		obj, err := r.Map()
		if err != nil {
			return err
		}
		data, _ := obj["data"].(map[string]any)
		stringData, _ := obj["stringData"].(map[string]any)
		if len(data) > 0 || len(stringData) > 0 {
			found = append(found, r.CurId().String())
		}
	}
	if len(found) > 0 {
		return fmt.Errorf("the source produces Secrets with data (%s), which would be published with the package; remove them from the source, and have a collector write *.env.secret files for a secretGenerator instead (see the author guide's spec.collector)", strings.Join(found, ", "))
	}
	return nil
}

func writeTopLevel(path, target string) error {
	body := fmt.Sprintf(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- %s
`, filepath.ToSlash(target))
	return os.WriteFile(path, []byte(body), 0o644)
}

// setImages writes images into the kustomization's images: block with the
// set-yq function, which leaves the rest of the file, comments included, as
// it was.
func setImages(path string, images []types.Image) error {
	if len(images) == 0 {
		return nil
	}
	list, err := json.Marshal(images)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	chain := &api.FunctionChain{Spec: api.FunctionChainSpec{Groups: []api.FunctionGroup{{
		Toolchain:   "AppConfig/YAML",
		Invocations: []api.FunctionInvocation{{Name: "set-yq", Args: []string{".images = " + string(list)}}},
	}}}}
	out, err := chainexec.RunChain(context.Background(), chain, data)
	if err != nil {
		return fmt.Errorf("set images in %s: %w", path, err)
	}
	return os.WriteFile(path, out, 0o644)
}

// build runs kustomize the way installer render does, with exec KRM
// functions enabled.
func build(dir string) (resmap.ResMap, error) {
	opts := krusty.MakeDefaultOptions()
	opts.PluginConfig = types.EnabledPluginConfig(types.BploUseStaticallyLinked)
	opts.PluginConfig.FnpLoadingOptions.EnableExec = true
	return krusty.MakeKustomizer(opts).Run(filesys.MakeFsOnDisk(), dir)
}

func sameOutput(want, got resmap.ResMap) error {
	w, err := want.AsYaml()
	if err != nil {
		return err
	}
	g, err := got.AsYaml()
	if err != nil {
		return err
	}
	if !bytes.Equal(w, g) {
		return fmt.Errorf("the imported copy does not build to the same output as the source; this can happen when the source uses KRM function or exec plugins, which kustomize localize does not copy")
	}
	return nil
}

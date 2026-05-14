package render

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/pkg/api"
)

// applyImageOverrides runs `kustomize edit set image <name>=<ref>` for
// each entry in overrides against the chosen base's
// kustomization.yaml. Mutates the package working copy in place; the
// next `installer pull` / `installer upgrade` re-fetches and starts
// clean, so the mutation is non-persistent at the package source
// level. Allowed by Principle 1's carve-out for the installer's own
// `--set-image` mutations.
//
// Pre-flight: the chosen base's kustomization.yaml must declare an
// `images:` block. If it does not, returns an error pointing the
// operator at the recommended alternatives. The check exists because
// `kustomize edit set image` would otherwise silently inject an
// `images:` block on a package whose author did not intend image
// overrides.
//
// No-op when overrides is empty.
func applyImageOverrides(loaded *ipkg.Loaded, sel *api.Selection, overrides map[string]string) error {
	if len(overrides) == 0 {
		return nil
	}
	baseRel, err := basePathForName(loaded.Package, sel.Spec.Base)
	if err != nil {
		return err
	}
	baseDir := filepath.Join(loaded.Root, baseRel)
	kustPath := filepath.Join(baseDir, "kustomization.yaml")

	if err := requireImagesBlock(kustPath); err != nil {
		return err
	}

	// Sort keys so the kustomize edit invocations (and any resulting
	// kustomization.yaml diff) are deterministic across runs.
	names := make([]string, 0, len(overrides))
	for n := range overrides {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		ref := overrides[name]
		cmd := exec.Command("kustomize", "edit", "set", "image", name+"="+ref)
		cmd.Dir = baseDir
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("kustomize edit set image %s=%s in %s: %w\n%s",
				name, ref, baseDir, err, stderr.String())
		}
	}
	return nil
}

// requireImagesBlock parses kustomization.yaml at path and returns an
// error if the top-level `images:` field is absent. Empty file or
// missing file is treated as "no images block" — render fails fast
// either way.
func requireImagesBlock(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("--set-image was given but %s does not exist; the chosen base must contain a kustomization.yaml that declares an `images:` block", path)
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if _, ok := doc["images"]; !ok {
		return fmt.Errorf(
			"%s has no `images:` block; declare one to use --set-image, "+
				"or have the package author declare image inputs and a "+
				"set-container-image group in functionChainTemplate (see "+
				"docs/principles.md Principle 5)", path)
	}
	return nil
}

package upload

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// AppConfig annotation keys — duplicated from internal/cli/appconfig.go to
// avoid pulling the CLI package into upload (which would invert the
// dependency direction). Keep the values in sync; both files document the
// same author contract.
const (
	annoToolchain = "installer.confighub.com/toolchain"
	annoMode      = "installer.confighub.com/appconfig-mode"
	annoSourceKey = "installer.confighub.com/appconfig-source-key"
	annoMutable   = "installer.confighub.com/appconfig-mutable"

	appConfigModeFile = "file"
	appConfigModeEnv  = "env"
)

// AppConfigManifest describes one annotated ConfigMap discovered in a
// rendered manifests directory. DetectAppConfigManifest fills it in;
// callers use it to drive renderer-Target + AppConfig-Unit creation and
// to know which files to skip in the normal Kubernetes/YAML upload path.
type AppConfigManifest struct {
	// ManifestPath is the absolute path to the rendered ConfigMap YAML.
	// The file itself is not uploaded as a Kubernetes/YAML Unit — the
	// renderer Target re-derives the ConfigMap at apply time.
	ManifestPath string
	// CarrierName is metadata.name of the rendered ConfigMap.
	CarrierName string
	// CarrierNamespace is metadata.namespace (may be empty for
	// cluster-scope ConfigMaps, but the kustomize default sets it).
	CarrierNamespace string
	// Toolchain is the value of installer.confighub.com/toolchain on
	// the ConfigMap (e.g., AppConfig/Properties, AppConfig/Env).
	Toolchain string
	// Mode is appconfig-mode (file|env). Set by the transformer's
	// pre-pass; this code reads it verbatim.
	Mode string
	// SourceKey is the data: key whose value is the raw file body
	// (file mode only). Empty in env mode.
	SourceKey string
	// Mutable reflects appconfig-mutable: when true, the renderer Target
	// gets RevisionHistoryLimit=0 so the rendered ConfigMap updates in
	// place (stable name, hash-annotation-driven workload rolling). When
	// false, RevisionHistoryLimit is left at the bridge default so each
	// content change produces a new, immutable ConfigMap revision.
	Mutable bool
	// Content is the raw AppConfig file body. file mode reads
	// data[SourceKey] verbatim; env mode emits a `.env`-shaped doc
	// from data: in sorted key order.
	Content []byte
}

// DetectAppConfigManifest reads path as a YAML document and returns an
// AppConfigManifest only when the document is a ConfigMap carrying
// installer.confighub.com/toolchain (with mode + source-key already
// injected by the transformer's pre-pass). Returns nil for every other
// manifest type so callers can branch with `if appCfg != nil`.
func DetectAppConfigManifest(path string) (*AppConfigManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var shape struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		Metadata   struct {
			Name        string            `yaml:"name"`
			Namespace   string            `yaml:"namespace"`
			Annotations map[string]string `yaml:"annotations"`
		} `yaml:"metadata"`
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(data, &shape); err != nil {
		// Not parseable as YAML — let the normal upload path surface that.
		return nil, nil
	}
	if shape.Kind != "ConfigMap" {
		return nil, nil
	}
	toolchain := shape.Metadata.Annotations[annoToolchain]
	if toolchain == "" {
		return nil, nil
	}
	if !strings.HasPrefix(toolchain, "AppConfig/") {
		return nil, fmt.Errorf("%s: %s=%q must be an AppConfig/* toolchain",
			path, annoToolchain, toolchain)
	}
	mode := shape.Metadata.Annotations[annoMode]
	if mode == "" {
		return nil, fmt.Errorf("%s: %s missing — the transformer should have injected it during render; re-run `installer render`",
			path, annoMode)
	}
	sourceKey := shape.Metadata.Annotations[annoSourceKey]
	mutable, err := parseMutable(shape.Metadata.Annotations[annoMutable])
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	m := &AppConfigManifest{
		ManifestPath:     path,
		CarrierName:      shape.Metadata.Name,
		CarrierNamespace: shape.Metadata.Namespace,
		Toolchain:        toolchain,
		Mode:             mode,
		SourceKey:        sourceKey,
		Mutable:          mutable,
	}
	switch mode {
	case appConfigModeFile:
		if sourceKey == "" {
			return nil, fmt.Errorf("%s: file mode requires %s annotation", path, annoSourceKey)
		}
		body, ok := shape.Data[sourceKey]
		if !ok {
			return nil, fmt.Errorf("%s: %s=%q references missing data: key", path, annoSourceKey, sourceKey)
		}
		m.Content = []byte(body)
	case appConfigModeEnv:
		keys := make([]string, 0, len(shape.Data))
		for k := range shape.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, k := range keys {
			fmt.Fprintf(&b, "%s=%s\n", k, shape.Data[k])
		}
		m.Content = []byte(b.String())
	default:
		return nil, fmt.Errorf("%s: %s=%q must be %q or %q", path, annoMode, mode, appConfigModeFile, appConfigModeEnv)
	}
	return m, nil
}

// UnitSlug returns the slug for the AppConfig Unit derived from the
// carrier ConfigMap's name. We append a stable suffix so the slug never
// collides with the (separate) Kubernetes Units rendered in the same
// Space.
func (m *AppConfigManifest) UnitSlug() string {
	return m.CarrierName + "-appconfig"
}

// TargetSlug returns the slug for the ConfigMapRenderer Target. One Target
// per AppConfig Unit, named for symmetry with UnitSlug.
func (m *AppConfigManifest) TargetSlug() string {
	return m.CarrierName + "-renderer"
}

// RendererOptions returns the value for a single `--option K=V` flag
// appropriate for this AppConfig manifest's ConfigMapRenderer Target.
// Multiple bridge options for one ConfigType are semicolon-joined in a
// single flag value — cub's `cub target create --option` is position-
// aligned (each --option flag corresponds to a ConfigType slot), so
// repeating --option N times would tell cub we want N ConfigTypes,
// which fails validation when only one --provider/--toolchain/--livestate-type
// triple is supplied. Returns "" when no options apply.
//
// AsKeyValue=true is set only when the carrier was generated from `envs:`
// AND the toolchain is AppConfig/Env (the bridge silently ignores it for
// other toolchains; we set it only where it's meaningful).
//
// RevisionHistoryLimit=0 is set when the carrier is mutable (kustomize
// did NOT append a hash suffix, indicating disableNameSuffixHash: true).
// Mutable ConfigMaps update in place and rely on a hash annotation on
// the workload to trigger rolling restarts; immutable ConfigMaps (the
// kustomize default) roll on every content change, so we leave
// RevisionHistoryLimit at the bridge default to retain a few revisions
// in cub for rollback.
func (m *AppConfigManifest) RendererOptions() string {
	var parts []string
	if m.Mode == appConfigModeEnv && m.Toolchain == "AppConfig/Env" {
		parts = append(parts, "AsKeyValue=true")
	}
	if m.Mutable {
		parts = append(parts, "RevisionHistoryLimit=0")
	}
	return strings.Join(parts, ";")
}

// parseMutable parses the appconfig-mutable annotation value. Missing →
// false (immutable, the kustomize default for configMapGenerator).
// "true"/"false" → straightforward. Anything else is rejected so a
// typo doesn't silently degrade to immutable.
func parseMutable(v string) (bool, error) {
	switch v {
	case "":
		return false, nil
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("%s=%q must be \"true\" or \"false\"", annoMutable, v)
	}
}

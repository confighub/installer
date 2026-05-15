package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/confighub/installer/internal/chainexec"
	"github.com/confighub/installer/pkg/api"
)

// kustomizeHashSuffix matches kustomize's configMapGenerator hash suffix:
// `-` followed by exactly 10 lowercase-alphanumeric chars at end-of-name.
// Anchored to the end so author-chosen names like `my-app-v1` aren't
// misdetected. When matched on the rendered ConfigMap, the carrier was
// generated with disableNameSuffixHash=false (kustomize default) →
// immutable mode → ConfigMap rolls on every content change. When not
// matched, the name is stable → mutable mode → ConfigMap is updated in
// place. Authors can override the inference by pre-setting
// installer.confighub.com/appconfig-mutable explicitly.
var kustomizeHashSuffix = regexp.MustCompile(`-[a-z0-9]{10}$`)

// AppConfig annotation keys. `toolchain` is the only one authors write by
// hand; mode and source-key are derived by the transformer from the carrier
// ConfigMap's data: shape (authors can pre-set them to override the
// inference).
const (
	annoToolchain = "installer.confighub.com/toolchain"
	annoMode      = "installer.confighub.com/appconfig-mode"
	annoSourceKey = "installer.confighub.com/appconfig-source-key"
	annoMutable   = "installer.confighub.com/appconfig-mutable"

	appConfigModeFile = "file"
	appConfigModeEnv  = "env"
)

// isAppConfigToolchain reports whether the given group toolchain identifies
// content stored in a ConfigMap carrier (vs. a top-level Kubernetes/YAML
// resource).
func isAppConfigToolchain(t string) bool {
	return strings.HasPrefix(t, "AppConfig/")
}

// configMapShape is the slice of a ConfigMap manifest the AppConfig
// round-trip needs. Other fields (binaryData, immutable, kustomize
// annotations like config.kubernetes.io/path) are carried through as
// `Extra` so we can re-emit a ConfigMap that differs from the input only in
// the fields we explicitly touched.
type configMapShape struct {
	APIVersion string            `yaml:"apiVersion"`
	Kind       string            `yaml:"kind"`
	Metadata   configMapMetadata `yaml:"metadata"`
	Data       map[string]string `yaml:"data,omitempty"`
	// Extra captures every other top-level field so we can round-trip
	// without dropping content we don't care about.
	Extra map[string]any `yaml:",inline"`
}

type configMapMetadata struct {
	Name        string            `yaml:"name"`
	Namespace   string            `yaml:"namespace,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty"`
	Annotations map[string]string `yaml:"annotations,omitempty"`
	Extra       map[string]any    `yaml:",inline"`
}

// appConfigCarrier wraps a configMapShape with the decoded AppConfig
// metadata derived from its annotations. Held briefly while we run a
// function over the content and write it back.
type appConfigCarrier struct {
	itemIndex int             // index into resourceList.Items
	shape     *configMapShape // decoded form (we re-encode after mutation)
	toolchain string
	mode      string
	sourceKey string // file mode only
}

// injectAppConfigAnnotations walks the ResourceList for ConfigMaps tagged
// with installer.confighub.com/toolchain. For each it derives the
// appconfig-mode (file/env, by inspecting data:) and, when in file mode,
// the appconfig-source-key (the single data: key, or the explicit override
// the author already wrote). The resolved annotations are re-injected onto
// the item so downstream consumers (the AppConfig round-trip below, the
// upload step) see one canonical contract.
//
// Returns an error for malformed inputs: unknown toolchain values, file
// mode with multiple data keys and no explicit source-key, env mode with
// keys that look like file paths.
func injectAppConfigAnnotations(list *resourceList) error {
	for i := range list.Items {
		shape, ok, err := decodeIfConfigMapWithToolchain(&list.Items[i])
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := validateAndFillAnnotations(shape); err != nil {
			return fmt.Errorf("ConfigMap %s/%s: %w", shape.Metadata.Namespace, shape.Metadata.Name, err)
		}
		if err := encodeShapeIntoItem(&list.Items[i], shape); err != nil {
			return err
		}
	}
	return nil
}

// decodeIfConfigMapWithToolchain returns the decoded shape only when the
// item is a ConfigMap carrying installer.confighub.com/toolchain. Cheap
// fast-path: we Decode into a tiny header first to filter, then re-decode
// the full shape only for matches.
func decodeIfConfigMapWithToolchain(item *yaml.Node) (*configMapShape, bool, error) {
	var header struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		Metadata   struct {
			Annotations map[string]string `yaml:"annotations"`
		} `yaml:"metadata"`
	}
	if err := item.Decode(&header); err != nil {
		// Items that don't decode as KRM-shaped resources (rare) are
		// not AppConfig carriers.
		return nil, false, nil
	}
	if header.Kind != "ConfigMap" || !strings.HasPrefix(header.APIVersion, "v1") && header.APIVersion != "v1" {
		return nil, false, nil
	}
	if _, ok := header.Metadata.Annotations[annoToolchain]; !ok {
		return nil, false, nil
	}
	var shape configMapShape
	if err := item.Decode(&shape); err != nil {
		return nil, false, fmt.Errorf("decode ConfigMap: %w", err)
	}
	return &shape, true, nil
}

func validateAndFillAnnotations(shape *configMapShape) error {
	if shape.Metadata.Annotations == nil {
		return fmt.Errorf("annotations dropped during decode") // shouldn't happen — toolchain was present
	}
	toolchain := shape.Metadata.Annotations[annoToolchain]
	if !isAppConfigToolchain(toolchain) {
		return fmt.Errorf("annotation %s=%q must be an AppConfig/* toolchain", annoToolchain, toolchain)
	}

	mode := shape.Metadata.Annotations[annoMode]
	if mode == "" {
		mode = inferMode(shape, toolchain)
	}
	if mode != appConfigModeFile && mode != appConfigModeEnv {
		return fmt.Errorf("annotation %s=%q must be %q or %q", annoMode, mode, appConfigModeFile, appConfigModeEnv)
	}
	shape.Metadata.Annotations[annoMode] = mode

	if mode == appConfigModeFile {
		key := shape.Metadata.Annotations[annoSourceKey]
		if key == "" {
			keys := sortedDataKeys(shape)
			if len(keys) != 1 {
				return fmt.Errorf("file mode requires exactly one data: key or an explicit %s annotation (have %d keys: %v)",
					annoSourceKey, len(keys), keys)
			}
			key = keys[0]
			shape.Metadata.Annotations[annoSourceKey] = key
		}
		if _, ok := shape.Data[key]; !ok {
			return fmt.Errorf("%s=%q references a data key that doesn't exist (keys: %v)",
				annoSourceKey, key, sortedDataKeys(shape))
		}
	} else {
		// env mode shouldn't carry a source-key annotation. Tolerate but
		// strip it so downstream code doesn't see contradictory state.
		delete(shape.Metadata.Annotations, annoSourceKey)
	}

	// Mutability is inferred from kustomize's hash-suffix convention on
	// the rendered name unless the author pre-set the annotation. The
	// inference works because kustomize appends `-<10-char hash>` exactly
	// when disableNameSuffixHash is false (the generator default).
	if _, set := shape.Metadata.Annotations[annoMutable]; !set {
		if kustomizeHashSuffix.MatchString(shape.Metadata.Name) {
			shape.Metadata.Annotations[annoMutable] = "false"
		} else {
			shape.Metadata.Annotations[annoMutable] = "true"
		}
	}
	return nil
}

// inferMode picks env when the toolchain is AppConfig/Env and the data:
// shape doesn't look like a single file blob. For non-Env toolchains we
// always pick file (the carrier is meant to hold a file body).
func inferMode(shape *configMapShape, toolchain string) string {
	if toolchain != "AppConfig/Env" {
		return appConfigModeFile
	}
	// AppConfig/Env normally maps to env mode (data keys are env-var
	// names). But if there's a single data key whose value looks
	// multi-line (i.e. someone stored a .env *file body* under a single
	// key via files: rather than envs:), it's file mode.
	keys := sortedDataKeys(shape)
	if len(keys) == 1 && strings.ContainsRune(shape.Data[keys[0]], '\n') {
		return appConfigModeFile
	}
	return appConfigModeEnv
}

func sortedDataKeys(shape *configMapShape) []string {
	keys := make([]string, 0, len(shape.Data))
	for k := range shape.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// runAppConfigGroup handles one FunctionGroup whose toolchain is
// AppConfig/*. Each matching ConfigMap is round-tripped independently
// through chainexec (extract content from data: → run function group →
// write mutated content back). The `mutating` flag selects between
// RunChain (chain mode) and RunValidators (validators mode); in validators
// mode the failures are appended to `results` annotated with the carrier
// ConfigMap's identity.
//
// Carrier-level whereResource filtering (beyond toolchain-annotation
// match) is deferred — the function executor's own WhereResource is
// passed through to the per-content invocation, so functions that
// support intra-content path filters still work.
func runAppConfigGroup(
	ctx context.Context,
	list *resourceList,
	group api.FunctionGroup,
	mutating bool,
	results *[]krmResult,
) error {
	for i := range list.Items {
		shape, ok, err := decodeIfConfigMapWithToolchain(&list.Items[i])
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if shape.Metadata.Annotations[annoToolchain] != group.Toolchain {
			continue
		}
		carrier := &appConfigCarrier{
			itemIndex: i,
			shape:     shape,
			toolchain: shape.Metadata.Annotations[annoToolchain],
			mode:      shape.Metadata.Annotations[annoMode],
			sourceKey: shape.Metadata.Annotations[annoSourceKey],
		}
		content, err := extractAppConfigContent(carrier)
		if err != nil {
			return fmt.Errorf("ConfigMap %s/%s: %w", shape.Metadata.Namespace, shape.Metadata.Name, err)
		}
		if mutating {
			mutated, err := chainexec.RunChain(ctx, &api.FunctionChain{
				APIVersion: api.APIVersion,
				Kind:       api.KindFunctionChain,
				Spec:       api.FunctionChainSpec{Groups: []api.FunctionGroup{group}},
			}, content)
			if err != nil {
				return fmt.Errorf("ConfigMap %s/%s: %w", shape.Metadata.Namespace, shape.Metadata.Name, err)
			}
			if err := writeAppConfigContent(carrier, mutated); err != nil {
				return fmt.Errorf("ConfigMap %s/%s: %w", shape.Metadata.Namespace, shape.Metadata.Name, err)
			}
			if err := encodeShapeIntoItem(&list.Items[i], shape); err != nil {
				return err
			}
		} else {
			failures, err := chainexec.RunValidators(ctx, []api.FunctionGroup{group}, content)
			if err != nil {
				return fmt.Errorf("ConfigMap %s/%s: %w", shape.Metadata.Namespace, shape.Metadata.Name, err)
			}
			carrierName := configMapDisplayName(shape)
			for _, f := range failures {
				prefix := f.FunctionName
				if prefix == "" {
					prefix = group.Toolchain
				}
				prefix = carrierName + "/" + prefix
				if len(f.Details) == 0 {
					*results = append(*results, krmResult{
						Message:  fmt.Sprintf("%s: failed", prefix),
						Severity: "error",
					})
					continue
				}
				for _, d := range f.Details {
					*results = append(*results, krmResult{
						Message:  fmt.Sprintf("%s: %s", prefix, d),
						Severity: "error",
					})
				}
			}
		}
	}
	return nil
}

// extractAppConfigContent pulls the raw AppConfig file body out of the
// carrier ConfigMap's data: map. file mode reads data[sourceKey]
// verbatim; env mode reconstructs a .env-shaped doc by joining the
// key/value pairs (sorted for determinism).
func extractAppConfigContent(c *appConfigCarrier) ([]byte, error) {
	switch c.mode {
	case appConfigModeFile:
		body, ok := c.shape.Data[c.sourceKey]
		if !ok {
			return nil, fmt.Errorf("data[%q] missing", c.sourceKey)
		}
		return []byte(body), nil
	case appConfigModeEnv:
		var buf bytes.Buffer
		for _, k := range sortedDataKeys(c.shape) {
			fmt.Fprintf(&buf, "%s=%s\n", k, c.shape.Data[k])
		}
		return buf.Bytes(), nil
	default:
		return nil, fmt.Errorf("unknown appconfig-mode %q", c.mode)
	}
}

// writeAppConfigContent is extractAppConfigContent's inverse. file mode
// overwrites data[sourceKey]; env mode parses the mutated .env-shaped
// content back into key/value pairs and replaces data: wholesale. Lines
// starting with '#' or blank are ignored on the env-parse side so
// authors / functions can include comments without breaking parity.
func writeAppConfigContent(c *appConfigCarrier, content []byte) error {
	switch c.mode {
	case appConfigModeFile:
		if c.shape.Data == nil {
			c.shape.Data = map[string]string{}
		}
		c.shape.Data[c.sourceKey] = string(content)
		return nil
	case appConfigModeEnv:
		next := map[string]string{}
		scanner := bufio.NewScanner(bytes.NewReader(content))
		for scanner.Scan() {
			line := scanner.Text()
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			eq := strings.IndexByte(line, '=')
			if eq < 0 {
				return fmt.Errorf("env-mode AppConfig line lacks '=': %q", line)
			}
			next[strings.TrimSpace(line[:eq])] = line[eq+1:]
		}
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("scan env content: %w", err)
		}
		c.shape.Data = next
		return nil
	default:
		return fmt.Errorf("unknown appconfig-mode %q", c.mode)
	}
}

// encodeShapeIntoItem marshals the shape back into the original item's
// yaml.Node slot. We re-Unmarshal so the resulting node carries the right
// internal scalar styles for the encoder downstream.
func encodeShapeIntoItem(item *yaml.Node, shape *configMapShape) error {
	body, err := yaml.Marshal(shape)
	if err != nil {
		return fmt.Errorf("marshal ConfigMap: %w", err)
	}
	var node yaml.Node
	if err := yaml.Unmarshal(body, &node); err != nil {
		return fmt.Errorf("re-parse ConfigMap: %w", err)
	}
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		*item = *node.Content[0]
	} else {
		*item = node
	}
	return nil
}

func configMapDisplayName(shape *configMapShape) string {
	if shape.Metadata.Namespace != "" {
		return shape.Metadata.Namespace + "/" + shape.Metadata.Name
	}
	return shape.Metadata.Name
}

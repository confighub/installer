package render

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/confighub/installer/internal/chainexec"
	"github.com/confighub/installer/pkg/api"
)

// runValidatorsAfterKustomize executes the resolved validator chain
// against the rendered output. Each group is dispatched by toolchain:
//
//   - Kubernetes/YAML (and anything not under AppConfig/*) — runs against
//     the full multi-doc stream in one chainexec.RunValidators call,
//     same as the in-kustomize K8s/YAML validator path did before.
//   - AppConfig/* — for each ConfigMap in the stream carrying
//     installer.confighub.com/toolchain matching the group's toolchain,
//     extract the raw AppConfig file body and validate that. Carriers
//     without the annotation, or with a different toolchain, are
//     skipped.
//
// On any severity-error failure across any group, returns a wrapped
// chainexec.FormatValidatorFailures-formatted message so Render fails.
func runValidatorsAfterKustomize(ctx context.Context, groups []api.FunctionGroup, rendered []byte) error {
	var all []chainexec.ValidatorFailure
	for i, group := range groups {
		var (
			failures []chainexec.ValidatorFailure
			err      error
		)
		if strings.HasPrefix(group.Toolchain, "AppConfig/") {
			failures, err = runAppConfigValidatorGroup(ctx, group, i, rendered)
		} else {
			failures, err = chainexec.RunValidators(ctx, []api.FunctionGroup{group}, rendered)
		}
		if err != nil {
			return err
		}
		all = append(all, failures...)
	}
	if len(all) > 0 {
		return fmt.Errorf("validation failed:\n%s", chainexec.FormatValidatorFailures(all))
	}
	return nil
}

// runAppConfigValidatorGroup walks the rendered manifest stream for
// ConfigMaps tagged with installer.confighub.com/toolchain matching
// group.Toolchain, extracts the raw AppConfig file body from each, and
// runs the group's invocations against that body. Each carrier becomes
// one chainexec.RunValidators call; failures are tagged with the
// carrier's namespace/name so error messages point at the right
// ConfigMap.
//
// AppConfig annotation injection (mode, source-key, mutable) already
// happened in the in-kustomize transformer pass, so we read the
// resolved annotations directly off each carrier here.
func runAppConfigValidatorGroup(ctx context.Context, group api.FunctionGroup, groupIdx int, rendered []byte) ([]chainexec.ValidatorFailure, error) {
	dec := yaml.NewDecoder(bytes.NewReader(rendered))
	var out []chainexec.ValidatorFailure
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if err != nil {
			break
		}
		shape, ok, err := decodeIfAppConfigConfigMap(&doc, group.Toolchain)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		content, err := extractAppConfigBody(shape)
		if err != nil {
			return nil, fmt.Errorf("ConfigMap %s/%s: %w", shape.Metadata.Namespace, shape.Metadata.Name, err)
		}
		failures, err := chainexec.RunValidators(ctx, []api.FunctionGroup{group}, content)
		if err != nil {
			return nil, fmt.Errorf("ConfigMap %s/%s: %w", shape.Metadata.Namespace, shape.Metadata.Name, err)
		}
		carrierName := shape.Metadata.Name
		if shape.Metadata.Namespace != "" {
			carrierName = shape.Metadata.Namespace + "/" + carrierName
		}
		for _, f := range failures {
			if f.UnitSlug == "" {
				f.UnitSlug = carrierName
			}
			out = append(out, f)
		}
		_ = groupIdx
	}
	return out, nil
}

// appConfigCarrierShape is the slice of a rendered ConfigMap we need to
// validate AppConfig content. Mirrors internal/cli's configMapShape; kept
// duplicated to avoid pulling the CLI package into render (the import
// direction is render → cli, so the reverse would be a cycle).
type appConfigCarrierShape struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name        string            `yaml:"name"`
		Namespace   string            `yaml:"namespace"`
		Annotations map[string]string `yaml:"annotations"`
	} `yaml:"metadata"`
	Data map[string]string `yaml:"data"`
}

func decodeIfAppConfigConfigMap(doc *yaml.Node, wantToolchain string) (*appConfigCarrierShape, bool, error) {
	var shape appConfigCarrierShape
	if err := doc.Decode(&shape); err != nil {
		return nil, false, nil
	}
	if shape.Kind != "ConfigMap" {
		return nil, false, nil
	}
	if shape.Metadata.Annotations["installer.confighub.com/toolchain"] != wantToolchain {
		return nil, false, nil
	}
	return &shape, true, nil
}

// extractAppConfigBody mirrors internal/cli/appconfig.go's
// extractAppConfigContent for the rendered (post-transformer) shape.
// The transformer pre-pass has already injected
// installer.confighub.com/appconfig-mode (and appconfig-source-key in
// file mode), so we trust the annotations here and surface a clear
// error if either is missing.
func extractAppConfigBody(shape *appConfigCarrierShape) ([]byte, error) {
	mode := shape.Metadata.Annotations["installer.confighub.com/appconfig-mode"]
	switch mode {
	case "file":
		key := shape.Metadata.Annotations["installer.confighub.com/appconfig-source-key"]
		if key == "" {
			return nil, fmt.Errorf("file mode missing installer.confighub.com/appconfig-source-key")
		}
		body, ok := shape.Data[key]
		if !ok {
			return nil, fmt.Errorf("data[%q] not present", key)
		}
		return []byte(body), nil
	case "env":
		keys := make([]string, 0, len(shape.Data))
		for k := range shape.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var buf bytes.Buffer
		for _, k := range keys {
			fmt.Fprintf(&buf, "%s=%s\n", k, shape.Data[k])
		}
		return buf.Bytes(), nil
	default:
		return nil, fmt.Errorf("unknown installer.confighub.com/appconfig-mode %q", mode)
	}
}

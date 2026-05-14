// Package collector runs a package's bundled fact-collection script during
// the wizard step. The script discovers install-time facts that depend on
// runtime state — e.g., the active cub context's server URL, a freshly
// fetched/created BridgeWorkerID, an image tag derived from the server's
// version — and emits them as a YAML map on stdout.
//
// The collector may also produce .env.secret files inside the package
// working copy at paths the package's kustomize secretGenerator references.
// Those files are sensitive material and never read or uploaded by the
// installer: they live in the kustomize tree, get baked into a Secret
// resource by `kustomize build`, and that resource is routed to out/secrets/.
package collector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/confighub/installer/pkg/api"
	"gopkg.in/yaml.v3"
)

// Inputs feeds the collector both as env vars and (via stdout YAML) the
// resolved Facts.
type Inputs struct {
	// PackageDir is the absolute path to the package working copy.
	PackageDir string
	// WorkDir is the absolute path to the parent working directory
	// (PackageDir's parent).
	WorkDir string
	// Namespace is the value of `installer wizard --namespace`.
	Namespace string
	// Base is the selected base name.
	Base string
	// SelectedComponents is the closure-resolved component list.
	SelectedComponents []string
	// InputValues are the user's typed input answers (key → value).
	InputValues map[string]any
}

// Run executes pkg.Spec.Collector with the given Inputs and parses stdout as
// a YAML map of facts. Returns (nil, nil) when the package has no collector.
//
// The collector inherits the parent environment (so `cub`, kubectl auth, etc.
// keep working) plus the INSTALLER_* env vars documented on api.Collector.
// Stderr is forwarded verbatim to the caller's stderr.
func Run(ctx context.Context, pkg *api.Package, in Inputs) (map[string]any, error) {
	if pkg.Spec.Collector == nil || pkg.Spec.Collector.Command == "" {
		return nil, nil
	}
	c := pkg.Spec.Collector

	cmdPath := c.Command
	if !filepath.IsAbs(cmdPath) {
		cmdPath = filepath.Join(in.PackageDir, cmdPath)
	}

	cmd := exec.CommandContext(ctx, cmdPath, c.Args...)
	cmd.Dir = in.PackageDir
	cmd.Env = append(os.Environ(),
		"INSTALLER_PACKAGE_DIR="+in.PackageDir,
		"INSTALLER_WORK_DIR="+in.WorkDir,
		"INSTALLER_OUT_DIR="+filepath.Join(in.WorkDir, "out"),
		"INSTALLER_NAMESPACE="+in.Namespace,
		"INSTALLER_BASE="+in.Base,
		"INSTALLER_SELECTED="+strings.Join(in.SelectedComponents, ","),
	)
	// One INSTALLER_INPUT_<NAME> per input. Iterate deterministically so
	// failure messages are reproducible.
	keys := make([]string, 0, len(in.InputValues))
	for k := range in.InputValues {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cmd.Env = append(cmd.Env, "INSTALLER_INPUT_"+strings.ToUpper(k)+"="+fmt.Sprint(in.InputValues[k]))
	}

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("collector %s exited %d", cmdPath, exitErr.ExitCode())
		}
		return nil, fmt.Errorf("collector %s: %w", cmdPath, err)
	}

	raw := bytes.TrimSpace(stdout.Bytes())
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var facts map[string]any
	if err := yaml.Unmarshal(raw, &facts); err != nil {
		return nil, fmt.Errorf("parse collector stdout as YAML map: %w\n----\n%s\n----", err, raw)
	}
	if facts == nil {
		facts = map[string]any{}
	}
	return facts, nil
}

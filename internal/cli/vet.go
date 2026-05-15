package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/internal/render"
	"github.com/confighub/installer/pkg/api"
)

func newVetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vet <work-dir>",
		Short: "Run the package's validators against the existing rendered output",
		Long: `Vet runs the package's spec.validators chain against the manifests
already in <work-dir>/out/manifests/ (and out/<dep>/manifests/ for
multi-package installs), without re-rendering. Useful when the
package author edits the validator list and wants to check the
existing render without re-running kustomize + the function chain.

Validators are also auto-invoked at the end of every 'installer
render' — vet only matters when the validator list itself changes.

Validators are templated against the same context as
transformers ({{ .Inputs.* }}, {{ .Selection.* }}, etc.),
so a validator may reference an input. The wizard's persisted
out/spec/{inputs,selection,facts}.yaml is the source of those
values. If those files are missing, vet fails fast (run 'installer
wizard' first).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workDir, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			loaded, err := ipkg.Load(filepath.Join(workDir, "package"))
			if err != nil {
				return fmt.Errorf("load package: %w", err)
			}
			specDir := filepath.Join(workDir, "out", "spec")
			sel, err := readSelection(filepath.Join(specDir, "selection.yaml"))
			if err != nil {
				return fmt.Errorf("read selection.yaml: %w (run `installer wizard` first)", err)
			}
			inputs, err := readInputs(filepath.Join(specDir, "inputs.yaml"))
			if err != nil {
				return fmt.Errorf("read inputs.yaml: %w (run `installer wizard` first)", err)
			}
			facts, err := readFactsOptional(filepath.Join(specDir, "facts.yaml"))
			if err != nil {
				return err
			}

			if len(loaded.Package.Spec.Validators) == 0 {
				fmt.Println("Package declares no validators (spec.validators is empty).")
				return nil
			}

			manifestsDir := filepath.Join(workDir, "out", "manifests")
			body, err := concatRenderedManifests(manifestsDir)
			if err != nil {
				return err
			}
			if len(body) == 0 {
				return fmt.Errorf("no manifests found in %s — run `installer render` first", manifestsDir)
			}

			fmt.Printf("Vetting %d resource(s) under %s against %d validator group(s)...\n",
				countDocs(body), manifestsDir, len(loaded.Package.Spec.Validators))

			failures, err := render.RunValidators(ctx, loaded.Package, sel, inputs, facts, body)
			if err != nil {
				return err
			}
			if len(failures) > 0 {
				return fmt.Errorf("validation failed:\n%s", render.FormatValidatorFailures(failures))
			}
			fmt.Println("All validators passed.")
			return nil
		},
	}
	return cmd
}

// concatRenderedManifests reads every .yaml/.yml file in dir (one
// level deep) and returns the concatenated multi-doc YAML stream
// suitable for passing to validators. Files are sorted by name so the
// output is deterministic. Excludes any subdirs (deps render into
// sibling dirs, vetted separately).
func concatRenderedManifests(dir string) ([]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasSuffix(n, ".yaml") && !strings.HasSuffix(n, ".yml") {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	var buf []byte
	for i, n := range names {
		data, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return nil, err
		}
		if i > 0 {
			buf = append(buf, []byte("---\n")...)
		}
		buf = append(buf, data...)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			buf = append(buf, '\n')
		}
	}
	return buf, nil
}

// countDocs estimates the number of YAML docs in a multi-doc stream
// by counting leading-on-line `---` separators plus one. Cheap; just
// for the user-facing progress line.
func countDocs(body []byte) int {
	n := 1
	for _, line := range strings.Split(string(body), "\n") {
		if strings.TrimSpace(line) == "---" {
			n++
		}
	}
	return n
}

// silence "imported and not used" in case the api package isn't
// referenced — keep the import explicit so future additions don't
// have to re-import.
var _ = api.KindPackage

package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/confighub/installer/pkg/api"
)

// defaultValidators is the validator chain `installer init` seeds new
// packages with. vet-placeholders is intentionally NOT in the list —
// some packages exist precisely to ship `confighubplaceholder`-bearing
// bases that get cloned into variants.
var defaultValidators = []api.FunctionGroup{{
	Toolchain:   "Kubernetes/YAML",
	Description: "Default validators applied at the end of every render. See docs/author-guide.md (spec.validators).",
	Invocations: []api.FunctionInvocation{
		{Name: "vet-schemas"},
		{Name: "vet-merge-keys"},
		{Name: "vet-format"},
	},
}}

func newInitCmd() *cobra.Command {
	var (
		name    string
		version string
		force   bool
	)
	cmd := &cobra.Command{
		Use:   "init <dir>",
		Short: "Scaffold a new installer package in <dir>",
		Long: `Init creates the on-disk skeleton of a new installer package:

  <dir>/
  ├── installer.yaml           # the manifest with default validators
  ├── bases/
  │   └── default/
  │       └── kustomization.yaml
  ├── components/
  └── validation/

The seeded installer.yaml declares one default base, no components,
and the recommended validator chain (vet-schemas, vet-merge-keys,
vet-format). vet-placeholders is intentionally omitted — packages
that ship cloneable bases would fail it. Add or remove validators
later with 'installer edit validator add/remove' (when shipped) or
by hand-editing installer.yaml.

Refuses to overwrite an existing installer.yaml unless --force.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			if name == "" {
				name = filepath.Base(dir)
			}
			pkgPath := filepath.Join(dir, "installer.yaml")
			if _, err := os.Stat(pkgPath); err == nil && !force {
				return fmt.Errorf("%s already exists; use --force to overwrite", pkgPath)
			}
			if err := os.MkdirAll(filepath.Join(dir, "bases", "default"), 0o755); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Join(dir, "components"), 0o755); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Join(dir, "validation"), 0o755); err != nil {
				return err
			}

			pkg := &api.Package{
				APIVersion: api.APIVersion,
				Kind:       api.KindPackage,
				Metadata: api.Metadata{
					Name:    name,
					Version: version,
				},
				Spec: api.PackageSpec{
					Bases: []api.Base{{
						Name:        "default",
						Path:        "bases/default",
						Default:     true,
						Description: "Default base — replace with your package's resources.",
					}},
					Transformers: []api.FunctionGroup{{
						Toolchain:   "Kubernetes/YAML",
						Description: "Set the namespace on every namespaced resource.",
						Invocations: []api.FunctionInvocation{
							{Name: "set-namespace", Args: []string{"{{ .Namespace }}"}},
						},
					}},
					Validators: defaultValidators,
				},
			}
			body, err := api.MarshalYAML(pkg)
			if err != nil {
				return err
			}
			if err := os.WriteFile(pkgPath, body, 0o644); err != nil {
				return err
			}

			// Seed an empty kustomization.yaml so `installer render`
			// works against the empty base. The author replaces this
			// with their actual resources.
			kustPath := filepath.Join(dir, "bases", "default", "kustomization.yaml")
			if _, err := os.Stat(kustPath); os.IsNotExist(err) {
				kust := []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: []
# images: declares the names overridable by --set-image at install
# or upgrade time. Add an entry per workload image so operators can
# bump tags without editing your source. See docs/author-guide.md
# (best practices).
# images:
#   - name: myorg/myapp
#     newName: myorg/myapp
#     newTag: v0.1.0
`)
				if err := os.WriteFile(kustPath, kust, 0o644); err != nil {
					return err
				}
			}

			fmt.Printf("Initialized package %q at %s\n", name, dir)
			fmt.Printf("  - %s\n", pkgPath)
			fmt.Printf("  - %s/\n", filepath.Join(dir, "bases", "default"))
			fmt.Printf("  - %s/\n", filepath.Join(dir, "components"))
			fmt.Printf("  - %s/\n", filepath.Join(dir, "validation"))
			fmt.Println()
			fmt.Println("Next: drop your kustomize resources under bases/default/, then run")
			fmt.Printf("  %s wizard %s --work-dir /tmp/dev --non-interactive --namespace demo\n", InvocationName(), dir)
			fmt.Printf("  %s render /tmp/dev\n", InvocationName())
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "package metadata.name (default: basename of <dir>)")
	cmd.Flags().StringVar(&version, "version", "0.1.0", "package metadata.version (SemVer)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing installer.yaml")
	return cmd
}

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/confighub/installer/pkg/api"
)

// `installer edit` lets package authors mutate installer.yaml fields
// without hand-editing YAML. Mirrors `kustomize edit add/remove/set`
// shape for inputs / components / dependencies.
//
// Scope (v1):
//   installer edit add input <name> --type T [--default V] [--required]
//                                   [--prompt P] [--description D]
//                                   [--option O ...]
//   installer edit remove input <name>
//   installer edit set input <name> [same flags as add]
//
//   installer edit add component <name> --path P [--default] [--description D]
//                                       [--requires NAME ...] [--conflicts NAME ...]
//                                       [--valid-for-bases NAME ...]
//   installer edit remove component <name>
//   installer edit set component <name> [same flags as add]
//
//   installer edit add dependency <name> --package OCI --version SEMVER
//                                        [--when-component NAME]
//                                        [--optional]
//   installer edit remove dependency <name>
//   installer edit set dependency <name> [same flags as add]
//
// Every leaf reads installer.yaml from --package (default ".") and
// writes it back through api.MarshalYAML for deterministic output.

func newEditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit",
		Short: "Edit installer.yaml fields (inputs, components, dependencies)",
		Long: `Edit walks installer.yaml field-by-field, like kustomize edit.

Subcommands:
  installer edit add input    <name> ...   add an input declaration
  installer edit remove input <name>       remove an input
  installer edit set input    <name> ...   update an existing input

  installer edit add component    <name> ...   add an opt-in component
  installer edit remove component <name>       remove a component
  installer edit set component    <name> ...   update an existing component

  installer edit add dependency    <name> ...   add a dependency
  installer edit remove dependency <name>       remove a dependency
  installer edit set dependency    <name> ...   update an existing dependency

Each leaf reads installer.yaml from --package (default ".") and
writes it back through the deterministic MarshalYAML.`,
	}
	cmd.AddCommand(newEditAddCmd(), newEditRemoveCmd(), newEditSetCmd())
	return cmd
}

func newEditAddCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "add", Short: "Add an input / component / dependency"}
	cmd.AddCommand(
		newEditAddInputCmd(),
		newEditAddComponentCmd(),
		newEditAddDependencyCmd(),
	)
	return cmd
}

func newEditRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "remove", Short: "Remove an input / component / dependency"}
	cmd.AddCommand(
		newEditRemoveCmdFor("input", removeInput),
		newEditRemoveCmdFor("component", removeComponent),
		newEditRemoveCmdFor("dependency", removeDependency),
	)
	return cmd
}

func newEditSetCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "set", Short: "Update an existing input / component / dependency"}
	cmd.AddCommand(
		newEditSetInputCmd(),
		newEditSetComponentCmd(),
		newEditSetDependencyCmd(),
	)
	return cmd
}

// --- input -----------------------------------------------------------------

type inputFlags struct {
	pkgDir      string
	typeName    string
	def         string
	required    bool
	prompt      string
	description string
	options     []string
	whenExtReq  string
	cleared     map[string]bool // set to true to explicitly clear a field on `set`
}

func bindInputFlags(cmd *cobra.Command, f *inputFlags) {
	cmd.Flags().StringVar(&f.pkgDir, "package", ".", "package directory containing installer.yaml")
	cmd.Flags().StringVar(&f.typeName, "type", "", "input type: string | int | bool | enum | list")
	cmd.Flags().StringVar(&f.def, "default", "", "default value (string form; coerced at render to --type)")
	cmd.Flags().BoolVar(&f.required, "required", false, "input must be answered")
	cmd.Flags().StringVar(&f.prompt, "prompt", "", "human-readable wizard prompt")
	cmd.Flags().StringVar(&f.description, "description", "", "longer help text")
	cmd.Flags().StringSliceVar(&f.options, "option", nil, "valid option (repeatable, for type=enum)")
	cmd.Flags().StringVar(&f.whenExtReq, "when-external-require", "", "only prompt when the package declares an externalRequire of this kind")
}

func newEditAddInputCmd() *cobra.Command {
	var f inputFlags
	cmd := &cobra.Command{
		Use:   "input <name>",
		Short: "Add an input declaration to installer.yaml",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if f.typeName == "" {
				return fmt.Errorf("--type is required")
			}
			return mutatePackage(f.pkgDir, func(p *api.Package) error {
				if findInputIdx(p, args[0]) >= 0 {
					return fmt.Errorf("input %q already exists; use `edit set input` to modify", args[0])
				}
				in, err := buildInput(args[0], f, true /*required-fields-must-be-set*/)
				if err != nil {
					return err
				}
				p.Spec.Inputs = append(p.Spec.Inputs, *in)
				return nil
			})
		},
	}
	bindInputFlags(cmd, &f)
	return cmd
}

func newEditSetInputCmd() *cobra.Command {
	var f inputFlags
	cmd := &cobra.Command{
		Use:   "input <name>",
		Short: "Update an existing input declaration",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return mutatePackage(f.pkgDir, func(p *api.Package) error {
				idx := findInputIdx(p, args[0])
				if idx < 0 {
					return fmt.Errorf("input %q not found", args[0])
				}
				ex := &p.Spec.Inputs[idx]
				if c.Flags().Changed("type") {
					ex.Type = f.typeName
				}
				if c.Flags().Changed("default") {
					coerced, err := coerceDefault(ex.Type, f.def)
					if err != nil {
						return err
					}
					ex.Default = coerced
				}
				if c.Flags().Changed("required") {
					ex.Required = f.required
				}
				if c.Flags().Changed("prompt") {
					ex.Prompt = f.prompt
				}
				if c.Flags().Changed("description") {
					ex.Description = f.description
				}
				if c.Flags().Changed("option") {
					ex.Options = f.options
				}
				if c.Flags().Changed("when-external-require") {
					ex.WhenExternalRequire = api.ExternalRequireKind(f.whenExtReq)
				}
				return nil
			})
		},
	}
	bindInputFlags(cmd, &f)
	return cmd
}

func buildInput(name string, f inputFlags, requireType bool) (*api.Input, error) {
	in := &api.Input{
		Name:                name,
		Type:                f.typeName,
		Required:            f.required,
		Prompt:              f.prompt,
		Description:         f.description,
		Options:             f.options,
		WhenExternalRequire: api.ExternalRequireKind(f.whenExtReq),
	}
	if requireType && in.Type == "" {
		return nil, fmt.Errorf("--type is required")
	}
	if f.def != "" {
		v, err := coerceDefault(in.Type, f.def)
		if err != nil {
			return nil, err
		}
		in.Default = v
	}
	if in.Type == "enum" && len(in.Options) == 0 {
		return nil, fmt.Errorf("type=enum requires --option (repeatable)")
	}
	return in, nil
}

// coerceDefault converts the --default flag string into the typed
// value Go YAML expects, mirroring the wizard's coerce() at install
// time so the on-disk default and the install-time value have the
// same shape.
func coerceDefault(typeName, raw string) (any, error) {
	switch typeName {
	case "", "string", "enum":
		return raw, nil
	case "int":
		return strconv.Atoi(raw)
	case "bool":
		return strconv.ParseBool(raw)
	case "list":
		parts := strings.Split(raw, ",")
		out := make([]any, 0, len(parts))
		for _, p := range parts {
			out = append(out, strings.TrimSpace(p))
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported input type %q", typeName)
	}
}

func findInputIdx(p *api.Package, name string) int {
	for i, in := range p.Spec.Inputs {
		if in.Name == name {
			return i
		}
	}
	return -1
}

func removeInput(p *api.Package, name string) error {
	idx := findInputIdx(p, name)
	if idx < 0 {
		return fmt.Errorf("input %q not found", name)
	}
	p.Spec.Inputs = append(p.Spec.Inputs[:idx], p.Spec.Inputs[idx+1:]...)
	return nil
}

// --- component -------------------------------------------------------------

type componentFlags struct {
	pkgDir         string
	path           string
	dflt           bool
	description    string
	requires       []string
	conflicts      []string
	validForBases  []string
}

func bindComponentFlags(cmd *cobra.Command, f *componentFlags) {
	cmd.Flags().StringVar(&f.pkgDir, "package", ".", "package directory containing installer.yaml")
	cmd.Flags().StringVar(&f.path, "path", "", "path to the component directory (relative to package root)")
	cmd.Flags().BoolVar(&f.dflt, "default", false, "include in the `default` preset")
	cmd.Flags().StringVar(&f.description, "description", "", "operator-facing description")
	cmd.Flags().StringSliceVar(&f.requires, "requires", nil, "other component(s) this requires (repeatable)")
	cmd.Flags().StringSliceVar(&f.conflicts, "conflicts", nil, "other component(s) this conflicts with (repeatable)")
	cmd.Flags().StringSliceVar(&f.validForBases, "valid-for-bases", nil, "bases this component is compatible with (repeatable; empty = all)")
}

func newEditAddComponentCmd() *cobra.Command {
	var f componentFlags
	cmd := &cobra.Command{
		Use:   "component <name>",
		Short: "Add an opt-in component to installer.yaml",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if f.path == "" {
				return fmt.Errorf("--path is required")
			}
			return mutatePackage(f.pkgDir, func(p *api.Package) error {
				if findComponentIdx(p, args[0]) >= 0 {
					return fmt.Errorf("component %q already exists; use `edit set component` to modify", args[0])
				}
				p.Spec.Components = append(p.Spec.Components, api.Component{
					Name:          args[0],
					Path:          f.path,
					Default:       f.dflt,
					Description:   f.description,
					Requires:      f.requires,
					Conflicts:     f.conflicts,
					ValidForBases: f.validForBases,
				})
				return nil
			})
		},
	}
	bindComponentFlags(cmd, &f)
	return cmd
}

func newEditSetComponentCmd() *cobra.Command {
	var f componentFlags
	cmd := &cobra.Command{
		Use:   "component <name>",
		Short: "Update an existing component",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return mutatePackage(f.pkgDir, func(p *api.Package) error {
				idx := findComponentIdx(p, args[0])
				if idx < 0 {
					return fmt.Errorf("component %q not found", args[0])
				}
				ex := &p.Spec.Components[idx]
				if c.Flags().Changed("path") {
					ex.Path = f.path
				}
				if c.Flags().Changed("default") {
					ex.Default = f.dflt
				}
				if c.Flags().Changed("description") {
					ex.Description = f.description
				}
				if c.Flags().Changed("requires") {
					ex.Requires = f.requires
				}
				if c.Flags().Changed("conflicts") {
					ex.Conflicts = f.conflicts
				}
				if c.Flags().Changed("valid-for-bases") {
					ex.ValidForBases = f.validForBases
				}
				return nil
			})
		},
	}
	bindComponentFlags(cmd, &f)
	return cmd
}

func findComponentIdx(p *api.Package, name string) int {
	for i, c := range p.Spec.Components {
		if c.Name == name {
			return i
		}
	}
	return -1
}

func removeComponent(p *api.Package, name string) error {
	idx := findComponentIdx(p, name)
	if idx < 0 {
		return fmt.Errorf("component %q not found", name)
	}
	p.Spec.Components = append(p.Spec.Components[:idx], p.Spec.Components[idx+1:]...)
	return nil
}

// --- dependency ------------------------------------------------------------

type depFlags struct {
	pkgDir        string
	pkg           string
	version       string
	whenComponent string
	optional      bool
}

func bindDepFlags(cmd *cobra.Command, f *depFlags) {
	cmd.Flags().StringVar(&f.pkgDir, "package", ".", "package directory containing installer.yaml")
	cmd.Flags().StringVar(&f.pkg, "package-ref", "", "OCI ref of the dependency package (without tag), e.g., oci://ghcr.io/myorg/dep")
	cmd.Flags().StringVar(&f.version, "version", "", "SemVer range (e.g., ^1.2.0)")
	cmd.Flags().StringVar(&f.whenComponent, "when-component", "", "only follow when this component is selected in the parent")
	cmd.Flags().BoolVar(&f.optional, "optional", false, "make this dependency conditional")
}

func newEditAddDependencyCmd() *cobra.Command {
	var f depFlags
	cmd := &cobra.Command{
		Use:   "dependency <name>",
		Short: "Add a dependency to installer.yaml",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if f.pkg == "" {
				return fmt.Errorf("--package-ref is required")
			}
			return mutatePackage(f.pkgDir, func(p *api.Package) error {
				if findDepIdx(p, args[0]) >= 0 {
					return fmt.Errorf("dependency %q already exists; use `edit set dependency` to modify", args[0])
				}
				p.Spec.Dependencies = append(p.Spec.Dependencies, api.Dependency{
					Name:          args[0],
					Package:       f.pkg,
					Version:       f.version,
					WhenComponent: f.whenComponent,
					Optional:      f.optional,
				})
				return nil
			})
		},
	}
	bindDepFlags(cmd, &f)
	return cmd
}

func newEditSetDependencyCmd() *cobra.Command {
	var f depFlags
	cmd := &cobra.Command{
		Use:   "dependency <name>",
		Short: "Update an existing dependency",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return mutatePackage(f.pkgDir, func(p *api.Package) error {
				idx := findDepIdx(p, args[0])
				if idx < 0 {
					return fmt.Errorf("dependency %q not found", args[0])
				}
				ex := &p.Spec.Dependencies[idx]
				if c.Flags().Changed("package-ref") {
					ex.Package = f.pkg
				}
				if c.Flags().Changed("version") {
					ex.Version = f.version
				}
				if c.Flags().Changed("when-component") {
					ex.WhenComponent = f.whenComponent
				}
				if c.Flags().Changed("optional") {
					ex.Optional = f.optional
				}
				return nil
			})
		},
	}
	bindDepFlags(cmd, &f)
	return cmd
}

func findDepIdx(p *api.Package, name string) int {
	for i, d := range p.Spec.Dependencies {
		if d.Name == name {
			return i
		}
	}
	return -1
}

func removeDependency(p *api.Package, name string) error {
	idx := findDepIdx(p, name)
	if idx < 0 {
		return fmt.Errorf("dependency %q not found", name)
	}
	p.Spec.Dependencies = append(p.Spec.Dependencies[:idx], p.Spec.Dependencies[idx+1:]...)
	return nil
}

// --- shared ---------------------------------------------------------------

func newEditRemoveCmdFor(noun string, fn func(*api.Package, string) error) *cobra.Command {
	var pkgDir string
	cmd := &cobra.Command{
		Use:   noun + " <name>",
		Short: "Remove a " + noun,
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return mutatePackage(pkgDir, func(p *api.Package) error {
				return fn(p, args[0])
			})
		},
	}
	cmd.Flags().StringVar(&pkgDir, "package", ".", "package directory containing installer.yaml")
	return cmd
}

// mutatePackage reads pkgDir/installer.yaml, runs mut, re-validates by
// re-parsing, and writes back. Validation by re-parse catches edits
// that break the schema (e.g., conflicts that reference unknown
// components) before they reach disk.
func mutatePackage(pkgDir string, mut func(*api.Package) error) error {
	abs, err := filepath.Abs(pkgDir)
	if err != nil {
		return err
	}
	path := filepath.Join(abs, "installer.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	pkg, err := api.ParsePackage(data)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if err := mut(pkg); err != nil {
		return err
	}
	out, err := api.MarshalYAML(pkg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	// Re-parse the marshaled bytes to verify the mutation produced
	// a schema-valid document. Better to fail before writing than to
	// leave the package in a bad state.
	if _, err := api.ParsePackage(out); err != nil {
		return fmt.Errorf("re-parse after mutation: %w", err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Printf("Updated %s\n", path)
	return nil
}

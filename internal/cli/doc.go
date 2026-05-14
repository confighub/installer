package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/pkg/api"
)

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func newDocCmd() *cobra.Command {
	var (
		outDir string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "doc <package-ref>",
		Short: "Print a package's components, requirements, and inputs",
		Long: `Pull a package and print its declared bases, components (with required-deps),
external requirements, and wizard inputs schema. Use --json for machine-
readable output suitable for agents.

Package references:
  ./path/to/pkg               local directory
  /abs/path/to/pkg.tgz        local archive
  oci://ghcr.io/org/pkg:tag   OCI artifact (Helm-OCI shaped)
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := args[0]
			ctx := context.Background()
			dir, err := ipkg.Pull(ctx, ref, outDir)
			if err != nil {
				return err
			}
			loaded, err := ipkg.Load(dir)
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(loaded.Package)
			}
			printPackageDoc(loaded.Package)
			return nil
		},
	}
	cmd.Flags().StringVar(&outDir, "out", "", "destination directory for fetched package (default: temp dir for archives, in place for local dirs)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

func printPackageDoc(p *api.Package) {
	fmt.Printf("Package: %s\n", p.Metadata.Name)
	if p.Metadata.Version != "" {
		fmt.Printf("Version: %s\n", p.Metadata.Version)
	}
	if p.Metadata.KubeVersion != "" {
		fmt.Printf("Kubernetes: %s\n", p.Metadata.KubeVersion)
	}
	if p.Metadata.InstallerVersion != "" {
		fmt.Printf("Installer:  %s\n", p.Metadata.InstallerVersion)
	}
	fmt.Println()

	fmt.Println("Bases:")
	for _, b := range p.Spec.Bases {
		def := ""
		if b.Default {
			def = " (default)"
		}
		fmt.Printf("  - %s%s\n", b.Name, def)
		if b.Description != "" {
			fmt.Printf("      %s\n", b.Description)
		}
	}
	fmt.Println()

	if len(p.Spec.Components) > 0 {
		fmt.Println("Components:")
		for _, c := range p.Spec.Components {
			fmt.Printf("  - %s\n", c.Name)
			if c.Description != "" {
				fmt.Printf("      %s\n", c.Description)
			}
			if len(c.Requires) > 0 {
				fmt.Printf("      requires:  %s\n", strings.Join(c.Requires, ", "))
			}
			if len(c.Conflicts) > 0 {
				fmt.Printf("      conflicts: %s\n", strings.Join(c.Conflicts, ", "))
			}
			if len(c.ValidForBases) > 0 {
				fmt.Printf("      valid-for-bases: %s\n", strings.Join(c.ValidForBases, ", "))
			}
		}
		fmt.Println()
	}

	if len(p.Spec.Dependencies) > 0 {
		fmt.Println("Dependencies:")
		for _, d := range p.Spec.Dependencies {
			line := fmt.Sprintf("  - %s -> %s", d.Name, d.Package)
			if d.Version != "" {
				line += " " + d.Version
			}
			fmt.Println(line)
			if d.Optional {
				cond := "optional"
				if d.WhenComponent != "" {
					cond = "optional, follows when component " + d.WhenComponent
				}
				fmt.Printf("      %s\n", cond)
			} else if d.WhenComponent != "" {
				fmt.Printf("      follows when component %s is selected\n", d.WhenComponent)
			}
			if d.Selection != nil {
				if d.Selection.Base != "" {
					fmt.Printf("      base: %s\n", d.Selection.Base)
				}
				if len(d.Selection.Components) > 0 {
					fmt.Printf("      components: %s\n", strings.Join(d.Selection.Components, ", "))
				}
			}
			if len(d.Inputs) > 0 {
				keys := sortedMapKeys(d.Inputs)
				fmt.Printf("      inputs: %s\n", strings.Join(keys, ", "))
			}
			if len(d.Satisfies) > 0 {
				parts := make([]string, 0, len(d.Satisfies))
				for _, s := range d.Satisfies {
					p := string(s.Kind)
					if s.Name != "" {
						p += "/" + s.Name
					} else if s.Capability != "" {
						p += " capability=" + s.Capability
					}
					parts = append(parts, p)
				}
				fmt.Printf("      satisfies: %s\n", strings.Join(parts, ", "))
			}
		}
		fmt.Println()
	}

	if len(p.Spec.Conflicts) > 0 {
		fmt.Println("Conflicts:")
		for _, c := range p.Spec.Conflicts {
			line := "  - " + c.Package
			if c.Version != "" {
				line += " " + c.Version
			}
			if c.Reason != "" {
				line += "  (" + c.Reason + ")"
			}
			fmt.Println(line)
		}
		fmt.Println()
	}

	if len(p.Spec.Replaces) > 0 {
		fmt.Println("Replaces:")
		for _, r := range p.Spec.Replaces {
			line := "  - " + r.Package
			if r.Version != "" {
				line += " " + r.Version
			}
			fmt.Println(line)
		}
		fmt.Println()
	}

	if len(p.Spec.ExternalRequires) > 0 {
		fmt.Println("External requirements:")
		for _, r := range p.Spec.ExternalRequires {
			line := fmt.Sprintf("  - %s", r.Kind)
			if r.Name != "" {
				line += " " + r.Name
			}
			if r.Version != "" {
				line += " " + r.Version
			}
			if r.Capability != "" {
				line += " capability=" + r.Capability
			}
			fmt.Println(line)
			if r.SuggestedSource != "" {
				fmt.Printf("      suggested: %s\n", r.SuggestedSource)
			}
			if len(r.SuggestedProviders) > 0 {
				fmt.Printf("      providers: %s\n", strings.Join(r.SuggestedProviders, ", "))
			}
		}
		fmt.Println()
	}

	if len(p.Spec.Inputs) > 0 {
		fmt.Println("Inputs:")
		for _, in := range p.Spec.Inputs {
			req := ""
			if in.Required {
				req = " (required)"
			}
			def := ""
			if in.Default != nil {
				def = fmt.Sprintf(" [default: %v]", in.Default)
			}
			fmt.Printf("  - %s (%s)%s%s\n", in.Name, in.Type, req, def)
			if in.Prompt != "" {
				fmt.Printf("      %s\n", in.Prompt)
			}
			if len(in.Options) > 0 {
				fmt.Printf("      options: %s\n", strings.Join(in.Options, ", "))
			}
		}
		fmt.Println()
	}

	if p.Spec.Collector != nil {
		fmt.Println("Collector:")
		fmt.Printf("  command: %s", p.Spec.Collector.Command)
		if len(p.Spec.Collector.Args) > 0 {
			fmt.Printf(" %s", strings.Join(p.Spec.Collector.Args, " "))
		}
		fmt.Println()
		if p.Spec.Collector.Description != "" {
			for _, line := range strings.Split(strings.TrimSpace(p.Spec.Collector.Description), "\n") {
				fmt.Printf("      %s\n", line)
			}
		}
		fmt.Println()
	}

	if p.Spec.Validation != nil {
		v := p.Spec.Validation
		fmt.Println("Validation artifacts:")
		if v.CommandHelp != "" {
			fmt.Printf("  command-help: %s\n", v.CommandHelp)
		}
		if v.EnvSchema != "" {
			fmt.Printf("  env-schema:   %s\n", v.EnvSchema)
		}
		if v.RuntimeSpec != "" {
			fmt.Printf("  runtime-spec: %s\n", v.RuntimeSpec)
		}
		if v.HowToRegenerate != "" {
			fmt.Println("  regenerate with:")
			for _, line := range strings.Split(strings.TrimSpace(v.HowToRegenerate), "\n") {
				fmt.Printf("      %s\n", line)
			}
		}
	}
}

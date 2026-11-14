// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/confighub/installer/internal/upload"
	"github.com/confighub/installer/pkg/api"
)

func newShowCmd() *cobra.Command {
	var (
		space  string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "show [<package>]",
		Short: "Show what was installed in a Space, from its installer record in ConfigHub",
		Long: `Show reads the installer record an upload keeps in ConfigHub and prints what it
says about the install: the package and where it was pulled from, the base and
the components selected among those the package offers, each input's answer
beside its default, the facts the collector found, the dependencies locked, the
cluster requirements of what was selected, and the functions render ran.

The record is the hidden Unit named installer in the install's Space, which
"cub unit data --space <space> installer" prints as stored. Name the Space with
--space, or the package, when the organization has one install of it.

Use --json for the record as stored, as JSON.`,
		Example: `  installer show --space nonprod-c3agent
  installer show c3agent`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			packageName := ""
			if len(args) == 1 {
				packageName = args[0]
			}
			if packageName == "" && space == "" {
				return fmt.Errorf("name the Space with --space, or the package")
			}
			found, err := findInstallerRecord(commandContext(cmd), space, packageName)
			if err != nil {
				return err
			}
			if found == nil {
				if space != "" {
					return fmt.Errorf("Space %s does not exist or has no installer record", space)
				}
				return fmt.Errorf("no Space has an installer record of %s", packageName)
			}
			if asJSON {
				var doc any
				if err := yaml.Unmarshal(found.Data, &doc); err != nil {
					return fmt.Errorf("parse installer record: %w", err)
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(doc)
			}
			rec, err := upload.UnmarshalRecord(found.Data)
			if err != nil {
				return err
			}
			return printRecord(os.Stdout, found.SpaceSlug, rec)
		},
	}
	cmd.Flags().StringVar(&space, "space", "", "Space the package is installed in (default: the package's only install in the organization)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the record as stored, as JSON")
	return cmd
}

// printRecord renders an installer record as sections, in the order the
// ConfigHub UI's Overview of the record shows them.
func printRecord(w io.Writer, space string, rec *upload.Record) error {
	p := &rec.Package
	spec := &p.Spec
	var base string
	selected := map[string]bool{}
	if rec.Selection != nil {
		base = rec.Selection.Base
		for _, c := range rec.Selection.Components {
			selected[c] = true
		}
	}

	pw := newPrinter(w)
	pw.field("Package", p.Name)
	pw.field("Version", p.InstallerMetadata.Version)
	pw.field("Space", space)
	if rec.Source != nil {
		pw.field("Source", rec.Source.Ref)
		pw.field("Digest", rec.Source.Digest)
	}
	if rec.Inputs != nil {
		pw.field("Namespace", rec.Inputs.Namespace)
	}
	pw.field("Base", base)
	pw.field("Installer version", p.InstallerMetadata.InstallerVersion)
	pw.field("Kubernetes version", p.InstallerMetadata.KubeVersion)
	pw.flushFields()

	if len(spec.Components) > 0 {
		groups := map[string]string{}
		for _, g := range spec.ComponentGroups {
			for _, m := range g.Components {
				groups[m] = g.Name
			}
		}
		pw.section("Components")
		for _, c := range spec.Components {
			mark := " "
			if selected[c.Name] {
				mark = "*"
			}
			line := "  " + mark + " " + c.Name
			if g := groups[c.Name]; g != "" {
				line += "  (one of " + g + ")"
			}
			pw.line(line)
			pw.wrapped(prose(c.Description), "        ")
		}
		pw.line("  (* selected)")
	}

	if rec.Inputs != nil || len(spec.Inputs) > 0 {
		var values map[string]any
		if rec.Inputs != nil {
			values = rec.Inputs.Values
		}
		var rows [][]string
		declared := map[string]bool{}
		for _, in := range spec.Inputs {
			declared[in.Name] = true
			rows = append(rows, []string{in.Name, displayValue(values[in.Name]), displayValue(in.Default)})
		}
		for _, name := range sortedMapKeys(values) {
			if !declared[name] {
				rows = append(rows, []string{name, displayValue(values[name]), ""})
			}
		}
		if len(rows) > 0 {
			pw.section("Inputs")
			pw.table([]string{"INPUT", "VALUE", "DEFAULT"}, rows)
		}
		if rec.Inputs != nil && len(rec.Inputs.ImageOverrides) > 0 {
			var images [][]string
			for _, name := range sortedStringKeys(rec.Inputs.ImageOverrides) {
				images = append(images, []string{name, rec.Inputs.ImageOverrides[name]})
			}
			pw.section("Image overrides")
			pw.table([]string{"IMAGE", "OVERRIDE"}, images)
		}
	}

	if rec.Facts != nil && len(rec.Facts.Values) > 0 {
		var rows [][]string
		for _, name := range sortedMapKeys(rec.Facts.Values) {
			rows = append(rows, []string{name, displayValue(rec.Facts.Values[name])})
		}
		pw.section("Facts")
		pw.table([]string{"FACT", "VALUE"}, rows)
	}

	if rec.Lock != nil && len(rec.Lock.Resolved) > 0 {
		var rows [][]string
		for _, d := range rec.Lock.Resolved {
			rows = append(rows, []string{d.Name, d.Version, d.Ref, strings.Join(d.RequestedBy, ", ")})
		}
		pw.section("Dependencies")
		pw.table([]string{"DEPENDENCY", "VERSION", "REFERENCE", "REQUESTED BY"}, rows)
	} else if len(spec.Dependencies) > 0 {
		var rows [][]string
		for _, d := range spec.Dependencies {
			rows = append(rows, []string{d.Name, d.Package, d.Version})
		}
		pw.section("Dependencies")
		pw.table([]string{"DEPENDENCY", "PACKAGE", "VERSION"}, rows)
	}

	contributors := recordContributors(spec, base, selected)

	var requirements [][]string
	for _, c := range contributors {
		for _, r := range c.requires {
			name := r.Name
			if name == "" && r.Capability != "" {
				name = "capability " + r.Capability
			}
			requirements = append(requirements, []string{string(r.Kind), name, r.Version, c.from})
		}
	}
	if len(requirements) > 0 {
		pw.section("Cluster requirements")
		pw.table(withoutEmptyColumns([]string{"KIND", "NAME", "VERSION", "REQUIRED BY"}, requirements))
	}

	var transformers [][]string
	var validators []string
	seenValidator := map[string]bool{}
	for _, c := range contributors {
		for _, g := range c.transformers {
			var names []string
			for _, inv := range g.Invocations {
				names = append(names, inv.Name)
			}
			transformers = append(transformers, []string{c.from, strings.Join(names, ", "), prose(g.Description)})
		}
		for _, g := range c.validators {
			for _, inv := range g.Invocations {
				// A validator's arguments say what it checks ("restricted").
				v := strings.Join(append([]string{inv.Name}, inv.Args...), " ")
				if !seenValidator[v] {
					seenValidator[v] = true
					validators = append(validators, v)
				}
			}
		}
	}
	if len(transformers) > 0 {
		pw.section("Transformers")
		pw.table([]string{"FROM", "FUNCTIONS", "DESCRIPTION"}, transformers)
	}
	if len(validators) > 0 {
		pw.section("Validators")
		pw.line("  " + strings.Join(validators, ", "))
	}
	return pw.err
}

// recordContributor is what the package, the chosen base, or one selected
// component adds to an install.
type recordContributor struct {
	from         string
	requires     []api.ExternalRequire
	transformers []api.FunctionGroup
	validators   []api.FunctionGroup
}

func recordContributors(spec *api.PackageSpec, base string, selected map[string]bool) []recordContributor {
	out := []recordContributor{{from: "package", requires: spec.ExternalRequires, transformers: spec.Transformers, validators: spec.Validators}}
	for _, b := range spec.Bases {
		if b.Name == base {
			out = append(out, recordContributor{from: "base " + b.Name, requires: b.ExternalRequires})
		}
	}
	for _, c := range spec.Components {
		if selected[c.Name] {
			out = append(out, recordContributor{from: c.Name, requires: c.ExternalRequires, transformers: c.Transformers, validators: c.Validators})
		}
	}
	return out
}

// prose rejoins the hand-wrapped lines of a description into one line per
// paragraph, so the terminal or the table can wrap it.
func prose(text string) string {
	paragraphs := strings.Split(strings.TrimSpace(text), "\n\n")
	for i, para := range paragraphs {
		paragraphs[i] = strings.Join(strings.Fields(para), " ")
	}
	return strings.Join(paragraphs, "\n")
}

// displayValue renders an input or fact value: scalars as written, an empty
// string quoted, and structures as YAML flow.
func displayValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		if t == "" {
			return `""`
		}
		return t
	case bool, int, int64, float64:
		return fmt.Sprint(t)
	default:
		out, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return string(out)
	}
}

// withoutEmptyColumns leaves out the columns no row has a value in, such as
// VERSION when no requirement pins one.
func withoutEmptyColumns(columns []string, rows [][]string) ([]string, [][]string) {
	keep := make([]bool, len(columns))
	for _, row := range rows {
		for j, cell := range row {
			if cell != "" {
				keep[j] = true
			}
		}
	}
	pick := func(cells []string) []string {
		var out []string
		for j, cell := range cells {
			if keep[j] {
				out = append(out, cell)
			}
		}
		return out
	}
	outRows := make([][]string, len(rows))
	for i, row := range rows {
		outRows[i] = pick(row)
	}
	return pick(columns), outRows
}

func sortedStringKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// printer writes the record's sections, remembering the first write error.
type printer struct {
	w      io.Writer
	err    error
	fields [][2]string
}

func newPrinter(w io.Writer) *printer { return &printer{w: w} }

func (p *printer) printf(format string, args ...any) {
	if p.err == nil {
		_, p.err = fmt.Fprintf(p.w, format, args...)
	}
}

func (p *printer) line(s string) { p.printf("%s\n", s) }

func (p *printer) section(title string) { p.printf("\n%s:\n", title) }

// field queues a label/value row, dropping it when the value is empty;
// flushFields aligns the queued rows.
func (p *printer) field(label, value string) {
	if value != "" {
		p.fields = append(p.fields, [2]string{label, value})
	}
}

func (p *printer) flushFields() {
	width := 0
	for _, f := range p.fields {
		width = max(width, len(f[0]))
	}
	for _, f := range p.fields {
		p.printf("%-*s  %s\n", width+1, f[0]+":", f[1])
	}
	p.fields = nil
}

// wrapped prints text wrapped at 80 columns, each line indented.
func (p *printer) wrapped(text, indent string) {
	for para := range strings.SplitSeq(text, "\n") {
		line := indent
		for word := range strings.FieldsSeq(para) {
			if len(line) > len(indent) && len(line)+1+len(word) > 80 {
				p.line(line)
				line = indent
			}
			if len(line) > len(indent) {
				line += " "
			}
			line += word
		}
		if len(line) > len(indent) {
			p.line(line)
		}
	}
}

func (p *printer) table(columns []string, rows [][]string) {
	if p.err != nil {
		return
	}
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "  %s\n", strings.Join(columns, "\t"))
	for _, row := range rows {
		fmt.Fprintf(tw, "  %s\n", strings.Join(row, "\t"))
	}
	if p.err = tw.Flush(); p.err != nil {
		return
	}
	// A row whose last cells are empty is padded out to the columns' width.
	for line := range strings.Lines(buf.String()) {
		p.line(strings.TrimRight(line, " \n"))
	}
}

// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/confighub/installer/internal/upload"
	"github.com/confighub/installer/pkg/installer"
)

// uploadFlags are the flags `installer upload` and `installer plan` share:
// everything that decides what is uploaded and where it goes.
type uploadFlags struct {
	// componentSet and variantSet say whether --component and --variant were
	// given: a Space an earlier upload labeled keeps its labels unless they are.
	componentSet     bool
	variantSet       bool
	workDir          string
	space            string
	spacePattern     string
	target           string
	changeSet        string
	component        string
	variant          string
	layer            string
	environment      string
	region           string
	owner            string
	spaceLabels      []string
	spaceAnnotations []string
	unitLabels       []string
	unitAnnotations  []string
}

func (f *uploadFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.workDir, "work-dir", ".", "working directory")
	cmd.Flags().StringVar(&f.space, "space", "", "Space slug for a package with no dependencies; overrides --space-pattern")
	cmd.Flags().StringVar(&f.spacePattern, "space-pattern", "{{.PackageName}}", "Go template for each package's Space slug (vars: .PackageName, .PackageVersion, .Variant)")
	cmd.Flags().StringVar(&f.target, "target", "", "release Target for each package's Space, which a Release of the Space publishes its Units for: <space>/<target>, a UUID, or a slug in the package's Space. Without it, a Space keeps the release Target it has.")
	cmd.Flags().StringVar(&f.changeSet, "changeset", "", "existing open ChangeSet every package's writes join: <space>/<changeset>, a UUID, or a slug in the parent's Space (default: one new ChangeSet per Space)")
	cmd.Flags().StringVar(&f.component, "component", "", "value for the parent's well-known \"Component\" Space label (default: the package name); a dependency's is its package name")
	cmd.Flags().StringVar(&f.variant, "variant", "base", "value for every package's well-known \"Variant\" Space label")
	cmd.Flags().StringVar(&f.layer, "layer", "", "value for the well-known \"Layer\" Space label (e.g. App)")
	cmd.Flags().StringVar(&f.environment, "environment", "", "value for the well-known \"Environment\" Space label (e.g. Prod)")
	cmd.Flags().StringVar(&f.region, "region", "", "value for the well-known \"Region\" Space label (e.g. us-east1)")
	cmd.Flags().StringVar(&f.owner, "owner", "", "value for the well-known \"Owner\" Space label (e.g. Engineering)")
	cmd.Flags().StringArrayVar(&f.spaceLabels, "space-label", nil, "label key=value to set on the Space(s) (repeatable); the well-known labels have their own flags")
	cmd.Flags().StringArrayVar(&f.spaceAnnotations, "space-annotation", nil, "annotation key=value to set on the Space(s) (repeatable); \"TargetID\" is reserved (use --target)")
	cmd.Flags().StringArrayVar(&f.unitLabels, "unit-label", nil, "label key=value to set on every Unit the upload writes (repeatable)")
	cmd.Flags().StringArrayVar(&f.unitAnnotations, "unit-annotation", nil, "annotation key=value to set on every Unit the upload writes (repeatable)")
}

func (f *uploadFlags) noteChanged(cmd *cobra.Command) {
	f.componentSet = cmd.Flags().Changed("component")
	f.variantSet = cmd.Flags().Changed("variant")
}

// options validates the flags and turns them into installer.UploadOptions.
func (f *uploadFlags) options() (installer.UploadOptions, error) {
	for flag, vals := range map[string][]string{"--unit-annotation": f.unitAnnotations, "--unit-label": f.unitLabels} {
		if err := validateKeyValueFlags(flag, vals); err != nil {
			return installer.UploadOptions{}, err
		}
	}
	if err := validateSpaceLabelFlags(f.spaceLabels); err != nil {
		return installer.UploadOptions{}, err
	}
	if err := validateSpaceAnnotationFlags(f.spaceAnnotations); err != nil {
		return installer.UploadOptions{}, err
	}
	if f.variant == "" {
		return installer.UploadOptions{}, fmt.Errorf("--variant must not be empty")
	}
	opts := installer.UploadOptions{
		WorkDir:      f.workDir,
		Space:        f.space,
		SpacePattern: f.spacePattern,
		Target:       f.target,
		ChangeSet:    f.changeSet,
		Layer:        f.layer,
		Environment:  f.environment,
		Region:       f.region,
		Owner:        f.owner,
	}
	if f.componentSet {
		opts.Component = f.component
	}
	if f.variantSet {
		opts.Variant = f.variant
	}
	var err error
	if opts.SpaceLabels, err = mergeKeyValues(nil, f.spaceLabels); err != nil {
		return installer.UploadOptions{}, err
	}
	if opts.SpaceAnnotations, err = mergeKeyValues(nil, f.spaceAnnotations); err != nil {
		return installer.UploadOptions{}, err
	}
	if opts.UnitLabels, err = mergeKeyValues(nil, f.unitLabels); err != nil {
		return installer.UploadOptions{}, err
	}
	if opts.UnitAnnotations, err = mergeKeyValues(nil, f.unitAnnotations); err != nil {
		return installer.UploadOptions{}, err
	}
	return opts, nil
}

// prepareUpload builds the upload the flags describe, adding the flag that
// remedies a failure when there is one.
func prepareUpload(ctx context.Context, inst *installer.Installer, f uploadFlags) (*installer.UploadPlan, error) {
	opts, err := f.options()
	if err != nil {
		return nil, err
	}
	plan, err := inst.PrepareUpload(ctx, opts)
	if err != nil {
		return nil, uploadError(err, f.workDir)
	}
	return plan, nil
}

// uploadError adds the flag or command that remedies an upload failure, when
// there is one.
func uploadError(err error, workDir string) error {
	switch {
	case errors.Is(err, installer.ErrSpaceWithDependencies):
		return fmt.Errorf("--space cannot be used when the package declares dependencies; use --space-pattern")
	case errors.Is(err, installer.ErrNoLock):
		return fmt.Errorf("%v; run `%s deps update --work-dir %s` and `%s render --work-dir %s` first",
			err, InvocationName(), absPath(workDir), InvocationName(), absPath(workDir))
	case errors.Is(err, installer.ErrStaleLock):
		return fmt.Errorf("%v; run `%s deps update --work-dir %s` and `%s render --work-dir %s` again",
			err, InvocationName(), absPath(workDir), InvocationName(), absPath(workDir))
	}
	return err
}

func newUploadCmd() *cobra.Command {
	var (
		flags uploadFlags
		yes   bool
	)
	cmd := &cobra.Command{
		Use:   "upload",
		Short: "Upload rendered manifests to ConfigHub, one Space per package",
		Long: `Upload sends each package's rendered output to ConfigHub: the parent package,
then each locked dependency, each into its own Space. ConfigHub makes the
Space match it. Every upload is create-or-update: resources new to the render
become Units, changed ones are merged, unchanged ones are left alone, and a
Unit whose resource the render no longer produces is emptied, never deleted.
Uploading an unchanged work-dir writes nothing.

Each package's request carries its rendered manifests (out/manifests/, or
out/<dependency>/manifests/) and its installer record: one AppConfig/YAML Unit,
"installer", holding the package definition, selection, inputs, facts, and,
for the parent, the lock. installer setup reads it back to re-enter the
install. Every resource becomes its own Unit; AppConfig carried in annotated
ConfigMaps becomes an AppConfig Unit rendered into the ConfigMap. Rendered
Secrets are in out/secrets/ and are never uploaded.

The install namespace is each Space's release namespace, recorded as its
Namespace label. Resources a package places in other namespaces stay in the
package's Space.

Spaces are named by --space-pattern, or by --space for a package with no
dependencies, and carry the well-known labels: Component (the parent's is
--component, defaulting to the package name; a dependency's is its package
name), Variant (default base), and any of --layer, --environment, --region,
and --owner given. The parent's Space records the dependencies' components in
its DependsOn annotation. Labels and annotations given again on a later upload
are updated; ones omitted are left alone.

When the upload would empty any Unit it lists them and asks first, unless
--yes is given.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := commandContext(cmd)
			flags.noteChanged(cmd)
			inst, err := newInstaller()
			if err != nil {
				return err
			}
			plan, err := prepareUpload(ctx, inst, flags)
			if err != nil {
				return err
			}

			// Emptying a Unit withdraws what it deployed, so an upload that
			// would empty anything is confirmed first. The preview is a dry
			// run of the same requests, so what is confirmed is what happens.
			if !yes {
				if err := inst.PreviewUpload(ctx, plan, installer.PreviewOptions{}); err != nil {
					return err
				}
				if err := confirmEmpties(plan.Emptied()); err != nil {
					return err
				}
			}

			result, err := inst.Upload(ctx, plan)
			if err != nil {
				return err
			}
			for _, pkg := range result.Packages {
				printRevert(pkg)
			}
			fmt.Printf("\n%s\n", upload.Summarize(result.Results()).Applied())

			// Record the parent's install in ~/.confighub/installer/state.yaml
			// for the packages `installer new` locates later. Best-effort.
			if err := recordUploadInUserState(ctx, result); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not record install in user state: %v\n", err)
			}
			if result.Failed() {
				return fmt.Errorf("some writes failed; the rest landed, and uploading again retries the failures")
			}
			return nil
		},
	}
	flags.register(cmd)
	cmd.Flags().BoolVar(&yes, "yes", false, "empty Units whose resources left the render without asking")
	return cmd
}

// printRevert prints how to revert what an upload wrote to each Space it
// changed: restoring the package's Units to before the upload's ChangeSet,
// which empties the Units it created and reverts the rest.
func printRevert(pkg installer.UploadedPackage) {
	for _, cs := range pkg.ChangeSets {
		fmt.Printf("\nRevert this upload of %s with:\n  cub unit update --patch --space %s --restore Before:ChangeSet:%s --where \"Labels.UploadSource = '%s'\" --include-hidden=%s\n",
			cs.Space, cs.Space, cs.ChangeSet, pkg.Name, upload.RecordHiddenReason)
	}
}

// confirmEmpties asks before an upload empties Units. Without a terminal to
// ask on, it refuses and names --yes.
func confirmEmpties(emptied []string) error {
	if len(emptied) == 0 {
		return nil
	}
	fmt.Printf("This upload empties %d Unit(s) whose resources are no longer rendered:\n", len(emptied))
	for _, u := range emptied {
		fmt.Printf("  - %s\n", u)
	}
	fmt.Println("Their resources are withdrawn from the cluster by the next Release. Nothing is deleted.")
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("refusing to empty %d Unit(s) without a terminal to confirm on; pass --yes", len(emptied))
	}
	fmt.Print("\nContinue? [y/N]: ")
	answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return fmt.Errorf("read confirmation: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	default:
		return fmt.Errorf("cancelled")
	}
}

func commandContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

// mergeKeyValues adds key=value flag values to into, returning it.
func mergeKeyValues(into map[string]string, vals []string) (map[string]string, error) {
	if len(vals) == 0 {
		return into, nil
	}
	if into == nil {
		into = map[string]string{}
	}
	for _, v := range vals {
		key, value, ok := strings.Cut(v, "=")
		if !ok {
			return nil, fmt.Errorf("%q must be key=value", v)
		}
		into[key] = value
	}
	return into, nil
}

func validateKeyValueFlags(flag string, vals []string) error {
	for _, v := range vals {
		if !strings.Contains(v, "=") {
			return fmt.Errorf("%s %q must be key=value", flag, v)
		}
	}
	return nil
}

// reservedSpaceLabelKeys maps each well-known Space label to the flag that
// owns it, so --space-label can reject attempts to set them directly.
var reservedSpaceLabelKeys = map[string]string{
	"Component":   "--component",
	"Layer":       "--layer",
	"Environment": "--environment",
	"Region":      "--region",
	"Owner":       "--owner",
	"Variant":     "--variant",
}

func validateSpaceLabelFlags(vals []string) error {
	for _, v := range vals {
		if !strings.Contains(v, "=") {
			return fmt.Errorf("--space-label %q must be key=value", v)
		}
		key := strings.SplitN(v, "=", 2)[0]
		if flag, ok := reservedSpaceLabelKeys[key]; ok {
			return fmt.Errorf("--space-label %q: %q is a reserved well-known label; set it with %s", v, key, flag)
		}
	}
	return nil
}

func validateSpaceAnnotationFlags(vals []string) error {
	for _, v := range vals {
		if !strings.Contains(v, "=") {
			return fmt.Errorf("--space-annotation %q must be key=value", v)
		}
		if key := strings.SplitN(v, "=", 2)[0]; key == "TargetID" {
			return fmt.Errorf("--space-annotation %q: %q is reserved; set it with --target", v, key)
		}
	}
	return nil
}

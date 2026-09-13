// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/confighub/sdk/core/cubapi"
	goclientnew "github.com/confighub/sdk/core/openapi/goclient-new"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/confighub/installer/internal/deps"
	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/internal/upload"
	"github.com/confighub/installer/internal/version"
	"github.com/confighub/installer/pkg/api"
)

// confighub is the installer's connection to ConfigHub: the cub plugin
// environment when run as `cub installer`, otherwise the active cub context.
var confighub = cubapi.MemoizedClient{UserAgent: "installer"}

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
	cmd.Flags().StringVar(&f.target, "target", "", "Target for new Units: <space>/<target>, a UUID, or a slug in the package's Space. Recorded as each Space's TargetID annotation; later uploads use it when --target is omitted.")
	cmd.Flags().StringVar(&f.changeSet, "changeset", "", "existing open ChangeSet every package's writes join: <space>/<changeset>, a UUID, or a slug in the parent's Space (default: one new ChangeSet per Space)")
	cmd.Flags().StringVar(&f.component, "component", "", "value for the parent's well-known \"Component\" Space label (default: the package name); a dependency's is its package name")
	cmd.Flags().StringVar(&f.variant, "variant", "base", "value for every package's well-known \"Variant\" Space label")
	cmd.Flags().StringVar(&f.layer, "layer", "", "value for the well-known \"Layer\" Space label (e.g. App)")
	cmd.Flags().StringVar(&f.environment, "environment", "", "value for the well-known \"Environment\" Space label (e.g. Prod)")
	cmd.Flags().StringVar(&f.region, "region", "", "value for the well-known \"Region\" Space label (e.g. us-east1)")
	cmd.Flags().StringVar(&f.owner, "owner", "", "value for the well-known \"Owner\" Space label (e.g. Engineering)")
	cmd.Flags().StringSliceVar(&f.spaceLabels, "space-label", nil, "label key=value to set on the Space(s) (repeatable); the well-known labels have their own flags")
	cmd.Flags().StringSliceVar(&f.spaceAnnotations, "space-annotation", nil, "annotation key=value to set on the Space(s) (repeatable); \"TargetID\" is reserved (use --target)")
	cmd.Flags().StringSliceVar(&f.unitLabels, "unit-label", nil, "label key=value to set on every Unit the upload writes (repeatable)")
	cmd.Flags().StringSliceVar(&f.unitAnnotations, "unit-annotation", nil, "annotation key=value to set on every Unit the upload writes (repeatable)")
}

func (f *uploadFlags) noteChanged(cmd *cobra.Command) {
	f.componentSet = cmd.Flags().Changed("component")
	f.variantSet = cmd.Flags().Changed("variant")
}

// preparedUpload is one request per package, in upload order: the parent, then
// each locked dependency.
type preparedUpload struct {
	client   *cubapi.Client
	packages []upload.Package
	requests []goclientnew.UploadRequest
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
			prepared, err := prepareUpload(ctx, flags)
			if err != nil {
				return err
			}

			// Emptying a Unit withdraws what it deployed, so an upload that
			// would empty anything is confirmed first. The preview is a dry
			// run of the same requests, so what is confirmed is what happens.
			if !yes {
				var previews []*goclientnew.UploadResult
				for _, req := range prepared.requests {
					preview, err := cubapi.Upload(ctx, prepared.client, req, true)
					if err != nil {
						return err
					}
					previews = append(previews, preview)
				}
				if err := confirmEmpties(upload.Emptied(previews)); err != nil {
					return err
				}
			}

			failed := false
			var results []*goclientnew.UploadResult
			for i, req := range prepared.requests {
				pkg := prepared.packages[i]
				fmt.Printf("== %s@%s → Space %s ==\n", pkg.Name, pkg.Version, pkg.SpaceSlug)
				result, err := cubapi.Upload(ctx, prepared.client, req, false)
				if err != nil {
					return fmt.Errorf("upload %s: %w", pkg.Name, err)
				}
				upload.Report(os.Stdout, result, false)
				failed = failed || upload.Failed(result)
				results = append(results, result)
				printRevert(ctx, prepared.client, pkg, result)
			}
			fmt.Printf("\n%s\n", upload.Summarize(results).Applied())

			// Record the parent's install in ~/.confighub/installer/state.yaml
			// for the packages `installer new` locates later. Best-effort.
			if err := recordUploadInUserState(ctx, prepared.packages); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not record install in user state: %v\n", err)
			}
			if failed {
				return fmt.Errorf("some writes failed; the rest landed, and uploading again retries the failures")
			}
			return nil
		},
	}
	flags.register(cmd)
	cmd.Flags().BoolVar(&yes, "yes", false, "empty Units whose resources left the render without asking")
	return cmd
}

// printRevert prints how to revert what an upload wrote to a Space: restoring
// the package's Units to before the upload's ChangeSet, which empties the Units
// it created and reverts the rest.
func printRevert(ctx context.Context, c *cubapi.Client, pkg upload.Package, result *goclientnew.UploadResult) {
	if !upload.Summarize([]*goclientnew.UploadResult{result}).Changes() {
		return
	}
	for _, comp := range result.Components {
		for _, s := range comp.Spaces {
			if s.ChangeSetID == nil {
				continue
			}
			ref := s.ChangeSetID.String()
			if cs, err := cubapi.ResolveChangeSet(ctx, c, cubapi.RefFromID(*s.ChangeSetID), cubapi.ResolveOpts{}); err == nil && cs.ChangeSet != nil {
				ref = cs.ChangeSet.Slug
			}
			fmt.Printf("\nRevert this upload of %s with:\n  cub unit update --patch --space %s --restore Before:ChangeSet:%s --where \"Labels.UploadSource = '%s'\"\n",
				s.SpaceSlug, s.SpaceSlug, ref, pkg.Name)
		}
	}
}

// prepareUpload loads the work-dir, finds its packages, resolves --target and
// --changeset, and builds one upload request per package.
func prepareUpload(ctx context.Context, f uploadFlags) (*preparedUpload, error) {
	for flag, vals := range map[string][]string{"--unit-annotation": f.unitAnnotations, "--unit-label": f.unitLabels} {
		if err := validateKeyValueFlags(flag, vals); err != nil {
			return nil, err
		}
	}
	if err := validateSpaceLabelFlags(f.spaceLabels); err != nil {
		return nil, err
	}
	if err := validateSpaceAnnotationFlags(f.spaceAnnotations); err != nil {
		return nil, err
	}
	if f.variant == "" {
		return nil, fmt.Errorf("--variant must not be empty")
	}

	workDir, err := filepath.Abs(f.workDir)
	if err != nil {
		return nil, err
	}
	loaded, err := ipkg.Load(filepath.Join(workDir, "package"))
	if err != nil {
		return nil, fmt.Errorf("load package: %w", err)
	}
	lock, err := loadLockIfNeeded(workDir, loaded.Package)
	if err != nil {
		return nil, err
	}
	pattern := f.spacePattern
	if f.space != "" {
		if len(loaded.Package.Spec.Dependencies) > 0 {
			return nil, fmt.Errorf("--space cannot be used when the package declares dependencies; use --space-pattern")
		}
		pattern = f.space
	}
	packages, err := upload.Discover(upload.DiscoverInput{
		WorkDir:       workDir,
		SpacePattern:  pattern,
		Variant:       f.variant,
		ParentPackage: loaded.Package,
		Lock:          lock,
	})
	if err != nil {
		return nil, err
	}

	client, err := confighub.Preflight(ctx)
	if err != nil {
		return nil, err
	}

	opts := upload.RequestOptions{
		Component:     f.component,
		Variant:       f.variant,
		SpaceLabels:   map[string]string{},
		TargetIDs:     map[string]goclientnew.UUID{},
		SourceRef:     workDir,
		ClientVersion: version.Version,
	}
	for label, value := range map[string]string{
		"Layer": f.layer, "Environment": f.environment, "Region": f.region, "Owner": f.owner,
	} {
		if value != "" {
			opts.SpaceLabels[label] = value
		}
	}
	if opts.SpaceLabels, err = mergeKeyValues(opts.SpaceLabels, f.spaceLabels); err != nil {
		return nil, err
	}
	if opts.SpaceAnnotations, err = mergeKeyValues(nil, f.spaceAnnotations); err != nil {
		return nil, err
	}
	if opts.UnitLabels, err = mergeKeyValues(nil, f.unitLabels); err != nil {
		return nil, err
	}
	if opts.UnitAnnotations, err = mergeKeyValues(nil, f.unitAnnotations); err != nil {
		return nil, err
	}

	if f.changeSet != "" {
		id, err := resolveChangeSet(ctx, client, packages[0].SpaceSlug, f.changeSet)
		if err != nil {
			return nil, err
		}
		opts.ChangeSetID = &id
	}

	prepared := &preparedUpload{client: client, packages: packages}
	for _, pkg := range packages {
		space, err := existingSpace(ctx, client, pkg.SpaceSlug)
		if err != nil {
			return nil, err
		}
		id, err := uploadTargetID(ctx, client, space, pkg.SpaceSlug, f.target)
		if err != nil {
			return nil, err
		}
		if id != nil {
			opts.TargetIDs[pkg.SpaceSlug] = *id
		}
		// Every request sets Component and Variant, so a Space an earlier
		// upload labeled keeps those labels unless the flags say otherwise.
		pkgOpts := opts
		if space != nil {
			if !f.componentSet && pkg.IsParent && space.Labels["Component"] != "" {
				pkgOpts.Component = space.Labels["Component"]
			}
			if !f.variantSet && space.Labels["Variant"] != "" {
				pkgOpts.Variant = space.Labels["Variant"]
			}
		}
		req, err := upload.BuildRequest(pkg, packages, pkgOpts)
		if err != nil {
			return nil, err
		}
		prepared.requests = append(prepared.requests, req)
	}
	return prepared, nil
}

// existingSpace returns the Space with slug, or nil when there is none yet.
func existingSpace(ctx context.Context, c *cubapi.Client, slug string) (*goclientnew.Space, error) {
	found, err := cubapi.ResolveSpace(ctx, c, cubapi.NewRef("", slug), cubapi.ResolveOpts{})
	if cubapi.IsNotFoundError(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("look up Space %s: %w", slug, err)
	}
	return found.Space, nil
}

// uploadTargetID decides the Target a package's new Units are created on. An
// explicit --target wins; a bare slug is looked up in the package's Space, so
// that Space has to exist already. Without --target, a Space an earlier upload
// bound to a Target keeps it, as its TargetID annotation records.
func uploadTargetID(ctx context.Context, c *cubapi.Client, space *goclientnew.Space, slug, targetRef string) (*goclientnew.UUID, error) {
	if targetRef == "" {
		if space == nil || space.Annotations["TargetID"] == "" {
			return nil, nil
		}
		ref := cubapi.ParseRef(space.Annotations["TargetID"])
		if !ref.IsID() {
			return nil, fmt.Errorf("Space %s has a TargetID annotation that is not a UUID: %q", slug, space.Annotations["TargetID"])
		}
		return &ref.ID, nil
	}
	ref := cubapi.ParseRef(targetRef)
	opts := cubapi.ResolveOpts{}
	if !ref.IsID() && ref.Space == "" {
		if space == nil {
			return nil, fmt.Errorf("--target %q names a Target in Space %s, which does not exist yet; name it as <space>/<target> or by UUID", targetRef, slug)
		}
		opts.Space = space.SpaceID
	}
	target, err := cubapi.ResolveTarget(ctx, c, ref, opts)
	if err != nil {
		return nil, fmt.Errorf("resolve --target %s: %w", targetRef, err)
	}
	return &target.Target.TargetID, nil
}

// resolveChangeSet resolves --changeset, looking a bare slug up in the
// parent's Space.
func resolveChangeSet(ctx context.Context, c *cubapi.Client, parentSlug, changeSetRef string) (goclientnew.UUID, error) {
	ref := cubapi.ParseRef(changeSetRef)
	opts := cubapi.ResolveOpts{}
	if !ref.IsID() && ref.Space == "" {
		space, err := existingSpace(ctx, c, parentSlug)
		if err != nil {
			return goclientnew.UUID{}, err
		}
		if space == nil {
			return goclientnew.UUID{}, fmt.Errorf("--changeset %q names a ChangeSet in Space %s, which does not exist yet; name it as <space>/<changeset> or by UUID", changeSetRef, parentSlug)
		}
		opts.Space = space.SpaceID
	}
	changeSet, err := cubapi.ResolveChangeSet(ctx, c, ref, opts)
	if err != nil {
		return goclientnew.UUID{}, fmt.Errorf("resolve --changeset %s: %w", changeSetRef, err)
	}
	return changeSet.ChangeSet.ChangeSetID, nil
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

// loadLockIfNeeded returns the work-dir's lock. A package without dependencies
// has none; a package with dependencies must have an up-to-date one, or the
// upload would target the wrong dependency versions.
func loadLockIfNeeded(workDir string, pkg *api.Package) (*api.Lock, error) {
	if len(pkg.Spec.Dependencies) == 0 {
		return nil, nil
	}
	lock, err := deps.ReadLock(workDir)
	if err != nil {
		return nil, err
	}
	if lock == nil {
		return nil, fmt.Errorf("package declares dependencies but %s does not exist; run `%s deps update --work-dir %s` and `%s render --work-dir %s` first",
			deps.LockPath(workDir), InvocationName(), workDir, InvocationName(), workDir)
	}
	if deps.IsStale(lock, pkg) {
		return nil, fmt.Errorf("lock at %s is stale; run `%s deps update --work-dir %s` and `%s render --work-dir %s` again",
			deps.LockPath(workDir), InvocationName(), workDir, InvocationName(), workDir)
	}
	return lock, nil
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

// installerRecordFetcher returns a wizard.RecordFetcher that reads the
// installer record an earlier upload wrote: the Unit keyed
// AppConfig/YAML/installer that the package's uploads own. With space set it
// reads the record in that Space. Without, it looks across the organization
// and uses the one it finds, refusing when several installs of the package
// exist, since picking one could re-enter someone else's.
func installerRecordFetcher(space string) func(ctx context.Context, packageName string) ([]byte, error) {
	return func(ctx context.Context, packageName string) ([]byte, error) {
		c, err := confighub.Client(ctx)
		if err != nil {
			return nil, err
		}
		where := cubapi.Where{}.
			Eq("Labels.UploadSource", packageName).
			Eq("Annotations.UploadResource", "AppConfig/YAML/"+upload.RecordConfigName)
		if space != "" {
			found, err := existingSpace(ctx, c, space)
			if err != nil {
				return nil, err
			}
			if found == nil {
				return nil, fmt.Errorf("Space %s does not exist", space)
			}
			where = where.Eq("SpaceID", found.SpaceID.String())
		}
		units, err := cubapi.ListUnits(ctx, c, where, cubapi.ListOpts{Include: "SpaceID"})
		if err != nil {
			return nil, err
		}
		switch len(units) {
		case 0:
			return nil, nil
		case 1:
		default:
			var spaces []string
			for _, u := range units {
				if u.Space != nil {
					spaces = append(spaces, u.Space.Slug)
				}
			}
			return nil, fmt.Errorf("%s is installed in %d Spaces (%s); pass --space to choose one",
				packageName, len(units), strings.Join(spaces, ", "))
		}
		unit := units[0].Unit
		data, err := cubapi.UnitData(ctx, c, unit.SpaceID, unit.UnitID)
		if err != nil {
			return nil, err
		}
		return []byte(data), nil
	}
}

// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/confighub/sdk/core/cubapi"
	goclientnew "github.com/confighub/sdk/core/openapi/goclient-new"

	"github.com/confighub/installer/internal/hubclient"
	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/internal/upload"
)

// UploadOptions say where a work-dir's render goes in ConfigHub, and how its
// Spaces and Units are labeled.
type UploadOptions struct {
	// WorkDir is the rendered work-dir; "" is the current directory.
	WorkDir string
	// Space is the parent package's Space. It overrides SpacePattern for the
	// parent; a dependency's Space is still SpacePattern's.
	Space string
	// SpacePattern is a Go template for each package's Space slug, over
	// .PackageName, .PackageVersion and .Variant. "" is {{.PackageName}}.
	SpacePattern string
	// Target is the release Target each package's Space is given:
	// <space>/<target>, a UUID, or a slug in the package's Space. "" leaves
	// each Space's release Target as it is.
	Target string
	// ChangeSet is an existing open ChangeSet every package's writes join:
	// <space>/<changeset>, a UUID, or a slug in the parent's Space. "" gives
	// each Space a new ChangeSet.
	ChangeSet string
	// Component is the parent's Space's Component label; a dependency's is its
	// package name. "" keeps the label the Space has, or else is the package
	// name.
	Component string
	// Variant is every package's Space's Variant label. "" keeps the label each
	// Space has, or else is "base", which is also what SpacePattern's .Variant
	// is.
	Variant string
	// Layer, Environment, Region and Owner set the Spaces' well-known labels
	// of those names when not "".
	Layer, Environment, Region, Owner string
	// SpaceLabels and SpaceAnnotations are set on every package's Space. The
	// well-known labels have their own options, and the TargetID annotation
	// is Target's.
	SpaceLabels, SpaceAnnotations map[string]string
	// UnitLabels and UnitAnnotations are set on every Unit the upload writes.
	UnitLabels, UnitAnnotations map[string]string
}

// defaultVariant is the Variant label of a Space with none of its own.
const defaultVariant = "base"

// reservedSpaceLabels are the well-known Space labels that have options of
// their own.
var reservedSpaceLabels = map[string]bool{
	"Component": true, "Variant": true, "Layer": true, "Environment": true, "Region": true, "Owner": true,
}

// UploadPlan is one upload request per package, in upload order: each locked
// dependency, then the parent. PreviewUpload adds what each would do.
type UploadPlan struct {
	Packages []PlannedPackage

	client *cubapi.Client
	// newComponents are the dependencies' Components that do not exist yet,
	// which the upload creates before the parent's request names them.
	newComponents map[string]bool
}

// PlannedPackage is one package's upload.
type PlannedPackage struct {
	Name     string
	Version  string
	Space    string
	IsParent bool
	// ManifestsDir holds the package's rendered manifests.
	ManifestsDir string
	Request      goclientnew.UploadRequest
	// Preview is what the request would do, once PreviewUpload has run it as
	// a dry run.
	Preview *goclientnew.UploadResult
	// PreviewOmitted are the Components the preview left out of the parent's
	// DependsOn, since they don't exist until the upload creates them.
	PreviewOmitted []string
}

// Emptied lists the Units the previewed upload would empty, as <space>/<unit>.
func (p *UploadPlan) Emptied() []string {
	var previews []*goclientnew.UploadResult
	for _, pkg := range p.Packages {
		if pkg.Preview != nil {
			previews = append(previews, pkg.Preview)
		}
	}
	return upload.Emptied(previews)
}

// PrepareUpload loads the work-dir, finds its packages, resolves the Target
// and ChangeSet, and builds one upload request per package. It writes nothing.
func (i *Installer) PrepareUpload(ctx context.Context, opts UploadOptions) (*UploadPlan, error) {
	for key := range opts.SpaceLabels {
		if reservedSpaceLabels[key] {
			return nil, fmt.Errorf("space label %q is a well-known label, which has an option of its own", key)
		}
	}
	if _, ok := opts.SpaceAnnotations["TargetID"]; ok {
		return nil, fmt.Errorf("space annotation \"TargetID\" is set by the Target option")
	}
	variant := opts.Variant
	if variant == "" {
		variant = defaultVariant
	}

	workDir := opts.WorkDir
	if workDir == "" {
		workDir = "."
	}
	absWork, err := filepath.Abs(workDir)
	if err != nil {
		return nil, err
	}
	loaded, err := ipkg.Load(filepath.Join(absWork, "package"))
	if err != nil {
		return nil, fmt.Errorf("load package: %w", err)
	}
	lock, err := readCurrentLock(absWork, loaded.Package)
	if err != nil {
		return nil, err
	}
	packages, err := upload.Discover(upload.DiscoverInput{
		WorkDir:       absWork,
		SpacePattern:  opts.SpacePattern,
		ParentSpace:   opts.Space,
		Variant:       variant,
		ParentPackage: loaded.Package,
		Lock:          lock,
	})
	if err != nil {
		return nil, err
	}

	client, err := i.verifiedClient(ctx)
	if err != nil {
		return nil, err
	}

	reqOpts := upload.RequestOptions{
		Component:        opts.Component,
		Variant:          variant,
		SpaceLabels:      map[string]string{},
		SpaceAnnotations: copyStrings(opts.SpaceAnnotations),
		UnitLabels:       copyStrings(opts.UnitLabels),
		UnitAnnotations:  copyStrings(opts.UnitAnnotations),
		TargetIDs:        map[string]goclientnew.UUID{},
		ClientVersion:    i.opts.ClientVersion,
	}
	for label, value := range map[string]string{
		"Layer": opts.Layer, "Environment": opts.Environment, "Region": opts.Region, "Owner": opts.Owner,
	} {
		if value != "" {
			reqOpts.SpaceLabels[label] = value
		}
	}
	for k, v := range opts.SpaceLabels {
		reqOpts.SpaceLabels[k] = v
	}

	if opts.ChangeSet != "" {
		id, err := resolveChangeSet(ctx, client, packages[0].SpaceSlug, opts.ChangeSet)
		if err != nil {
			return nil, err
		}
		reqOpts.ChangeSetID = &id
	}

	plan := &UploadPlan{client: client, newComponents: map[string]bool{}}
	for _, pkg := range uploadOrder(packages) {
		space, err := hubclient.ExistingSpace(ctx, client, pkg.SpaceSlug)
		if err != nil {
			return nil, err
		}
		id, err := uploadTargetID(ctx, client, space, pkg.SpaceSlug, opts.Target)
		if err != nil {
			return nil, err
		}
		if id != nil {
			reqOpts.TargetIDs[pkg.SpaceSlug] = *id
		}
		// Every request sets Component and Variant, so a Space an earlier
		// upload labeled keeps those labels unless the options say otherwise.
		pkgOpts := reqOpts
		if space != nil {
			if opts.Component == "" && pkg.IsParent && space.Labels["Component"] != "" {
				pkgOpts.Component = space.Labels["Component"]
			}
			if opts.Variant == "" && space.Labels["Variant"] != "" {
				pkgOpts.Variant = space.Labels["Variant"]
			}
		}
		req, err := upload.BuildRequest(pkg, packages, pkgOpts)
		if err != nil {
			return nil, err
		}
		plan.Packages = append(plan.Packages, PlannedPackage{
			Name:         pkg.Name,
			Version:      pkg.Version,
			Space:        pkg.SpaceSlug,
			IsParent:     pkg.IsParent,
			ManifestsDir: pkg.ManifestsDir,
			Request:      req,
		})

		if !pkg.IsParent {
			name := pkgOpts.ComponentName(pkg)
			exists, err := componentExists(ctx, client, name)
			if err != nil {
				return nil, err
			}
			if !exists {
				plan.newComponents[name] = true
			}
		}
	}
	return plan, nil
}

// PreviewOptions configure PreviewUpload.
type PreviewOptions struct {
	// Mutations asks for each Unit write's Mutations: the paths it would
	// change. The server then runs merges it would otherwise skip.
	Mutations bool
}

// PreviewUpload runs each of the plan's requests as a dry run and records what
// it would do in the package's Preview. When a dry run fails, the packages
// before it keep their Previews.
func (i *Installer) PreviewUpload(ctx context.Context, plan *UploadPlan, opts PreviewOptions) error {
	var with []func(*goclientnew.UploadParams)
	if opts.Mutations {
		with = append(with, cubapi.WithUploadMutations)
	}
	for n := range plan.Packages {
		pkg := &plan.Packages[n]
		req, omitted := plan.dryRunRequest(n)
		preview, err := cubapi.Upload(ctx, plan.client, req, true, with...)
		if err != nil {
			return fmt.Errorf("plan %s: %w", pkg.Name, err)
		}
		pkg.Preview = preview
		pkg.PreviewOmitted = omitted
	}
	return nil
}

// PlanUpload prepares the upload and previews it with each Unit write's
// Mutations, as `installer plan` does. It writes nothing.
func (i *Installer) PlanUpload(ctx context.Context, opts UploadOptions) (*UploadPlan, error) {
	plan, err := i.PrepareUpload(ctx, opts)
	if err != nil {
		return nil, err
	}
	if err := i.PreviewUpload(ctx, plan, PreviewOptions{Mutations: true}); err != nil {
		return nil, err
	}
	return plan, nil
}

// dryRunRequest is request n as a dry run can send it. A dependency's dry run
// creates no Component, so the parent's cannot name one the upload has yet to
// create, and ConfigHub refuses a DependsOn that names a missing Component. It
// leaves those out and returns them. DependsOn only records the dependency on
// the parent's Space, so what the dry run reports for each Unit is unchanged.
func (p *UploadPlan) dryRunRequest(n int) (goclientnew.UploadRequest, []string) {
	req := p.Packages[n].Request
	if len(p.newComponents) == 0 {
		return req, nil
	}
	var omitted []string
	components := make([]goclientnew.UploadComponentRequest, len(req.Components))
	for j, component := range req.Components {
		var kept []string
		for _, name := range component.DependsOn {
			if p.newComponents[name] {
				omitted = append(omitted, name)
			} else {
				kept = append(kept, name)
			}
		}
		component.DependsOn = kept
		components[j] = component
	}
	req.Components = components
	return req, omitted
}

// UploadResult is what an upload did, per package in upload order.
type UploadResult struct {
	Packages []UploadedPackage
}

// UploadedPackage is what one package's upload did.
type UploadedPackage struct {
	Name     string
	Version  string
	Space    string
	IsParent bool
	Result   *goclientnew.UploadResult
	// ChangeSets are the ChangeSets the upload recorded its writes in, per
	// Space it changed; restoring a Space's Units to before one reverts the
	// upload there.
	ChangeSets []UploadChangeSet
}

// UploadChangeSet is the ChangeSet an upload recorded its writes to a Space in.
type UploadChangeSet struct {
	Space string
	// ChangeSet is the ChangeSet's slug, or its ID when it can't be read.
	ChangeSet string
}

// Results returns each package's upload result.
func (r *UploadResult) Results() []*goclientnew.UploadResult {
	out := make([]*goclientnew.UploadResult, 0, len(r.Packages))
	for _, p := range r.Packages {
		out = append(out, p.Result)
	}
	return out
}

// Failed reports whether any Unit or Link write failed. The writes that
// succeeded are real either way, and uploading again retries the failures.
func (r *UploadResult) Failed() bool {
	for _, p := range r.Packages {
		if upload.Failed(p.Result) {
			return true
		}
	}
	return false
}

// Changed reports whether the upload changed anything.
func (r *UploadResult) Changed() bool {
	return upload.Summarize(r.Results()).Changes()
}

// Upload sends the plan's requests, each dependency's before the parent's,
// and hides each installer record Unit. A failed write in one Space doesn't
// stop the others, and is reported in the result. An upload ConfigHub refuses
// outright stops the upload with an error, returned with the result of the
// packages uploaded before it, whose writes are real.
func (i *Installer) Upload(ctx context.Context, plan *UploadPlan) (*UploadResult, error) {
	result := &UploadResult{}
	for _, pkg := range plan.Packages {
		i.printf("== %s@%s → Space %s ==\n", pkg.Name, pkg.Version, pkg.Space)
		res, err := cubapi.Upload(ctx, plan.client, pkg.Request, false)
		if err != nil {
			return result, fmt.Errorf("upload %s: %w", pkg.Name, err)
		}
		upload.Report(i.out, res, false)
		if err := hideRecord(ctx, plan.client, pkg); err != nil {
			i.warn(fmt.Errorf("could not hide the installer record in Space %s: %w", pkg.Space, err))
		}
		result.Packages = append(result.Packages, UploadedPackage{
			Name:       pkg.Name,
			Version:    pkg.Version,
			Space:      pkg.Space,
			IsParent:   pkg.IsParent,
			Result:     res,
			ChangeSets: changeSets(ctx, plan.client, res),
		})
	}
	return result, nil
}

// changeSets returns the ChangeSets an upload that changed anything recorded
// its writes in.
func changeSets(ctx context.Context, c *cubapi.Client, result *goclientnew.UploadResult) []UploadChangeSet {
	if !upload.Summarize([]*goclientnew.UploadResult{result}).Changes() {
		return nil
	}
	var out []UploadChangeSet
	for _, comp := range result.Components {
		for _, s := range comp.Spaces {
			if s.ChangeSetID == nil {
				continue
			}
			ref := s.ChangeSetID.String()
			if cs, err := cubapi.ResolveChangeSet(ctx, c, cubapi.RefFromID(*s.ChangeSetID), cubapi.ResolveOpts{}); err == nil && cs.ChangeSet != nil {
				ref = cs.ChangeSet.Slug
			}
			out = append(out, UploadChangeSet{Space: s.SpaceSlug, ChangeSet: ref})
		}
	}
	return out
}

// uploadOrder is the order packages are uploaded in: each dependency, then the
// parent. ConfigHub resolves the Components the parent's DependsOn names when
// the parent is uploaded, so the dependencies' uploads have to create them first.
func uploadOrder(packages []upload.Package) []upload.Package {
	ordered := make([]upload.Package, 0, len(packages))
	for _, pkg := range packages {
		if !pkg.IsParent {
			ordered = append(ordered, pkg)
		}
	}
	for _, pkg := range packages {
		if pkg.IsParent {
			ordered = append(ordered, pkg)
		}
	}
	return ordered
}

// componentExists reports whether the Component name exists in the organization.
func componentExists(ctx context.Context, c *cubapi.Client, name string) (bool, error) {
	_, err := cubapi.ResolveComponent(ctx, c, cubapi.NewRef("", name), cubapi.ResolveOpts{})
	if cubapi.IsNotFoundError(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("look up Component %s: %w", name, err)
	}
	return true, nil
}

// uploadTargetID resolves the release Target of a package's Space. A bare slug
// is looked up in the package's Space, so that Space has to exist already.
// Without a Target there is none to set: the Space keeps the release Target an
// earlier upload, or anyone else, gave it, and its Units take that.
func uploadTargetID(ctx context.Context, c *cubapi.Client, space *goclientnew.Space, slug, targetRef string) (*goclientnew.UUID, error) {
	if targetRef == "" {
		return nil, nil
	}
	ref := cubapi.ParseRef(targetRef)
	opts := cubapi.ResolveOpts{}
	if !ref.IsID() && ref.Space == "" {
		if space == nil {
			return nil, fmt.Errorf("Target %q names a Target in Space %s, which does not exist yet; name it as <space>/<target> or by UUID", targetRef, slug)
		}
		opts.Space = space.SpaceID
	}
	target, err := cubapi.ResolveTarget(ctx, c, ref, opts)
	if err != nil {
		return nil, fmt.Errorf("resolve Target %s: %w", targetRef, err)
	}
	return &target.Target.TargetID, nil
}

// resolveChangeSet resolves a ChangeSet reference, looking a bare slug up in
// the parent's Space.
func resolveChangeSet(ctx context.Context, c *cubapi.Client, parentSlug, changeSetRef string) (goclientnew.UUID, error) {
	ref := cubapi.ParseRef(changeSetRef)
	opts := cubapi.ResolveOpts{}
	if !ref.IsID() && ref.Space == "" {
		space, err := hubclient.ExistingSpace(ctx, c, parentSlug)
		if err != nil {
			return goclientnew.UUID{}, err
		}
		if space == nil {
			return goclientnew.UUID{}, fmt.Errorf("ChangeSet %q names a ChangeSet in Space %s, which does not exist yet; name it as <space>/<changeset> or by UUID", changeSetRef, parentSlug)
		}
		opts.Space = space.SpaceID
	}
	changeSet, err := cubapi.ResolveChangeSet(ctx, c, ref, opts)
	if err != nil {
		return goclientnew.UUID{}, fmt.Errorf("resolve ChangeSet %s: %w", changeSetRef, err)
	}
	return changeSet.ChangeSet.ChangeSetID, nil
}

// hideRecord hides the installer record Unit an upload of pkg wrote, as a dot
// file is hidden: it is there for the installer to re-enter the install, and
// users rarely need to see it among the package's Units. A record already
// hidden, for this reason or another someone gave it, is left alone.
func hideRecord(ctx context.Context, c *cubapi.Client, pkg PlannedPackage) error {
	space, err := hubclient.ExistingSpace(ctx, c, pkg.Space)
	if err != nil || space == nil {
		return err
	}
	where := cubapi.Where{}.
		Eq("SpaceID", space.SpaceID.String()).
		Eq("Labels.UploadSource", pkg.Name).
		Eq("Annotations.UploadResource", "AppConfig/YAML/"+upload.RecordConfigName)
	units, err := cubapi.ListUnits(ctx, c, where, cubapi.ListOpts{IncludeHidden: "*"})
	if err != nil {
		return err
	}
	for _, u := range units {
		if u.Unit.HiddenReason != "" {
			continue
		}
		body, err := json.Marshal(map[string]string{"HiddenReason": upload.RecordHiddenReason})
		if err != nil {
			return err
		}
		res, err := c.API.PatchUnitWithBodyWithResponse(ctx, u.Unit.SpaceID, u.Unit.UnitID, nil,
			"application/merge-patch+json", bytes.NewReader(body))
		if cubapi.IsAPIError(err, res) {
			return cubapi.InterpretErrorGeneric(err, res)
		}
	}
	return nil
}

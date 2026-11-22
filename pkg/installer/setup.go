// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/AlecAivazis/survey/v2/terminal"

	"github.com/confighub/installer/internal/deps"
	"github.com/confighub/installer/internal/hubclient"
	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/internal/render"
	"github.com/confighub/installer/internal/upload"
	"github.com/confighub/installer/internal/wizard"
	"github.com/confighub/installer/pkg/api"
)

// SetupOptions say what to install, and how to answer the package's wizard.
type SetupOptions struct {
	// WorkDir holds the install's state; it is created if missing. "" is the
	// current directory.
	WorkDir string
	// Pull is the package to install or upgrade to: an oci:// reference, a
	// local directory, or a .tgz. It replaces <WorkDir>/package. "" uses the
	// package already there, or the one the installer record in Space names.
	Pull string
	// Space is the Space whose installer record a work-dir with no prior
	// state of its own re-enters. "" looks for the package's only install in
	// the organization. A Space that doesn't exist yet, or holds no installer
	// record, is a fresh install.
	Space string

	// Base is the package's base to install; "" is its default base, or the
	// prior install's.
	Base string
	// Namespace is the install namespace. A fresh install that doesn't prompt
	// needs one; an upgrade keeps the prior install's when it is "".
	Namespace string
	// Select names the components to install, closed over their requirements.
	Select []string
	// Preset picks components instead of Select: minimal, default, all, or
	// selected.
	Preset string
	// Inputs answers the package's inputs, by name, over the prior install's.
	Inputs map[string]string
	// SetImage overrides images, by name without tag or digest, as
	// `kustomize edit set image` would, over the prior install's overrides.
	SetImage map[string]string
	// Reuse takes the prior install's choices as they are, without prompting.
	Reuse bool
	// Terminal is where the wizard prompts; nil never prompts.
	Terminal *Terminal

	// Render also locks the package's dependencies and renders it.
	Render bool
	// OutputOCI also publishes the render as a rendered OCI artifact: a local
	// directory for an OCI image layout, or oci://host/repo:tag. It requires
	// Render.
	OutputOCI string
}

// Terminal is the terminal the wizard prompts on.
type Terminal struct {
	In interface {
		io.Reader
		Fd() uintptr
	}
	Out interface {
		io.Writer
		Fd() uintptr
	}
	Err io.Writer
}

func (t *Terminal) askOptions() wizard.AskOptions {
	if t == nil {
		return wizard.AskOptions{}
	}
	return wizard.AskOptions{Stdio: &terminal.Stdio{In: t.In, Out: t.Out, Err: t.Err}}
}

// PriorSource names where an install's prior choices came from.
type PriorSource string

const (
	// PriorNone: there were none; this is a fresh install.
	PriorNone PriorSource = PriorSource(wizard.SourceNone)
	// PriorLocal: the work-dir's own record of its last render.
	PriorLocal PriorSource = PriorSource(wizard.SourceLocal)
	// PriorConfigHub: the installer record an earlier upload left in ConfigHub.
	PriorConfigHub PriorSource = PriorSource(wizard.SourceConfigHub)
)

// SetupResult is what Setup chose and wrote.
type SetupResult struct {
	// WorkDir is the absolute work-dir.
	WorkDir     string
	Package     *api.Package
	PriorSource PriorSource
	Selection   *api.Selection
	Inputs      *api.Inputs
	// Facts is nil when the package has no collector.
	Facts *api.Facts
	// Lock is nil when the package has no dependencies or Render is false.
	Lock *api.Lock
	// Render is nil unless SetupOptions.Render.
	Render *RenderResult
	// RenderedOCI is nil unless SetupOptions.OutputOCI.
	RenderedOCI *RenderedOCI
}

// RenderResult lists the files a render wrote.
type RenderResult struct {
	// Manifests are the rendered resources' files, under <WorkDir>/out/manifests.
	Manifests []string
	// Secrets are the rendered Secrets' files, under <WorkDir>/out/secrets,
	// which are never uploaded.
	Secrets []string
	// Dependencies are each locked dependency's render, in lock order.
	Dependencies []DependencyRender
}

// DependencyRender lists the files a dependency's render wrote, under
// <WorkDir>/out/<Name>.
type DependencyRender struct {
	Name      string
	Manifests []string
	Secrets   []string
}

// RenderedOCI describes the rendered OCI artifact Setup published.
type RenderedOCI struct {
	Ref             string
	ManifestDigest  string
	ObjectSetDigest string
	ManifestCount   int
}

// Setup pulls the package when asked, loads the install's prior choices,
// answers the package's wizard from them and the options, and, with Render,
// locks the dependencies, renders, and publishes the rendered OCI artifact.
// It is `installer setup`, or `installer wizard` without Render.
func (i *Installer) Setup(ctx context.Context, opts SetupOptions) (*SetupResult, error) {
	if opts.Preset != "" && len(opts.Select) > 0 {
		return nil, ErrPresetAndSelect
	}
	if opts.Render {
		if err := i.checkTransformer(); err != nil {
			return nil, err
		}
	}
	workDir := opts.WorkDir
	if workDir == "" {
		workDir = "."
	}
	absWork, err := filepath.Abs(workDir)
	if err != nil {
		return nil, err
	}

	pkgDir := filepath.Join(absWork, "package")
	sourceReference := ""
	sourceDigest := ""
	pullRef := opts.Pull
	if opts.OutputOCI != "" && strings.HasPrefix(opts.Pull, "oci://") {
		sourceReference = opts.Pull
		sourceDigest, err = i.registry.ResolveManifestDigest(ctx, opts.Pull)
		if err != nil {
			return nil, fmt.Errorf("resolve source package digest: %w", err)
		}
		if !strings.Contains(pullRef, "@") {
			pullRef += "@" + sourceDigest
		}
	}
	// A work-dir with no package re-enters the install in Space from its
	// installer record alone, pulling the package the record says it came from.
	recordedPull := false
	if pullRef == "" && opts.Space != "" {
		if _, err := os.Stat(filepath.Join(pkgDir, ipkg.ManifestFile)); os.IsNotExist(err) {
			pullRef, err = i.recordedSourceRef(ctx, opts.Space)
			if err != nil {
				return nil, err
			}
			recordedPull = true
			i.printf("Pulling %s, which the installer record in Space %s says the install came from.\n", pullRef, opts.Space)
		}
	}
	// A pulled package is staged beside the work-dir's own until the answers
	// for it are worked out: the prior install's choices are compared with
	// the package that made them, and an upgrade that can't be answered
	// leaves the work-dir as it was.
	var staged *ipkg.StagedPull
	if pullRef != "" {
		staged, err = i.registry.StagePull(ctx, pullRef, absWork)
		if err != nil {
			if recordedPull {
				return nil, optionErrorf(ErrRecordedSourcePull, err, "pull %s, from the installer record in Space %s: %v",
					pullRef, opts.Space, err)
			}
			return nil, fmt.Errorf("pull %s: %w", pullRef, err)
		}
		defer staged.Discard()
	}
	var loaded *ipkg.Loaded
	if staged != nil {
		if loaded, err = ipkg.Load(staged.Root); err != nil {
			return nil, fmt.Errorf("load package pulled from %s: %w", pullRef, err)
		}
	} else if loaded, err = ipkg.Load(pkgDir); err != nil {
		if os.IsNotExist(rootCause(err)) {
			return nil, optionErrorf(ErrNoPackage, err, "no package found in %s", pkgDir)
		}
		return nil, fmt.Errorf("load package from %s: %w", pkgDir, err)
	}

	prior, source, err := wizard.LoadPriorState(ctx, absWork, loaded.Package.Metadata.Name, i.recordFetcher(opts.Space), i.warn)
	if err != nil {
		return nil, fmt.Errorf("load prior state: %w", err)
	}
	if source != wizard.SourceNone {
		i.printf("Loaded prior install state from %s.\n", source)
	}

	raw, err := i.buildAnswers(loaded.Package, prior, opts)
	if err != nil {
		return nil, err
	}
	if staged != nil {
		if _, err := staged.Promote(ctx); err != nil {
			return nil, fmt.Errorf("pull %s: %w", pullRef, err)
		}
		if loaded, err = ipkg.Load(pkgDir); err != nil {
			return nil, fmt.Errorf("load package from %s: %w", pkgDir, err)
		}
	}
	raw.ImageOverrides = copyStrings(opts.SetImage)
	raw.CollectorStderr = i.err
	if prior != nil && prior.Inputs != nil {
		raw.PriorImageOverrides = prior.Inputs.Spec.ImageOverrides
	}

	outDir := filepath.Join(absWork, "out")
	res, err := wizard.Run(ctx, loaded.Package, raw, loaded.Root, outDir)
	if err != nil {
		return nil, err
	}
	i.printf("Wizard wrote %s/record/selection.yaml and inputs.yaml\n", outDir)
	if res.Facts != nil {
		i.printf("Collector produced %d fact(s) in %s/record/facts.yaml\n", len(res.Facts.Spec.Values), outDir)
	}
	i.printf("Base: %s; components: %v\n", res.Selection.Spec.Base, res.Selection.Spec.Components)
	i.printf("Namespace: %s\n", raw.Namespace)

	result := &SetupResult{
		WorkDir:     absWork,
		Package:     loaded.Package,
		PriorSource: PriorSource(source),
		Selection:   res.Selection,
		Inputs:      res.Inputs,
		Facts:       res.Facts,
	}
	if !opts.Render {
		return result, nil
	}

	var lock *api.Lock
	if len(loaded.Package.Spec.Dependencies) > 0 {
		if lock, err = i.lockDependencies(ctx, absWork, loaded.Package, res.Selection); err != nil {
			return nil, err
		}
		result.Lock = lock
	}

	if result.Render, err = i.render(ctx, absWork, loaded, res.Selection, res.Inputs, res.Facts, lock, false); err != nil {
		return nil, err
	}

	if opts.OutputOCI != "" {
		published, err := i.registry.PublishRenderedOCI(ctx, ipkg.RenderedOCIOptions{
			WorkDir:          absWork,
			Destination:      opts.OutputOCI,
			SourceReference:  sourceReference,
			SourceDigest:     sourceDigest,
			Package:          loaded.Package,
			Selection:        res.Selection,
			Inputs:           res.Inputs,
			InstallerVersion: i.opts.ClientVersion,
		})
		if err != nil {
			return nil, fmt.Errorf("write rendered OCI: %w", err)
		}
		i.printf("Wrote rendered OCI %s\n", published.Ref)
		i.printf("  manifest:  %s\n", published.ManifestDigest)
		i.printf("  objects:   %s (%d manifest files)\n", published.ObjectSetDigest, published.ManifestCount)
		i.printf("  pull-back: verified\n")
		result.RenderedOCI = &RenderedOCI{
			Ref:             published.Ref,
			ManifestDigest:  published.ManifestDigest,
			ObjectSetDigest: published.ObjectSetDigest,
			ManifestCount:   published.ManifestCount,
		}
	}
	return result, nil
}

// buildAnswers picks between the fresh-install builder (when no prior state
// exists) and the upgrade builder (when it does). Both translate prior values
// and the options into the wizard.RawAnswers the wizard expects.
func (i *Installer) buildAnswers(pkg *api.Package, prior *wizard.PriorState, opts SetupOptions) (wizard.RawAnswers, error) {
	if hasPriorChoices(prior) {
		return i.buildUpgradeAnswers(pkg, prior, opts)
	}
	return buildFreshAnswers(pkg, prior, opts)
}

// hasPriorChoices reports whether the prior state contains usable selection or
// inputs to carry forward, which gates whether the upgrade schema diff runs.
func hasPriorChoices(prior *wizard.PriorState) bool {
	if prior == nil {
		return false
	}
	return prior.Selection != nil || prior.Inputs != nil
}

// buildFreshAnswers assembles wizard.RawAnswers when there is no prior state,
// in priority order: Reuse, then prompts, then the options.
func buildFreshAnswers(pkg *api.Package, prior *wizard.PriorState, opts SetupOptions) (wizard.RawAnswers, error) {
	if opts.Reuse {
		if prior == nil || (prior.Selection == nil && prior.Inputs == nil) {
			return wizard.RawAnswers{}, ErrNoPriorInstall
		}
		return wizard.RawAnswersFromPrior(pkg, prior), nil
	}

	if opts.Terminal != nil {
		raw, err := wizard.Ask(pkg, prior, opts.Terminal.askOptions())
		if err != nil {
			return wizard.RawAnswers{}, err
		}
		if opts.Base != "" {
			raw.BaseName = opts.Base
		}
		if opts.Namespace != "" {
			raw.Namespace = opts.Namespace
		}
		if len(opts.Select) > 0 {
			raw.SelectedComponents = opts.Select
		}
		for k, v := range opts.Inputs {
			raw.Inputs[k] = v
		}
		return raw, nil
	}

	if opts.Namespace == "" {
		return wizard.RawAnswers{}, ErrNamespaceRequired
	}
	raw := wizard.RawAnswers{
		Inputs:             copyStrings(opts.Inputs),
		SelectedComponents: opts.Select,
		BaseName:           opts.Base,
		Namespace:          opts.Namespace,
	}
	if raw.Inputs == nil {
		raw.Inputs = map[string]string{}
	}
	if opts.Preset != "" {
		picked, err := wizard.ResolvePreset(pkg, opts.Preset)
		if err != nil {
			return wizard.RawAnswers{}, err
		}
		raw.SelectedComponents = picked
	}
	return raw, nil
}

// buildUpgradeAnswers translates prior state and the new package's input
// schema into RawAnswers by schema diff: it carries forward values that still
// apply, adopts new defaults, drops removed inputs, and prompts (on a
// Terminal) or fails for new required inputs without defaults.
//
// The options override the carried-forward values: Namespace, Base, Select and
// Inputs override the prior install's, and Preset overrides Select.
func (i *Installer) buildUpgradeAnswers(newPkg *api.Package, prior *wizard.PriorState, opts SetupOptions) (wizard.RawAnswers, error) {
	if opts.Reuse {
		return wizard.RawAnswersFromPrior(newPkg, prior), nil
	}

	out := wizard.RawAnswers{Inputs: map[string]string{}}

	priorPkg := prior.PriorPackage
	if priorPkg == nil {
		priorPkg = newPkg
	}

	priorVals := map[string]any{}
	if prior.Inputs != nil {
		priorVals = prior.Inputs.Spec.Values
	}
	id := wizard.DiffInputs(priorPkg, newPkg, priorVals)

	// An input the options answer needs no carried-forward value, prompt or
	// default.
	typeChanged := unanswered(id.TypeChanged, opts.Inputs)
	newRequired := unanswered(id.NewRequired, opts.Inputs)
	if len(typeChanged) != 0 && opts.Terminal == nil {
		names := make([]string, 0, len(typeChanged))
		for _, in := range typeChanged {
			names = append(names, in.Name)
		}
		return out, optionErrorf(ErrInputTypesChanged, nil, "input type(s) changed across upgrade for: %s", strings.Join(names, ", "))
	}
	for k, v := range id.Carry {
		out.Inputs[k] = wizard.StringifyAny(v)
	}
	for k, v := range id.AdoptedDefaults {
		out.Inputs[k] = wizard.StringifyAny(v)
		i.printf("Adopted new default for input %q: %v\n", k, v)
	}
	for _, k := range id.Dropped {
		i.printf("Dropped removed input %q (was: %v)\n", k, priorVals[k])
	}
	if len(newRequired) != 0 && opts.Terminal == nil {
		return out, &NewRequiredInputsError{Inputs: newRequired}
	}
	for _, in := range typeChanged {
		ra, err := wizard.AskOneInput(in, opts.Terminal.askOptions())
		if err != nil {
			return out, err
		}
		out.Inputs[in.Name] = ra
		i.printf("Answered input %q, whose type changed\n", in.Name)
	}
	for _, in := range newRequired {
		ra, err := wizard.AskOneInput(in, opts.Terminal.askOptions())
		if err != nil {
			return out, err
		}
		out.Inputs[in.Name] = ra
		i.printf("Answered new required input %q\n", in.Name)
	}

	priorSel := []string{}
	if prior.Selection != nil {
		priorSel = prior.Selection.Spec.Components
	}
	cd := wizard.DiffComponents(priorPkg, newPkg, priorSel)
	out.SelectedComponents = cd.Components
	if len(cd.AdoptedNewDefaults) != 0 {
		i.printf("Adopted new default-flagged component(s): %s\n", strings.Join(cd.AdoptedNewDefaults, ", "))
	}
	if len(cd.RemovedFromPrior) != 0 {
		i.printf("Dropped component(s) no longer in package: %s\n", strings.Join(cd.RemovedFromPrior, ", "))
	}

	if prior.Selection != nil {
		out.BaseName = prior.Selection.Spec.Base
	}
	if prior.Inputs != nil {
		out.Namespace = prior.Inputs.Spec.Namespace
	}

	if opts.Base != "" {
		out.BaseName = opts.Base
	}
	if opts.Namespace != "" {
		out.Namespace = opts.Namespace
	}
	if len(opts.Select) > 0 {
		out.SelectedComponents = opts.Select
	}
	if opts.Preset != "" {
		picked, err := wizard.ResolvePreset(newPkg, opts.Preset)
		if err != nil {
			return wizard.RawAnswers{}, err
		}
		out.SelectedComponents = picked
	}
	for k, v := range opts.Inputs {
		out.Inputs[k] = v
	}
	return out, nil
}

// unanswered returns the inputs that answers has no value for.
func unanswered(inputs []api.Input, answers map[string]string) []api.Input {
	var out []api.Input
	for _, in := range inputs {
		if _, ok := answers[in.Name]; !ok {
			out = append(out, in)
		}
	}
	return out
}

// lockDependencies resolves the dependency DAG against the OCI registry and
// writes <workDir>/out/record/lock.yaml, following the optional dependencies
// the selection turns on.
func (i *Installer) lockDependencies(ctx context.Context, workDir string, pkg *api.Package, sel *api.Selection) (*api.Lock, error) {
	res, err := deps.Resolve(ctx, pkg, deps.OCISource{Registry: i.registry},
		deps.Options{Selection: sel, Verifier: i.registry.Verifier})
	if err != nil {
		return nil, fmt.Errorf("deps update: %w", err)
	}
	if err := deps.WriteLock(workDir, pkg, res.Lock); err != nil {
		return nil, err
	}
	i.printf("Locked %d dependency(ies) to %s\n", len(res.Lock.Spec.Resolved), deps.LockPath(workDir))
	return res.Lock, nil
}

// recordFetcher returns a wizard.RecordFetcher that reads the installer record
// an earlier upload wrote, in space or, when space is "", the package's only
// install in the organization. Without a ConfigHub client it returns nil
// unless a Space is named: re-entering from ConfigHub is then not asked for.
func (i *Installer) recordFetcher(space string) wizard.RecordFetcher {
	if i.opts.ConfigHub == nil && space == "" {
		return nil
	}
	return func(ctx context.Context, packageName string) ([]byte, error) {
		c, err := i.client()
		if err != nil {
			return nil, err
		}
		found, err := hubclient.FindInstallerRecord(ctx, c, space, packageName)
		if err != nil || found == nil {
			return nil, err
		}
		return found.Data, nil
	}
}

// recordedSourceRef returns the reference that pulls the package an install's
// installer record in space says it came from.
func (i *Installer) recordedSourceRef(ctx context.Context, space string) (string, error) {
	data, err := i.recordFetcher(space)(ctx, "")
	if err != nil {
		return "", fmt.Errorf("read the installer record in Space %s: %w", space, err)
	}
	if data == nil {
		return "", optionErrorf(ErrNoInstallerRecord, nil, "Space %s does not exist or has no installer record", space)
	}
	rec, err := upload.ParseRecord(data)
	if err != nil {
		return "", err
	}
	if rec.Source == nil || rec.Source.Ref == "" {
		return "", optionErrorf(ErrNoRecordedSource, nil, "the installer record in Space %s doesn't say where its package came from", space)
	}
	return rec.Source.PinnedRef(), nil
}

// Render renders the work-dir's package from the selection, inputs and facts
// in out/record, and its locked dependencies, as `installer render` does.
func (i *Installer) Render(ctx context.Context, workDir string) (*RenderResult, error) {
	if err := i.checkTransformer(); err != nil {
		return nil, err
	}
	absWork, err := filepath.Abs(workDir)
	if err != nil {
		return nil, err
	}
	pkgDir := filepath.Join(absWork, "package")
	recordDir := filepath.Join(absWork, "out", api.RecordDir)

	loaded, err := ipkg.Load(pkgDir)
	if err != nil {
		return nil, fmt.Errorf("load package from %s: %w", pkgDir, err)
	}
	sel, err := readSelection(filepath.Join(recordDir, "selection.yaml"))
	if err != nil {
		return nil, err
	}
	inputs, err := readInputs(filepath.Join(recordDir, "inputs.yaml"))
	if err != nil {
		return nil, err
	}
	facts, err := readFacts(filepath.Join(recordDir, "facts.yaml"))
	if err != nil {
		return nil, err
	}
	lock, err := readCurrentLock(absWork, loaded.Package)
	if err != nil {
		return nil, err
	}
	return i.render(ctx, absWork, loaded, sel, inputs, facts, lock, true)
}

// readCurrentLock returns the work-dir's lock. A package without dependencies
// has none; a package with dependencies must have one that matches them.
func readCurrentLock(workDir string, pkg *api.Package) (*api.Lock, error) {
	if len(pkg.Spec.Dependencies) == 0 {
		return nil, nil
	}
	lock, err := deps.ReadLock(workDir)
	if err != nil {
		return nil, err
	}
	if lock == nil {
		return nil, optionErrorf(ErrNoLock, nil, "package declares dependencies but %s does not exist", deps.LockPath(workDir))
	}
	if deps.IsStale(lock, pkg) {
		return nil, optionErrorf(ErrStaleLock, nil, "lock at %s is stale (installer.yaml's dependencies have changed)", deps.LockPath(workDir))
	}
	return lock, nil
}

// render renders the package, and with a lock its dependencies, into the
// work-dir.
func (i *Installer) render(ctx context.Context, workDir string, loaded *ipkg.Loaded, sel *api.Selection,
	inputs *api.Inputs, facts *api.Facts, lock *api.Lock, reportRecord bool) (*RenderResult, error) {
	outDir := filepath.Join(workDir, "out")
	manifestsDir := filepath.Join(outDir, "manifests")
	r, err := render.Render(ctx, render.Options{
		Loaded:             loaded,
		Selection:          sel,
		Inputs:             inputs,
		Facts:              facts,
		TransformerCommand: i.opts.TransformerCommand,
	}, outDir)
	if err != nil {
		return nil, err
	}
	if notice := r.UnusedNamespaceWarning(inputs.Spec.Namespace); notice != "" {
		fmt.Fprintln(i.err, notice)
	}
	i.printf("Rendered %d manifest(s) to %s\n", len(r.Manifests), manifestsDir)
	if len(r.Secrets) > 0 {
		i.printf("Rendered %d secret(s) to %s/secrets (not uploaded)\n", len(r.Secrets), outDir)
	}
	if reportRecord {
		i.printf("Spec docs in %s\n", filepath.Join(outDir, api.RecordDir))
	}
	result := &RenderResult{
		Manifests: filePaths(manifestsDir, r.Manifests),
		Secrets:   filePaths(filepath.Join(outDir, "secrets"), r.Secrets),
	}
	if lock == nil {
		return result, nil
	}
	depResults, err := render.RenderDependencies(ctx, render.DepsOptions{
		Lock:               lock,
		ParentInputs:       inputs,
		WorkDir:            workDir,
		Registry:           i.registry,
		TransformerCommand: i.opts.TransformerCommand,
	})
	if err != nil {
		return nil, err
	}
	for _, dr := range depResults {
		depManifests := filepath.Join(dr.OutDir, "manifests")
		i.printf("Rendered dep %s: %d manifest(s) to %s\n", dr.Name, len(dr.Manifests), depManifests)
		if len(dr.Secrets) > 0 {
			i.printf("  secrets: %d (not uploaded)\n", len(dr.Secrets))
		}
		result.Dependencies = append(result.Dependencies, DependencyRender{
			Name:      dr.Name,
			Manifests: filePaths(depManifests, dr.Manifests),
			Secrets:   filePaths(filepath.Join(dr.OutDir, "secrets"), dr.Secrets),
		})
	}
	return result, nil
}

func filePaths(dir string, files []render.File) []string {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, filepath.Join(dir, f.Filename))
	}
	return paths
}

// readSelection reads a selection.yaml.
func readSelection(path string) (*api.Selection, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return api.ParseSelection(data)
}

// readInputs reads an inputs.yaml.
func readInputs(path string) (*api.Inputs, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return api.ParseInputs(data)
}

// readFacts reads a facts.yaml, returning nil when there is none.
func readFacts(path string) (*api.Facts, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return api.ParseFacts(data)
}

// rootCause unwraps wrapped errors until it finds one os.IsNotExist
// understands. Used to surface "no package directory yet" cleanly.
func rootCause(err error) error {
	for {
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		next := u.Unwrap()
		if next == nil {
			return err
		}
		err = next
	}
}

func copyStrings(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/confighub/installer/internal/cubctx"
	"github.com/confighub/installer/internal/deps"
	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/internal/render"
	"github.com/confighub/installer/internal/wizard"
	"github.com/confighub/installer/pkg/api"
)

// flowOptions configures the shared pull+wizard+render flow used by
// both `setup` and `wizard`.
type flowOptions struct {
	workDir string
	pullRef string // empty means: do not pull; require an existing <work-dir>/package/.

	// Wizard answer flags.
	baseName       string
	namespace      string
	selectFlags    []string
	inputFlags     []string
	nonInteractive bool
	reuse          bool
	preset         string
	setImage       []string

	// Render controls.
	runRender bool
	clean     bool
	outputOCI string
}

// runFlow is the shared body of `installer setup` and `installer wizard`.
// It pulls (if pullRef is set), loads prior state, builds answers,
// runs the wizard, optionally re-resolves deps, and optionally renders.
func runFlow(ctx context.Context, opts flowOptions) error {
	if opts.workDir == "" {
		opts.workDir = "."
	}
	absWork, err := filepath.Abs(opts.workDir)
	if err != nil {
		return err
	}

	pkgDir := filepath.Join(absWork, "package")
	sourceReference := ""
	sourceDigest := ""
	pullRef := opts.pullRef
	if opts.outputOCI != "" && strings.HasPrefix(opts.pullRef, "oci://") {
		sourceReference = opts.pullRef
		sourceDigest, err = ipkg.ResolveManifestDigest(ctx, opts.pullRef)
		if err != nil {
			return fmt.Errorf("resolve source package digest: %w", err)
		}
		if !strings.Contains(pullRef, "@") {
			pullRef += "@" + sourceDigest
		}
	}
	if pullRef != "" {
		if _, err := ipkg.PullToWorkDir(ctx, pullRef, absWork); err != nil {
			return fmt.Errorf("pull %s: %w", pullRef, err)
		}
	}
	loaded, err := ipkg.Load(pkgDir)
	if err != nil {
		if opts.pullRef == "" && os.IsNotExist(rootCause(err)) {
			return fmt.Errorf(
				"no package found in %s — pass --pull <ref> to fetch one, or `%s pull <ref> --work-dir %s` first",
				pkgDir, InvocationName(), absWork)
		}
		return fmt.Errorf("load package from %s: %w", pkgDir, err)
	}

	interactive := !opts.nonInteractive && term.IsTerminal(int(os.Stdin.Fd()))

	prior, source, err := wizard.LoadPriorState(ctx, absWork, func(msg string) {
		fmt.Fprintln(os.Stderr, "warning:", msg)
	})
	if err != nil {
		return fmt.Errorf("load prior state: %w", err)
	}
	if prior != nil && prior.Upload != nil {
		if err := cubctx.CheckMatches(ctx, prior.Upload.Spec.OrganizationID, prior.Upload.Spec.Server); err != nil {
			return err
		}
	}
	if source != wizard.SourceNone {
		fmt.Printf("Loaded prior install state from %s.\n", source)
	}

	raw, err := buildAnswers(loaded.Package, prior, interactive, absWork, opts)
	if err != nil {
		return err
	}
	imgOverrides, err := wizard.ParseSetImageFlags(opts.setImage)
	if err != nil {
		return err
	}
	raw.ImageOverrides = imgOverrides
	if prior != nil && prior.Inputs != nil {
		raw.PriorImageOverrides = prior.Inputs.Spec.ImageOverrides
	}

	outDir := filepath.Join(absWork, "out")
	res, err := wizard.Run(ctx, loaded.Package, raw, loaded.Root, outDir)
	if err != nil {
		return err
	}
	fmt.Printf("Wizard wrote %s/spec/selection.yaml and inputs.yaml\n", outDir)
	if res.Facts != nil {
		fmt.Printf("Collector produced %d fact(s) in %s/spec/facts.yaml\n", len(res.Facts.Spec.Values), outDir)
	}
	fmt.Printf("Base: %s; components: %v\n", res.Selection.Spec.Base, res.Selection.Spec.Components)
	fmt.Printf("Namespace: %s\n", raw.Namespace)

	if !opts.runRender {
		fmt.Printf("Next: %s render --work-dir %s\n", InvocationName(), absWork)
		return nil
	}

	if len(loaded.Package.Spec.Dependencies) > 0 {
		if err := runDepsUpdate(ctx, absWork, loaded.Package, res.Selection); err != nil {
			return err
		}
	}

	if err := runRenderInWorkDir(ctx, absWork, loaded, res, opts.clean); err != nil {
		return err
	}

	if opts.outputOCI != "" {
		published, err := ipkg.PublishRenderedOCI(ctx, ipkg.RenderedOCIOptions{
			WorkDir:         absWork,
			Destination:     opts.outputOCI,
			SourceReference: sourceReference,
			SourceDigest:    sourceDigest,
			Package:         loaded.Package,
			Selection:       res.Selection,
			Inputs:          res.Inputs,
		})
		if err != nil {
			return fmt.Errorf("write rendered OCI: %w", err)
		}
		fmt.Printf("Wrote rendered OCI %s\n", published.Ref)
		fmt.Printf("  manifest:  %s\n", published.ManifestDigest)
		fmt.Printf("  objects:   %s (%d manifest files)\n", published.ObjectSetDigest, published.ManifestCount)
		fmt.Printf("  pull-back: verified\n")
	}

	fmt.Printf("Next: %s upload --work-dir %s --space <slug>\n", InvocationName(), absWork)
	return nil
}

// buildAnswers picks between the fresh-install builder (when no prior
// state exists) and the upgrade-style builder (when it does). The two
// builders both validate flag combinations and translate prior values
// + CLI flags into the wizard.RawAnswers the wizard expects.
func buildAnswers(pkg *api.Package, prior *wizard.PriorState, interactive bool, workDir string, opts flowOptions) (wizard.RawAnswers, error) {
	if hasPriorChoices(prior) {
		return buildUpgradeAnswers(pkg, prior, interactive, workDir, opts)
	}
	return buildFreshAnswers(pkg, prior, interactive, opts)
}

// hasPriorChoices reports whether the prior state contains usable
// selection or inputs to carry forward — gates whether the upgrade
// schema-diff path runs.
func hasPriorChoices(prior *wizard.PriorState) bool {
	if prior == nil {
		return false
	}
	return prior.Selection != nil || prior.Inputs != nil
}

// buildFreshAnswers assembles wizard.RawAnswers for the no-prior-state
// case, in priority order: --reuse → interactive prompts → CLI flags.
// Validates flag combinations.
func buildFreshAnswers(pkg *api.Package, prior *wizard.PriorState, interactive bool, opts flowOptions) (wizard.RawAnswers, error) {
	if opts.preset != "" && len(opts.selectFlags) > 0 {
		return wizard.RawAnswers{}, fmt.Errorf("--components and --select are mutually exclusive")
	}
	if opts.reuse {
		if prior == nil || (prior.Selection == nil && prior.Inputs == nil) {
			return wizard.RawAnswers{}, fmt.Errorf("--reuse requires a prior install — none found in this work-dir")
		}
		return wizard.RawAnswersFromPrior(pkg, prior), nil
	}

	if interactive {
		raw, err := wizard.Ask(pkg, prior, wizard.AskOptions{})
		if err != nil {
			return wizard.RawAnswers{}, err
		}
		if opts.baseName != "" {
			raw.BaseName = opts.baseName
		}
		if opts.namespace != "" {
			raw.Namespace = opts.namespace
		}
		if len(opts.selectFlags) > 0 {
			raw.SelectedComponents = opts.selectFlags
		}
		flagInputs, err := wizard.ParseInputFlags(opts.inputFlags)
		if err != nil {
			return wizard.RawAnswers{}, err
		}
		for k, v := range flagInputs {
			raw.Inputs[k] = v
		}
		return raw, nil
	}

	if opts.namespace == "" {
		return wizard.RawAnswers{}, fmt.Errorf("--namespace is required in non-interactive mode")
	}
	flagInputs, err := wizard.ParseInputFlags(opts.inputFlags)
	if err != nil {
		return wizard.RawAnswers{}, err
	}
	raw := wizard.RawAnswers{
		Inputs:             flagInputs,
		SelectedComponents: opts.selectFlags,
		BaseName:           opts.baseName,
		Namespace:          opts.namespace,
	}
	if opts.preset != "" {
		picked, err := wizard.ResolvePreset(pkg, opts.preset)
		if err != nil {
			return wizard.RawAnswers{}, err
		}
		raw.SelectedComponents = picked
	}
	return raw, nil
}

// buildUpgradeAnswers translates prior state + the new package's input
// schema into a RawAnswers via schema-diff: silently carry forward
// values that still apply, adopt new defaults, drop removed inputs,
// prompt (interactive) or fail fast (non-interactive) for new
// required-without-default. The same machinery the old `installer
// upgrade` command used; now applied to every re-entry.
//
// CLI flags from this run override the carried-forward values:
// --namespace, --base, --select, --input override prior; --components
// preset overrides --select.
func buildUpgradeAnswers(newPkg *api.Package, prior *wizard.PriorState, interactive bool, workDir string, opts flowOptions) (wizard.RawAnswers, error) {
	if opts.preset != "" && len(opts.selectFlags) > 0 {
		return wizard.RawAnswers{}, fmt.Errorf("--components and --select are mutually exclusive")
	}
	if opts.reuse {
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

	if len(id.TypeChanged) != 0 {
		names := make([]string, 0, len(id.TypeChanged))
		for _, in := range id.TypeChanged {
			names = append(names, in.Name)
		}
		return out, fmt.Errorf("input type(s) changed across upgrade for: %s — re-run interactively (`%s setup --work-dir %s`) to re-answer them",
			strings.Join(names, ", "), InvocationName(), workDir)
	}
	for k, v := range id.Carry {
		out.Inputs[k] = wizard.StringifyAny(v)
	}
	for k, v := range id.AdoptedDefaults {
		out.Inputs[k] = wizard.StringifyAny(v)
		fmt.Printf("Adopted new default for input %q: %v\n", k, v)
	}
	for _, k := range id.Dropped {
		fmt.Printf("Dropped removed input %q (was: %v)\n", k, priorVals[k])
	}
	if len(id.NewRequired) != 0 {
		if !interactive {
			return out, fmt.Errorf("%s", wizard.FormatNewRequiredHint(workDir, id.NewRequired))
		}
		for _, in := range id.NewRequired {
			ra, err := wizard.AskOneInput(in)
			if err != nil {
				return out, err
			}
			out.Inputs[in.Name] = ra
			fmt.Printf("Answered new required input %q\n", in.Name)
		}
	}

	priorSel := []string{}
	if prior.Selection != nil {
		priorSel = prior.Selection.Spec.Components
	}
	cd := wizard.DiffComponents(priorPkg, newPkg, priorSel)
	out.SelectedComponents = cd.Components
	if len(cd.AdoptedNewDefaults) != 0 {
		fmt.Printf("Adopted new default-flagged component(s): %s\n", strings.Join(cd.AdoptedNewDefaults, ", "))
	}
	if len(cd.RemovedFromPrior) != 0 {
		fmt.Printf("Dropped component(s) no longer in package: %s\n", strings.Join(cd.RemovedFromPrior, ", "))
	}

	if prior.Selection != nil {
		out.BaseName = prior.Selection.Spec.Base
	}
	if prior.Inputs != nil {
		out.Namespace = prior.Inputs.Spec.Namespace
	}

	// CLI overrides on top of carried-forward values.
	if opts.baseName != "" {
		out.BaseName = opts.baseName
	}
	if opts.namespace != "" {
		out.Namespace = opts.namespace
	}
	if len(opts.selectFlags) > 0 {
		out.SelectedComponents = opts.selectFlags
	}
	if opts.preset != "" {
		picked, err := wizard.ResolvePreset(newPkg, opts.preset)
		if err != nil {
			return wizard.RawAnswers{}, err
		}
		out.SelectedComponents = picked
	}
	flagInputs, err := wizard.ParseInputFlags(opts.inputFlags)
	if err != nil {
		return wizard.RawAnswers{}, err
	}
	for k, v := range flagInputs {
		out.Inputs[k] = v
	}
	return out, nil
}

// runDepsUpdate resolves the dependency DAG against the OCI registry
// and writes <work-dir>/out/spec/lock.yaml. Honors the package's
// optional-deps gated by whenComponent, using the supplied selection.
func runDepsUpdate(ctx context.Context, workDir string, pkg *api.Package, sel *api.Selection) error {
	res, err := deps.Resolve(ctx, pkg, deps.OCISource{}, deps.Options{Selection: sel})
	if err != nil {
		return fmt.Errorf("deps update: %w", err)
	}
	if err := deps.WriteLock(workDir, pkg, res.Lock); err != nil {
		return err
	}
	fmt.Printf("Locked %d dependency(ies) to %s\n", len(res.Lock.Spec.Resolved), deps.LockPath(workDir))
	return nil
}

// runRenderInWorkDir runs render.Render (and the per-dep renderer for
// multi-package installs) against the work-dir's package + spec.
func runRenderInWorkDir(ctx context.Context, workDir string, loaded *ipkg.Loaded, res *wizard.Result, clean bool) error {
	outDir := filepath.Join(workDir, "out")
	manifestsDir := filepath.Join(outDir, "manifests")
	if clean {
		if err := render.CleanRenderedOutput(outDir); err != nil {
			return err
		}
	}
	r, err := render.Render(ctx, render.Options{
		Loaded:    loaded,
		Selection: res.Selection,
		Inputs:    res.Inputs,
		Facts:     res.Facts,
	}, outDir)
	if err != nil {
		return err
	}
	fmt.Printf("Rendered %d manifest(s) to %s\n", len(r.Manifests), manifestsDir)
	if len(r.Secrets) > 0 {
		fmt.Printf("Rendered %d secret(s) to %s/secrets (not uploaded)\n", len(r.Secrets), outDir)
	}
	if len(loaded.Package.Spec.Dependencies) > 0 {
		lock, err := deps.ReadLock(workDir)
		if err != nil {
			return err
		}
		depResults, err := render.RenderDependencies(ctx, render.DepsOptions{
			Lock:         lock,
			ParentInputs: res.Inputs,
			WorkDir:      workDir,
		})
		if err != nil {
			return err
		}
		for _, dr := range depResults {
			fmt.Printf("Rendered dep %s: %d manifest(s) to %s\n", dr.Name, len(dr.Manifests), filepath.Join(dr.OutDir, "manifests"))
			if len(dr.Secrets) > 0 {
				fmt.Printf("  secrets: %d (not uploaded)\n", len(dr.Secrets))
			}
		}
	}
	return nil
}

// rootCause unwraps wrapped errors until it finds one os.IsNotExist
// understands. Used to surface "no package directory yet" cleanly.
func rootCause(err error) error {
	for {
		next := unwrapErr(err)
		if next == nil {
			return err
		}
		err = next
	}
}

func unwrapErr(err error) error {
	type unwrapper interface{ Unwrap() error }
	if u, ok := err.(unwrapper); ok {
		return u.Unwrap()
	}
	return nil
}

// newSetupCmd is the all-in-one consumer entry point: optional pull,
// wizard (smart re-entry with schema-diff when prior state exists),
// render. Does not upload — `installer upload` is the next step.
func newSetupCmd() *cobra.Command {
	var opts flowOptions
	opts.runRender = true
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Pull (optional), run the wizard, and render — first install or upgrade",
		Long: `Setup is the consumer's one-shot entry point. It combines pull,
wizard, and render into a single command, and auto-detects whether
this is a first install or a re-render against an existing work-dir
(including an upgrade to a newer package version).

Working directory:
  --work-dir <dir>   defaults to the current directory. Pull writes to
                     <work-dir>/package/; spec docs to <work-dir>/out/spec/;
                     manifests to <work-dir>/out/manifests/.

Rendered OCI output:
  --output-oci <dest> optional. After a successful render, write those exact
                      non-secret manifests as an OCI artifact. Use a local
                      path for an OCI image layout, or oci://host/repo:tag to
                      push to a registry. The command reads the artifact back
                      and compares its recorded object-set digest before
                      reporting success.

Pull:
  --pull <ref>       optional. When present, fetches the package and
                     replaces <work-dir>/package/ atomically (failed
                     pulls leave the prior tree intact). When absent,
                     the work-dir must already contain a package/ tree
                     (run setup with --pull first, or installer pull
                     <ref> --work-dir <dir>).

Auto-detection:
  - <work-dir>/out/spec/upload.yaml exists → load prior install state
    from the recorded ConfigHub Space.
  - else <work-dir>/out/spec/{selection,inputs,facts}.yaml exist → load
    prior locally.
  - else → fresh install.

When prior state is loaded, setup runs the schema-diff machinery
against the (possibly newer) package's input + component schema:
silently carries forward values that still apply, adopts new defaults,
drops removed inputs, prompts (interactive) or fails fast
(non-interactive) for newly-required inputs without defaults. Same
machinery as the prior installer upgrade command, applied uniformly
to every re-entry.

Setup does NOT upload. Run installer upload to push to ConfigHub.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			return runFlow(ctx, opts)
		},
	}
	cmd.Flags().StringVar(&opts.workDir, "work-dir", ".", "working directory (gets ./package and ./out subdirs)")
	cmd.Flags().StringVar(&opts.pullRef, "pull", "", "fetch this package reference (oci://..., local path, or .tgz) before running the wizard")
	cmd.Flags().StringVar(&opts.baseName, "base", "", "base name (default: package's default base)")
	cmd.Flags().StringVar(&opts.namespace, "namespace", "", "Kubernetes namespace for the install (exposed to chain templates as {{ .Namespace }}). Required for fresh install in non-interactive mode.")
	cmd.Flags().StringSliceVar(&opts.selectFlags, "select", nil, "component to select (repeatable; required-deps closed automatically). Mutually exclusive with --components.")
	cmd.Flags().StringSliceVar(&opts.inputFlags, "input", nil, "input value as key=value (repeatable)")
	cmd.Flags().BoolVar(&opts.nonInteractive, "non-interactive", false, "do not prompt; require --input/--select for everything")
	cmd.Flags().BoolVar(&opts.reuse, "reuse", false, "skip prompts and re-use the prior install's selection + inputs (requires prior state)")
	cmd.Flags().StringVar(&opts.preset, "components", "", "component preset: minimal | default | all | selected. Mutually exclusive with --select.")
	cmd.Flags().StringSliceVar(&opts.setImage, "set-image", nil, "image override as name=ref (repeatable); applied via `kustomize edit set image` against the chosen base before render. The base's kustomization.yaml must declare an `images:` block.")
	cmd.Flags().BoolVar(&opts.clean, "clean", false, "remove previously rendered out/manifests/ (preserving any kpt Kptfile) and out/secrets/ before rendering")
	cmd.Flags().StringVar(&opts.outputOCI, "output-oci", "", "write rendered manifests to a local OCI layout path or push them to oci://host/repo:tag")
	return cmd
}

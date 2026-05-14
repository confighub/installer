package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/confighub/installer/internal/cubctx"
	"github.com/confighub/installer/internal/deps"
	"github.com/confighub/installer/internal/diff"
	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/internal/render"
	"github.com/confighub/installer/internal/upload"
	"github.com/confighub/installer/internal/wizard"
	"github.com/confighub/installer/pkg/api"
)

// upgradeStageDir is the sibling of work-dir/package and work-dir/out
// that upgrade stages into. After a successful upgrade, the operator
// runs `installer upgrade-apply <work-dir>` to atomically swap it
// over the working tree.
const upgradeStageDir = ".upgrade"

// upgradePrevDir is where the prior package/ and out/ are archived
// during upgrade-apply, kept for one rollback step.
const upgradePrevDir = ".upgrade-prev"

func newUpgradeCmd() *cobra.Command {
	var (
		apply    bool
		yes      bool
		setImage []string
	)
	cmd := &cobra.Command{
		Use:   "upgrade <work-dir> <ref>",
		Short: "Stage a package upgrade (or facts-only re-render) and show the plan",
		Long: `Upgrade pulls <ref> into <work-dir>/.upgrade/package, runs the
wizard non-interactively against the new package using the prior
selection + inputs as defaults, re-runs the collector, re-renders
into <work-dir>/.upgrade/out, and prints a plan against the current
ConfigHub state.

Upgrade does NOT mutate ConfigHub. To execute the staged upgrade,
run 'installer upgrade-apply <work-dir>' (or pass --apply on this
command to chain them in one shot).

Schema-diff handling between the prior install's package and <ref>:

  - inputs new in this version with a default value: silently
    adopted.
  - inputs new and required without a default: prompted in
    interactive mode; non-interactive mode fails fast naming each
    missing input.
  - inputs removed in this version: silently dropped from the new
    inputs.yaml.
  - input types that changed: error — operator must re-run
    'installer wizard' interactively to re-answer.
  - components: if the prior selection matched the old package's
    'default' preset, the upgrade adopts the new package's default
    preset (so a newly-flagged 'default: true' component flows in
    automatically). Otherwise the prior list is filtered to
    components that still exist in the new package.

<ref> may be the same ref as the current install — useful when
cluster state has changed and you want to re-collect facts and
re-render.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			workDir, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			ref := args[1]
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			if _, err := exec.LookPath("cub"); err != nil {
				return fmt.Errorf("cub CLI not found on PATH: %w", err)
			}

			interactive := term.IsTerminal(int(os.Stdin.Fd()))

			stageDir := filepath.Join(workDir, upgradeStageDir)
			if err := prepareStageDir(stageDir); err != nil {
				return err
			}

			fmt.Printf("Staging upgrade in %s\n", stageDir)
			pkgDir, err := ipkg.Pull(ctx, ref, filepath.Join(stageDir, "package"))
			if err != nil {
				return fmt.Errorf("pull %s: %w", ref, err)
			}
			loaded, err := ipkg.Load(pkgDir)
			if err != nil {
				return fmt.Errorf("load %s: %w", pkgDir, err)
			}

			prior, source, err := wizard.LoadPriorState(ctx, workDir, func(msg string) {
				fmt.Fprintln(os.Stderr, "warning:", msg)
			})
			if err != nil {
				return fmt.Errorf("load prior state: %w", err)
			}
			if prior == nil {
				return fmt.Errorf("no prior install found in %s — run `installer wizard %s <ref>` and `installer upload` first",
					workDir, workDir)
			}
			if prior.Upload != nil {
				if err := cubctx.CheckMatches(ctx, prior.Upload.Spec.OrganizationID, prior.Upload.Spec.Server); err != nil {
					return err
				}
			}
			fmt.Printf("Prior install loaded from %s.\n", source)

			raw, err := buildUpgradeAnswers(loaded.Package, prior, interactive, workDir)
			if err != nil {
				return err
			}
			imgOverrides, err := wizard.ParseSetImageFlags(setImage)
			if err != nil {
				return err
			}
			raw.ImageOverrides = imgOverrides
			if prior.Inputs != nil {
				raw.PriorImageOverrides = prior.Inputs.Spec.ImageOverrides
			}

			outDir := filepath.Join(stageDir, "out")
			res, err := wizard.Run(ctx, loaded.Package, raw, pkgDir, outDir)
			if err != nil {
				return fmt.Errorf("wizard: %w", err)
			}
			fmt.Printf("Wizard wrote %s/spec/{selection,inputs}.yaml\n", outDir)
			if res.Facts != nil {
				fmt.Printf("Collector produced %d fact(s)\n", len(res.Facts.Spec.Values))
			}

			if len(loaded.Package.Spec.Dependencies) > 0 {
				if err := runDepsUpdate(ctx, stageDir, loaded.Package, res.Selection); err != nil {
					return err
				}
			}

			if err := runRender(ctx, stageDir, loaded, res); err != nil {
				return err
			}

			// Carry the upload.yaml from the existing out/spec into
			// the staged out/spec so plan/upgrade-apply can locate
			// the destination Spaces. (BuildInstallerRecord includes
			// it on the next upload too.)
			if err := copyUploadDoc(workDir, stageDir); err != nil {
				return err
			}

			plan, err := computeStagedPlan(ctx, stageDir, loaded.Package)
			if err != nil {
				return err
			}
			diff.Print(os.Stdout, plan)

			if apply {
				fmt.Println()
				fmt.Println("--apply: chaining `installer upgrade-apply`")
				priorPkg := prior.PriorPackage
				return runUpgradeApply(ctx, workDir, loaded.Package, yes, priorPkg)
			}
			fmt.Printf("\nNext: installer upgrade-apply %s   (or rerun with --apply)\n", workDir)
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "after staging, immediately run `installer upgrade-apply` (one-shot upgrade + execute)")
	cmd.Flags().BoolVar(&yes, "yes", false, "with --apply, skip confirmation for any deletes the resulting update would perform")
	cmd.Flags().StringSliceVar(&setImage, "set-image", nil, "image override as name=ref (repeatable); applied via `kustomize edit set image` against the chosen base before render. Carries forward across upgrades unless replaced.")
	return cmd
}

// prepareStageDir clears any prior .upgrade/ tree so a re-run of
// upgrade starts clean. Existing .upgrade-prev/ is left alone (it's
// the rollback target from the LAST successful upgrade-apply).
func prepareStageDir(stageDir string) error {
	if err := os.RemoveAll(stageDir); err != nil {
		return fmt.Errorf("clear %s: %w", stageDir, err)
	}
	return os.MkdirAll(stageDir, 0o755)
}

// buildUpgradeAnswers translates prior state + new package's input
// schema into a RawAnswers ready for wizard.Run. Honors interactive vs
// non-interactive mode for new required inputs.
func buildUpgradeAnswers(newPkg *api.Package, prior *wizard.PriorState, interactive bool, workDir string) (wizard.RawAnswers, error) {
	out := wizard.RawAnswers{Inputs: map[string]string{}}

	priorPkg := prior.PriorPackage
	if priorPkg == nil {
		// No prior package source available — treat prior as having
		// matched whatever the new package declares, so every prior
		// value carries forward.
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
		return out, fmt.Errorf("input type(s) changed across upgrade for: %s — re-run `installer wizard %s <ref>` to re-answer them, then re-run upgrade",
			strings.Join(names, ", "), workDir)
	}
	for k, v := range id.Carry {
		out.Inputs[k] = stringifyAnyForUpgrade(v)
	}
	for k, v := range id.AdoptedDefaults {
		out.Inputs[k] = stringifyAnyForUpgrade(v)
		fmt.Printf("Adopted new default for input %q: %v\n", k, v)
	}
	for _, k := range id.Dropped {
		fmt.Printf("Dropped removed input %q (was: %v)\n", k, priorVals[k])
	}
	if len(id.NewRequired) != 0 {
		if !interactive {
			return out, fmt.Errorf("%s", wizard.FormatNewRequiredHint(workDir, id.NewRequired))
		}
		// Interactive: prompt for each new required via the same
		// helpers the wizard uses.
		for _, in := range id.NewRequired {
			ra, err := wizard.AskOneInput(in)
			if err != nil {
				return out, err
			}
			out.Inputs[in.Name] = ra
			fmt.Printf("Answered new required input %q\n", in.Name)
		}
	}

	// Components.
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

	// Base + namespace carry forward verbatim.
	if prior.Selection != nil {
		out.BaseName = prior.Selection.Spec.Base
	}
	if prior.Inputs != nil {
		out.Namespace = prior.Inputs.Spec.Namespace
	}
	return out, nil
}

// stringifyAnyForUpgrade is the same coercion the interactive wizard
// uses to round-trip prior typed values through the string-keyed
// RawAnswers map.
func stringifyAnyForUpgrade(v any) string {
	// Reuse the wizard package's helper via a typed-value round-trip:
	// the wizard.RawAnswersFromPrior path uses identical logic.
	return wizard.StringifyAny(v)
}

func runDepsUpdate(ctx context.Context, stageDir string, pkg *api.Package, sel *api.Selection) error {
	res, err := deps.Resolve(ctx, pkg, deps.OCISource{}, deps.Options{Selection: sel})
	if err != nil {
		return fmt.Errorf("deps update: %w", err)
	}
	if err := deps.WriteLock(stageDir, pkg, res.Lock); err != nil {
		return err
	}
	fmt.Printf("Locked %d dependency(ies) to %s\n", len(res.Lock.Spec.Resolved), deps.LockPath(stageDir))
	return nil
}

func runRender(ctx context.Context, stageDir string, loaded *ipkg.Loaded, res *wizard.Result) error {
	outDir := filepath.Join(stageDir, "out")
	r, err := render.Render(ctx, render.Options{
		Loaded:    loaded,
		Selection: res.Selection,
		Inputs:    res.Inputs,
		Facts:     res.Facts,
	}, outDir)
	if err != nil {
		return err
	}
	fmt.Printf("Rendered %d manifest(s) to %s\n", len(r.Manifests), filepath.Join(outDir, "manifests"))
	if len(r.Secrets) > 0 {
		fmt.Printf("Rendered %d secret(s) (not uploaded)\n", len(r.Secrets))
	}
	if len(loaded.Package.Spec.Dependencies) > 0 {
		lock, err := deps.ReadLock(stageDir)
		if err != nil {
			return err
		}
		depResults, err := render.RenderDependencies(ctx, render.DepsOptions{
			Lock:         lock,
			ParentInputs: res.Inputs,
			WorkDir:      stageDir,
		})
		if err != nil {
			return err
		}
		for _, dr := range depResults {
			fmt.Printf("Rendered dep %s: %d manifest(s) to %s\n", dr.Name, len(dr.Manifests), filepath.Join(dr.OutDir, "manifests"))
		}
	}
	return nil
}

// copyUploadDoc copies <work-dir>/out/spec/upload.yaml into
// <stage-dir>/out/spec/ so the staged tree carries the upload
// destination forward through plan + upgrade-apply.
func copyUploadDoc(workDir, stageDir string) error {
	src := filepath.Join(workDir, "out", "spec", upload.UploadDocFilename)
	dst := filepath.Join(stageDir, "out", "spec", upload.UploadDocFilename)
	in, err := os.Open(src)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s not found — upgrade requires the work-dir to have been uploaded; run `installer upload` first",
				src)
		}
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// computeStagedPlan reads upload.yaml from the staged tree and runs
// diff.Compute against the staged manifests, exactly mirroring what
// `installer plan` would do on the swapped-in tree.
func computeStagedPlan(ctx context.Context, stageDir string, pkg *api.Package) (diff.Plan, error) {
	uploadDoc, err := readUploadDoc(stageDir)
	if err != nil {
		return diff.Plan{}, err
	}
	pattern := uploadDoc.Spec.SpacePattern
	if pattern == "" {
		pattern = "{{.PackageName}}"
	}
	var lock *api.Lock
	if len(pkg.Spec.Dependencies) > 0 {
		lock, err = deps.ReadLock(stageDir)
		if err != nil {
			return diff.Plan{}, err
		}
	}
	packages, err := upload.Discover(upload.DiscoverInput{
		WorkDir:       stageDir,
		SpacePattern:  pattern,
		ParentPackage: pkg,
		Lock:          lock,
	})
	if err != nil {
		return diff.Plan{}, err
	}
	return diff.Compute(ctx, packages)
}

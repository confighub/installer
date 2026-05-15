package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/confighub/installer/internal/deps"
	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/internal/upload"
	"github.com/confighub/installer/pkg/api"
)

func newUploadCmd() *cobra.Command {
	var (
		space         string
		spacePattern  string
		target        string
		annotations   []string
		labels        []string
		appCfgWorker  string
	)
	cmd := &cobra.Command{
		Use:   "upload <work-dir>",
		Short: "Upload rendered manifests to ConfigHub Space(s)",
		Long: `Upload sends rendered manifests to ConfigHub. The shape depends on whether
the package declares dependencies:

Single-package (no deps):
  --space chooses the Space; one Unit per file in <work-dir>/out/manifests.

Multi-package (parent declares dependencies):
  --space-pattern is a Go template evaluated per package (vars:
  .PackageName, .PackageVersion, .Variant — Variant is empty in v1).
  Default: '{{.PackageName}}'. Each package — parent + each locked dep —
  gets its own Space. The Units for the parent come from
  <work-dir>/out/manifests; each dep's Units come from
  <work-dir>/out/<dep>/manifests.

In both modes, every Space additionally receives one untargeted
"installer-record" Unit holding installer.yaml + that package's spec docs
(selection, inputs, function-chain, manifest-index, plus the lock for the
parent). The record Unit makes a Space self-describing — it can be
re-rendered from its own ConfigHub state.

Cross-Space NeedsProvides Links are created from the parent's record Unit
to each dep's record Unit so downstream tooling can see the dependency
relationship.

Files in <work-dir>/out/secrets/ (and each <dep>/secrets/) are never
uploaded — they hold rendered Secret resources flagged as sensitive by
render.

--target, --annotation, and --label are forwarded to ` + "`cub unit create`" + ` on
each rendered manifest Unit. They do NOT apply to the installer-record
Unit (which must remain untargeted).

Every Unit and Link created by upload also carries a "Component" label
whose value is the package name (the parent's name for cross-Space dep
links), so all entities belonging to one package can be queried together.

After per-Space upload, the existing get-resources / get-references /
get-workload-labels link-inference runs once per Space to materialize
intra-Space NeedsProvides links.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := exec.LookPath("cub"); err != nil {
				return fmt.Errorf("cub CLI not found on PATH: %w", err)
			}
			if err := validateKeyValueFlags("--annotation", annotations); err != nil {
				return err
			}
			if err := validateKeyValueFlags("--label", labels); err != nil {
				return err
			}
			workDir, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}

			loaded, err := ipkg.Load(filepath.Join(workDir, "package"))
			if err != nil {
				return fmt.Errorf("load package: %w", err)
			}

			// Reconcile --space vs --space-pattern.
			//
			//   no deps:   --space S        → equivalent to pattern "S".
			//              --space-pattern  → also fine.
			//              both             → error.
			//              neither          → default pattern "{{.PackageName}}".
			//   with deps: --space          → error (one slug can't host N packages).
			//              --space-pattern  → fine.
			//              neither          → default pattern "{{.PackageName}}".
			hasDeps := len(loaded.Package.Spec.Dependencies) > 0
			pattern := spacePattern
			if space != "" && spacePattern != "" {
				return fmt.Errorf("--space and --space-pattern are mutually exclusive")
			}
			if space != "" {
				if hasDeps {
					return fmt.Errorf("--space cannot be used when the package declares dependencies; use --space-pattern")
				}
				pattern = space
			}
			if pattern == "" {
				pattern = "{{.PackageName}}"
			}

			var lock *api.Lock
			if hasDeps {
				lock, err = deps.ReadLock(workDir)
				if err != nil {
					return err
				}
				if lock == nil {
					return fmt.Errorf("package declares dependencies but %s does not exist; run `installer deps update %s` and `installer render %s` first",
						deps.LockPath(workDir), workDir, workDir)
				}
				if deps.IsStale(lock, loaded.Package) {
					return fmt.Errorf("lock at %s is stale; run `installer deps update %s` and `installer render %s` again",
						deps.LockPath(workDir), workDir, workDir)
				}
			}

			packages, err := upload.Discover(upload.DiscoverInput{
				WorkDir:       workDir,
				SpacePattern:  pattern,
				ParentPackage: loaded.Package,
				Lock:          lock,
			})
			if err != nil {
				return err
			}

			// Persist where this work-dir is being uploaded before any
			// cub calls so the upload.yaml is included in each parent's
			// installer-record body. Subsequent `installer wizard /
			// plan / update / upgrade` invocations read it to re-enter
			// from ConfigHub and to sanity-check the active cub
			// context.
			if err := upload.WriteUploadDoc(cmd.Context(), workDir, pattern, packages); err != nil {
				return fmt.Errorf("write upload.yaml: %w", err)
			}

			for _, pkg := range packages {
				if err := uploadOnePackage(pkg, target, annotations, labels, appCfgWorker); err != nil {
					return err
				}
			}

			for _, l := range upload.PlanCrossSpaceLinks(packages) {
				if err := createCrossSpaceLink(l); err != nil {
					return err
				}
			}

			// Record the parent package's install in the per-user
			// state file (~/.confighub/installer/state.yaml). Other
			// commands (notably `installer new`) read this to find
			// kubernetes-resources without re-asking the operator.
			// Best-effort: failure here should NOT fail the upload.
			if err := recordUploadInUserState(cmd.Context(), packages); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not record install in user state: %v\n", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&space, "space", "", "destination ConfigHub Space slug (single-package mode)")
	cmd.Flags().StringVar(&spacePattern, "space-pattern", "", "Go template for the Space slug per package (vars: .PackageName, .PackageVersion, .Variant). Default: '{{.PackageName}}'.")
	cmd.Flags().StringVar(&target, "target", "", "target slug; forwarded to cub unit create --target on every rendered Unit (not the installer-record Unit)")
	cmd.Flags().StringSliceVar(&annotations, "annotation", nil, "annotation key=value to set on every rendered Unit (repeatable)")
	cmd.Flags().StringSliceVar(&labels, "label", nil, "label key=value to set on every rendered Unit (repeatable)")
	cmd.Flags().StringVar(&appCfgWorker, "appconfig-worker", "server-worker", "worker slug (Space-relative) to attach to ConfigMapRenderer Targets created for AppConfig-annotated ConfigMaps")
	return cmd
}

// uploadOnePackage creates pkg.SpaceSlug if missing, uploads each rendered
// manifest as a Unit (with --target/--annotation/--label applied), splits
// AppConfig-annotated ConfigMaps into AppConfig Unit + ConfigMapRenderer
// Target pairs, creates the untargeted installer-record Unit, and runs
// the intra-Space link inference.
func uploadOnePackage(pkg upload.Package, target string, annotations, labels []string, appCfgWorker string) error {
	fmt.Printf("== %s@%s → Space %s ==\n", pkg.Name, pkg.Version, pkg.SpaceSlug)

	if err := ensureSpace(pkg.SpaceSlug); err != nil {
		return err
	}

	componentLabel := "Component=" + pkg.Name

	entries, err := os.ReadDir(pkg.ManifestsDir)
	if err != nil {
		return fmt.Errorf("read %s: %w", pkg.ManifestsDir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(pkg.ManifestsDir, e.Name())
		appCfg, err := upload.DetectAppConfigManifest(path)
		if err != nil {
			return err
		}
		if appCfg != nil {
			if err := uploadAppConfigManifest(pkg, appCfg, appCfgWorker, target, componentLabel, annotations, labels); err != nil {
				return err
			}
			continue
		}
		slug := trimExt(e.Name())
		cubArgs := []string{"unit", "create", "--space", pkg.SpaceSlug, "--merge-external-source", e.Name(), "--label", componentLabel}
		if target != "" {
			cubArgs = append(cubArgs, "--target", target)
		}
		for _, a := range annotations {
			cubArgs = append(cubArgs, "--annotation", a)
		}
		for _, l := range labels {
			cubArgs = append(cubArgs, "--label", l)
		}
		cubArgs = append(cubArgs, slug, path)
		ccmd := exec.Command("cub", cubArgs...)
		ccmd.Stdout = os.Stdout
		ccmd.Stderr = os.Stderr
		if err := ccmd.Run(); err != nil {
			return fmt.Errorf("cub unit create %s in %s: %w", slug, pkg.SpaceSlug, err)
		}
	}

	if err := createInstallerRecordUnit(pkg); err != nil {
		return err
	}
	return upload.ReconcileLinks(context.Background(), pkg.SpaceSlug, pkg.Name)
}

// uploadAppConfigManifest materializes the AppConfig triple in pkg's
// Space:
//
//  1. ConfigMapRenderer Target — one per carrier ConfigMap.
//  2. AppConfig Unit — Data is the extracted raw file body, target is
//     the renderer. This is the day-2 source of truth in the native
//     format (.properties, .env, etc.).
//  3. Placeholder Kubernetes/YAML ConfigMap Unit — same slug as the
//     carrier so other Units in the Space link to it by name via
//     intra-Space link inference. Body is empty; populated at apply
//     time via a live-state MergeUnits link to the AppConfig Unit.
//
// The placeholder Unit inherits the upload-wide `target` flag (typically a
// Kubernetes namespace target) so it applies into the same place as every
// other rendered manifest. The renderer Target itself is attached only to
// the AppConfig Unit.
//
// Idempotent via --allow-exists on the Target and the link; re-running
// upload after re-rendering refreshes the AppConfig Unit body via the
// --merge-external-source mechanism the regular path uses.
func uploadAppConfigManifest(pkg upload.Package, appCfg *upload.AppConfigManifest, worker, target, componentLabel string, annotations, labels []string) error {
	fmt.Printf("AppConfig: %s (toolchain=%s, mode=%s)\n", appCfg.CarrierName, appCfg.Toolchain, appCfg.Mode)

	// 1. Stage the extracted raw config in a temp file so cub unit
	//    create reads it from disk like every other Unit body.
	tmp, err := os.CreateTemp("", "appconfig-*-"+sanitizeForFilename(appCfg.CarrierName))
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(appCfg.Content); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()

	// 2. Create (or update — --allow-exists) the ConfigMapRenderer
	//    Target. The worker arg is required by `cub target create` and
	//    is Space-relative by convention.
	workerRef := pkg.SpaceSlug + "/" + worker
	targetArgs := []string{
		"target", "create", "--allow-exists",
		"--space", pkg.SpaceSlug,
		appCfg.TargetSlug(), "", workerRef,
		"--provider", "ConfigMapRenderer",
		"--toolchain", appCfg.Toolchain,
		"--livestate-type", "Kubernetes/YAML",
	}
	for _, opt := range appCfg.RendererOptions() {
		targetArgs = append(targetArgs, "--option", opt)
	}
	tcmd := exec.Command("cub", targetArgs...)
	tcmd.Stdout = os.Stdout
	tcmd.Stderr = os.Stderr
	if err := tcmd.Run(); err != nil {
		return fmt.Errorf("cub target create %s in %s: %w", appCfg.TargetSlug(), pkg.SpaceSlug, err)
	}

	// 3. Create the AppConfig Unit pointing at that Target.
	unitArgs := []string{
		"unit", "create",
		"--space", pkg.SpaceSlug,
		"--toolchain", appCfg.Toolchain,
		"--target", appCfg.TargetSlug(),
		"--merge-external-source", filepath.Base(appCfg.ManifestPath),
		"--label", componentLabel,
	}
	for _, a := range annotations {
		unitArgs = append(unitArgs, "--annotation", a)
	}
	for _, l := range labels {
		unitArgs = append(unitArgs, "--label", l)
	}
	unitArgs = append(unitArgs, appCfg.UnitSlug(), tmp.Name())
	ucmd := exec.Command("cub", unitArgs...)
	ucmd.Stdout = os.Stdout
	ucmd.Stderr = os.Stderr
	if err := ucmd.Run(); err != nil {
		return fmt.Errorf("cub unit create %s in %s: %w", appCfg.UnitSlug(), pkg.SpaceSlug, err)
	}

	// 4. Create the placeholder ConfigMap Unit whose slug matches the
	//    carrier name. Empty body — populated at apply time via the
	//    live-merge link below. Other workload Units in the same Space
	//    reference the carrier by name; intra-Space link inference wires
	//    them into this placeholder so the runtime ConfigMap name flows
	//    through.
	placeholderArgs := []string{
		"unit", "create",
		"--space", pkg.SpaceSlug,
		"--toolchain", "Kubernetes/YAML",
		"--label", componentLabel,
	}
	if target != "" {
		placeholderArgs = append(placeholderArgs, "--target", target)
	}
	for _, a := range annotations {
		placeholderArgs = append(placeholderArgs, "--annotation", a)
	}
	for _, l := range labels {
		placeholderArgs = append(placeholderArgs, "--label", l)
	}
	placeholderArgs = append(placeholderArgs, appCfg.CarrierName)
	pcmd := exec.Command("cub", placeholderArgs...)
	pcmd.Stdout = os.Stdout
	pcmd.Stderr = os.Stderr
	if err := pcmd.Run(); err != nil {
		return fmt.Errorf("cub unit create %s in %s: %w", appCfg.CarrierName, pkg.SpaceSlug, err)
	}

	// 5. Live-state MergeUnits link from placeholder → AppConfig Unit.
	//    --use-live-state pulls the rendered ConfigMap from the
	//    AppConfig Unit's live state into the placeholder's Data;
	//    --auto-update keeps it in sync as the AppConfig Unit is
	//    re-applied. --update-type MergeUnits is the rendering link.
	linkSlug := appCfg.CarrierName + "-from-" + appCfg.UnitSlug()
	linkArgs := []string{
		"link", "create", "--allow-exists", "--wait",
		"--space", pkg.SpaceSlug,
		"--use-live-state", "--auto-update", "--update-type", "MergeUnits",
		"--label", componentLabel,
		linkSlug, appCfg.CarrierName, appCfg.UnitSlug(),
	}
	lcmd := exec.Command("cub", linkArgs...)
	lcmd.Stdout = os.Stdout
	lcmd.Stderr = os.Stderr
	if err := lcmd.Run(); err != nil {
		return fmt.Errorf("cub link create %s in %s: %w", linkSlug, pkg.SpaceSlug, err)
	}
	return nil
}

// sanitizeForFilename returns name with non-[A-Za-z0-9._-] runes replaced
// by '-' so it can be embedded in os.CreateTemp's pattern safely.
func sanitizeForFilename(name string) string {
	out := []byte(name)
	for i, b := range out {
		switch {
		case b >= 'a' && b <= 'z',
			b >= 'A' && b <= 'Z',
			b >= '0' && b <= '9',
			b == '.', b == '_', b == '-':
		default:
			out[i] = '-'
		}
	}
	return string(out)
}

// ensureSpace creates the Space if it does not exist. Idempotent via
// --allow-exists.
func ensureSpace(slug string) error {
	ccmd := exec.Command("cub", "space", "create", "--allow-exists", "--quiet", slug)
	ccmd.Stderr = os.Stderr
	if err := ccmd.Run(); err != nil {
		return fmt.Errorf("cub space create %s: %w", slug, err)
	}
	return nil
}

// createInstallerRecordUnit builds the multi-doc YAML body and creates the
// untargeted installer-record Unit. The body file is staged in a temp
// location and passed to `cub unit create`.
func createInstallerRecordUnit(pkg upload.Package) error {
	body, err := upload.BuildInstallerRecord(pkg)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "installer-record-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()

	ccmd := exec.Command("cub", "unit", "create",
		"--space", pkg.SpaceSlug,
		"--annotation", "installer.confighub.com/role=installer-record",
		"--annotation", "installer.confighub.com/package="+pkg.Name,
		"--label", "Component="+pkg.Name,
		upload.InstallerRecordSlug, tmp.Name(),
	)
	ccmd.Stdout = os.Stdout
	ccmd.Stderr = os.Stderr
	if err := ccmd.Run(); err != nil {
		return fmt.Errorf("cub unit create installer-record in %s: %w", pkg.SpaceSlug, err)
	}
	return nil
}

// createCrossSpaceLink wires the parent's record Unit to a dep's record
// Unit. The 4th positional arg to `cub link create` is the target Space.
func createCrossSpaceLink(l upload.CrossSpaceLink) error {
	ccmd := exec.Command("cub", "link", "create",
		"--space", l.FromSpace, "--quiet",
		"--label", "Component="+l.Component,
		l.Slug, l.FromUnit, l.ToUnit, l.ToSpace,
	)
	ccmd.Stderr = os.Stderr
	if err := ccmd.Run(); err != nil {
		return fmt.Errorf("cub link create %s (%s/%s -> %s/%s): %w",
			l.Slug, l.FromSpace, l.FromUnit, l.ToSpace, l.ToUnit, err)
	}
	fmt.Printf("Linked %s/%s -> %s/%s (%s)\n", l.FromSpace, l.FromUnit, l.ToSpace, l.ToUnit, l.Reason)
	return nil
}

func validateKeyValueFlags(flag string, vals []string) error {
	for _, v := range vals {
		if !strings.Contains(v, "=") {
			return fmt.Errorf("%s %q must be key=value", flag, v)
		}
	}
	return nil
}


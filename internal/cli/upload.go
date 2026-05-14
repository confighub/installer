package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/confighubai/installer/internal/deps"
	ipkg "github.com/confighubai/installer/internal/pkg"
	"github.com/confighubai/installer/internal/upload"
	"github.com/confighubai/installer/pkg/api"
)

func newUploadCmd() *cobra.Command {
	var (
		space        string
		spacePattern string
		target       string
		annotations  []string
		labels       []string
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
				if err := uploadOnePackage(pkg, target, annotations, labels); err != nil {
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
	return cmd
}

// uploadOnePackage creates pkg.SpaceSlug if missing, uploads each rendered
// manifest as a Unit (with --target/--annotation/--label applied), creates
// the untargeted installer-record Unit, and runs the intra-Space link
// inference.
func uploadOnePackage(pkg upload.Package, target string, annotations, labels []string) error {
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
		slug := trimExt(e.Name())
		path := filepath.Join(pkg.ManifestsDir, e.Name())
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


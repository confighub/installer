// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/confighub/installer/internal/changeset"
	"github.com/confighub/installer/internal/cubctx"
	"github.com/confighub/installer/internal/deps"
	"github.com/confighub/installer/internal/diff"
	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/internal/upload"
	"github.com/confighub/installer/pkg/api"
)

func newUploadCmd() *cobra.Command {
	var (
		workDir          string
		space            string
		spacePattern     string
		target           string
		unitAnnotations  []string
		unitLabels       []string
		spaceAnnotations []string
		spaceLabels      []string
		component        string
		layer            string
		environment      string
		region           string
		owner            string
		variant          string
		retry            bool
		yes              bool
		changeSetSlug    string
	)
	cmd := &cobra.Command{
		Use:   "upload",
		Short: "Reconcile rendered manifests with ConfigHub Space(s) (create or update)",
		Long: `Upload reconciles <work-dir>/out/manifests/ with ConfigHub. It auto-
detects whether this is a first upload or a subsequent reconcile via the
presence of <work-dir>/out/spec/upload.yaml (written by the first upload):

First upload (no upload.yaml):
  Creates Space(s), one Unit per rendered manifest, plus the untargeted
  installer-record Unit. For AppConfig-annotated ConfigMaps: an AppConfig
  Unit + a render-configmap Invocation + a placeholder Kubernetes/YAML
  Unit, wired by an Upsert link that renders the ConfigMap into the
  placeholder. Cross-Space links between parent and dependency
  installer-record Units. Intra-Space NeedsProvides link inference runs
  per Space at the end. Writes upload.yaml on success.

Reconcile (upload.yaml exists):
  Re-computes the same plan installer plan would produce. Opens one
  ChangeSet per Space, runs cub unit update --merge-external-source
  --changeset for updates, cub unit create for adds (with --label
  Package=<pkg>), cub unit update with no data for deletes (gated on --yes
  or per-delete confirmation). Re-runs intra-Space link inference; existing
  links pointing at still-present Units are left as-is. Refreshes the
  installer-record Unit so subsequent setups re-enter from up-to-date
  state. A reconcile against an unchanged work-dir is a no-op (no
  ChangeSet opened). Only updates are revertable via
  cub unit update --restore Before:ChangeSet:<slug>; creates
  from a reconcile require manual undo (delete the Unit / re-render +
  re-upload).

Space selection (first upload only):
  Single-package: --space S       → one Space named S.
  Multi-package : --space-pattern → Go template evaluated per package
                                    (vars: .PackageName, .PackageVersion,
                                    .Variant). Default '{{.PackageName}}'.
  On reconcile, --space / --space-pattern are read from upload.yaml.

Every Unit and Link upload creates carries a "Package" label whose
value is the package name (the parent's name for cross-Space dep
links), so all entities belonging to one package can be queried
together and Units the operator added to the Space themselves are left
alone. Units also get a "PackageVersion" annotation, refreshed on
update, so you can see which package version last touched each Unit.

Spaces carry the well-known capitalized labels set via --component
(default: the package name), --layer, --environment, --region, --owner,
and --variant, plus any --space-label / --space-annotation pairs. When
--target is given its resolved TargetID is recorded as the Space's
"TargetID" annotation; on a later reconcile --target may be omitted and
is read back from there. None of this Space/Unit metadata is stored in
upload.yaml — ConfigHub is its source of truth; upload.yaml only needs
enough to find the Space(s).

Files in <work-dir>/out/secrets/ are never uploaded — they hold rendered
Secret resources flagged as sensitive by render. Apply them out-of-band
or stage them however your environment manages secrets.

--target, --unit-annotation, and --unit-label are forwarded to ` + "`cub unit create`" + `
on adds (first upload OR reconcile). They do NOT apply to existing Units
on reconcile — that would clobber post-install metadata edits in
ConfigHub. The well-known Space labels and --space-* pairs are set on
first upload and re-applied on a reconcile only when their flag is
passed again.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := exec.LookPath("cub"); err != nil {
				return fmt.Errorf("cub CLI not found on PATH: %w", err)
			}
			if err := validateKeyValueFlags("--unit-annotation", unitAnnotations); err != nil {
				return err
			}
			if err := validateKeyValueFlags("--unit-label", unitLabels); err != nil {
				return err
			}
			if err := validateSpaceLabelFlags(spaceLabels); err != nil {
				return err
			}
			if err := validateSpaceAnnotationFlags(spaceAnnotations); err != nil {
				return err
			}
			meta := spaceMetaInput{
				component:      component,
				layer:          layer,
				environment:    environment,
				region:         region,
				owner:          owner,
				variant:        variant,
				labels:         spaceLabels,
				annotations:    spaceAnnotations,
				target:         target,
				componentSet:   cmd.Flags().Changed("component"),
				layerSet:       cmd.Flags().Changed("layer"),
				environmentSet: cmd.Flags().Changed("environment"),
				regionSet:      cmd.Flags().Changed("region"),
				ownerSet:       cmd.Flags().Changed("owner"),
				variantSet:     cmd.Flags().Changed("variant"),
				targetSet:      cmd.Flags().Changed("target"),
			}
			absWork, err := filepath.Abs(workDir)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			loaded, err := ipkg.Load(filepath.Join(absWork, "package"))
			if err != nil {
				return fmt.Errorf("load package: %w", err)
			}

			// Auto-detect first-upload vs reconcile. The presence of
			// <work-dir>/out/spec/upload.yaml is the canonical signal that
			// this work-dir has already been pushed to ConfigHub once.
			uploadDocPath := filepath.Join(absWork, "out", "spec", upload.UploadDocFilename)
			if _, statErr := os.Stat(uploadDocPath); statErr == nil {
				return runUploadReconcile(ctx, absWork, loaded, meta, unitAnnotations, unitLabels, yes, changeSetSlug, space, spacePattern)
			} else if !os.IsNotExist(statErr) {
				return statErr
			}
			workDir = absWork

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
					return fmt.Errorf("package declares dependencies but %s does not exist; run `%s deps update --work-dir %s` and `%s render --work-dir %s` first",
						deps.LockPath(workDir), InvocationName(), workDir, InvocationName(), workDir)
				}
				if deps.IsStale(lock, loaded.Package) {
					return fmt.Errorf("lock at %s is stale; run `%s deps update --work-dir %s` and `%s render --work-dir %s` again",
						deps.LockPath(workDir), InvocationName(), workDir, InvocationName(), workDir)
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
				if err := uploadOnePackage(ctx, pkg, meta, unitAnnotations, unitLabels, retry); err != nil {
					return err
				}
			}

			for _, l := range upload.PlanCrossSpaceLinks(packages) {
				if err := createCrossSpaceLink(l, retry); err != nil {
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
	cmd.Flags().StringVar(&target, "target", "", "target ref (<target-slug>, <space-slug>/<target-slug>, or UUID); forwarded to cub unit create --target on every rendered Unit (not the installer-record Unit). Its resolved TargetID is recorded as the Space's \"TargetID\" annotation; on a later reconcile --target may be omitted and is read back from there.")
	cmd.Flags().StringSliceVar(&unitAnnotations, "unit-annotation", nil, "annotation key=value to set on every rendered Unit (repeatable)")
	cmd.Flags().StringSliceVar(&unitLabels, "unit-label", nil, "label key=value to set on every rendered Unit (repeatable)")
	cmd.Flags().StringSliceVar(&spaceAnnotations, "space-annotation", nil, "annotation key=value to set on the Space(s) (repeatable); \"TargetID\" is reserved (use --target)")
	cmd.Flags().StringSliceVar(&spaceLabels, "space-label", nil, "label key=value to set on the Space(s) (repeatable); the well-known labels Component/Layer/Environment/Owner/Variant are reserved (use their dedicated flags)")
	cmd.Flags().StringVar(&component, "component", "", "value for the well-known \"Component\" Space label (default: the package name)")
	cmd.Flags().StringVar(&layer, "layer", "", "value for the well-known \"Layer\" Space label (e.g. App)")
	cmd.Flags().StringVar(&environment, "environment", "", "value for the well-known \"Environment\" Space label (e.g. Prod)")
	cmd.Flags().StringVar(&region, "region", "", "value for the well-known \"Region\" Space label (e.g. us-east1)")
	cmd.Flags().StringVar(&owner, "owner", "", "value for the well-known \"Owner\" Space label (e.g. Engineering)")
	cmd.Flags().StringVar(&variant, "variant", "", "value for the well-known \"Variant\" Space label (e.g. Base)")
	cmd.Flags().StringVar(&workDir, "work-dir", ".", "working directory")
	cmd.Flags().BoolVar(&retry, "retry", false, "first upload only: tolerate Units, Invocations, and Links that already exist so a partially-failed first upload can be retried. Space auto-create is always idempotent — this flag only affects content. Ignored on reconcile (reconcile is always idempotent).")
	cmd.Flags().BoolVar(&yes, "yes", false, "reconcile only: skip confirmation for deletes (required when stdin is not a TTY and the plan contains deletes)")
	cmd.Flags().StringVar(&changeSetSlug, "changeset", "", "reconcile only: ChangeSet slug to open per Space (default: installer-update-<timestamp>)")
	return cmd
}

// runUploadReconcile is the second-and-subsequent-upload code path:
// reads upload.yaml, computes a diff against ConfigHub, opens a
// ChangeSet, and applies. Same semantics as the previous standalone
// `installer update` command.
func runUploadReconcile(ctx context.Context, workDir string, loaded *ipkg.Loaded, meta spaceMetaInput, unitAnnotations, unitLabels []string, yes bool, changeSetSlug, spaceFlag, spacePatternFlag string) error {
	if spaceFlag != "" || spacePatternFlag != "" {
		fmt.Fprintln(os.Stderr, "note: --space / --space-pattern are read from upload.yaml on reconcile; flag value ignored")
	}
	uploadDoc, err := readUploadDoc(workDir)
	if err != nil {
		return err
	}
	if err := cubctx.CheckMatches(ctx, uploadDoc.Spec.OrganizationID, uploadDoc.Spec.Server); err != nil {
		return err
	}
	lock, err := loadLockIfNeeded(workDir, loaded.Package)
	if err != nil {
		return err
	}
	pattern := uploadDoc.Spec.SpacePattern
	if pattern == "" {
		pattern = "{{.PackageName}}"
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

	// Reconcile Space metadata and resolve the per-Space target up front,
	// before diffing, so a metadata-only reconcile (no Unit changes) still
	// updates the Space and so any adds bind to the right target. The
	// well-known labels / --space-* pairs are applied only when re-passed
	// ("set once, update if re-passed"); --target, when omitted, is read
	// back from each Space's recorded TargetID annotation.
	targets := map[string]string{}
	for _, pkg := range packages {
		var targetID, effective string
		switch {
		case meta.targetSet && meta.target != "":
			effective = meta.target
			id, err := resolveTargetID(ctx, pkg.SpaceSlug, meta.target)
			if err != nil {
				return err
			}
			targetID = id
		case !meta.targetSet:
			// Read the recorded TargetID (a UUID) and resolve it to a
			// <space>/<slug> reference so adds can bind even when the
			// Target lives in another Space (a bare UUID --target is
			// resolved scoped to the Unit's Space and would 404).
			id, err := readSpaceTargetID(ctx, pkg.SpaceSlug)
			if err != nil {
				return err
			}
			if id != "" {
				ref, err := targetRefFromID(ctx, id)
				if err != nil {
					return err
				}
				effective = ref
			}
		}
		targets[pkg.SpaceSlug] = effective
		if err := applySpaceMeta(ctx, pkg.SpaceSlug, meta.labelArgs(pkg.Name, false), meta.annotationArgs(targetID)); err != nil {
			return err
		}
	}

	plan, err := diff.Compute(ctx, packages)
	if err != nil {
		return err
	}
	diff.Print(os.Stdout, plan)

	if !plan.HasChanges() {
		return nil
	}

	if changeSetSlug == "" {
		changeSetSlug = changeset.DefaultSlug(time.Now())
	}
	res, err := diff.Apply(ctx, plan, diff.ApplyOptions{
		Yes:                  yes,
		ChangeSetSlug:        changeSetSlug,
		ChangeSetDescription: fmt.Sprintf("installer upload (reconcile) from %s@%s", loaded.Package.Metadata.Name, loaded.Package.InstallerMetadata.Version),
		Targets:              targets,
		Annotations:          unitAnnotations,
		Labels:               unitLabels,
		PostSpaceHook:        refreshInstallerRecordHook(packages),
	})
	if err != nil {
		return err
	}
	fmt.Printf("\nApplied: %d created, %d updated, %d emptied.\n", res.Created, res.Updated, res.Deleted)
	if len(res.ChangeSetsOpened) > 0 {
		fmt.Println("Updates revertable via:")
		for _, cs := range res.ChangeSetsOpened {
			fmt.Printf("  %s\n", changeset.RestoreCommand(cs.Space, cs.Slug, cs.UpdatedSlugs))
		}
	}
	return nil
}

// refreshInstallerRecordHook builds an Apply PostSpaceHook that
// matches each SpacePlan to its upload.Package by space slug and
// upserts the installer-record Unit so the cub-side spec body stays
// in sync with the local out/spec/. Without this, a subsequent
// setup reads stale state (notably ImageOverrides) from ConfigHub
// via wizard.LoadPriorState.
func refreshInstallerRecordHook(packages []upload.Package) diff.PackageRefresher {
	bySlug := make(map[string]upload.Package, len(packages))
	for _, p := range packages {
		bySlug[p.SpaceSlug] = p
	}
	return func(ctx context.Context, sp diff.SpacePlan) error {
		pkg, ok := bySlug[sp.SpaceSlug]
		if !ok {
			return nil
		}
		return upload.RefreshInstallerRecord(ctx, pkg)
	}
}

// uploadOnePackage creates pkg.SpaceSlug if missing, stamps the Space's
// well-known labels + operator metadata + TargetID annotation, uploads
// each rendered manifest as a Unit (with --target/--unit-annotation/
// --unit-label plus the Package label and PackageVersion annotation),
// splits AppConfig-annotated ConfigMaps into AppConfig Unit +
// render-configmap Invocation + placeholder + Upsert link, creates the
// untargeted installer-record Unit, and runs the intra-Space link
// inference.
func uploadOnePackage(ctx context.Context, pkg upload.Package, meta spaceMetaInput, unitAnnotations, unitLabels []string, retry bool) error {
	fmt.Printf("== %s@%s → Space %s ==\n", pkg.Name, pkg.Version, pkg.SpaceSlug)

	if err := ensureSpace(pkg.SpaceSlug); err != nil {
		return err
	}

	// Stamp the Space's well-known labels and operator-supplied metadata.
	// Component defaults to the package name on a first upload. When
	// --target is given, record its resolved TargetID so later reconciles
	// can omit --target and read it back from here.
	var targetID string
	if meta.target != "" {
		id, err := resolveTargetID(ctx, pkg.SpaceSlug, meta.target)
		if err != nil {
			return err
		}
		targetID = id
	}
	if err := applySpaceMeta(ctx, pkg.SpaceSlug, meta.labelArgs(pkg.Name, true), meta.annotationArgs(targetID)); err != nil {
		return err
	}

	// Read the wizard's namespace from out/spec/inputs.yaml. AppConfig
	// placeholders need it post-render: render-configmap stamps
	// metadata.namespace=confighubplaceholder onto the rendered ConfigMap
	// (it expects a namespace link to fill that in at apply time), but our
	// intra-Space link inference matches by namespace and a placeholder
	// value never resolves. set-namespace on the placeholder Unit lets
	// the inference wire the Deployment → ConfigMap link.
	inputs, err := readInputs(filepath.Join(pkg.SpecDir, "inputs.yaml"))
	if err != nil {
		return fmt.Errorf("read inputs.yaml for %s: %w", pkg.Name, err)
	}
	namespace := inputs.Spec.Namespace

	packageLabel := "Package=" + pkg.Name
	packageVersionAnnotation := "PackageVersion=" + pkg.Version

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
			if err := uploadAppConfigManifest(pkg, appCfg, meta.target, packageLabel, packageVersionAnnotation, namespace, unitAnnotations, unitLabels, retry); err != nil {
				return err
			}
			continue
		}
		slug := trimExt(e.Name())
		cubArgs := []string{"unit", "create"}
		if retry {
			cubArgs = append(cubArgs, "--allow-exists")
		}
		cubArgs = append(cubArgs, "--space", pkg.SpaceSlug, "--merge-external-source", e.Name(),
			"--label", packageLabel, "--annotation", packageVersionAnnotation)
		if meta.target != "" {
			cubArgs = append(cubArgs, "--target", meta.target)
		}
		for _, a := range unitAnnotations {
			cubArgs = append(cubArgs, "--annotation", a)
		}
		for _, l := range unitLabels {
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

	if err := createInstallerRecordUnit(pkg, retry); err != nil {
		return err
	}
	renderedSecrets := loadRenderedSecretsFromDir(pkg.SecretsDir)
	skipUnmatched := skipKeysForRenderedSecrets(renderedSecrets)
	if err := upload.ReconcileLinks(ctx, pkg.SpaceSlug, pkg.Name, skipUnmatched); err != nil {
		return err
	}
	reportSecretsNotUploaded(pkg, renderedSecrets)
	return nil
}

// renderedSecret describes one rendered Secret manifest in pkg.SecretsDir.
// We parse the file enough to print kind/name/namespace in the reminder
// AND to skip the matching entry in ReconcileLinks' unmatched-references
// list — otherwise the operator sees the same Secret called out twice.
type renderedSecret struct {
	Filename  string
	Type      string // apiVersion/Kind, e.g. v1/Secret
	Name      string
	Namespace string
}

// loadRenderedSecretsFromDir scans pkg.SecretsDir and returns one entry
// per .yaml/.yml file. Best-effort: a missing dir, an unparseable file,
// or a doc that doesn't look like a Kubernetes resource is just skipped.
// The dir may legitimately not exist when no Secrets were rendered.
func loadRenderedSecretsFromDir(secretsDir string) []renderedSecret {
	if secretsDir == "" {
		return nil
	}
	entries, err := os.ReadDir(secretsDir)
	if err != nil {
		return nil
	}
	var out []renderedSecret
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasSuffix(n, ".yaml") && !strings.HasSuffix(n, ".yml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(secretsDir, n))
		if err != nil {
			continue
		}
		var doc struct {
			APIVersion string `yaml:"apiVersion"`
			Kind       string `yaml:"kind"`
			Metadata   struct {
				Name      string `yaml:"name"`
				Namespace string `yaml:"namespace"`
			} `yaml:"metadata"`
		}
		if err := yaml.Unmarshal(data, &doc); err != nil {
			continue
		}
		if doc.Kind == "" || doc.Metadata.Name == "" {
			continue
		}
		out = append(out, renderedSecret{
			Filename:  n,
			Type:      doc.APIVersion + "/" + doc.Kind,
			Name:      doc.Metadata.Name,
			Namespace: doc.Metadata.Namespace,
		})
	}
	return out
}

func skipKeysForRenderedSecrets(secrets []renderedSecret) map[string]struct{} {
	if len(secrets) == 0 {
		return nil
	}
	keys := make(map[string]struct{}, len(secrets))
	for _, s := range secrets {
		keys[upload.UnmatchedKey(s.Type, s.Name)] = struct{}{}
	}
	return keys
}

// reportSecretsNotUploaded surfaces any rendered Secrets that landed in
// pkg.SecretsDir. The installer renders them (so the operator can inspect
// and apply them out-of-band) but never uploads them as Units — they
// don't belong in ConfigHub today.
func reportSecretsNotUploaded(pkg upload.Package, secrets []renderedSecret) {
	if len(secrets) == 0 {
		return
	}
	fmt.Println()
	fmt.Printf("Note: %d rendered Secret(s) in %s were NOT uploaded to ConfigHub.\n", len(secrets), pkg.SecretsDir)
	fmt.Println("Apply them out-of-band (e.g., `kubectl apply -f`) or stage them however your")
	fmt.Println("environment manages secrets:")
	for _, s := range secrets {
		fullName := s.Name
		if s.Namespace != "" {
			fullName = s.Namespace + "/" + s.Name
		}
		fmt.Printf("  - %s %q (%s)\n", s.Type, fullName, s.Filename)
	}
}

// uploadAppConfigManifest materializes the AppConfig bundle in pkg's
// Space:
//
//  1. render-configmap Invocation — one per carrier ConfigMap. Its
//     toolchain matches the AppConfig Unit; its args encode immutable /
//     as-key-value mode.
//  2. AppConfig Unit — Data is the extracted raw file body. No target;
//     it is a pure data source. This is the day-2 source of truth in the
//     native format (.properties, .env, etc.).
//  3. Placeholder Kubernetes/YAML ConfigMap Unit — carries a "-rendered"
//     suffix so it doesn't collide with the AppConfig Unit (which owns
//     the carrier's name, equal to the rendered ConfigMap's
//     metadata.name). Body is empty at creation; populated by the Upsert
//     link below. Other workload Units reference the ConfigMap by
//     metadata.name; intra-Space link inference wires them into this
//     placeholder via the rendered content.
//  4. Upsert link placeholder → AppConfig Unit, with the render-configmap
//     Invocation as its TransformInvocation. On resolution the function
//     renders the AppConfig Unit's Data into a Kubernetes ConfigMap and
//     upserts it into the placeholder's Data. --auto-update keeps it in
//     sync as the AppConfig Unit changes. No bridge, no worker, no apply.
//  5. set-namespace on the placeholder so intra-Space link inference can
//     match it (the rendered ConfigMap carries a namespace placeholder).
//
// The placeholder Unit inherits the upload-wide `target` flag (typically a
// Kubernetes namespace target) so it applies into the same place as every
// other rendered manifest.
//
// Idempotent via --allow-exists on the Invocation, Units, and link;
// re-running upload after re-rendering refreshes the AppConfig Unit body
// via the --merge-external-source mechanism the regular path uses.
func uploadAppConfigManifest(pkg upload.Package, appCfg *upload.AppConfigManifest, target, packageLabel, packageVersionAnnotation, namespace string, unitAnnotations, unitLabels []string, retry bool) error {
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

	// 2. Create the render-configmap Invocation. The "--" separator tells
	//    cub the following tokens are function arguments, not invocation
	//    flags.
	invArgs := []string{"invocation", "create"}
	if retry {
		invArgs = append(invArgs, "--allow-exists")
	}
	invArgs = append(invArgs,
		"--space", pkg.SpaceSlug,
		"--label", packageLabel,
		appCfg.InvocationSlug(), appCfg.Toolchain,
		"--", "render-configmap",
	)
	invArgs = append(invArgs, appCfg.RenderConfigMapArgs()...)
	icmd := exec.Command("cub", invArgs...)
	icmd.Stdout = os.Stdout
	icmd.Stderr = os.Stderr
	if err := icmd.Run(); err != nil {
		return fmt.Errorf("cub invocation create %s in %s: %w", appCfg.InvocationSlug(), pkg.SpaceSlug, err)
	}

	// 3. Create the AppConfig Unit (no target — pure data source).
	unitArgs := []string{"unit", "create"}
	if retry {
		unitArgs = append(unitArgs, "--allow-exists")
	}
	unitArgs = append(unitArgs,
		"--space", pkg.SpaceSlug,
		"--toolchain", appCfg.Toolchain,
		"--merge-external-source", filepath.Base(appCfg.ManifestPath),
		"--label", packageLabel,
		"--annotation", packageVersionAnnotation,
	)
	for _, a := range unitAnnotations {
		unitArgs = append(unitArgs, "--annotation", a)
	}
	for _, l := range unitLabels {
		unitArgs = append(unitArgs, "--label", l)
	}
	unitArgs = append(unitArgs, appCfg.UnitSlug(), tmp.Name())
	ucmd := exec.Command("cub", unitArgs...)
	ucmd.Stdout = os.Stdout
	ucmd.Stderr = os.Stderr
	if err := ucmd.Run(); err != nil {
		return fmt.Errorf("cub unit create %s in %s: %w", appCfg.UnitSlug(), pkg.SpaceSlug, err)
	}

	// 4. Create the placeholder ConfigMap Unit. Body is empty at creation;
	//    populated by the Upsert link below.
	placeholderArgs := []string{"unit", "create"}
	if retry {
		placeholderArgs = append(placeholderArgs, "--allow-exists")
	}
	placeholderArgs = append(placeholderArgs,
		"--space", pkg.SpaceSlug,
		"--toolchain", "Kubernetes/YAML",
		"--label", packageLabel,
		"--annotation", packageVersionAnnotation,
	)
	if target != "" {
		placeholderArgs = append(placeholderArgs, "--target", target)
	}
	for _, a := range unitAnnotations {
		placeholderArgs = append(placeholderArgs, "--annotation", a)
	}
	for _, l := range unitLabels {
		placeholderArgs = append(placeholderArgs, "--label", l)
	}
	placeholderArgs = append(placeholderArgs, appCfg.PlaceholderSlug())
	pcmd := exec.Command("cub", placeholderArgs...)
	pcmd.Stdout = os.Stdout
	pcmd.Stderr = os.Stderr
	if err := pcmd.Run(); err != nil {
		return fmt.Errorf("cub unit create %s in %s: %w", appCfg.PlaceholderSlug(), pkg.SpaceSlug, err)
	}

	// 5. Upsert link placeholder → AppConfig Unit. On resolution the
	//    TransformInvocation (render-configmap) renders the AppConfig
	//    Unit's Data into a Kubernetes ConfigMap and upserts it into the
	//    placeholder's Data; --auto-update keeps it in sync as the
	//    AppConfig Unit changes. Slug "-" tells the server to assign one.
	linkArgs := []string{"link", "create"}
	if retry {
		linkArgs = append(linkArgs, "--allow-exists")
	}
	linkArgs = append(linkArgs,
		"--wait",
		"--space", pkg.SpaceSlug,
		"--update-type", "Upsert", "--auto-update",
		"--transform-invocation", pkg.SpaceSlug+"/"+appCfg.InvocationSlug(),
		"--label", packageLabel,
		"-", appCfg.PlaceholderSlug(), appCfg.UnitSlug(),
	)
	lcmd := exec.Command("cub", linkArgs...)
	lcmd.Stdout = os.Stdout
	lcmd.Stderr = os.Stderr
	if err := lcmd.Run(); err != nil {
		return fmt.Errorf("cub link create %s → %s in %s: %w", appCfg.PlaceholderSlug(), appCfg.UnitSlug(), pkg.SpaceSlug, err)
	}

	// 6. Stamp the correct namespace onto the placeholder Unit's now-
	//    populated Data. render-configmap writes
	//    metadata.namespace=confighubplaceholder (it expects a namespace
	//    link to fill that in at apply time). But link inference (the
	//    intra-Space pass below) matches by metadata.namespace, and a
	//    placeholder value never resolves, so Deployments referencing
	//    the carrier by name don't link to the placeholder Unit. Setting
	//    the real namespace here unblocks that inference. The wizard's
	//    Inputs.Spec.Namespace is the authoritative source.
	if namespace != "" {
		fcmd := exec.Command("cub", "function", "do", "--quiet",
			"--space", pkg.SpaceSlug,
			"--toolchain", "Kubernetes/YAML",
			"--unit", appCfg.PlaceholderSlug(),
			"set-namespace", namespace)
		fcmd.Stdout = os.Stdout
		fcmd.Stderr = os.Stderr
		if err := fcmd.Run(); err != nil {
			return fmt.Errorf("cub function do set-namespace on %s in %s: %w", appCfg.PlaceholderSlug(), pkg.SpaceSlug, err)
		}
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
func createInstallerRecordUnit(pkg upload.Package, retry bool) error {
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

	args := []string{"unit", "create"}
	if retry {
		args = append(args, "--allow-exists")
	}
	args = append(args,
		"--space", pkg.SpaceSlug,
		"--annotation", "installer.confighub.com/role=installer-record",
		"--annotation", "installer.confighub.com/package="+pkg.Name,
		"--annotation", "PackageVersion="+pkg.Version,
		"--label", "Package="+pkg.Name,
		upload.InstallerRecordSlug, tmp.Name(),
	)
	ccmd := exec.Command("cub", args...)
	ccmd.Stdout = os.Stdout
	ccmd.Stderr = os.Stderr
	if err := ccmd.Run(); err != nil {
		return fmt.Errorf("cub unit create installer-record in %s: %w", pkg.SpaceSlug, err)
	}
	return nil
}

// createCrossSpaceLink wires the parent's record Unit to a dep's record
// Unit. The 4th positional arg to `cub link create` is the target Space.
func createCrossSpaceLink(l upload.CrossSpaceLink, retry bool) error {
	args := []string{"link", "create"}
	if retry {
		args = append(args, "--allow-exists")
	}
	args = append(args,
		"--space", l.FromSpace, "--quiet",
		"--label", "Package="+l.Package,
		l.Slug, l.FromUnit, l.ToUnit, l.ToSpace,
	)
	ccmd := exec.Command("cub", args...)
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

// reservedSpaceLabelKeys maps each well-known Space label to the flag
// that owns it, so --space-label can reject attempts to set them
// directly.
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

// spaceMetaInput carries the Space-level metadata flags for one upload
// run: the well-known capitalized labels (Component/Layer/Environment/
// Owner/Variant), the free-form --space-label / --space-annotation
// pairs, and the --target whose resolved TargetID is recorded as a Space
// annotation. None of this is persisted in upload.yaml — ConfigHub is
// the source of truth for it; the installer only needs enough to find
// the Space(s) again.
type spaceMetaInput struct {
	component   string
	layer       string
	environment string
	region      string
	owner       string
	variant     string
	labels      []string // --space-label key=value pairs
	annotations []string // --space-annotation key=value pairs
	target      string   // --target ref (slug, space/slug, or UUID)

	componentSet   bool
	layerSet       bool
	environmentSet bool
	regionSet      bool
	ownerSet       bool
	variantSet     bool
	targetSet      bool
}

// labelArgs returns the Space label key=value pairs to apply via
// `cub space update --patch`. On a first upload (firstUpload=true) the
// Component label is always included, defaulting to the package name
// when --component was not given. On a reconcile the well-known labels
// are included only when their flag was explicitly passed this run
// ("set once, update if re-passed"); --space-label pairs are always
// applied because their presence means the operator passed them.
func (m spaceMetaInput) labelArgs(pkgName string, firstUpload bool) []string {
	var out []string
	if firstUpload || m.componentSet {
		comp := m.component
		if comp == "" {
			comp = pkgName
		}
		out = append(out, "Component="+comp)
	}
	if m.layerSet {
		out = append(out, "Layer="+m.layer)
	}
	if m.environmentSet {
		out = append(out, "Environment="+m.environment)
	}
	if m.regionSet {
		out = append(out, "Region="+m.region)
	}
	if m.ownerSet {
		out = append(out, "Owner="+m.owner)
	}
	if m.variantSet {
		out = append(out, "Variant="+m.variant)
	}
	out = append(out, m.labels...)
	return out
}

// annotationArgs returns the Space annotation key=value pairs to apply.
// targetID, when non-empty, is recorded as the well-known "TargetID"
// annotation; the caller resolves it only when it intends to (re)write
// it (a first upload with --target, or a reconcile that re-passed
// --target).
func (m spaceMetaInput) annotationArgs(targetID string) []string {
	out := append([]string(nil), m.annotations...)
	if targetID != "" {
		out = append(out, "TargetID="+targetID)
	}
	return out
}

// applySpaceMeta patches the Space's labels and annotations in one
// `cub space update --patch` call. Patch mode merges, so labels and
// annotations the call does not name are preserved. A no-op when there
// is nothing to set.
func applySpaceMeta(ctx context.Context, space string, labelKVs, annotationKVs []string) error {
	if len(labelKVs) == 0 && len(annotationKVs) == 0 {
		return nil
	}
	args := []string{"space", "update", "--patch", "--quiet"}
	for _, l := range labelKVs {
		args = append(args, "--label", l)
	}
	for _, a := range annotationKVs {
		args = append(args, "--annotation", a)
	}
	args = append(args, space)
	ccmd := exec.Command("cub", args...)
	ccmd.Stderr = os.Stderr
	if err := ccmd.Run(); err != nil {
		return fmt.Errorf("cub space update --patch %s: %w", space, err)
	}
	return nil
}

// resolveTargetID resolves a --target ref to its TargetID UUID. The ref
// is interpreted the way `cub unit create --target` interprets it: a
// bare slug resolves in the Unit's Space (unitSpace), a
// <space-slug>/<target-slug> resolves the target in the named Space, and
// a UUID resolves directly.
func resolveTargetID(ctx context.Context, unitSpace, targetRef string) (string, error) {
	lookupSpace, slug := unitSpace, targetRef
	if i := strings.IndexByte(targetRef, '/'); i >= 0 {
		lookupSpace, slug = targetRef[:i], targetRef[i+1:]
	}
	var stdout, stderr bytes.Buffer
	ccmd := exec.CommandContext(ctx, "cub", "target", "get",
		"--space", lookupSpace, "-o", "jq=.Target.TargetID", slug)
	ccmd.Stdout = &stdout
	ccmd.Stderr = &stderr
	if err := ccmd.Run(); err != nil {
		return "", fmt.Errorf("cub target get %s in %s: %w\n%s", slug, lookupSpace, err, stderr.String())
	}
	id := strings.Trim(strings.TrimSpace(stdout.String()), "\"")
	if id == "" || id == "null" {
		return "", fmt.Errorf("target %q in space %s has no TargetID", slug, lookupSpace)
	}
	return id, nil
}

// targetRefFromID resolves a TargetID UUID to a
// "<space-slug>/<target-slug>" reference that `cub unit create --target`
// can bind. A bare UUID can't be passed straight through: cub resolves a
// UUID --target scoped to the Unit's Space (parse.go), so a Target living
// in another Space 404s. The reference form looks the Target up in its
// own Space. Returns "" (no error) when no Target with that ID exists
// (e.g. it was deleted since the annotation was written).
func targetRefFromID(ctx context.Context, targetID string) (string, error) {
	var stdout, stderr bytes.Buffer
	ccmd := exec.CommandContext(ctx, "cub", "target", "list",
		"--space", "*",
		"--where", fmt.Sprintf("TargetID = '%s'", targetID),
		"-o", `jq=.[] | .Space.Slug + "/" + .Target.Slug`)
	ccmd.Stdout = &stdout
	ccmd.Stderr = &stderr
	if err := ccmd.Run(); err != nil {
		return "", fmt.Errorf("cub target list (resolve TargetID %s): %w\n%s", targetID, err, stderr.String())
	}
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		ref := strings.Trim(strings.TrimSpace(line), "\"")
		if ref != "" && ref != "/" {
			return ref, nil
		}
	}
	return "", nil
}

// readSpaceTargetID reads the "TargetID" annotation a prior upload
// recorded on the Space. Returns "" (no error) when the Space has no
// such annotation — e.g. it was uploaded without --target.
func readSpaceTargetID(ctx context.Context, space string) (string, error) {
	var stdout, stderr bytes.Buffer
	ccmd := exec.CommandContext(ctx, "cub", "space", "get",
		"-o", "jq=.Space.Annotations.TargetID", space)
	ccmd.Stdout = &stdout
	ccmd.Stderr = &stderr
	if err := ccmd.Run(); err != nil {
		return "", fmt.Errorf("cub space get %s: %w\n%s", space, err, stderr.String())
	}
	id := strings.Trim(strings.TrimSpace(stdout.String()), "\"")
	if id == "null" {
		return "", nil
	}
	return id, nil
}

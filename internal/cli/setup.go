// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/confighub/installer/internal/wizard"
	"github.com/confighub/installer/pkg/installer"
)

// flowOptions configures the shared pull+wizard+render flow used by
// both `setup` and `wizard`.
type flowOptions struct {
	workDir string
	pullRef string // empty means: do not pull; require an existing <work-dir>/package/.
	// recoverSpace is the Space to recover a prior install from when the
	// work-dir has none of its own.
	recoverSpace string

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
	outputOCI string
}

// runFlow is the shared body of `installer setup` and `installer wizard`: it
// turns the flags into installer.SetupOptions, runs Setup, and prints what to
// run next.
func runFlow(ctx context.Context, opts flowOptions) error {
	if opts.preset != "" && len(opts.selectFlags) > 0 {
		return fmt.Errorf("--components and --select are mutually exclusive")
	}
	inputs, err := wizard.ParseInputFlags(opts.inputFlags)
	if err != nil {
		return err
	}
	images, err := wizard.ParseSetImageFlags(opts.setImage)
	if err != nil {
		return err
	}
	if opts.workDir == "" {
		opts.workDir = "."
	}
	absWork, err := filepath.Abs(opts.workDir)
	if err != nil {
		return err
	}
	var terminal *installer.Terminal
	if !opts.nonInteractive && term.IsTerminal(int(os.Stdin.Fd())) {
		terminal = &installer.Terminal{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}
	}

	inst, err := newInstaller()
	if err != nil {
		return err
	}
	_, err = inst.Setup(ctx, installer.SetupOptions{
		WorkDir:   absWork,
		Pull:      opts.pullRef,
		Space:     opts.recoverSpace,
		Base:      opts.baseName,
		Namespace: opts.namespace,
		Select:    opts.selectFlags,
		Preset:    opts.preset,
		Inputs:    inputs,
		SetImage:  images,
		Reuse:     opts.reuse,
		Terminal:  terminal,
		Render:    opts.runRender,
		OutputOCI: opts.outputOCI,
	})
	if err != nil {
		return setupError(err, absWork)
	}
	if !opts.runRender {
		fmt.Printf("Next: %s render --work-dir %s\n", InvocationName(), absWork)
		return nil
	}
	fmt.Printf("Next: %s upload --work-dir %s\n", InvocationName(), absWork)
	return nil
}

// setupError adds the flag that remedies a Setup failure, when there is one.
func setupError(err error, workDir string) error {
	var newRequired *installer.NewRequiredInputsError
	switch {
	case errors.Is(err, installer.ErrNoPackage):
		return fmt.Errorf("%v — pass --pull <ref> to fetch one, --space <slug> to re-enter an install from its installer record, or `%s pull <ref> --work-dir %s` first",
			err, InvocationName(), workDir)
	case errors.Is(err, installer.ErrRecordedSourcePull):
		return fmt.Errorf("%v; pass --pull <ref> to pull the package from elsewhere", err)
	case errors.Is(err, installer.ErrNoInstallerRecord):
		return fmt.Errorf("%v; pass --pull <ref> to install a package", err)
	case errors.Is(err, installer.ErrNoRecordedSource):
		return fmt.Errorf("%v; pass --pull <ref>", err)
	case errors.Is(err, installer.ErrSeveralPackages):
		return fmt.Errorf("%v; pass --pull <ref> to say which package", err)
	case errors.Is(err, installer.ErrPresetAndSelect):
		return fmt.Errorf("--components and --select are mutually exclusive")
	case errors.Is(err, installer.ErrNoPriorInstall):
		return fmt.Errorf("--reuse requires a prior install — none found in this work-dir")
	case errors.Is(err, installer.ErrNamespaceRequired):
		return fmt.Errorf("--namespace is required in non-interactive mode")
	case errors.Is(err, installer.ErrInputTypesChanged):
		return fmt.Errorf("%v — pass --input <name>=<value> for each, or run `%s setup --work-dir %s` on a terminal to answer them again", err, InvocationName(), workDir)
	case errors.As(err, &newRequired):
		return fmt.Errorf("%s", wizard.FormatNewRequiredHint(InvocationName(), workDir, newRequired.Inputs))
	}
	return err
}

// newInstaller returns the Installer the CLI's commands run: printing to the
// process's stdout and stderr, and using the cub environment's ConfigHub client.
func newInstaller() (*installer.Installer, error) {
	return installer.New(installer.Options{
		Out:    os.Stdout,
		ErrOut: os.Stderr,
		Warn:   warn,
	})
}

// warn prints a warning, adding the flag that remedies it.
func warn(err error) {
	msg := err.Error()
	switch {
	case errors.Is(err, installer.ErrSeveralInstalls):
		msg += "; pass --space to choose one"
	case errors.Is(err, installer.ErrSeveralPackages):
		msg += "; pass --pull <ref> to say which package"
	}
	fmt.Fprintln(os.Stderr, "warning:", msg)
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
                     <work-dir>/package/; spec docs to <work-dir>/out/record/;
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
  - <work-dir>/out/record/{selection,inputs,facts}.yaml exist → load prior
    install state from them.
  - else an earlier upload of the package left an "installer" record Unit in
    ConfigHub → load prior install state from it. This recovers a fresh
    clone. --space names the Space to recover from; without it the
    package's only install in the organization is used, and several
    installs are refused. The record also says where the package was
    pulled from, so in an empty work-dir "setup --space <slug>" needs no
    --pull: anyone can change an install from what ConfigHub holds, with
    no work-dir kept in git or elsewhere.
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
	cmd.Flags().StringVar(&opts.recoverSpace, "space", "", "Space to recover a prior install from when the work-dir has no out/record/ of its own (default: the package's only install in the organization); a Space that does not exist yet, or holds no installer record, is a fresh install there. Without --pull, a work-dir with no package pulls the one the Space's installer record says the install came from")
	cmd.Flags().StringVar(&opts.baseName, "base", "", "base name (default: package's default base)")
	cmd.Flags().StringVar(&opts.namespace, "namespace", "", "Kubernetes namespace for the install (exposed to chain templates as {{ .Namespace }}). Required for fresh install in non-interactive mode.")
	cmd.Flags().StringSliceVar(&opts.selectFlags, "select", nil, "component to select (repeatable; required-deps closed automatically). Mutually exclusive with --components.")
	cmd.Flags().StringArrayVar(&opts.inputFlags, "input", nil, "input value as key=value (repeatable)")
	cmd.Flags().BoolVar(&opts.nonInteractive, "non-interactive", false, "do not prompt; require --input/--select for everything")
	cmd.Flags().BoolVar(&opts.reuse, "reuse", false, "skip prompts and re-use the prior install's selection + inputs (requires prior state)")
	cmd.Flags().StringVar(&opts.preset, "components", "", "component preset: minimal | default | all | selected. Mutually exclusive with --select.")
	cmd.Flags().StringArrayVar(&opts.setImage, "set-image", nil, "image override as name=ref (repeatable), set before render as `kustomize edit set image` would, for every container of the chosen base and selected components; name is the image without its tag or digest, which a container runs or an images: block declares")
	cmd.Flags().StringVar(&opts.outputOCI, "output-oci", "", "write rendered manifests to a local OCI layout path or push them to oci://host/repo:tag")
	return cmd
}

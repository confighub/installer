// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/confighub/installer/pkg/installer"
)

func newInstallCmd() *cobra.Command {
	var (
		answers flowOptions
		flags   uploadFlags
		yes     bool
	)
	cmd := &cobra.Command{
		Use:   "install <package>",
		Short: "Install a package into ConfigHub: pull, wizard, render and upload",
		Long: `Install takes a package to its Spaces in ConfigHub in one command: setup
--pull <package> followed by upload. <package> is an oci:// reference, a
local directory, or a .tgz.

Install checks the ConfigHub session before pulling anything. It does not
release the Spaces, and creates no variants or Targets.

Installing again upgrades: a work-dir with an earlier install, or a Space
whose installer record holds one, is re-entered as setup re-enters it, so
answers still apply carry forward, and only what changed is written.

--space, which is required, is the Space to install into: the installer
record there is re-entered, and the package uploads to it. Its
dependencies, if it has any, upload into the Spaces --space-pattern names.

The upload asks before emptying Units whose resources are no longer
rendered, as upload does, unless --yes is given. Rendered Secrets stay in
<work-dir>/out/secrets/ and are never uploaded.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := commandContext(cmd)
			flags.noteChanged(cmd)
			answers.pullRef = args[0]
			answers.workDir = flags.workDir
			answers.runRender = true
			setup, err := answers.setupOptions()
			if err != nil {
				return err
			}
			uploadOpts, err := flags.options()
			if err != nil {
				return err
			}
			opts := installer.InstallOptions{Space: flags.space, Setup: setup, Upload: uploadOpts}
			if !yes {
				opts.ConfirmEmpty = confirmEmpties
			}

			inst, err := newInstaller(ctx, true)
			if err != nil {
				return err
			}
			res, err := inst.Install(ctx, opts)
			if err != nil && res.Upload == nil {
				return installError(inst, res, err, setup.WorkDir)
			}
			return reportUpload(ctx, res.Upload, err, "installing")
		},
	}
	registerAnswerFlags(cmd, &answers)
	flags.register(cmd)
	cmd.Flags().Lookup("space").Usage = "Space to install into (required): its installer record is re-entered, and the package uploads to it; dependencies upload into --space-pattern's Spaces"
	_ = cmd.MarkFlagRequired("space")
	cmd.Flags().BoolVar(&yes, "yes", false, "empty Units whose resources left the render without asking")
	return cmd
}

// installError adds the flag or command that remedies an install that stopped
// before uploading, when there is one.
func installError(inst *cliInstaller, res *installer.InstallResult, err error, workDir string) error {
	switch {
	case errors.Is(err, installer.ErrNoConfigHub) && inst.hubErr != nil:
		return notAuthenticated(inst.hubErr)
	case errors.Is(err, installer.ErrNotAuthenticated):
		return notAuthenticated(err)
	case res.Setup == nil:
		return setupError(err, workDir)
	}
	return uploadError(err, workDir)
}

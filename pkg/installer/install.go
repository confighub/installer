// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package installer

import "context"

// InstallOptions say what to install and where it goes.
type InstallOptions struct {
	// Space is the Space to install the package into, which is required:
	// the installer record there is re-entered, and the package uploads to
	// it. A dependency uploads to the Space Upload.SpacePattern names. It
	// replaces Setup.Space and Upload.Space, so an install never re-enters
	// one Space and uploads to another.
	Space string
	// Setup says what to install and how to answer the package's wizard.
	// Install always renders: Render is taken to be true.
	Setup SetupOptions
	// Upload says where the rendered packages go. Its WorkDir is Setup's.
	Upload UploadOptions
	// ConfirmEmpty is asked before an upload that would empty Units whose
	// resources are no longer rendered, which the next Release withdraws
	// from the cluster. When it is set, Install previews the upload, calls it
	// with those Units, and stops before writing if it returns an error. Nil
	// uploads without previewing.
	ConfirmEmpty func(units []string) error
}

// InstallResult is what Install set up and uploaded.
type InstallResult struct {
	// Setup is nil when Install stopped before setting up.
	Setup *SetupResult
	// Upload is nil when Install stopped before uploading. An upload refused
	// partway holds what landed before it was; one whose Unit writes failed
	// says so with Failed.
	Upload *UploadResult
}

// Install takes a package to its Spaces in ConfigHub in one call: Setup,
// rendering, then PrepareUpload and Upload. It verifies the ConfigHub session
// before pulling anything. It does not release, and creates no variants or
// Targets.
func (i *Installer) Install(ctx context.Context, opts InstallOptions) (*InstallResult, error) {
	res := &InstallResult{}
	if opts.Space == "" {
		return res, ErrSpaceRequired
	}
	if _, err := i.writeClient(ctx); err != nil {
		return res, err
	}
	setup := opts.Setup
	setup.Space = opts.Space
	setup.Render = true
	var err error
	if res.Setup, err = i.Setup(ctx, setup); err != nil {
		return res, err
	}

	upload := opts.Upload
	upload.Space = opts.Space
	upload.WorkDir = res.Setup.WorkDir
	plan, err := i.PrepareUpload(ctx, upload)
	if err != nil {
		return res, err
	}
	if opts.ConfirmEmpty != nil {
		if err := i.PreviewUpload(ctx, plan, PreviewOptions{}); err != nil {
			return res, err
		}
		if emptied := plan.Emptied(); len(emptied) > 0 {
			if err := opts.ConfirmEmpty(emptied); err != nil {
				return res, err
			}
		}
	}
	res.Upload, err = i.Upload(ctx, plan)
	return res, err
}

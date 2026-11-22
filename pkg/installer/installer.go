// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

// Package installer runs the installer's install lifecycle in the calling
// program: setup (pull, re-entry from prior state, wizard, dependency lock,
// render, rendered OCI output) and upload (plan, preview, apply), with the
// same results as the installer CLI given the same inputs. The CLI is built on
// it.
//
// An install's state is a work-dir on disk, laid out as the CLI lays it out:
// package/ holds the pulled package, out/record/ the choices and the lock,
// out/manifests/ the render. A program that has no work-dir of its own uses a
// temporary directory.
//
// Errors whose remedy is an option are, or wrap, the sentinel errors in this
// package, so a program can name its own flag or setting for the remedy.
package installer

import (
	"context"
	"fmt"
	"io"

	"github.com/confighub/sdk/core/cubapi"

	"github.com/confighub/installer/internal/hubclient"
	"github.com/confighub/installer/internal/version"
)

// Options configure an Installer. The zero value prints nothing and never
// prompts.
type Options struct {
	// ConfigHub is the client for reading installer records and uploading.
	// Nil uses the cub plugin environment or the active cub context, as the
	// installer CLI does, verifying the session before an upload.
	ConfigHub *cubapi.Client
	// Out receives progress lines; nil discards them.
	Out io.Writer
	// ErrOut receives warnings, unless Warn is set, and render's notices;
	// nil discards them.
	ErrOut io.Writer
	// Warn receives each warning: a problem that doesn't stop the operation,
	// such as an installer record that can't be read, after which setup starts
	// fresh. Nil prints "warning: <err>" to ErrOut. The error may be one of
	// this package's sentinel errors, for a program to add its own remedy.
	Warn func(error)
	// ClientVersion is sent with uploads; "" means the installer's version.
	ClientVersion string
}

// Installer runs the install lifecycle with one set of Options.
type Installer struct {
	opts Options
	out  io.Writer
	err  io.Writer
}

// New returns an Installer.
func New(opts Options) (*Installer, error) {
	i := &Installer{opts: opts, out: opts.Out, err: opts.ErrOut}
	if i.out == nil {
		i.out = io.Discard
	}
	if i.err == nil {
		i.err = io.Discard
	}
	if i.opts.ClientVersion == "" {
		i.opts.ClientVersion = version.Version
	}
	return i, nil
}

func (i *Installer) printf(format string, args ...any) {
	fmt.Fprintf(i.out, format, args...)
}

func (i *Installer) warn(err error) {
	if i.opts.Warn != nil {
		i.opts.Warn(err)
		return
	}
	fmt.Fprintln(i.err, "warning:", err)
}

// client returns the ConfigHub client for reads.
func (i *Installer) client(ctx context.Context) (*cubapi.Client, error) {
	if i.opts.ConfigHub != nil {
		return i.opts.ConfigHub, nil
	}
	return hubclient.Default.Client(ctx)
}

// writeClient returns the ConfigHub client for an upload, verifying the
// session first when it is the cub environment's, so an expired token is
// reported before anything else is done.
func (i *Installer) writeClient(ctx context.Context) (*cubapi.Client, error) {
	if i.opts.ConfigHub != nil {
		return i.opts.ConfigHub, nil
	}
	return hubclient.Default.Preflight(ctx)
}

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
	"runtime/debug"

	"github.com/confighub/sdk/core/cubapi"
	"oras.land/oras-go/v2/registry/remote/credentials"

	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/internal/sign"
	"github.com/confighub/installer/internal/version"
	"github.com/confighub/installer/pkg/api"
)

// Options configure an Installer. The zero value prints nothing, never
// prompts, reads nothing from ConfigHub, uses the Docker credential store,
// and checks no signatures. An Installer reads no environment variables and
// nothing under the home directory; the collectors packages declare inherit
// the environment.
type Options struct {
	// ConfigHub is the client for reading installer records and uploading.
	// Setup needs it to re-enter an install from ConfigHub; uploads always
	// do. Without it, Setup re-enters only from the work-dir, and fails with
	// ErrNoConfigHub when SetupOptions.Space needs a record.
	ConfigHub *cubapi.Client
	// Out receives progress lines; nil discards them.
	Out io.Writer
	// ErrOut receives warnings, unless Warn is set, the stderr of the
	// package's collector, and render's notices; nil discards them.
	ErrOut io.Writer
	// Warn receives each warning: a problem that doesn't stop the operation,
	// such as an installer record that can't be read, after which setup starts
	// fresh. Nil prints "warning: <err>" to ErrOut. The error may be one of
	// this package's sentinel errors, for a program to add its own remedy.
	Warn func(error)
	// Registry configures access to OCI registries.
	Registry RegistryOptions
	// SigningPolicy is enforced on every package pulled from a registry,
	// including dependencies. Nil enforces none. LoadSigningPolicy reads the
	// file the installer CLI uses.
	SigningPolicy *api.SigningPolicy
	// CosignBinary runs signature verification; "" is cosign on PATH.
	CosignBinary string
	// ClientVersion is sent with uploads and stamped into rendered OCI
	// artifacts; "" is the version of the installer module the program was
	// built with.
	ClientVersion string
	// TransformerCommand is the command kustomize runs for render's transform
	// step, which has to run RunTransformer on its stdin and write the result
	// to stdout, as `installer transformer` does. Nil re-executes the running
	// program, which then has to call RunTransformerIfRequested first thing in
	// main.
	TransformerCommand []string
}

// RegistryOptions configure access to OCI registries.
type RegistryOptions struct {
	// Credentials authenticates to registries; nil uses the Docker
	// credential store.
	Credentials credentials.Store
	// PlainHTTP uses HTTP for every registry. localhost and 127.0.0.1
	// always use HTTP.
	PlainHTTP bool
}

// LoadSigningPolicy reads the signing policy at path, or at
// ~/.config/installer/policy.yaml when path is "". It returns nil when the
// file doesn't exist.
func LoadSigningPolicy(path string) (*api.SigningPolicy, error) {
	return sign.LoadPolicyFile(path)
}

// Installer runs the install lifecycle with one set of Options.
type Installer struct {
	opts     Options
	out      io.Writer
	err      io.Writer
	registry ipkg.Registry
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
		i.opts.ClientVersion = moduleVersion()
	}
	i.registry = ipkg.Registry{
		Credentials: opts.Registry.Credentials,
		PlainHTTP:   opts.Registry.PlainHTTP,
		Verifier:    &sign.Verifier{Policy: opts.SigningPolicy, Cosign: opts.CosignBinary},
	}
	return i, nil
}

// modulePath is the installer module's path, which build info lists a
// program's version of.
const modulePath = "github.com/confighub/installer"

// moduleVersion is the installer's version: the one the installer binary was
// linked with, or else the version of the installer module the program
// requires.
func moduleVersion() string {
	if version.Version != "dev" {
		return version.Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range bi.Deps {
			if dep.Path != modulePath {
				continue
			}
			if dep.Replace != nil && dep.Replace.Version != "" {
				return dep.Replace.Version
			}
			return dep.Version
		}
	}
	return version.Version
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

// client returns the ConfigHub client, or ErrNoConfigHub.
func (i *Installer) client() (*cubapi.Client, error) {
	if i.opts.ConfigHub == nil {
		return nil, ErrNoConfigHub
	}
	return i.opts.ConfigHub, nil
}

// writeClient returns the ConfigHub client for an upload, having verified the
// session against the server, so an expired token is reported before
// anything else is done.
func (i *Installer) writeClient(ctx context.Context) (*cubapi.Client, error) {
	c, err := i.client()
	if err != nil {
		return nil, err
	}
	if _, err := c.VerifyAuth(ctx); err != nil {
		return nil, optionErrorf(ErrNotAuthenticated, err, "%v", err)
	}
	return c, nil
}

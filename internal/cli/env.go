// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/confighub/sdk/core/cubapi"

	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/internal/sign"
	"github.com/confighub/installer/internal/version"
	"github.com/confighub/installer/pkg/installer"
)

// The CLI is where the installer's settings come from the environment and
// the home directory; everything below it takes them as arguments.

// confighub is the installer binary's connection to ConfigHub: the cub plugin
// environment when run as `cub installer`, otherwise the active cub context.
var confighub = cubapi.MemoizedClient{UserAgent: "installer"}

// plainHTTP reports whether ORAS_OCI_INSECURE=1 asks for plain HTTP to every
// registry, for parity with the oras CLI.
func plainHTTP() bool {
	return os.Getenv("ORAS_OCI_INSECURE") == "1"
}

// registry is how the CLI's commands reach OCI registries. A command that
// pulls packages enforces the signing policy INSTALLER_SIGNING_POLICY or
// ~/.config/installer/policy.yaml names, with the cosign binary
// INSTALLER_COSIGN_BIN names.
func registry(pulls bool) (ipkg.Registry, error) {
	reg := ipkg.Registry{PlainHTTP: plainHTTP()}
	if !pulls {
		return reg, nil
	}
	policy, err := sign.LoadPolicy()
	if err != nil {
		return ipkg.Registry{}, err
	}
	reg.Verifier = &sign.Verifier{Policy: policy, Cosign: sign.CosignFromEnv()}
	return reg, nil
}

// cliInstaller is the Installer a command runs, and why it has no ConfigHub
// client, when it has none.
type cliInstaller struct {
	*installer.Installer
	hubErr error
}

// newInstaller returns the Installer the CLI's commands run: printing to the
// process's stdout and stderr, with the cub environment's ConfigHub client and
// the environment's registry settings. pulls says whether the command may
// pull packages, which loads the signing policy.
func newInstaller(ctx context.Context, pulls bool) (*cliInstaller, error) {
	c := &cliInstaller{}
	client, err := confighub.Client(ctx)
	if err != nil {
		c.hubErr = err
	}
	opts := installer.Options{
		ConfigHub:     client,
		Out:           os.Stdout,
		ErrOut:        os.Stderr,
		Warn:          func(err error) { warn(c.explain(err)) },
		Registry:      installer.RegistryOptions{PlainHTTP: plainHTTP()},
		CosignBinary:  sign.CosignFromEnv(),
		ClientVersion: version.Version,
	}
	if pulls {
		if opts.SigningPolicy, err = sign.LoadPolicy(); err != nil {
			return nil, err
		}
	}
	if c.Installer, err = installer.New(opts); err != nil {
		return nil, err
	}
	return c, nil
}

// explain puts why there is no ConfigHub client where an error says there is
// none.
func (c *cliInstaller) explain(err error) error {
	if err == nil || c.hubErr == nil || !errors.Is(err, installer.ErrNoConfigHub) {
		return err
	}
	return &explainedError{
		msg: strings.Replace(err.Error(), installer.ErrNoConfigHub.Error(), c.hubErr.Error(), 1),
		err: err,
	}
}

type explainedError struct {
	msg string
	err error
}

func (e *explainedError) Error() string { return e.msg }
func (e *explainedError) Unwrap() error { return e.err }

// notAuthenticated says how to log in to ConfigHub when an upload has no
// session to send with.
func notAuthenticated(err error) error {
	return fmt.Errorf("not authenticated to ConfigHub — run `cub auth login` (interactive) and retry: %w", err)
}

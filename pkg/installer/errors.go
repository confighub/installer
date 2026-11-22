// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package installer

import (
	"errors"
	"fmt"
	"strings"

	"github.com/confighub/installer/internal/hubclient"
	"github.com/confighub/installer/pkg/api"
)

// The sentinel errors mark the failures a program can remedy by changing an
// option, so it can say which of its own flags or settings to change. Errors
// are matched with errors.Is; the error returned says what failed.
var (
	// ErrNoPackage: the work-dir has no package and SetupOptions.Pull and
	// SetupOptions.Space are empty.
	ErrNoPackage = errors.New("no package found")
	// ErrRecordedSourcePull: the package an installer record says the install
	// came from could not be pulled. SetupOptions.Pull pulls it from elsewhere.
	ErrRecordedSourcePull = errors.New("pull the package the installer record names")
	// ErrNoInstallerRecord: SetupOptions.Space names a Space with no installer
	// record and SetupOptions.Pull is empty, so there is no package to install.
	ErrNoInstallerRecord = errors.New("no installer record")
	// ErrNoRecordedSource: the installer record in SetupOptions.Space doesn't
	// say where its package came from; SetupOptions.Pull has to.
	ErrNoRecordedSource = errors.New("the installer record names no source")
	// ErrSeveralPackages: the Space holds the installer records of more than
	// one package; SetupOptions.Pull says which.
	ErrSeveralPackages = hubclient.ErrSeveralPackages
	// ErrSeveralInstalls: the package is installed in more than one Space;
	// SetupOptions.Space says which to re-enter.
	ErrSeveralInstalls = hubclient.ErrSeveralInstalls

	// ErrPresetAndSelect: SetupOptions.Preset and SetupOptions.Select are both set.
	ErrPresetAndSelect = errors.New("a component preset and a component selection are mutually exclusive")
	// ErrNoPriorInstall: SetupOptions.Reuse is set and there is no prior install.
	ErrNoPriorInstall = errors.New("reusing the prior install's choices requires a prior install — none found in the work-dir")
	// ErrNamespaceRequired: a fresh install without a Terminal has no
	// SetupOptions.Namespace.
	ErrNamespaceRequired = errors.New("a fresh install needs a namespace when it does not prompt")
	// ErrInputTypesChanged: an upgrade changed the type of inputs the prior
	// install answered, which prompting on a Terminal re-answers.
	ErrInputTypesChanged = errors.New("input type(s) changed across upgrade")
	// ErrNewRequiredInputs: an upgrade added required inputs without
	// defaults, which SetupOptions.Inputs or prompting on a Terminal answers.
	// The error is a *NewRequiredInputsError.
	ErrNewRequiredInputs = errors.New("the new package adds required inputs the prior install did not answer")

	// ErrOutputOCIWithoutRender: SetupOptions.OutputOCI is set without
	// SetupOptions.Render, which produces what it publishes.
	ErrOutputOCIWithoutRender = errors.New("publishing a rendered OCI artifact requires rendering")

	// ErrNoLock: the package declares dependencies and the work-dir has no
	// lock. Setup with SetupOptions.Render writes one.
	ErrNoLock = errors.New("no dependency lock")
	// ErrStaleLock: the work-dir's lock no longer matches the package's
	// dependencies. Setup with SetupOptions.Render writes a new one.
	ErrStaleLock = errors.New("stale dependency lock")
	// ErrNoConfigHub: the operation needs ConfigHub and Options.ConfigHub is
	// nil.
	ErrNoConfigHub = errors.New("no ConfigHub client")
	// ErrNotAuthenticated: ConfigHub refused Options.ConfigHub's credentials.
	ErrNotAuthenticated = errors.New("not authenticated to ConfigHub")

	// ErrSpaceWithDependencies: UploadOptions.Space is set for a package that
	// declares dependencies, which each need a Space of their own;
	// UploadOptions.SpacePattern names them.
	ErrSpaceWithDependencies = errors.New("a single Space cannot hold a package with dependencies")
)

// optionError is an error that reads as msg, is the sentinel for errors.Is,
// and unwraps to its cause.
type optionError struct {
	sentinel error
	msg      string
	cause    error
}

func (e *optionError) Error() string { return e.msg }

func (e *optionError) Unwrap() []error {
	if e.cause == nil {
		return []error{e.sentinel}
	}
	return []error{e.sentinel, e.cause}
}

func optionErrorf(sentinel, cause error, format string, args ...any) error {
	return &optionError{sentinel: sentinel, msg: fmt.Sprintf(format, args...), cause: cause}
}

// NewRequiredInputsError lists the inputs an upgrade added that are required,
// have no default, and the prior install did not answer.
type NewRequiredInputsError struct {
	Inputs []api.Input
}

func (e *NewRequiredInputsError) Error() string {
	names := make([]string, 0, len(e.Inputs))
	for _, in := range e.Inputs {
		names = append(names, in.Name)
	}
	return fmt.Sprintf("the new package adds %d required input(s) that the prior install did not answer: %s",
		len(e.Inputs), strings.Join(names, ", "))
}

func (e *NewRequiredInputsError) Is(target error) bool { return target == ErrNewRequiredInputs }

// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package installer

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/confighub/installer/internal/transform"
)

// transformerReady records that the program called RunTransformerIfRequested,
// so render can count on the program answering the transform step.
var transformerReady atomic.Bool

// RunTransformerIfRequested runs render's transform step when the process was
// started for it, and then exits; otherwise it returns at once. A program
// that renders calls it first thing in main, unless it sets
// Options.TransformerCommand.
//
// Render runs a package's function chain inside kustomize's build, which
// runs it as an exec plugin: a wrapper script, out/compose/installer-transformer.sh,
// that re-executes the program rendering with INSTALLER_RUN_TRANSFORMER=1.
// The same wrapper lets `kustomize build --enable-exec --enable-alpha-plugins`
// reproduce the render from out/compose.
func RunTransformerIfRequested() {
	if transform.Requested() {
		transform.Main()
	}
	transformerReady.Store(true)
}

// RunTransformer runs the transform step on a KRM Functions ResourceList whose
// functionConfig is a ConfigHubTransformers or ConfigHubValidators document,
// and returns the resulting ResourceList, for a program wiring the transform
// step into kustomizations of its own.
func RunTransformer(ctx context.Context, in []byte) ([]byte, error) {
	return transform.Run(ctx, in)
}

// ErrNoTransformer: rendering needs the program to answer the transform step,
// and it neither called RunTransformerIfRequested nor set
// Options.TransformerCommand.
var ErrNoTransformer = errors.New("rendering needs installer.RunTransformerIfRequested called at the start of main, or Options.TransformerCommand set")

func (i *Installer) checkTransformer() error {
	if len(i.opts.TransformerCommand) == 0 && !transformerReady.Load() {
		return ErrNoTransformer
	}
	return nil
}

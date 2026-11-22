// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package installer

import (
	"context"
	"errors"
	"testing"
)

// A program that never called RunTransformerIfRequested, and set no
// TransformerCommand, is told so before anything renders.
func TestRenderNeedsTransformer(t *testing.T) {
	saved := transformerReady.Load()
	transformerReady.Store(false)
	defer transformerReady.Store(saved)

	ctx := context.Background()
	inst, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inst.Render(ctx, t.TempDir()); !errors.Is(err, ErrNoTransformer) {
		t.Errorf("Render: err = %v, want ErrNoTransformer", err)
	}
	if _, err := inst.Setup(ctx, SetupOptions{WorkDir: t.TempDir(), Render: true}); !errors.Is(err, ErrNoTransformer) {
		t.Errorf("Setup: err = %v, want ErrNoTransformer", err)
	}
	// Setup without rendering doesn't need it.
	if _, err := inst.Setup(ctx, SetupOptions{WorkDir: t.TempDir()}); errors.Is(err, ErrNoTransformer) {
		t.Errorf("Setup without Render: err = %v", err)
	}

	withCommand, err := New(Options{TransformerCommand: []string{"installer", "transformer"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withCommand.Render(ctx, t.TempDir()); errors.Is(err, ErrNoTransformer) {
		t.Errorf("Render with a TransformerCommand: err = %v", err)
	}
}

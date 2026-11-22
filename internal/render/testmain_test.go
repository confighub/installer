// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package render_test

import (
	"os"
	"testing"

	"github.com/confighub/installer/internal/transform"
)

// TestMain answers the transform step that the wrapper script in out/compose/
// asks for by re-executing this test binary with INSTALLER_RUN_TRANSFORMER=1,
// as the installer binary does.
func TestMain(m *testing.M) {
	if transform.Requested() {
		transform.Main()
	}
	os.Exit(m.Run())
}

// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package render

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The wrapper's command line reaches the program as the arguments it was
// given, whatever characters they hold.
func TestTransformerWrapperCommandQuoting(t *testing.T) {
	args := []string{"printf", "%s|", `plain`, `with space`, `dollar $HOME`, `quote "x"`, "back`tick`", `back\slash`}
	line, err := transformerWrapperCommand(args)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("/bin/sh", "-c", line).Output()
	if err != nil {
		t.Fatalf("sh -c %s: %v", line, err)
	}
	if want := strings.Join(args[2:], "|") + "|"; string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

// By default the wrapper re-executes the running program, asking for the
// transform step.
func TestTransformerWrapperCommandDefault(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	line, err := transformerWrapperCommand(nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := `INSTALLER_RUN_TRANSFORMER=1 exec "` + exe + `"`; line != want {
		t.Errorf("got %q, want %q", line, want)
	}
}

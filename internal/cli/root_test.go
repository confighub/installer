// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"strings"
	"testing"
)

// The root command prints the error it returns, which is why the
// installer's main does not.
func TestRootPrintsErrorsOnce(t *testing.T) {
	for _, args := range [][]string{
		{"init"},                     // a command's own argument check
		{"no-such-command"},          // cobra's unknown-command error
		{"render", "--no-such-flag"}, // a flag error
	} {
		root := NewRoot()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(args)
		err := root.Execute()
		if err == nil {
			t.Errorf("%v: want an error", args)
			continue
		}
		if n := strings.Count(out.String(), err.Error()); n != 1 {
			t.Errorf("%v: the error appears %d times in %q, want once", args, n, out.String())
		}
	}
}

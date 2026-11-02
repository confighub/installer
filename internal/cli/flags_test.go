// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

import (
	"slices"
	"testing"
)

// Each key=value flag takes its value whole: a JSON value or one with a
// comma is a single entry, not CSV to be split or unquoted.
func TestKeyValueFlagsAreNotCSV(t *testing.T) {
	values := []string{
		`admin_jwk={"kty":"OKP","crv":"Ed25519","x":"abc"}`,
		`host=a.example.com,b.example.com`,
	}
	cases := map[string][]string{
		"setup":  {"input", "set-image"},
		"wizard": {"input", "set-image"},
		"upload": {"space-label", "space-annotation", "unit-label", "unit-annotation"},
	}
	root := NewRoot()
	for name, flags := range cases {
		cmd, _, err := root.Find([]string{name})
		if err != nil || cmd.Name() != name {
			t.Fatalf("find %s: %v", name, err)
		}
		for _, flag := range flags {
			var args []string
			for _, v := range values {
				args = append(args, "--"+flag, v)
			}
			if err := cmd.ParseFlags(args); err != nil {
				t.Errorf("%s --%s: %v", name, flag, err)
				continue
			}
			got, err := cmd.Flags().GetStringArray(flag)
			if err != nil {
				t.Errorf("%s --%s: %v", name, flag, err)
				continue
			}
			if !slices.Equal(got, values) {
				t.Errorf("%s --%s = %q, want %q", name, flag, got, values)
			}
		}
	}
}

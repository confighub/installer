// Package repohygiene holds checks on the repository itself rather than on the
// installer code.
package repohygiene

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// The first names this repository keeps out of committed files, a home
// directory path included. Write "the team", "a colleague" or the issue number.
const personalNames = "alexis|jesper|brian|charlie"

// This file holds the pattern, so it is the one file left out of the scan.
const self = "internal/repohygiene/names_test.go"

// TestNoPersonalNames fails when a tracked file contains one of the names as a
// whole word, in any letter case, and prints the lines. git grep -w matches a
// whole word, so a longer identifier that merely contains a name (a GitHub
// handle, say) is not a hit.
func TestNoPersonalNames(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed, so there are no tracked files to read")
	}
	top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skip("not a git checkout, so there are no tracked files to read")
	}

	cmd := exec.Command("git", "grep", "-nwiE", personalNames, "--", ".", ":(exclude)"+self)
	cmd.Dir = strings.TrimSpace(string(top))
	out, err := cmd.Output()

	// git grep exits 1 when nothing matches.
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return
	}
	if err != nil {
		t.Fatalf("git grep: %v", err)
	}
	t.Errorf("these lines name a person; replace each name with neutral wording:\n%s", out)
}

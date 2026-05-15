package render_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// installerBin is the path to a freshly-built installer binary, set by
// TestMain and consumed by Render-driven tests via Options.TransformerBinary.
// We can't use the go-test binary directly because it doesn't implement the
// `installer transformer` subcommand kustomize invokes through the wrapper
// script in out/compose/.
var installerBin string

func TestMain(m *testing.M) {
	code, err := buildAndRun(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(code)
}

func buildAndRun(m *testing.M) (int, error) {
	tmp, err := os.MkdirTemp("", "installer-bin-*")
	if err != nil {
		return 0, fmt.Errorf("mkdir: %w", err)
	}
	defer os.RemoveAll(tmp)
	bin := filepath.Join(tmp, "installer")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/confighub/installer/cmd/installer")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("build installer: %w", err)
	}
	installerBin = bin
	return m.Run(), nil
}

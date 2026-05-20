package render_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/confighub/installer/internal/render"
)

func TestCleanRenderedOutput(t *testing.T) {
	outDir := t.TempDir()
	manifestsDir := filepath.Join(outDir, "manifests")
	secretsDir := filepath.Join(outDir, "secrets")
	for _, d := range []string{manifestsDir, secretsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path string) {
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(manifestsDir, "Kptfile"))
	write(filepath.Join(manifestsDir, "deployment.yaml"))
	write(filepath.Join(manifestsDir, "service.yaml"))
	write(filepath.Join(secretsDir, "secret.yaml"))

	if err := render.CleanRenderedOutput(outDir); err != nil {
		t.Fatalf("CleanRenderedOutput: %v", err)
	}

	// Kptfile is preserved so the dir stays a valid kpt base package.
	if _, err := os.Stat(filepath.Join(manifestsDir, "Kptfile")); err != nil {
		t.Errorf("Kptfile should be preserved, got: %v", err)
	}
	// Rendered manifests are removed.
	for _, f := range []string{"deployment.yaml", "service.yaml"} {
		if _, err := os.Stat(filepath.Join(manifestsDir, f)); !os.IsNotExist(err) {
			t.Errorf("%s should be removed, stat err = %v", f, err)
		}
	}
	// secrets/ is removed wholesale.
	if _, err := os.Stat(secretsDir); !os.IsNotExist(err) {
		t.Errorf("secrets/ should be removed, stat err = %v", err)
	}
}

// A missing manifests/ directory is not an error (clean before first render).
func TestCleanRenderedOutput_NoManifestsDir(t *testing.T) {
	outDir := t.TempDir()
	if err := render.CleanRenderedOutput(outDir); err != nil {
		t.Fatalf("CleanRenderedOutput on empty out/: %v", err)
	}
}

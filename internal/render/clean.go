package render

import (
	"os"
	"path/filepath"
)

// CleanRenderedOutput clears previously rendered output so a re-render starts
// from a clean slate — in particular so resources dropped by an upgrade don't
// linger as stale files (a plain Render overwrites produced files but never
// removes ones it didn't produce).
//
// It deletes the rendered manifests in out/manifests/ but PRESERVES a Kptfile
// there, if present, so the directory stays a valid kpt base package across
// upgrades (its package identity and any consumer's upstream pointer survive).
// See docs/kpt-guide.md. out/secrets/ is removed wholesale.
func CleanRenderedOutput(outDir string) error {
	manifestsDir := filepath.Join(outDir, "manifests")
	entries, err := os.ReadDir(manifestsDir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, e := range entries {
		if e.Name() == "Kptfile" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(manifestsDir, e.Name())); err != nil {
			return err
		}
	}
	return os.RemoveAll(filepath.Join(outDir, "secrets"))
}

package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// installerYAML is the minimum manifest Bundle requires to consider a
// directory a package.
const installerYAML = `apiVersion: installer.confighub.com/v1alpha1
kind: Package
metadata:
  name: test
  version: 0.0.1
spec:
  bases:
    - {name: default, path: bases/default, default: true}
`

// writeTree creates files relative to root from a path → contents map. Paths
// with a trailing "/" are created as directories.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(root, p)
		if strings.HasSuffix(p, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", full, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
}

// minimalPkg produces the smallest valid package: installer.yaml plus a base
// kustomization with a single resource.
func minimalPkg(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"installer.yaml":                  installerYAML,
		"bases/default/kustomization.yaml": "resources:\n  - cm.yaml\n",
		"bases/default/cm.yaml":            "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n",
	})
	return dir
}

func TestBundleDeterministic(t *testing.T) {
	src := minimalPkg(t)
	d1 := filepath.Join(t.TempDir(), "1.tgz")
	d2 := filepath.Join(t.TempDir(), "2.tgz")

	r1, err := Bundle(src, d1)
	if err != nil {
		t.Fatalf("bundle 1: %v", err)
	}
	r2, err := Bundle(src, d2)
	if err != nil {
		t.Fatalf("bundle 2: %v", err)
	}
	if r1.Digest != r2.Digest {
		t.Fatalf("digests differ: %s != %s", r1.Digest, r2.Digest)
	}
	if !equalBytes(d1, d2) {
		t.Fatalf(".tgz bytes differ for the same source tree")
	}
}

func TestBundleStableAcrossMtimeChanges(t *testing.T) {
	src := minimalPkg(t)
	d1 := filepath.Join(t.TempDir(), "1.tgz")
	d2 := filepath.Join(t.TempDir(), "2.tgz")

	r1, err := Bundle(src, d1)
	if err != nil {
		t.Fatalf("bundle 1: %v", err)
	}

	// Touch every file with an arbitrary mtime; this must not change the
	// resulting tar bytes.
	stamp := time.Date(2017, 3, 14, 12, 0, 0, 0, time.UTC)
	_ = filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		_ = os.Chtimes(path, stamp, stamp)
		return nil
	})

	r2, err := Bundle(src, d2)
	if err != nil {
		t.Fatalf("bundle 2: %v", err)
	}
	if r1.Digest != r2.Digest {
		t.Fatalf("digest changed after mtime touch: %s → %s", r1.Digest, r2.Digest)
	}
}

func TestBundleChangesWhenContentChanges(t *testing.T) {
	src := minimalPkg(t)
	d1 := filepath.Join(t.TempDir(), "1.tgz")
	d2 := filepath.Join(t.TempDir(), "2.tgz")

	r1, err := Bundle(src, d1)
	if err != nil {
		t.Fatalf("bundle 1: %v", err)
	}

	// Replace one byte in cm.yaml — digest must change.
	cm := filepath.Join(src, "bases/default/cm.yaml")
	if err := os.WriteFile(cm, []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: y\n"), 0o644); err != nil {
		t.Fatalf("rewrite cm: %v", err)
	}
	r2, err := Bundle(src, d2)
	if err != nil {
		t.Fatalf("bundle 2: %v", err)
	}
	if r1.Digest == r2.Digest {
		t.Fatalf("digest did not change after content edit")
	}
}

func TestBundleRefusesEnvSecret(t *testing.T) {
	src := minimalPkg(t)
	writeTree(t, src, map[string]string{
		"collector/.env.secret": "PASSWORD=hunter2\n",
	})
	_, err := Bundle(src, filepath.Join(t.TempDir(), "p.tgz"))
	if err == nil {
		t.Fatalf("expected refusal for .env.secret, got nil")
	}
	if !strings.Contains(err.Error(), ".env.secret") {
		t.Fatalf("error should mention .env.secret: %v", err)
	}
}

func TestBundleExcludesOutDir(t *testing.T) {
	src := minimalPkg(t)
	writeTree(t, src, map[string]string{
		"out/manifests/deploy.yaml": "junk\n",
		"out/spec/selection.yaml":   "junk\n",
	})
	r, err := Bundle(src, filepath.Join(t.TempDir(), "p.tgz"))
	if err != nil {
		t.Fatalf("bundle: %v", err)
	}
	for _, f := range r.Files {
		if strings.HasPrefix(f, "out/") {
			t.Fatalf("out/ file leaked into bundle: %s", f)
		}
	}
}

func TestBundleHonoursInstallerIgnore(t *testing.T) {
	src := minimalPkg(t)
	writeTree(t, src, map[string]string{
		".installerignore":      "*.bak\nlocal/\n",
		"notes.bak":             "scratch\n",
		"local/private.yaml":    "shh\n",
		"bases/default/x.yaml":  "kept\n",
	})
	r, err := Bundle(src, filepath.Join(t.TempDir(), "p.tgz"))
	if err != nil {
		t.Fatalf("bundle: %v", err)
	}
	for _, f := range r.Files {
		if f == "notes.bak" || strings.HasPrefix(f, "local/") {
			t.Fatalf("ignored path %q leaked into bundle", f)
		}
	}
	if !slices.Contains(r.Files, "bases/default/x.yaml") {
		t.Fatalf("unrelated file dropped: %v", r.Files)
	}
}

func TestBundleRequiresInstallerYAML(t *testing.T) {
	src := t.TempDir()
	// no installer.yaml
	_, err := Bundle(src, filepath.Join(t.TempDir(), "p.tgz"))
	if err == nil || !strings.Contains(err.Error(), "installer.yaml") {
		t.Fatalf("expected installer.yaml error, got %v", err)
	}
}

func TestBundleTarHeadersZeroed(t *testing.T) {
	src := minimalPkg(t)
	dst := filepath.Join(t.TempDir(), "p.tgz")
	if _, err := Bundle(src, dst); err != nil {
		t.Fatalf("bundle: %v", err)
	}

	f, err := os.Open(dst)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		// tar stores time as a Unix epoch second count, so Go's "zero
		// Time" round-trips as 1970-01-01 UTC. Both have Unix() == 0.
		if hdr.ModTime.Unix() != 0 {
			t.Fatalf("non-zero mtime on %s: %v", hdr.Name, hdr.ModTime)
		}
		if hdr.Uid != 0 || hdr.Gid != 0 {
			t.Fatalf("non-zero uid/gid on %s: %d/%d", hdr.Name, hdr.Uid, hdr.Gid)
		}
		if hdr.Uname != "" || hdr.Gname != "" {
			t.Fatalf("non-empty uname/gname on %s: %q/%q", hdr.Name, hdr.Uname, hdr.Gname)
		}
	}
}

func equalBytes(a, b string) bool {
	x, err := os.ReadFile(a)
	if err != nil {
		return false
	}
	y, err := os.ReadFile(b)
	if err != nil {
		return false
	}
	return bytes.Equal(x, y)
}


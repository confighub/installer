package pkg

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/file"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/confighubai/installer/pkg/api"
)

// Pull resolves a package reference to a local directory ready for Load.
//
// Supported reference formats:
//   - oci://registry/path/repo:tag    fetched via oras-go to dest, Helm chart unpacked
//   - file:///abs/path                local directory or .tgz file
//   - /abs/path or ./relative/path    local directory or .tgz file
//
// dest is the working directory the caller wants the package extracted to;
// for local directory references it is returned unchanged.
func Pull(ctx context.Context, ref, dest string) (string, error) {
	switch {
	case strings.HasPrefix(ref, "oci://"):
		return pullOCI(ctx, strings.TrimPrefix(ref, "oci://"), dest)
	case strings.HasPrefix(ref, "file://"):
		return pullLocal(strings.TrimPrefix(ref, "file://"), dest)
	default:
		return pullLocal(ref, dest)
	}
}

func pullLocal(src, dest string) (string, error) {
	abs, err := filepath.Abs(src)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", abs, err)
	}
	if info.IsDir() {
		// For local directories, prefer in-place use unless an explicit dest
		// is requested. This keeps `installer wizard ./examples/hello-app`
		// fast and avoids copying.
		if dest == "" {
			return abs, nil
		}
		if err := copyDir(abs, dest); err != nil {
			return "", err
		}
		return dest, nil
	}
	// .tgz / .tar.gz: extract
	if dest == "" {
		dest, err = os.MkdirTemp("", "installer-pkg-*")
		if err != nil {
			return "", err
		}
	} else if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := extractTarGz(f, dest); err != nil {
		return "", err
	}
	return resolveSingleSubdir(dest), nil
}

func pullOCI(ctx context.Context, ref, dest string) (string, error) {
	repoRef, tag, want, err := parseRef(ref, false)
	if err != nil {
		return "", err
	}
	repo, err := newRepo(repoRef)
	if err != nil {
		return "", err
	}

	resolveRef := tag
	if tag == "" {
		resolveRef = want.String()
	}
	desc, err := repo.Resolve(ctx, resolveRef)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", ref, err)
	}
	if want != "" && desc.Digest != want {
		return "", fmt.Errorf("digest mismatch on %s: pinned %s but registry returned %s", ref, want, desc.Digest)
	}

	// Peek at the manifest to choose the extraction path. Native installer
	// artifacts have artifactType set to api.ArtifactType; everything else
	// (notably Helm-OCI charts) takes the file-store fallback.
	manifest, err := fetchManifest(ctx, repo, desc)
	if err != nil {
		return "", err
	}

	if dest == "" {
		dest, err = os.MkdirTemp("", "installer-oci-*")
		if err != nil {
			return "", err
		}
	} else if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}

	if manifest.ArtifactType == api.ArtifactType {
		return pullNative(ctx, repo, manifest, dest)
	}
	return pullHelmShaped(ctx, repo, desc.Digest.String(), dest)
}

// pullNative extracts the single tar.gz layer of a native installer artifact
// into <dest>/package/, mirroring the single-subdir shape of Helm pulls.
func pullNative(ctx context.Context, repo *remote.Repository, manifest *ocispec.Manifest, dest string) (string, error) {
	if len(manifest.Layers) != 1 {
		return "", fmt.Errorf("native installer artifact must have exactly 1 layer, got %d", len(manifest.Layers))
	}
	layer := manifest.Layers[0]
	if layer.MediaType != api.LayerMediaType {
		return "", fmt.Errorf("native installer artifact has unexpected layer mediaType %q", layer.MediaType)
	}
	pkgDir := filepath.Join(dest, api.LayerTitle)
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		return "", err
	}
	rc, err := repo.Fetch(ctx, layer)
	if err != nil {
		return "", fmt.Errorf("fetch layer: %w", err)
	}
	defer rc.Close()
	if err := extractTarGz(rc, pkgDir); err != nil {
		return "", err
	}
	return pkgDir, nil
}

// pullHelmShaped is the legacy file-store path: copy the layer to disk via
// oras.Copy and post-extract any single .tgz file. Used for Helm-OCI charts.
func pullHelmShaped(ctx context.Context, repo *remote.Repository, byDigest string, dest string) (string, error) {
	store, err := file.New(dest)
	if err != nil {
		return "", err
	}
	defer store.Close()
	if _, err := oras.Copy(ctx, repo, byDigest, store, byDigest, oras.DefaultCopyOptions); err != nil {
		return "", fmt.Errorf("oras copy %s: %w", byDigest, err)
	}
	if extracted, ok, err := tryExtractHelmTGZ(dest); err != nil {
		return "", err
	} else if ok {
		return extracted, nil
	}
	return resolveSingleSubdir(dest), nil
}

// fetchManifest fetches and decodes an OCI image manifest by descriptor.
func fetchManifest(ctx context.Context, repo *remote.Repository, desc ocispec.Descriptor) (*ocispec.Manifest, error) {
	rc, err := repo.Manifests().Fetch(ctx, desc)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	var m ocispec.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	return &m, nil
}

// tryExtractHelmTGZ looks for a single .tgz file at the top level of dest and,
// if found, extracts it in place and returns the extracted dir. Used because
// Helm OCI charts arrive as one .tgz blob.
func tryExtractHelmTGZ(dest string) (string, bool, error) {
	entries, err := os.ReadDir(dest)
	if err != nil {
		return "", false, err
	}
	var tgz string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasSuffix(n, ".tgz") || strings.HasSuffix(n, ".tar.gz") {
			if tgz != "" {
				return "", false, nil // multiple tgzs; not the helm-chart shape
			}
			tgz = n
		}
	}
	if tgz == "" {
		return "", false, nil
	}
	tgzPath := filepath.Join(dest, tgz)
	f, err := os.Open(tgzPath)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	extractDir := filepath.Join(dest, "_chart")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return "", false, err
	}
	if err := extractTarGz(f, extractDir); err != nil {
		return "", false, err
	}
	_ = os.Remove(tgzPath)
	return resolveSingleSubdir(extractDir), true, nil
}

// resolveSingleSubdir descends into dest if it contains exactly one directory
// (the common shape of extracted archives).
func resolveSingleSubdir(dest string) string {
	entries, err := os.ReadDir(dest)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		return dest
	}
	return filepath.Join(dest, entries[0].Name())
}

func extractTarGz(r io.Reader, destDir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "..") || strings.HasPrefix(clean, "/") {
			continue
		}
		target := filepath.Join(destDir, clean)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)) {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)|0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
	return nil
}

func copyDir(src, dest string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode()|0o755)
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dest string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

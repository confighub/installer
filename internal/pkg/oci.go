package pkg

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/confighubai/installer/internal/bundle"
	"github.com/confighubai/installer/internal/version"
	"github.com/confighubai/installer/pkg/api"
)

// PushResult is what Push returns about a completed push.
type PushResult struct {
	// Ref is the full reference (without the oci:// prefix) the artifact was
	// pushed to, in <host>/<repo>:<tag> form.
	Ref string
	// ManifestDigest is the digest of the pushed manifest, in "sha256:<hex>".
	ManifestDigest string
	// LayerDigest is the digest of the package layer.
	LayerDigest string
	// LayerSize is the size of the layer in bytes.
	LayerSize int64
	// Files is the list of paths inside the layer, in tar order.
	Files []string
}

// Push publishes a package to an OCI registry as a native installer artifact.
//
// src may be either a .tgz file produced by internal/bundle or a source
// directory; directories are bundled to a temp file first. ref must be of the
// form oci://host/repo:tag — pushing to a digest is not supported.
func Push(ctx context.Context, src, ref string) (*PushResult, error) {
	repoRef, tag, _, err := parseRef(ref, true /*requireTag*/)
	if err != nil {
		return nil, err
	}
	repo, err := newRepo(repoRef)
	if err != nil {
		return nil, err
	}
	res, err := stageAndCopy(ctx, src, tag, repo)
	if err != nil {
		return nil, err
	}
	res.Ref = repoRef + ":" + tag
	return res, nil
}

// stageAndCopy builds the installer artifact (layer + config blob + manifest)
// in an in-memory store, tags it as tag, and copies it to dst. dst may be a
// *remote.Repository or any oras.Target (memory store in tests). Returns a
// PushResult with Ref left blank for the caller to fill.
func stageAndCopy(ctx context.Context, src, tag string, dst oras.Target) (*PushResult, error) {
	tgz, loaded, cleanup, err := materializeForPush(src)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	layerData, err := os.ReadFile(tgz)
	if err != nil {
		return nil, err
	}
	layerDigest := digest.FromBytes(layerData)
	// The layer is the source tree's tarball with paths relative to root
	// (e.g. "installer.yaml", not "package/installer.yaml"). We extract it
	// ourselves on pull, so we deliberately do NOT set
	// file.AnnotationUnpack — the file-store would try to unpack into a
	// "package/" subdir and reject paths that escape it.
	layerDesc := ocispec.Descriptor{
		MediaType: api.LayerMediaType,
		Digest:    layerDigest,
		Size:      int64(len(layerData)),
		Annotations: map[string]string{
			ocispec.AnnotationTitle: api.LayerTitle + ".tgz",
		},
	}

	files, err := listTarFiles(layerData)
	if err != nil {
		return nil, err
	}

	cbBytes, err := buildConfigBlob(loaded.Package, layerDigest.String(), int64(len(layerData)), files)
	if err != nil {
		return nil, err
	}
	configDesc := ocispec.Descriptor{
		MediaType: api.ConfigMediaType,
		Digest:    digest.FromBytes(cbBytes),
		Size:      int64(len(cbBytes)),
	}

	staging := memory.New()
	if err := staging.Push(ctx, layerDesc, bytes.NewReader(layerData)); err != nil {
		return nil, fmt.Errorf("stage layer: %w", err)
	}
	if err := staging.Push(ctx, configDesc, bytes.NewReader(cbBytes)); err != nil {
		return nil, fmt.Errorf("stage config: %w", err)
	}

	manifestDesc, err := oras.PackManifest(ctx, staging, oras.PackManifestVersion1_1, api.ArtifactType, oras.PackManifestOptions{
		Layers:           []ocispec.Descriptor{layerDesc},
		ConfigDescriptor: &configDesc,
		ManifestAnnotations: map[string]string{
			// Fixed value → deterministic manifest digest.
			ocispec.AnnotationCreated:      "1970-01-01T00:00:00Z",
			ocispec.AnnotationTitle:        loaded.Package.Metadata.Name,
			api.AnnotationName:             loaded.Package.Metadata.Name,
			api.AnnotationVersion:          loaded.Package.Metadata.Version,
			api.AnnotationInstallerVersion: version.Version,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("pack manifest: %w", err)
	}
	if err := staging.Tag(ctx, manifestDesc, tag); err != nil {
		return nil, err
	}
	if _, err := oras.Copy(ctx, staging, tag, dst, tag, oras.DefaultCopyOptions); err != nil {
		return nil, fmt.Errorf("oras copy: %w", err)
	}

	return &PushResult{
		ManifestDigest: manifestDesc.Digest.String(),
		LayerDigest:    layerDigest.String(),
		LayerSize:      int64(len(layerData)),
		Files:          files,
	}, nil
}

// Inspect fetches only the manifest + config blob for a native installer
// artifact. The layer is not pulled, making this cheap enough for the
// resolver to call on every dependency candidate.
func Inspect(ctx context.Context, ref string) (*api.ConfigBlob, error) {
	repoRef, tag, want, err := parseRef(ref, false)
	if err != nil {
		return nil, err
	}
	repo, err := newRepo(repoRef)
	if err != nil {
		return nil, err
	}
	resolveRef := tag
	if tag == "" {
		resolveRef = want.String()
	}
	return inspectFromTarget(ctx, repo, resolveRef, want)
}

// inspectFromTarget reads the manifest at ref from any oras target and
// decodes the config blob. Used by Inspect against a remote.Repository and by
// tests against an in-memory store.
func inspectFromTarget(ctx context.Context, target oras.Target, ref string, want digest.Digest) (*api.ConfigBlob, error) {
	desc, err := target.Resolve(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", ref, err)
	}
	if want != "" && desc.Digest != want {
		return nil, fmt.Errorf("digest mismatch: ref pinned %s but target returned %s", want, desc.Digest)
	}
	mrc, err := target.Fetch(ctx, desc)
	if err != nil {
		return nil, err
	}
	defer mrc.Close()
	mbytes, err := io.ReadAll(mrc)
	if err != nil {
		return nil, err
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(mbytes, &manifest); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	if manifest.ArtifactType != api.ArtifactType && manifest.Config.MediaType != api.ConfigMediaType {
		return nil, fmt.Errorf("%s is not a native installer artifact (artifactType %q, config %q)",
			ref, manifest.ArtifactType, manifest.Config.MediaType)
	}
	cbRC, err := target.Fetch(ctx, manifest.Config)
	if err != nil {
		return nil, fmt.Errorf("fetch config blob: %w", err)
	}
	defer cbRC.Close()
	cbBytes, err := io.ReadAll(cbRC)
	if err != nil {
		return nil, err
	}
	var cb api.ConfigBlob
	if err := json.Unmarshal(cbBytes, &cb); err != nil {
		return nil, fmt.Errorf("decode config blob: %w", err)
	}
	return &cb, nil
}

// List returns the tags of repoRef. The endpoint is `/tags/list` per the OCI
// distribution spec; the registry decides whether catalogs are public.
func List(ctx context.Context, repoRef string) ([]string, error) {
	parsed, err := parseRepoOnly(repoRef)
	if err != nil {
		return nil, err
	}
	repo, err := newRepo(parsed)
	if err != nil {
		return nil, err
	}
	var out []string
	if err := repo.Tags(ctx, "", func(ts []string) error {
		out = append(out, ts...)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("list tags %s: %w", parsed, err)
	}
	return out, nil
}

// Tag points dstTag at the same manifest digest as srcRef. The original tag,
// if any, is preserved. srcRef is a full ref (oci://host/repo:tag or
// oci://host/repo@sha256:...); dstTag is a bare tag name.
func Tag(ctx context.Context, srcRef, dstTag string) error {
	if dstTag == "" {
		return errors.New("destination tag required")
	}
	if strings.ContainsAny(dstTag, "/:@") {
		return fmt.Errorf("destination tag %q must be a bare tag (no /, :, or @)", dstTag)
	}
	repoRef, srcTag, want, err := parseRef(srcRef, false)
	if err != nil {
		return err
	}
	repo, err := newRepo(repoRef)
	if err != nil {
		return err
	}
	resolveRef := srcTag
	if srcTag == "" {
		resolveRef = want.String()
	}
	desc, err := repo.Resolve(ctx, resolveRef)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", srcRef, err)
	}
	if want != "" && desc.Digest != want {
		return fmt.Errorf("digest mismatch on %s: pinned %s but registry returned %s", srcRef, want, desc.Digest)
	}
	if err := repo.Tag(ctx, desc, dstTag); err != nil {
		return fmt.Errorf("tag %s as %s: %w", repoRef, dstTag, err)
	}
	return nil
}

// --- helpers ------------------------------------------------------------

// parseRef accepts oci:// prefixed or bare references and splits them into
// (repoRef, tag, digest). One of tag or digest is always populated unless
// requireTag is set (in which case a tag is mandatory and digest is rejected).
// repoRef has the form host/path[/...].
func parseRef(ref string, requireTag bool) (repoRef, tag string, want digest.Digest, err error) {
	ref = strings.TrimPrefix(ref, "oci://")
	// Split off @sha256:... if present.
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		want, err = digest.Parse(ref[at+1:])
		if err != nil {
			return "", "", "", fmt.Errorf("parse digest in %q: %w", ref, err)
		}
		ref = ref[:at]
	}
	if requireTag && want != "" {
		return "", "", "", fmt.Errorf("push to %s: digest-only refs are not supported; use :tag", ref)
	}
	// Split off :tag.
	if colon := strings.LastIndex(ref, ":"); colon >= 0 && !strings.Contains(ref[colon:], "/") {
		tag = ref[colon+1:]
		ref = ref[:colon]
	}
	if requireTag && tag == "" {
		return "", "", "", fmt.Errorf("push requires :tag, got %q", ref)
	}
	if tag == "" && want == "" {
		return "", "", "", fmt.Errorf("ref %q has neither :tag nor @digest", ref)
	}
	return ref, tag, want, nil
}

// parseRepoOnly strips the oci:// prefix and refuses any ref carrying a
// :tag or @digest. Used by List, which targets the repo itself.
func parseRepoOnly(ref string) (string, error) {
	ref = strings.TrimPrefix(ref, "oci://")
	if strings.Contains(ref, "@") {
		return "", fmt.Errorf("list takes a repo ref without @digest, got %q", ref)
	}
	if colon := strings.LastIndex(ref, ":"); colon >= 0 && !strings.Contains(ref[colon:], "/") {
		return "", fmt.Errorf("list takes a repo ref without :tag, got %q", ref)
	}
	return ref, nil
}

func newRepo(repoRef string) (*remote.Repository, error) {
	repo, err := remote.NewRepository(repoRef)
	if err != nil {
		return nil, fmt.Errorf("repo %s: %w", repoRef, err)
	}
	client, err := authClient()
	if err != nil {
		return nil, err
	}
	repo.Client = client
	// Allow http for plaintext registries (local zot, registry:2 in tests).
	// oras-go defaults to https; the PlainHTTP toggle is controlled by env
	// var ORAS_OCI_INSECURE for parity with the oras CLI.
	if os.Getenv("ORAS_OCI_INSECURE") == "1" {
		repo.PlainHTTP = true
	}
	if strings.HasPrefix(repoRef, "localhost") || strings.HasPrefix(repoRef, "127.0.0.1") {
		repo.PlainHTTP = true
	}
	return repo, nil
}

// materializeForPush returns a path to a .tgz and the parsed Package. If src
// is a directory, it is bundled to a temp file; the cleanup func removes the
// temp dir.
func materializeForPush(src string) (tgz string, loaded *Loaded, cleanup func(), err error) {
	info, err := os.Stat(src)
	if err != nil {
		return "", nil, func() {}, err
	}
	if info.IsDir() {
		tmp, err := os.MkdirTemp("", "installer-push-*")
		if err != nil {
			return "", nil, func() {}, err
		}
		cleanup = func() { _ = os.RemoveAll(tmp) }
		tgzPath := filepath.Join(tmp, "package.tgz")
		if _, err := bundle.Bundle(src, tgzPath); err != nil {
			cleanup()
			return "", nil, func() {}, err
		}
		loaded, err = Load(src)
		if err != nil {
			cleanup()
			return "", nil, func() {}, err
		}
		return tgzPath, loaded, cleanup, nil
	}
	// src is a .tgz — extract to a temp dir just to read installer.yaml.
	tmp, err := os.MkdirTemp("", "installer-push-*")
	if err != nil {
		return "", nil, func() {}, err
	}
	cleanup = func() { _ = os.RemoveAll(tmp) }
	f, err := os.Open(src)
	if err != nil {
		cleanup()
		return "", nil, func() {}, err
	}
	defer f.Close()
	if err := extractTarGz(f, tmp); err != nil {
		cleanup()
		return "", nil, func() {}, err
	}
	loaded, err = Load(tmp)
	if err != nil {
		cleanup()
		return "", nil, func() {}, fmt.Errorf("load %s: %w", src, err)
	}
	return src, loaded, cleanup, nil
}

func buildConfigBlob(pkg *api.Package, layerDigest string, layerSize int64, files []string) ([]byte, error) {
	cb := api.ConfigBlob{
		Bundle: api.BundleInfo{
			InstallerVersion: version.Version,
			LayerDigest:      layerDigest,
			LayerSize:        layerSize,
			Files:            files,
		},
		Manifest: pkg,
	}
	return json.Marshal(cb)
}

// listTarFiles reads a gzipped tar from data and returns its file paths in
// the order they appear in the tar (the bundler emits them sorted).
func listTarFiles(data []byte) ([]string, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var out []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar read: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		out = append(out, hdr.Name)
	}
	sort.Strings(out)
	return out, nil
}


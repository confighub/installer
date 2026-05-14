// Package deps walks a Package's Dependencies, picks a version per child
// package satisfying every constraint, and writes a Lock pinning each pick
// to a manifest digest.
//
// The resolver consumes a Source — a small interface over the OCI primitives
// it needs (List tags, Inspect a ref). The default OCISource is backed by
// internal/pkg; tests use an in-memory Source.
package deps

import (
	"context"
	"strings"

	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/pkg/api"
)

// Source is the resolver's view of a package registry. It returns one
// candidate version list per repo and the parsed manifest + config blob
// for any specific ref.
type Source interface {
	// ListVersions returns the set of tags published at repo. The resolver
	// filters to SemVer-shaped tags and picks the highest one satisfying
	// the dependency's version constraint. Non-SemVer tags (e.g. channel
	// aliases like "stable", "latest") are returned but ignored by the
	// version-picker; they can still be used as an explicit pin if a
	// downstream tool chooses to do so.
	ListVersions(ctx context.Context, repo string) ([]string, error)

	// Inspect fetches only the manifest + config blob at ref. The layer is
	// not pulled. ref is the full OCI ref including tag (or @digest).
	Inspect(ctx context.Context, ref string) (*ipkg.InspectResult, error)
}

// OCISource is the production Source: List + Inspect against a live registry
// via internal/pkg.
type OCISource struct{}

func (OCISource) ListVersions(ctx context.Context, repo string) ([]string, error) {
	return ipkg.List(ctx, repo)
}

func (OCISource) Inspect(ctx context.Context, ref string) (*ipkg.InspectResult, error) {
	return ipkg.Inspect(ctx, ref)
}

// stripOCIPrefix strips the optional oci:// scheme from a package ref.
func stripOCIPrefix(ref string) string {
	return strings.TrimPrefix(ref, "oci://")
}

// joinRef rejoins a repo and tag into the canonical oci:// form used in
// the lock file and surfaced to users.
func joinRef(repo, tag string) string {
	return "oci://" + stripOCIPrefix(repo) + ":" + tag
}

// pinDigestRef appends @<digest> to a tag-pinned oci ref. Used to give
// cosign a fully-pinned reference for verification.
func pinDigestRef(ref, digest string) string {
	return ref + "@" + digest
}

// _ ensures the api package import is retained.
var _ = api.KindLock

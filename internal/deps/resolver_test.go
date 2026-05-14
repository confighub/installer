package deps

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	ipkg "github.com/confighubai/installer/internal/pkg"
	"github.com/confighubai/installer/pkg/api"
)

// mockSource implements Source by returning canned manifests + tag lists,
// keyed by the OCI repo (without scheme or tag).
type mockSource struct {
	// versions[repo] is the published tag list (must include the tag that
	// each entry in manifests uses).
	versions map[string][]string
	// manifests[repo:tag] is the parsed Package returned by Inspect.
	manifests map[string]*api.Package
}

func newMockSource() *mockSource {
	return &mockSource{
		versions:  map[string][]string{},
		manifests: map[string]*api.Package{},
	}
}

func (m *mockSource) publish(repo, tag string, pkg *api.Package) {
	repo = strings.TrimPrefix(repo, "oci://")
	m.versions[repo] = append(m.versions[repo], tag)
	m.manifests[repo+":"+tag] = pkg
}

func (m *mockSource) ListVersions(_ context.Context, ref string) ([]string, error) {
	ref = strings.TrimPrefix(ref, "oci://")
	vs, ok := m.versions[ref]
	if !ok {
		return nil, fmt.Errorf("mock: no repo %q", ref)
	}
	return vs, nil
}

func (m *mockSource) Inspect(_ context.Context, ref string) (*ipkg.InspectResult, error) {
	ref = strings.TrimPrefix(ref, "oci://")
	pkg, ok := m.manifests[ref]
	if !ok {
		return nil, fmt.Errorf("mock: no manifest at %q", ref)
	}
	// Deterministic synthetic digest derived from the ref.
	digest := "sha256:" + strings.Repeat("a", 56) + fmt.Sprintf("%08x", hash32(ref))
	return &ipkg.InspectResult{
		ManifestDigest: digest,
		Config: &api.ConfigBlob{
			Bundle:   api.BundleInfo{LayerDigest: digest, LayerSize: 0, InstallerVersion: "test"},
			Manifest: pkg,
		},
	}, nil
}

// minimal hash for synthetic digests.
func hash32(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h = (h ^ uint32(s[i])) * 16777619
	}
	return h
}

// pkgWith builds a minimal Package with the supplied name/version/deps.
func pkgWith(name, version string, deps ...api.Dependency) *api.Package {
	return &api.Package{
		APIVersion: api.APIVersion,
		Kind:       api.KindPackage,
		Metadata:   api.Metadata{Name: name, Version: version},
		Spec: api.PackageSpec{
			Bases:        []api.Base{{Name: "default", Path: "bases/default", Default: true}},
			Dependencies: deps,
		},
	}
}

func TestResolveLinearChain(t *testing.T) {
	ctx := context.Background()
	src := newMockSource()
	c := pkgWith("c", "0.3.0")
	b := pkgWith("b", "0.2.0", api.Dependency{Name: "c", Package: "oci://reg/c", Version: ">= 0.3.0"})
	a := pkgWith("a", "0.1.0", api.Dependency{Name: "b", Package: "oci://reg/b", Version: "^0.2.0"})
	src.publish("reg/b", "0.2.0", b)
	src.publish("reg/c", "0.3.0", c)

	got, err := Resolve(ctx, a, src, Options{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got.Lock.Spec.Resolved) != 2 {
		t.Fatalf("resolved count = %d, want 2: %+v", len(got.Lock.Spec.Resolved), got.Lock.Spec.Resolved)
	}
	// b before c — DFS pre-order from root.
	if got.Lock.Spec.Resolved[0].Name != "b" || got.Lock.Spec.Resolved[1].Name != "c" {
		t.Errorf("order = %s,%s; want b,c", got.Lock.Spec.Resolved[0].Name, got.Lock.Spec.Resolved[1].Name)
	}
	if got.Lock.Spec.Resolved[1].RequestedBy[0] != "b" {
		t.Errorf("c.requestedBy = %v; want [b]", got.Lock.Spec.Resolved[1].RequestedBy)
	}
}

func TestResolveDiamondSharedDep(t *testing.T) {
	// a -> b -> d
	// a -> c -> d
	// d should appear once with requestedBy [b, c].
	ctx := context.Background()
	src := newMockSource()
	d := pkgWith("d", "1.0.0")
	b := pkgWith("b", "0.2.0", api.Dependency{Name: "d", Package: "oci://reg/d", Version: ">= 1.0.0"})
	c := pkgWith("c", "0.3.0", api.Dependency{Name: "d", Package: "oci://reg/d", Version: "^1.0.0"})
	a := pkgWith("a", "0.1.0",
		api.Dependency{Name: "b", Package: "oci://reg/b", Version: "*"},
		api.Dependency{Name: "c", Package: "oci://reg/c", Version: "*"},
	)
	src.publish("reg/b", "0.2.0", b)
	src.publish("reg/c", "0.3.0", c)
	src.publish("reg/d", "1.0.0", d)

	got, err := Resolve(ctx, a, src, Options{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// 3 entries: b, c, d
	if len(got.Lock.Spec.Resolved) != 3 {
		t.Fatalf("resolved = %d, want 3: %+v", len(got.Lock.Spec.Resolved), got.Lock.Spec.Resolved)
	}
	var dEntry *api.LockedDependency
	for i := range got.Lock.Spec.Resolved {
		if got.Lock.Spec.Resolved[i].Name == "d" {
			dEntry = &got.Lock.Spec.Resolved[i]
		}
	}
	if dEntry == nil {
		t.Fatalf("no d entry")
	}
	if len(dEntry.RequestedBy) != 2 {
		t.Errorf("d.requestedBy = %v, want [b, c]", dEntry.RequestedBy)
	}
}

func TestResolveVersionConflict(t *testing.T) {
	ctx := context.Background()
	src := newMockSource()
	d10 := pkgWith("d", "1.0.0")
	d20 := pkgWith("d", "2.0.0")
	b := pkgWith("b", "0.2.0", api.Dependency{Name: "d", Package: "oci://reg/d", Version: "^1.0.0"})
	c := pkgWith("c", "0.3.0", api.Dependency{Name: "d", Package: "oci://reg/d", Version: "^2.0.0"})
	a := pkgWith("a", "0.1.0",
		api.Dependency{Name: "b", Package: "oci://reg/b", Version: "*"},
		api.Dependency{Name: "c", Package: "oci://reg/c", Version: "*"},
	)
	src.publish("reg/b", "0.2.0", b)
	src.publish("reg/c", "0.3.0", c)
	src.publish("reg/d", "1.0.0", d10)
	src.publish("reg/d", "2.0.0", d20)

	_, err := Resolve(ctx, a, src, Options{})
	if err == nil {
		t.Fatalf("expected conflict error")
	}
	if !strings.Contains(err.Error(), "incompatible constraints on reg/d") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveOptionalSkippedWithoutSelection(t *testing.T) {
	ctx := context.Background()
	src := newMockSource()
	b := pkgWith("b", "0.2.0")
	a := pkgWith("a", "0.1.0", api.Dependency{
		Name:    "b",
		Package: "oci://reg/b",
		Version: "*",
		Optional: true,
		WhenComponent: "ingress",
	})
	src.publish("reg/b", "0.2.0", b)

	got, err := Resolve(ctx, a, src, Options{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got.Lock.Spec.Resolved) != 0 {
		t.Errorf("expected no resolved deps, got %d", len(got.Lock.Spec.Resolved))
	}
	if len(got.SkippedOptional) != 1 {
		t.Errorf("expected 1 skipped, got %v", got.SkippedOptional)
	}
}

func TestResolveOptionalFollowedWhenComponentSelected(t *testing.T) {
	ctx := context.Background()
	src := newMockSource()
	b := pkgWith("b", "0.2.0")
	a := pkgWith("a", "0.1.0", api.Dependency{
		Name:    "b",
		Package: "oci://reg/b",
		Version: "*",
		Optional: true,
		WhenComponent: "ingress",
	})
	a.Spec.Components = []api.Component{{Name: "ingress", Path: "components/ingress"}}
	src.publish("reg/b", "0.2.0", b)

	sel := &api.Selection{Spec: api.SelectionSpec{Components: []string{"ingress"}}}
	got, err := Resolve(ctx, a, src, Options{Selection: sel})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got.Lock.Spec.Resolved) != 1 {
		t.Errorf("expected 1 resolved, got %d", len(got.Lock.Spec.Resolved))
	}
}

func TestResolvePicksHighestSatisfying(t *testing.T) {
	ctx := context.Background()
	src := newMockSource()
	for _, v := range []string{"1.2.5", "1.3.0", "2.0.0", "1.2.7"} {
		src.publish("reg/b", v, pkgWith("b", v))
	}
	a := pkgWith("a", "0.1.0", api.Dependency{Name: "b", Package: "oci://reg/b", Version: "^1.2.0"})
	got, err := Resolve(ctx, a, src, Options{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Lock.Spec.Resolved[0].Version != "1.3.0" {
		t.Errorf("picked %s, want 1.3.0", got.Lock.Spec.Resolved[0].Version)
	}
}

func TestResolveNoMatchingVersion(t *testing.T) {
	ctx := context.Background()
	src := newMockSource()
	src.publish("reg/b", "0.5.0", pkgWith("b", "0.5.0"))
	a := pkgWith("a", "0.1.0", api.Dependency{Name: "b", Package: "oci://reg/b", Version: "^1.0.0"})
	_, err := Resolve(ctx, a, src, Options{})
	if err == nil || !errors.Is(err, ErrNoVersionMatches) && !strings.Contains(err.Error(), "no version matches") {
		t.Fatalf("expected no-version error, got %v", err)
	}
}

func TestResolveIgnoresChannelTags(t *testing.T) {
	ctx := context.Background()
	src := newMockSource()
	src.publish("reg/b", "stable", pkgWith("b", "stable"))
	src.publish("reg/b", "1.0.0", pkgWith("b", "1.0.0"))
	a := pkgWith("a", "0.1.0", api.Dependency{Name: "b", Package: "oci://reg/b", Version: "*"})
	got, err := Resolve(ctx, a, src, Options{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Lock.Spec.Resolved[0].Version != "1.0.0" {
		t.Errorf("picked %s, want 1.0.0", got.Lock.Spec.Resolved[0].Version)
	}
}

func TestResolveRootConflict(t *testing.T) {
	ctx := context.Background()
	src := newMockSource()
	src.publish("reg/old", "1.0.0", pkgWith("old", "1.0.0"))
	a := pkgWith("a", "0.1.0", api.Dependency{Name: "old", Package: "oci://reg/old", Version: "*"})
	a.Spec.Conflicts = []api.ConflictRef{{Package: "oci://reg/old", Reason: "deprecated"}}
	_, err := Resolve(ctx, a, src, Options{})
	if err == nil || !strings.Contains(err.Error(), "conflicts with root") {
		t.Fatalf("expected conflict error, got %v", err)
	}
}

func TestRootDepsHashStable(t *testing.T) {
	a := pkgWith("a", "0.1.0",
		api.Dependency{Name: "b", Package: "oci://reg/b", Version: "^0.2"},
		api.Dependency{Name: "c", Package: "oci://reg/c", Version: "^0.3"},
	)
	h1 := RootDepsHash(a)
	// Reorder; hash should be the same.
	a.Spec.Dependencies[0], a.Spec.Dependencies[1] = a.Spec.Dependencies[1], a.Spec.Dependencies[0]
	h2 := RootDepsHash(a)
	if h1 != h2 {
		t.Fatalf("RootDepsHash should be stable across order: %s vs %s", h1, h2)
	}
	// Changing a constraint should change the hash.
	a.Spec.Dependencies[0].Version = "^99"
	h3 := RootDepsHash(a)
	if h3 == h1 {
		t.Fatalf("hash should change when constraint changes")
	}
}

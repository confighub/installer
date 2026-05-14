package deps

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/confighub/installer/pkg/api"
)

// Annotation key under which the resolver records a hash of the root
// package's spec.dependencies list. The renderer compares the lock's hash
// to a fresh recomputation on load and refuses to render if they differ
// (stale-lock detection).
const AnnotationRootDepsHash = "installer.confighub.com/root-dependencies-hash"

// LockPath returns the canonical path where deps update writes the lock.
func LockPath(workDir string) string {
	return filepath.Join(workDir, "out", "spec", "lock.yaml")
}

// WriteLock stamps the root-deps hash onto lock and writes it under workDir.
func WriteLock(workDir string, pkg *api.Package, lock *api.Lock) error {
	if lock.Metadata.Annotations == nil {
		lock.Metadata.Annotations = map[string]string{}
	}
	lock.Metadata.Annotations[AnnotationRootDepsHash] = RootDepsHash(pkg)

	data, err := api.MarshalYAML(lock)
	if err != nil {
		return err
	}
	path := LockPath(workDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ReadLock returns the lock at workDir/out/spec/lock.yaml, or (nil, nil)
// if absent.
func ReadLock(workDir string) (*api.Lock, error) {
	data, err := os.ReadFile(LockPath(workDir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return api.ParseLock(data)
}

// IsStale reports whether lock is missing or out-of-date relative to pkg's
// declared dependencies. A nil lock is stale by definition.
func IsStale(lock *api.Lock, pkg *api.Package) bool {
	if lock == nil {
		return true
	}
	have := lock.Metadata.Annotations[AnnotationRootDepsHash]
	return have != RootDepsHash(pkg)
}

// RootDepsHash computes a canonical hash of pkg's spec.dependencies. Two
// packages with the same set of (Name, Package, Version, WhenComponent,
// Optional) fields — in any order — produce the same hash.
func RootDepsHash(pkg *api.Package) string {
	type entry struct {
		Name, Package, Version, WhenComponent string
		Optional                              bool
	}
	deps := make([]entry, 0, len(pkg.Spec.Dependencies))
	for _, d := range pkg.Spec.Dependencies {
		deps = append(deps, entry{
			Name:          d.Name,
			Package:       d.Package,
			Version:       d.Version,
			WhenComponent: d.WhenComponent,
			Optional:      d.Optional,
		})
	}
	sort.Slice(deps, func(i, j int) bool { return deps[i].Name < deps[j].Name })
	var b strings.Builder
	for _, d := range deps {
		fmt.Fprintf(&b, "%s|%s|%s|%s|%v\n", d.Name, d.Package, d.Version, d.WhenComponent, d.Optional)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

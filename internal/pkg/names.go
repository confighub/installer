// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package pkg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/confighub/installer/pkg/api"
)

// packageNameRegexp is a bare package name, optionally with a version:
// k8sbot, or k8sbot:0.2.12.
var packageNameRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*(:[A-Za-z0-9._-]+)?$`)

// IsPath reports whether a package reference is written as a path on this
// machine: as file://, with a path separator, starting with ".", or naming
// an archive. A local package is always written as one, so a directory that
// happens to share a published package's name never stands in for it.
func IsPath(ref string) bool {
	return strings.HasPrefix(ref, "file://") ||
		strings.ContainsRune(ref, '/') || strings.ContainsRune(ref, filepath.Separator) ||
		strings.HasPrefix(ref, ".") ||
		strings.HasSuffix(ref, ".tgz") || strings.HasSuffix(ref, ".tar.gz")
}

// isPackageName reports whether ref is neither an oci:// reference nor a
// path, so names a package in the package repository. "" names none.
func isPackageName(ref string) bool {
	return ref != "" && !strings.HasPrefix(ref, "oci://") && !IsPath(ref)
}

// packageRepository is where bare package names resolve.
func (reg Registry) packageRepository() string {
	if reg.PackageRepository != "" {
		return strings.TrimSuffix(reg.PackageRepository, "/")
	}
	return api.PackageRepository
}

// ResolveRef returns the reference a package reference names. A bare package
// name, <name>[:<version>], is the oci:// reference of that release in the
// package repository, its newest X.Y.Z release when no version is given. An
// oci:// reference or a path is returned as it is.
func (reg Registry) ResolveRef(ctx context.Context, ref string) (string, error) {
	if !isPackageName(ref) {
		return ref, nil
	}
	if !packageNameRegexp.MatchString(ref) {
		return "", fmt.Errorf("%q is not a package: name one ConfigHub publishes, as <name>[:<version>], or give an oci:// reference, or a directory or .tgz written as a path (./%s)", ref, ref)
	}
	name, version, _ := strings.Cut(ref, ":")
	repo := reg.packageRepository() + "/" + name
	if version == "" {
		newest, err := reg.newestRelease(ctx, repo)
		if err != nil {
			return "", localHint(ref, fmt.Errorf("find the newest release of package %s: %w", name, err))
		}
		version = newest
	}
	return "oci://" + repo + ":" + version, nil
}

// newestRelease is the highest X.Y.Z or vX.Y.Z tag of repo.
func (reg Registry) newestRelease(ctx context.Context, repo string) (string, error) {
	tags, err := reg.List(ctx, repo)
	if err != nil {
		return "", err
	}
	newest, newestVersion := "", [3]int{}
	for _, tag := range tags {
		v, ok := releaseVersion(tag)
		if ok && (newest == "" || compareVersions(v, newestVersion) > 0) {
			newest, newestVersion = tag, v
		}
	}
	if newest == "" {
		return "", fmt.Errorf("%s has no release tag (X.Y.Z or vX.Y.Z)", repo)
	}
	return newest, nil
}

// releaseVersion parses a release tag, X.Y.Z or vX.Y.Z; any other tag, a
// prerelease included, is not a release.
func releaseVersion(tag string) ([3]int, bool) {
	parts := strings.Split(strings.TrimPrefix(tag, "v"), ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var v [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p != strconv.Itoa(n) {
			return [3]int{}, false
		}
		v[i] = n
	}
	return v, true
}

func compareVersions(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// localHint adds, to the error fetching the package a bare name names, how to
// use the local directory or archive of that name instead, when there is one.
func localHint(ref string, err error) error {
	if _, statErr := os.Stat(ref); statErr == nil {
		return fmt.Errorf("%w; for the local %s, write it as a path: ./%s", err, ref, ref)
	}
	return err
}

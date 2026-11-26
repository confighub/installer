// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package pkg

import (
	"context"
	"net/url"
	"os/exec"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/confighub/installer/pkg/api"
)

// detectGitSource returns nil unless dir's installer.yaml is tracked by git
// and the repository has a usable origin remote.
func detectGitSource(ctx context.Context, dir string) *api.GitSource {
	git := func(args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
		return strings.TrimSpace(string(out)), err
	}
	// Don't pick up an unrelated parent repository.
	if _, err := git("ls-files", "--error-unmatch", ManifestFile); err != nil {
		return nil
	}
	rev, err := git("rev-parse", "HEAD")
	if err != nil {
		return nil
	}
	remote, err := git("remote", "get-url", "origin")
	if err != nil {
		return nil
	}
	repo := CleanRepoURL(remote)
	if repo == "" {
		return nil
	}
	prefix, err := git("rev-parse", "--show-prefix")
	if err != nil {
		return nil
	}
	status, err := git("status", "--porcelain", "--", ".")
	if err != nil {
		return nil
	}
	return &api.GitSource{
		Repository: repo,
		Revision:   rev,
		Path:       strings.TrimSuffix(prefix, "/"),
		Dirty:      status != "",
	}
}

// CleanRepoURL turns a remote into an https (or http) URL with no
// credentials. It returns "" for local paths and unknown forms.
func CleanRepoURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		// scp form: [user@]host:path
		host, path, ok := strings.Cut(raw, ":")
		if !ok || len(host) < 2 || strings.ContainsAny(host, `/\`) {
			return ""
		}
		host = host[strings.LastIndex(host, "@")+1:]
		raw = "https://" + host + "/" + strings.TrimPrefix(path, "/")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	switch u.Scheme {
	case "https", "http":
	case "ssh", "git":
		u.Scheme, u.Host = "https", u.Hostname()
	default:
		return ""
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return strings.TrimSuffix(strings.TrimSuffix(u.String(), "/"), ".git")
}

func gitSourceFromAnnotations(a map[string]string) *api.GitSource {
	if a[ocispec.AnnotationSource] == "" {
		return nil
	}
	return &api.GitSource{
		Repository: a[ocispec.AnnotationSource],
		Revision:   a[ocispec.AnnotationRevision],
		Path:       a[api.AnnotationGitPath],
		Dirty:      a[api.AnnotationGitDirty] == "true",
	}
}

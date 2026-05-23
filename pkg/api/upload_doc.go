// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package api

// Upload records where a work-dir's spec was last uploaded so the wizard
// (and plan/update/upgrade) can re-enter from ConfigHub instead of from
// the local files. Persisted as <work-dir>/out/spec/upload.yaml after a
// successful `installer upload`. Also embedded in the per-Space
// installer-record Unit body so a freshly cloned work-dir can be
// recovered from ConfigHub alone.
type Upload struct {
	// APIVersion is the installer API group/version.
	APIVersion string `yaml:"apiVersion" json:"apiVersion"`
	// Kind is "Upload".
	Kind string `yaml:"kind" json:"kind"`
	// Metadata is the doc's ObjectMeta-shaped metadata block.
	Metadata Metadata `yaml:"metadata" json:"metadata"`
	// Spec carries the destination Space(s) and the cub context used.
	Spec UploadSpec `yaml:"spec" json:"spec"`
}

type UploadSpec struct {
	// Package is the parent package name from installer.yaml.
	Package string `yaml:"package" json:"package"`
	// PackageVersion is the parent package version.
	PackageVersion string `yaml:"packageVersion,omitempty" json:"packageVersion,omitempty"`
	// SpacePattern is the --space-pattern (or single --space) used at
	// upload time. Recorded so re-uploads with the same pattern are
	// idempotent.
	SpacePattern string `yaml:"spacePattern,omitempty" json:"spacePattern,omitempty"`
	// Spaces is one entry per package uploaded — the parent first, then
	// each locked dep — naming the resolved Space slug.
	Spaces []UploadedSpace `yaml:"spaces" json:"spaces"`
	// UploadedAt is the RFC3339 timestamp of the upload.
	UploadedAt string `yaml:"uploadedAt,omitempty" json:"uploadedAt,omitempty"`
	// Server is the ConfigHub server URL the upload targeted, taken from
	// the cub context at upload time. Sanity-checked on every subsequent
	// command against the current cub context.
	Server string `yaml:"server,omitempty" json:"server,omitempty"`
	// OrganizationID is the ConfigHub organization ID from the cub
	// context at upload time. Sanity-checked on every subsequent command.
	OrganizationID string `yaml:"organizationID,omitempty" json:"organizationID,omitempty"`
}

// UploadedSpace records one (package, resolved Space slug) pair from an
// upload. Each parent or locked dependency uploads into its own Space.
type UploadedSpace struct {
	// Package is the package's metadata.name.
	Package string `yaml:"package" json:"package"`
	// Version is the package's installerMetadata.version at upload time.
	Version string `yaml:"version,omitempty" json:"version,omitempty"`
	// Slug is the resolved ConfigHub Space slug (from SpacePattern).
	Slug string `yaml:"slug" json:"slug"`
	// IsParent flags the entry for the parent (root) package as opposed to
	// a locked dependency.
	IsParent bool `yaml:"isParent,omitempty" json:"isParent,omitempty"`
}

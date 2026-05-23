// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package api

// ConfigBlob is the JSON-encoded config blob attached to a native installer
// OCI artifact. It holds enough metadata for the resolver and `installer
// inspect` to operate without pulling the layer.
//
// Field naming uses JSON tags only — the blob is always serialized as JSON
// (mediaType ConfigMediaType).
type ConfigBlob struct {
	// Bundle is the computed-at-bundle-time header (digests, file list,
	// installer-CLI version that produced the artifact).
	Bundle BundleInfo `json:"bundle"`
	// Manifest is the parsed installer.yaml from the bundled .tgz, included
	// here so `installer inspect` and the resolver can read it without
	// pulling the layer.
	Manifest *Package `json:"manifest"`
}

// BundleInfo is the computed-at-bundle-time header.
type BundleInfo struct {
	// InstallerVersion is the version of the installer CLI that produced
	// this artifact (from internal/version.Version).
	InstallerVersion string `json:"installerVersion,omitempty"`

	// LayerDigest is the sha256 digest of the package .tgz, in the
	// "sha256:<hex>" form. Matches the layer descriptor's Digest.
	LayerDigest string `json:"layerDigest"`

	// LayerSize is the size in bytes of the package .tgz.
	LayerSize int64 `json:"layerSize"`

	// Files is the list of paths in the .tgz, in tar order (sorted). Useful
	// for `installer inspect` and for resolver sanity checks.
	Files []string `json:"files"`
}

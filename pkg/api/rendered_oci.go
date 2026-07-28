// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package api

// Media types for OCI artifacts containing rendered Kubernetes objects.
//
// The layer uses the standard OCI image-layer media type so Argo CD, Flux,
// ORAS, and other OCI consumers can unpack it without installer-specific
// support. Installer provenance and check results live in the config blob,
// outside the Kubernetes files consumed by delivery controllers.
const (
	RenderedArtifactType    = "application/vnd.confighub.installer.rendered.v1+json"
	RenderedConfigMediaType = "application/vnd.confighub.installer.rendered.config.v1+json"
	RenderedLayerMediaType  = "application/vnd.oci.image.layer.v1.tar+gzip"
	RenderedLayerTitle      = "rendered-manifests.tar.gz"
)

const (
	AnnotationSourceReference = "installer.confighub.com/source-reference"
	AnnotationSourceDigest    = "installer.confighub.com/source-digest"
	AnnotationBase            = "installer.confighub.com/base"
	AnnotationObjectSetDigest = "installer.confighub.com/object-set-digest"
)

// RenderedConfigBlob records how a rendered OCI artifact was produced without
// embedding input values or rendered Secrets in registry metadata.
type RenderedConfigBlob struct {
	SchemaVersion    string             `json:"schemaVersion"`
	InstallerVersion string             `json:"installerVersion,omitempty"`
	Source           RenderedSource     `json:"source"`
	Render           RenderedContext    `json:"render"`
	Checks           []RenderedCheck    `json:"checks,omitempty"`
	Output           RenderedBundleInfo `json:"output"`
}

type RenderedSource struct {
	Reference      string `json:"reference,omitempty"`
	ManifestDigest string `json:"manifestDigest,omitempty"`
	Package        string `json:"package"`
	PackageVersion string `json:"packageVersion,omitempty"`
}

type RenderedContext struct {
	Base                string   `json:"base"`
	Components          []string `json:"components,omitempty"`
	Namespace           string   `json:"namespace,omitempty"`
	SelectionSHA256     string   `json:"selectionSHA256"`
	InputsSHA256        string   `json:"inputsSHA256"`
	FunctionChainSHA256 string   `json:"functionChainSHA256"`
}

type RenderedCheck struct {
	Name   string `json:"name"`
	Result string `json:"result"`
}

type RenderedBundleInfo struct {
	ManifestCount   int      `json:"manifestCount"`
	ObjectSetDigest string   `json:"objectSetDigest"`
	LayerDigest     string   `json:"layerDigest"`
	LayerSize       int64    `json:"layerSize"`
	Files           []string `json:"files"`
}

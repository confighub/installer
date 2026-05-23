// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

// Package api defines the schemas the installer reads and writes:
//
//   - Package        (installer.yaml inside a package, hand-authored)
//   - Inputs         (user answers to wizard prompts)
//   - Selection      (chosen base + components, derived from Inputs by the wizard)
//   - FunctionChain  (resolved function invocations, executed by render)
//
// Each schema is shaped as a Kubernetes-style document with apiVersion, kind,
// metadata, spec. They are stored as Kubernetes/YAML Units in ConfigHub today;
// later they may move to a ConfigHub/YAML toolchain when first-class entities
// exist for them.
package api

const APIVersion = "installer.confighub.com/v1alpha1"

const (
	KindPackage       = "Package"
	KindInputs        = "Inputs"
	KindSelection     = "Selection"
	KindFunctionChain = "FunctionChain"
	KindFacts         = "Facts"
	KindLock          = "Lock"
	KindUpload        = "Upload"
)

// Metadata is the common metadata block on every installer doc. The shape
// matches Kubernetes ObjectMeta (name + labels + annotations) so the docs
// look familiar to anyone reading Kubernetes YAML.
type Metadata struct {
	// Name identifies the doc within its kind. Required.
	Name string `yaml:"name" json:"name"`
	// Labels are short key/value pairs used for selection and grouping.
	Labels map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	// Annotations are arbitrary key/value pairs carrying out-of-band
	// metadata. Used by the installer for things like the
	// PackageVersion= annotation written onto uploaded Units.
	Annotations map[string]string `yaml:"annotations,omitempty" json:"annotations,omitempty"`
}

// InstallerMetadata carries Package-level version metadata. Only meaningful
// on a Package (not on Inputs / Selection / Lock / etc.). Both *Version
// fields are SemVer range strings (e.g. ">= 1.28"); empty means
// unconstrained.
type InstallerMetadata struct {
	// Version is the package's own SemVer version (e.g. "0.3.0"). Used as
	// the right-hand side of `<package>@<version>` everywhere the
	// installer prints, labels, or annotates with package identity.
	Version string `yaml:"version,omitempty" json:"version,omitempty"`
	// KubeVersion is a SemVer range the cluster must satisfy
	// (e.g. ">= 1.28"). Empty means unconstrained.
	KubeVersion string `yaml:"kubeVersion,omitempty" json:"kubeVersion,omitempty"`
	// InstallerVersion is a SemVer range the installer CLI must satisfy
	// (e.g. ">= 0.2.0"). Empty means unconstrained.
	InstallerVersion string `yaml:"installerVersion,omitempty" json:"installerVersion,omitempty"`
}

// Header pairs APIVersion + Kind for sniffing the leading bytes of an
// installer doc without parsing its full body.
type Header struct {
	// APIVersion is the installer API group/version (e.g.
	// "installer.confighub.com/v1alpha1").
	APIVersion string `yaml:"apiVersion" json:"apiVersion"`
	// Kind is one of KindPackage / KindInputs / KindSelection / etc.
	Kind string `yaml:"kind" json:"kind"`
	// Metadata is the doc's ObjectMeta-shaped metadata block.
	Metadata Metadata `yaml:"metadata" json:"metadata"`
}

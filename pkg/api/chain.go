// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package api

// FunctionChain is the resolved (template-expanded) function chain that the
// render step actually executes. Persisted as a Unit alongside the rendered
// output so the exact transforms applied are inspectable and replayable.
type FunctionChain struct {
	// APIVersion is the installer API group/version.
	APIVersion string `yaml:"apiVersion" json:"apiVersion"`
	// Kind is "FunctionChain".
	Kind string `yaml:"kind" json:"kind"`
	// Metadata is the doc's ObjectMeta-shaped metadata block.
	Metadata Metadata `yaml:"metadata" json:"metadata"`
	// Spec carries the resolved function-group list that ran during render.
	Spec FunctionChainSpec `yaml:"spec" json:"spec"`
}

type FunctionChainSpec struct {
	// Package is the source package name the chain was resolved from.
	Package string `yaml:"package" json:"package"`
	// PackageVersion is the source package's SemVer at resolve time.
	PackageVersion string `yaml:"packageVersion,omitempty" json:"packageVersion,omitempty"`
	// Groups is the resolved (template-expanded) function-group list that
	// render executed in order.
	Groups []FunctionGroup `yaml:"groups" json:"groups"`
}

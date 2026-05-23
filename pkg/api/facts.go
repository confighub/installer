// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package api

// Facts holds the values produced by the package's Collector script — install-
// time discovery that depends on cluster, environment, or ConfigHub state and
// cannot be supplied by the user up front (e.g., a server-derived image tag,
// a freshly created BridgeWorkerID, the active context's server URL).
//
// Facts are persisted as out/spec/facts.yaml so re-render is reproducible from
// the same captured state. Re-run `installer wizard` to refresh.
//
// Sensitive material (passwords, tokens, worker secrets) MUST NOT be placed
// in Facts.Values; the collector writes those as .env.secret files consumed
// by a kustomize secretGenerator, and the rendered Secret is routed to
// out/secrets/ (never uploaded as a Unit).
type Facts struct {
	// APIVersion is the installer API group/version.
	APIVersion string `yaml:"apiVersion" json:"apiVersion"`
	// Kind is "Facts".
	Kind string `yaml:"kind" json:"kind"`
	// Metadata is the doc's ObjectMeta-shaped metadata block.
	Metadata Metadata `yaml:"metadata" json:"metadata"`
	// Spec carries the collected fact values.
	Spec FactsSpec `yaml:"spec" json:"spec"`
}

type FactsSpec struct {
	// Package is the source package name the facts were collected for.
	Package string `yaml:"package" json:"package"`
	// PackageVersion is the source package's SemVer at collection time.
	PackageVersion string `yaml:"packageVersion,omitempty" json:"packageVersion,omitempty"`
	// Values is the YAML map the collector wrote to stdout. Each key is
	// referenced from function-chain templates as `{{ .Facts.<name> }}`.
	Values map[string]any `yaml:"values" json:"values"`
}

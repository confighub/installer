// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package api

import "strings"

// Source records where a work-dir's package was pulled from. Pulling writes
// it to out/record/source.yaml, and upload carries it in the installer record,
// so that the install can be re-entered from the record alone: the package is
// pulled again from here.
type Source struct {
	// APIVersion is the installer API group/version.
	APIVersion string `yaml:"apiVersion" json:"apiVersion"`
	// Kind is "Source".
	Kind string `yaml:"kind" json:"kind"`
	// Metadata is the doc's ObjectMeta-shaped metadata block.
	Metadata Metadata `yaml:"metadata" json:"metadata"`
	// Spec says where the package came from.
	Spec SourceSpec `yaml:"spec" json:"spec"`
}

type SourceSpec struct {
	// Ref is the package reference as pulled: an oci:// reference, or a
	// local directory or .tgz made absolute. Only an oci:// reference can
	// be pulled on another machine.
	Ref string `yaml:"ref" json:"ref"`
	// Digest is the manifest digest an oci:// Ref resolved to when it was
	// pulled, so that pulling again gets the same package even if its tag
	// has since moved.
	Digest string `yaml:"digest,omitempty" json:"digest,omitempty"`
	// Git is where the package's source is kept, if known.
	Git *GitSource `yaml:"git,omitempty" json:"git,omitempty"`
}

// GitSource says which git source a package was built from.
type GitSource struct {
	Repository string `yaml:"repository" json:"repository"`
	Revision   string `yaml:"revision,omitempty" json:"revision,omitempty"`
	// Path is the package directory within the repository; empty means the
	// root.
	Path string `yaml:"path,omitempty" json:"path,omitempty"`
	// Dirty means the package directory had uncommitted changes.
	Dirty bool `yaml:"dirty,omitempty" json:"dirty,omitempty"`
}

// PinnedRef is the reference that pulls the same package again: Ref with
// Digest appended when Ref doesn't already name one.
func (s SourceSpec) PinnedRef() string {
	if s.Digest == "" || strings.Contains(s.Ref, "@") {
		return s.Ref
	}
	return s.Ref + "@" + s.Digest
}

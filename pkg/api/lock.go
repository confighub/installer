package api

// Lock pins every dependency of a package to a specific OCI digest. The
// resolver (Phase 4) writes one Lock as <work-dir>/out/spec/lock.yaml; the
// renderer (Phase 5) reads it and refuses to proceed if stale.
//
// The lock is also embedded in the parent's installer-record Unit on upload
// (Phase 6), so each rendered package's ConfigHub Space carries enough
// metadata to reproduce its own render — without keeping a separate file
// under version control.
type Lock struct {
	APIVersion string   `yaml:"apiVersion" json:"apiVersion"`
	Kind       string   `yaml:"kind" json:"kind"`
	Metadata   Metadata `yaml:"metadata" json:"metadata"`
	Spec       LockSpec `yaml:"spec" json:"spec"`
}

type LockSpec struct {
	// Package identifies the root package this lock was generated for.
	Package LockedPackage `yaml:"package" json:"package"`

	// Resolved is the dependency DAG in topological order: parents before
	// children. Each entry records the OCI ref + digest the resolver chose.
	Resolved []LockedDependency `yaml:"resolved,omitempty" json:"resolved,omitempty"`
}

// LockedPackage describes the root package the lock was computed against.
type LockedPackage struct {
	Name    string `yaml:"name" json:"name"`
	Version string `yaml:"version,omitempty" json:"version,omitempty"`
	// Digest is the OCI manifest digest of the root package, if it was
	// pulled from a registry. Empty for in-tree (un-published) root
	// packages, which is the common case during authoring.
	Digest string `yaml:"digest,omitempty" json:"digest,omitempty"`
}

// LockedDependency pins one resolved dependency.
type LockedDependency struct {
	// Name matches the Dependency.Name from the parent's installer.yaml.
	Name string `yaml:"name" json:"name"`

	// Ref is the full pinned OCI ref the resolver chose, including tag.
	// Example: oci://ghcr.io/confighubai/gateway-api:1.4.2
	Ref string `yaml:"ref" json:"ref"`

	// Digest is the sha256:<hex> of the manifest at Ref. The renderer
	// re-verifies the digest at fetch time so retagging upstream cannot
	// silently change the resolved content.
	Digest string `yaml:"digest" json:"digest"`

	// Version is the resolved SemVer string (without any range operator).
	Version string `yaml:"version,omitempty" json:"version,omitempty"`

	// RequestedBy lists the names of parents that requested this
	// dependency (transitively). The root is named "root".
	RequestedBy []string `yaml:"requestedBy,omitempty" json:"requestedBy,omitempty"`

	// Selection and Inputs are the pre-answers the resolver passed down
	// (merged across multiple parents requesting the same dep).
	Selection *DependencySelection `yaml:"selection,omitempty" json:"selection,omitempty"`
	Inputs    map[string]any       `yaml:"inputs,omitempty" json:"inputs,omitempty"`
}

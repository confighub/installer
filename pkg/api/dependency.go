package api

// Dependency declares another installer package this package composes with.
// Multiple parents may request the same dependency; the resolver picks one
// version satisfying every constraint, with conflicts/replaces honored.
//
// Phase 3 is parse-only — these fields are validated structurally but not
// acted upon. The Phase 4 resolver consumes them.
type Dependency struct {
	// Name is the local handle for this dependency within the parent
	// package. Used in lock files and error messages. Must be unique within
	// a package's Dependencies list.
	Name string `yaml:"name" json:"name"`

	// Package is the OCI ref (oci://host/repo) without a tag — the
	// resolver picks the tag matching Version.
	Package string `yaml:"package" json:"package"`

	// Version is a SemVer range expression (e.g. "^1.2.0", ">= 0.3, < 0.5",
	// "*"). Empty means "any version".
	Version string `yaml:"version,omitempty" json:"version,omitempty"`

	// Selection pre-answers the dep's wizard. If nil, the dep's defaults
	// apply.
	Selection *DependencySelection `yaml:"selection,omitempty" json:"selection,omitempty"`

	// Inputs pre-answers the dep's wizard prompts (input name → value).
	Inputs map[string]any `yaml:"inputs,omitempty" json:"inputs,omitempty"`

	// Optional, when true, makes this dependency conditional on
	// WhenComponent being selected in the parent's Selection. Optional
	// without WhenComponent means the dep is followed if no parent
	// component disables it (today: always followed; reserved for future
	// nuance).
	Optional bool `yaml:"optional,omitempty" json:"optional,omitempty"`

	// WhenComponent names a parent Component whose selection turns this
	// dependency on. Mirrors Helm subchart conditions and Debian Recommends.
	WhenComponent string `yaml:"whenComponent,omitempty" json:"whenComponent,omitempty"`

	// Satisfies lists ExternalRequire entries from the parent's package
	// that this dependency provides. Lets the resolver mark
	// externalRequires as covered without a separate cluster probe.
	Satisfies []ExternalRequire `yaml:"satisfies,omitempty" json:"satisfies,omitempty"`
}

// DependencySelection is the parent-visible part of a child package's
// Selection — base and components only. The full Selection adds metadata
// the resolver fills in (package name, version), so DependencySelection
// keeps the authored surface small.
type DependencySelection struct {
	Base       string   `yaml:"base,omitempty" json:"base,omitempty"`
	Components []string `yaml:"components,omitempty" json:"components,omitempty"`
}

// ConflictRef hard-excludes a package from the resolution set. The
// resolver fails if any dependency (direct or transitive) names a package
// matching one of these entries.
type ConflictRef struct {
	// Package is the OCI ref (oci://host/repo) of the excluded package.
	Package string `yaml:"package" json:"package"`
	// Version is a SemVer range; empty or "*" matches any version.
	Version string `yaml:"version,omitempty" json:"version,omitempty"`
	// Reason is shown in resolver error messages.
	Reason string `yaml:"reason,omitempty" json:"reason,omitempty"`
}

// ReplaceRef declares that this package supersedes another. The resolver
// treats a dependency on the named package matching the version range as
// satisfied by the package declaring the replacement.
type ReplaceRef struct {
	Package string `yaml:"package" json:"package"`
	Version string `yaml:"version,omitempty" json:"version,omitempty"`
}

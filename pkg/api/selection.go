package api

// Selection records the chosen base + components after the wizard's solver
// closes the user's picks under Requires and validates ValidForBases /
// Conflicts. Persisted as a Unit alongside the rendered output; user-editable
// for re-render ("add cache-server", "switch to knative base").
type Selection struct {
	APIVersion string        `yaml:"apiVersion" json:"apiVersion"`
	Kind       string        `yaml:"kind" json:"kind"`
	Metadata   Metadata      `yaml:"metadata" json:"metadata"`
	Spec       SelectionSpec `yaml:"spec" json:"spec"`
}

type SelectionSpec struct {
	// Package identifies the source package this selection is against.
	Package        string `yaml:"package" json:"package"`
	PackageVersion string `yaml:"packageVersion,omitempty" json:"packageVersion,omitempty"`
	// Base is the chosen Base.Name from the package.
	Base string `yaml:"base" json:"base"`
	// Components is the closure-resolved list of Component names.
	Components []string `yaml:"components,omitempty" json:"components,omitempty"`
}

package api

// Inputs holds the user's wizard answers, persisted as a Unit alongside the
// rendered output so re-render is reproducible. The wizard authors this from
// CLI flags (--input k=v); the user may also hand-edit before re-render.
type Inputs struct {
	APIVersion string     `yaml:"apiVersion" json:"apiVersion"`
	Kind       string     `yaml:"kind" json:"kind"`
	Metadata   Metadata   `yaml:"metadata" json:"metadata"`
	Spec       InputsSpec `yaml:"spec" json:"spec"`
}

type InputsSpec struct {
	// Package identifies the source package (name@version) these inputs answer.
	Package        string `yaml:"package" json:"package"`
	PackageVersion string `yaml:"packageVersion,omitempty" json:"packageVersion,omitempty"`
	// Values maps input Name → user-provided value, coerced to the declared Type.
	Values map[string]any `yaml:"values" json:"values"`
}

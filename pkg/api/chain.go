package api

// FunctionChain is the resolved (template-expanded) function chain that the
// render step actually executes. Persisted as a Unit alongside the rendered
// output so the exact transforms applied are inspectable and replayable.
type FunctionChain struct {
	APIVersion string            `yaml:"apiVersion" json:"apiVersion"`
	Kind       string            `yaml:"kind" json:"kind"`
	Metadata   Metadata          `yaml:"metadata" json:"metadata"`
	Spec       FunctionChainSpec `yaml:"spec" json:"spec"`
}

type FunctionChainSpec struct {
	Package        string          `yaml:"package" json:"package"`
	PackageVersion string          `yaml:"packageVersion,omitempty" json:"packageVersion,omitempty"`
	Groups         []FunctionGroup `yaml:"groups" json:"groups"`
}

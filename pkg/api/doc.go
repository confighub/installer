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
)

// Metadata is the common metadata block on every installer doc.
type Metadata struct {
	Name        string            `yaml:"name" json:"name"`
	Version     string            `yaml:"version,omitempty" json:"version,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	Annotations map[string]string `yaml:"annotations,omitempty" json:"annotations,omitempty"`
}

// Header pairs APIVersion + Kind for sniffing.
type Header struct {
	APIVersion string   `yaml:"apiVersion" json:"apiVersion"`
	Kind       string   `yaml:"kind" json:"kind"`
	Metadata   Metadata `yaml:"metadata" json:"metadata"`
}

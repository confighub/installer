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
	// Namespace is the Kubernetes namespace into which the package will install.
	// Captured at wizard time via --namespace so that every package does not
	// need to declare its own `namespace` input. Function-chain templates
	// reference it as `{{ .Namespace }}`.
	Namespace string `yaml:"namespace,omitempty" json:"namespace,omitempty"`
	// Values maps input Name → user-provided value, coerced to the declared Type.
	Values map[string]any `yaml:"values" json:"values"`
	// ImageOverrides maps kustomize image transformer name → image
	// reference (e.g., "hello" → "hello:v2"). Populated by the
	// `--set-image` flag on `installer wizard` / `installer upgrade`.
	// At render time the installer runs `kustomize edit set image
	// <name>=<ref>` for each entry against the chosen base's
	// kustomization.yaml, before invoking `kustomize build`. The
	// package's chosen base must declare an `images:` block in its
	// kustomization.yaml; render fails fast otherwise.
	//
	// Persisted here (rather than in Values) so it round-trips across
	// upgrades without operator re-typing: the next upgrade carries
	// these overrides forward unless the operator passes a different
	// `--set-image` for the same name.
	ImageOverrides map[string]string `yaml:"imageOverrides,omitempty" json:"imageOverrides,omitempty"`
}

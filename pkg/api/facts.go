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
	APIVersion string    `yaml:"apiVersion" json:"apiVersion"`
	Kind       string    `yaml:"kind" json:"kind"`
	Metadata   Metadata  `yaml:"metadata" json:"metadata"`
	Spec       FactsSpec `yaml:"spec" json:"spec"`
}

type FactsSpec struct {
	Package        string         `yaml:"package" json:"package"`
	PackageVersion string         `yaml:"packageVersion,omitempty" json:"packageVersion,omitempty"`
	Values         map[string]any `yaml:"values" json:"values"`
}

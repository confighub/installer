package api

// Package is the installer.yaml document a package author writes by hand.
// It declares what bases and components are available, what inputs the wizard
// should ask for, what external preconditions the package needs, and what the
// function chain template looks like.
type Package struct {
	APIVersion string      `yaml:"apiVersion" json:"apiVersion"`
	Kind       string      `yaml:"kind" json:"kind"`
	Metadata   Metadata    `yaml:"metadata" json:"metadata"`
	Spec       PackageSpec `yaml:"spec" json:"spec"`
}

type PackageSpec struct {
	// Bases are alternative top-level kustomize trees. Exactly one is selected
	// at install time (default: the one with Default: true). Use this when the
	// package supports orthogonal deployment shapes that cannot be expressed
	// as opt-in Components (e.g., KServe Knative vs Raw, llm-d colocated vs
	// P/D-disaggregated).
	Bases []Base `yaml:"bases" json:"bases"`

	// Components are kustomize Components (kind: Component) that may be
	// selected to add features on top of the chosen Base. Selection is
	// closed under Requires.
	Components []Component `yaml:"components,omitempty" json:"components,omitempty"`

	// ExternalRequires lists preconditions the cluster must satisfy. Evaluated
	// at install-time by `installer preflight` and surfaced in the wizard.
	ExternalRequires []ExternalRequire `yaml:"externalRequires,omitempty" json:"externalRequires,omitempty"`

	// Provides lists CRDs and other resources this package installs. Used to
	// detect double-install conflicts when multiple packages are deployed in
	// the same cluster.
	Provides []Provide `yaml:"provides,omitempty" json:"provides,omitempty"`

	// ClusterSingleton lists leader-election leases this package claims at
	// cluster scope. Two packages claiming the same lease cannot coexist.
	ClusterSingleton []SingletonClaim `yaml:"clusterSingleton,omitempty" json:"clusterSingleton,omitempty"`

	// ExternalManifests are remote manifest files (e.g., release tarballs of
	// CRDs) that get fetched at render time and merged into the rendered
	// output as additional Units. Used by projects like Gateway API Inference
	// Extension that distribute CRDs as a release-tarball outside any chart.
	ExternalManifests []ExternalManifest `yaml:"externalManifests,omitempty" json:"externalManifests,omitempty"`

	// Inputs declares the wizard prompts. Inputs are referenced by Go template
	// expressions in Transformers (e.g., "{{ .Inputs.namespace }}").
	Inputs []Input `yaml:"inputs,omitempty" json:"inputs,omitempty"`

	// Collector is an executable bundled in the package that the wizard runs
	// to discover install-time facts (server URL, image tag, worker IDs, etc.)
	// and to produce sensitive material as .env.secret files consumed by
	// kustomize secretGenerator. See Transformers for how facts are
	// referenced.
	Collector *Collector `yaml:"collector,omitempty" json:"collector,omitempty"`

	// Validation points at machine-readable component documentation bundled in
	// the package (typically under ./validation/): a JSON Schema of accepted
	// env vars, the command help YAML, and a runtime spec. Not consumed by
	// render today — surfaced by `installer doc` so an AI agent or human can
	// inspect what the rendered workload supports.
	Validation *Validation `yaml:"validation,omitempty" json:"validation,omitempty"`

	// Phases groups rendered output Units for ordered apply. Each rendered
	// Unit is labeled with the first matching phase. The last phase with an
	// empty WhereResource matches everything else.
	Phases []Phase `yaml:"phases,omitempty" json:"phases,omitempty"`

	// Transformers is a list of function-invocation groups that mutate the
	// rendered output. At render time it is resolved with the wizard answers
	// (Go templates), serialized to out/compose/transformers.yaml as a
	// ConfigHubTransformers KRM function config, and run by `installer
	// transformer` (which kustomize invokes as an exec plugin via the
	// out/compose/installer-transformer.sh wrapper). Each group runs through
	// funcimpl.NewStandardExecutor with its own toolchain and whereResource
	// filter, output of each group feeding the next.
	Transformers []FunctionGroup `yaml:"transformers,omitempty" json:"transformers,omitempty"`

	// Validators is a list of validating-function invocation groups, run
	// at the end of render against the mutated output. Same shape as
	// Transformers, but every named function must be a
	// Validating function (Mutating=false). Validators do not modify
	// the rendered manifests; they fail render if any validator
	// returns Passed=false.
	//
	// The `installer init` command seeds new packages with vet-schemas,
	// vet-merge-keys, and vet-format. Authors can edit this list with
	// `installer edit validator add/remove`. The full list of available
	// validators can be discovered with
	//   `cub function list --where "Validating = TRUE" --toolchain Kubernetes/YAML`.
	Validators []FunctionGroup `yaml:"validators,omitempty" json:"validators,omitempty"`

	// Dependencies declares other installer packages this package composes
	// with. Each entry pins an OCI ref + SemVer constraint; the resolver
	// (Phase 4) walks the DAG and writes out/spec/lock.yaml. Parse-only in
	// Phase 3 — wizard, render, and upload ignore this field.
	Dependencies []Dependency `yaml:"dependencies,omitempty" json:"dependencies,omitempty"`

	// Conflicts hard-excludes other packages from the resolution set.
	// Mirrors Debian's Conflicts:.
	Conflicts []ConflictRef `yaml:"conflicts,omitempty" json:"conflicts,omitempty"`

	// Replaces declares packages this one supersedes (typically across a
	// rename). The resolver treats a request for a Replaces[i].Package
	// matching the version range as satisfied by this package. Mirrors
	// Debian's Replaces:.
	Replaces []ReplaceRef `yaml:"replaces,omitempty" json:"replaces,omitempty"`

	// BundleExamples controls whether the examples/ subtree is included by
	// `installer package`. Default behavior (when nil) is true — examples
	// are bundled. Set to false to exclude them from published artifacts.
	BundleExamples *bool `yaml:"bundleExamples,omitempty" json:"bundleExamples,omitempty"`
}

type Base struct {
	// Name is the slug used in Selection.spec.base.
	Name string `yaml:"name" json:"name"`
	// Path is the directory in the package tree containing kustomization.yaml.
	Path string `yaml:"path" json:"path"`
	// Default selects this base when the user does not pick one.
	Default bool `yaml:"default,omitempty" json:"default,omitempty"`
	// Description is shown by `installer doc`.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// ExternalRequires scoped to this base (in addition to package-level requires).
	ExternalRequires []ExternalRequire `yaml:"externalRequires,omitempty" json:"externalRequires,omitempty"`
}

type Component struct {
	// Name is the slug used in Selection.spec.components.
	Name string `yaml:"name" json:"name"`
	// Path is the directory in the package tree containing the kind: Component
	// kustomization.yaml.
	Path string `yaml:"path" json:"path"`
	// Default selects this component when the user picks the `default`
	// preset in the wizard. Components without `default: true` are only
	// installed under the `all` preset or via explicit selection.
	Default bool `yaml:"default,omitempty" json:"default,omitempty"`
	// Description is shown by `installer doc`.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Requires names other Components that must be selected. Closure is
	// computed by the wizard's solver before render.
	Requires []string `yaml:"requires,omitempty" json:"requires,omitempty"`
	// Conflicts names Components that cannot be selected together.
	Conflicts []string `yaml:"conflicts,omitempty" json:"conflicts,omitempty"`
	// ValidForBases names Bases this Component is compatible with. Empty
	// means valid for all bases.
	ValidForBases []string `yaml:"validForBases,omitempty" json:"validForBases,omitempty"`
	// ExternalRequires scoped to this Component.
	ExternalRequires []ExternalRequire `yaml:"externalRequires,omitempty" json:"externalRequires,omitempty"`
	// Transformers is an optional component-scoped mixin: function groups
	// that only run when this component is selected. Appended to the
	// package-wide PackageSpec.Transformers in declaration order so the
	// resolved chain still runs in a single in-process executor pass.
	// Authored exactly like the package-wide list — same toolchain /
	// whereResource / invocations shape, same restricted Go template
	// surface ({{ .Namespace }}, {{ .Inputs.* }}, {{ .Selection.* }},
	// {{ .Facts.* }}, {{ .Package.* }}).
	Transformers []FunctionGroup `yaml:"transformers,omitempty" json:"transformers,omitempty"`
	// Validators is the analogous component-scoped validator list. Appended
	// to PackageSpec.Validators when this component is selected; same
	// "Mutating=false only" contract.
	Validators []FunctionGroup `yaml:"validators,omitempty" json:"validators,omitempty"`
}

// ExternalRequireKind enumerates the typed precondition categories observed
// across real inference-stack projects (KServe, KubeRay, GAIE, llm-d, vLLM).
type ExternalRequireKind string

const (
	ExtReqCRD                 ExternalRequireKind = "CRD"
	ExtReqClusterFeature      ExternalRequireKind = "ClusterFeature"
	ExtReqWebhookCertProvider ExternalRequireKind = "WebhookCertProvider"
	ExtReqGatewayClass        ExternalRequireKind = "GatewayClass"
	ExtReqOperator            ExternalRequireKind = "Operator"
	ExtReqStorageClass        ExternalRequireKind = "StorageClass"
	ExtReqRuntimeClass        ExternalRequireKind = "RuntimeClass"
)

type ExternalRequire struct {
	Kind ExternalRequireKind `yaml:"kind" json:"kind"`
	Name string              `yaml:"name,omitempty" json:"name,omitempty"`
	// Version is a constraint expression (e.g., ">= v0.4.0").
	Version string `yaml:"version,omitempty" json:"version,omitempty"`
	// Capability is used with GatewayClass to require a specific feature
	// (e.g., "ext-proc"). Any GatewayClass providing the capability satisfies.
	Capability string `yaml:"capability,omitempty" json:"capability,omitempty"`
	// Namespace pins the requirement to a specific namespace (Operator/StorageClass).
	Namespace string `yaml:"namespace,omitempty" json:"namespace,omitempty"`
	// IssuerKind constrains a WebhookCertProvider (e.g., "ClusterIssuer").
	IssuerKind string `yaml:"issuerKind,omitempty" json:"issuerKind,omitempty"`
	// SuggestedSource points the user at a package or chart that satisfies
	// this requirement, surfaced in the wizard.
	SuggestedSource string `yaml:"suggestedSource,omitempty" json:"suggestedSource,omitempty"`
	// SuggestedProviders lists multiple acceptable providers (e.g., for
	// GatewayClass: Istio, EnvoyGateway, kgateway, ...).
	SuggestedProviders []string `yaml:"suggestedProviders,omitempty" json:"suggestedProviders,omitempty"`
}

// ProvideKind enumerates what a package can claim to provide.
type ProvideKind string

const (
	ProvideCRD          ProvideKind = "CRD"
	ProvideOperator     ProvideKind = "Operator"
	ProvideGatewayClass ProvideKind = "GatewayClass"
)

type Provide struct {
	Kind ProvideKind `yaml:"kind" json:"kind"`
	Name string      `yaml:"name" json:"name"`
}

type SingletonClaim struct {
	Lease     string `yaml:"lease" json:"lease"`
	Namespace string `yaml:"namespace,omitempty" json:"namespace,omitempty"`
}

type ExternalManifest struct {
	// Name identifies this manifest within the package.
	Name string `yaml:"name" json:"name"`
	// URL is fetched at render time. Must include a digest pin via Digest.
	URL string `yaml:"url" json:"url"`
	// Digest is the expected sha256:... of the fetched bytes; render fails on mismatch.
	Digest string `yaml:"digest" json:"digest"`
	// SplitByResource splits the fetched multi-doc YAML stream into one Unit
	// per resource (default true). Set false to keep as a single Unit.
	SplitByResource *bool `yaml:"splitByResource,omitempty" json:"splitByResource,omitempty"`
	// Phase assigns these resources to a named phase from spec.phases.
	Phase string `yaml:"phase,omitempty" json:"phase,omitempty"`
}

// Input declares one wizard prompt.
type Input struct {
	// Name is the key in the resolved Inputs map and the variable name used
	// in function chain templates ({{ .Inputs.<name> }}).
	Name string `yaml:"name" json:"name"`
	// Type constrains the value: string, int, bool, enum, list.
	Type string `yaml:"type" json:"type"`
	// Default is used when the user does not supply a value.
	Default any `yaml:"default,omitempty" json:"default,omitempty"`
	// Required fails if missing and no default is set.
	Required bool `yaml:"required,omitempty" json:"required,omitempty"`
	// Prompt is the human-readable question.
	Prompt string `yaml:"prompt,omitempty" json:"prompt,omitempty"`
	// Description is longer help text.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Options are valid values when Type == "enum".
	Options []string `yaml:"options,omitempty" json:"options,omitempty"`
	// WhenExternalRequire only prompts this input if the package has an
	// ExternalRequire of this Kind.
	WhenExternalRequire ExternalRequireKind `yaml:"whenExternalRequire,omitempty" json:"whenExternalRequire,omitempty"`
}

type Phase struct {
	Name string `yaml:"name" json:"name"`
	// WhereResource is a ConfigHub function-executor filter expression. The
	// first phase whose filter matches is assigned to a resource. The last
	// phase with empty WhereResource catches everything else.
	WhereResource string `yaml:"whereResource,omitempty" json:"whereResource,omitempty"`
}

// FunctionGroup is one batch of function invocations sharing a toolchain and
// a whereResource filter, mirroring the cub function-executor SDK signature
// (one call to invokeLocalFunctions per group).
type FunctionGroup struct {
	// Toolchain is the executor toolchain (e.g., "Kubernetes/YAML",
	// "AppConfig/Properties"). Per-group so a single chain can mutate both
	// raw Kubernetes manifests and AppConfig Units in the same render.
	Toolchain string `yaml:"toolchain" json:"toolchain"`
	// WhereResource scopes which resources this group operates on. Empty
	// means all resources.
	WhereResource string `yaml:"whereResource,omitempty" json:"whereResource,omitempty"`
	// Invocations runs in order. Output of each invocation feeds the next
	// (within the group). Output of each group feeds the next group.
	Invocations []FunctionInvocation `yaml:"invocations" json:"invocations"`
	// Description is shown when previewing the chain.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

type FunctionInvocation struct {
	Name string   `yaml:"name" json:"name"`
	Args []string `yaml:"args,omitempty" json:"args,omitempty"`
}

// Collector declares a fact-collection executable bundled in the package. The
// wizard runs it after answering inputs and writes its stdout (a YAML map) to
// out/spec/facts.yaml. The script may also write .env.secret files into the
// package working copy at paths its kustomize secretGenerator references; the
// installer never reads or uploads those files.
//
// The wizard runs the command with the package root as the working directory
// and the following env vars set (parent env is also inherited so `cub` works):
//
//   INSTALLER_PACKAGE_DIR      absolute path to the package working copy
//   INSTALLER_WORK_DIR         absolute path to the parent working directory
//   INSTALLER_OUT_DIR          absolute path to <work-dir>/out
//   INSTALLER_NAMESPACE        value of --namespace
//   INSTALLER_BASE             chosen base name
//   INSTALLER_SELECTED         comma-separated selected component names
//   INSTALLER_INPUT_<NAME>     one variable per declared input (uppercased)
type Collector struct {
	// Command is the executable. May be relative (resolved against the package
	// root) or absolute. Required.
	Command string `yaml:"command" json:"command"`
	// Args are passed verbatim after Command. Optional.
	Args []string `yaml:"args,omitempty" json:"args,omitempty"`
	// Description is shown by `installer doc`.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// Validation points at component documentation bundled in the package.
//
// Typically generated by running `docker run <image> docgen {command,env,runtime}`
// and committed under ./validation/ so consumers (including AI agents) can read
// what env vars the workload accepts and what runtime expectations it has
// without re-pulling the image.
type Validation struct {
	// CommandHelp is a YAML file produced by `cub-worker-run docgen command`
	// (Cobra command tree).
	CommandHelp string `yaml:"commandHelp,omitempty" json:"commandHelp,omitempty"`
	// EnvSchema is a JSON Schema describing accepted env vars, produced by
	// `cub-worker-run docgen env`.
	EnvSchema string `yaml:"envSchema,omitempty" json:"envSchema,omitempty"`
	// RuntimeSpec is the runtime spec YAML produced by
	// `cub-worker-run docgen runtime` (ports, paths, probes).
	RuntimeSpec string `yaml:"runtimeSpec,omitempty" json:"runtimeSpec,omitempty"`
	// HowToRegenerate is human-readable shell text shown by `installer doc`.
	HowToRegenerate string `yaml:"howToRegenerate,omitempty" json:"howToRegenerate,omitempty"`
}

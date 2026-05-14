package api

// SigningPolicy declares which signatures the installer trusts when
// verifying OCI artifacts on pull / deps update. The file lives at
// ~/.config/installer/policy.yaml. Absent file ⇒ no verification.
//
// When Enforce is true and a ref's signature does not satisfy at least one
// entry in TrustedKeys or TrustedKeyless, the operation fails.
type SigningPolicy struct {
	APIVersion string            `yaml:"apiVersion" json:"apiVersion"`
	Kind       string            `yaml:"kind" json:"kind"`
	Metadata   Metadata          `yaml:"metadata,omitempty" json:"metadata,omitempty"`
	Spec       SigningPolicySpec `yaml:"spec" json:"spec"`
}

const KindSigningPolicy = "SigningPolicy"

type SigningPolicySpec struct {
	// Enforce, when true, makes pull/deps update fail on unverified
	// artifacts. When false, the policy is treated as advisory:
	// `installer verify` still works, but pull and deps update do not
	// gate on it.
	Enforce bool `yaml:"enforce" json:"enforce"`

	// TrustedKeys lists cosign public-key entries.
	TrustedKeys []TrustedKey `yaml:"trustedKeys,omitempty" json:"trustedKeys,omitempty"`

	// TrustedKeyless lists Sigstore-keyless identity entries (Fulcio
	// certificate identity + OIDC issuer).
	TrustedKeyless []TrustedKeyless `yaml:"trustedKeyless,omitempty" json:"trustedKeyless,omitempty"`
}

// TrustedKey points at a cosign-compatible public key.
type TrustedKey struct {
	// PublicKey is the path on disk OR a cosign key reference
	// (k8s://, awskms://, etc.) passed verbatim to `cosign verify --key`.
	PublicKey string `yaml:"publicKey" json:"publicKey"`
	// Repos optionally scopes this key to specific OCI repos. Each entry
	// is matched as a prefix (no globs in v1). Empty = matches all repos.
	Repos []string `yaml:"repos,omitempty" json:"repos,omitempty"`
	// Description is shown in error messages when no key matches.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// TrustedKeyless is a Sigstore identity claim.
type TrustedKeyless struct {
	// Identity is the cosign --certificate-identity value (the email or
	// URI in the Fulcio cert). Required.
	Identity string `yaml:"identity" json:"identity"`
	// Issuer is the OIDC issuer URL (--certificate-oidc-issuer).
	// Required.
	Issuer string `yaml:"issuer" json:"issuer"`
	// Repos scopes this identity to specific OCI repos (prefix match).
	Repos []string `yaml:"repos,omitempty" json:"repos,omitempty"`
	// Description is shown in error messages when no identity matches.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

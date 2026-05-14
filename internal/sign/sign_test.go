package sign

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/confighub/installer/pkg/api"
)

const policyYAML = `apiVersion: installer.confighub.com/v1alpha1
kind: SigningPolicy
spec:
  enforce: true
  trustedKeys:
    - publicKey: /etc/keys/all.pub
    - publicKey: /etc/keys/ghcr.pub
      repos: [ghcr.io/confighubai]
  trustedKeyless:
    - identity: ops@confighub.com
      issuer: https://accounts.google.com
      repos: [oci://ghcr.io/confighubai]
`

const policyYAMLNoEnforce = `apiVersion: installer.confighub.com/v1alpha1
kind: SigningPolicy
spec:
  enforce: false
`

const policyYAMLBadEnforceNoEntries = `apiVersion: installer.confighub.com/v1alpha1
kind: SigningPolicy
spec:
  enforce: true
`

func TestLoadPolicy_Absent(t *testing.T) {
	t.Setenv(EnvPolicyPath, filepath.Join(t.TempDir(), "missing.yaml"))
	got, err := LoadPolicy()
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for absent policy file")
	}
}

func TestLoadPolicy_Parsed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(p, []byte(policyYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvPolicyPath, p)
	got, err := LoadPolicy()
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	if !got.Spec.Enforce {
		t.Errorf("Enforce should be true")
	}
	if len(got.Spec.TrustedKeys) != 2 {
		t.Errorf("got %d trusted keys", len(got.Spec.TrustedKeys))
	}
	if len(got.Spec.TrustedKeyless) != 1 {
		t.Errorf("got %d keyless entries", len(got.Spec.TrustedKeyless))
	}
}

func TestLoadPolicy_NoEnforce(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(p, []byte(policyYAMLNoEnforce), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvPolicyPath, p)
	got, err := LoadPolicy()
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	if got.Spec.Enforce {
		t.Errorf("Enforce should be false")
	}
}

func TestLoadPolicy_RejectsEnforceWithoutEntries(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(p, []byte(policyYAMLBadEnforceNoEntries), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvPolicyPath, p)
	_, err := LoadPolicy()
	if err == nil || !strings.Contains(err.Error(), "no trustedKeys") {
		t.Fatalf("expected enforce-without-entries error, got %v", err)
	}
}

func TestMatchEntries(t *testing.T) {
	p := &api.SigningPolicy{
		Spec: api.SigningPolicySpec{
			Enforce: true,
			TrustedKeys: []api.TrustedKey{
				{PublicKey: "all.pub"},                                            // matches all
				{PublicKey: "ghcr.pub", Repos: []string{"ghcr.io/confighubai"}},   // scoped
				{PublicKey: "other.pub", Repos: []string{"docker.io/other"}},      // doesn't match
			},
			TrustedKeyless: []api.TrustedKeyless{
				{Identity: "ops@ch", Issuer: "x", Repos: []string{"oci://ghcr.io/confighubai"}},
			},
		},
	}
	keys, kls := matchEntries(p, "oci://ghcr.io/confighubai/foo:v1@sha256:abc")
	if len(keys) != 2 {
		t.Errorf("expected 2 keys matched, got %d (%+v)", len(keys), keys)
	}
	if len(kls) != 1 {
		t.Errorf("expected 1 keyless matched, got %d", len(kls))
	}

	// A different repo only matches the unrestricted key.
	keys, kls = matchEntries(p, "oci://example.com/foo:v1")
	if len(keys) != 1 || keys[0].PublicKey != "all.pub" {
		t.Errorf("expected only all.pub, got %+v", keys)
	}
	if len(kls) != 0 {
		t.Errorf("expected no keyless match, got %+v", kls)
	}
}

func TestEnforceVerification_NoPolicy(t *testing.T) {
	t.Setenv(EnvPolicyPath, filepath.Join(t.TempDir(), "absent.yaml"))
	if err := EnforceVerification(t.Context(), "oci://anything:v1"); err != nil {
		t.Fatalf("absent policy should be a no-op: %v", err)
	}
}

func TestEnforceVerification_DisabledPolicy(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(p, []byte(policyYAMLNoEnforce), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvPolicyPath, p)
	if err := EnforceVerification(t.Context(), "oci://anything:v1"); err != nil {
		t.Fatalf("disabled policy should be a no-op: %v", err)
	}
}

func TestEnforceVerification_NoMatchingEntry(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(p, []byte(`apiVersion: installer.confighub.com/v1alpha1
kind: SigningPolicy
spec:
  enforce: true
  trustedKeys:
    - publicKey: /x.pub
      repos: [ghcr.io/confighubai]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvPolicyPath, p)
	err := EnforceVerification(t.Context(), "oci://other.io/foo:v1")
	if err == nil || !strings.Contains(err.Error(), "no trust entry matches") {
		t.Fatalf("expected no-match error, got %v", err)
	}
}

func TestStripTagAndDigest(t *testing.T) {
	cases := map[string]string{
		"ghcr.io/o/r:v1":                                                                          "ghcr.io/o/r",
		"ghcr.io/o/r@sha256:abc":                                                                  "ghcr.io/o/r",
		"ghcr.io/o/r:v1@sha256:abc":                                                               "ghcr.io/o/r",
		"localhost:5555/o/r:v1":                                                                   "localhost:5555/o/r",
		"localhost:5555/o/r":                                                                      "localhost:5555/o/r",
	}
	for in, want := range cases {
		if got := stripTagAndDigest(in); got != want {
			t.Errorf("stripTagAndDigest(%q) = %q want %q", in, got, want)
		}
	}
}

func TestExpandTilde(t *testing.T) {
	got, err := expandTilde("~/foo/bar")
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if got != filepath.Join(home, "foo/bar") {
		t.Errorf("expandTilde = %q", got)
	}
	// No tilde — returned as-is.
	if got, _ := expandTilde("/abs/path"); got != "/abs/path" {
		t.Errorf("non-tilde unchanged: %q", got)
	}
}

// Package sign shells out to the cosign binary to sign and verify installer
// OCI artifacts. Cosign's Go SDK is large and pulls a lot of sigstore
// dependencies; for v1 we keep the installer binary small by invoking
// cosign as an external process.
//
// Policy is loaded from ~/.config/installer/policy.yaml. When the file is
// absent or Enforce is false, EnforceVerification is a no-op.
package sign

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/confighub/installer/pkg/api"
)

// Defaults
const (
	// DefaultPolicyPath is where LoadPolicy looks if no override is set.
	DefaultPolicyPath = "~/.config/installer/policy.yaml"
	// EnvPolicyPath overrides DefaultPolicyPath at runtime.
	EnvPolicyPath = "INSTALLER_SIGNING_POLICY"
	// EnvCosignBinary names the cosign binary; default "cosign".
	EnvCosignBinary = "INSTALLER_COSIGN_BIN"
)

// SignOptions control Sign.
type SignOptions struct {
	// Key is the cosign key reference for keyed mode. Empty ⇒ keyless.
	Key string
	// Recursive signs every layer/manifest. Off by default for installer
	// artifacts (single manifest is enough).
	Recursive bool
	// Yes suppresses the keyless TTY confirmation prompt. Matches
	// `cosign sign --yes`.
	Yes bool
}

// Sign attaches a cosign signature to ref. ref must be a digest-pinned or
// tag-pinned reference; cosign resolves it. The cosign binary inherits the
// caller's environment, so SIGSTORE_*, COSIGN_*, and OIDC tokens are
// honored.
func Sign(ctx context.Context, ref string, opts SignOptions) error {
	bin := cosignBin()
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("cosign binary %q not found on PATH: %w", bin, err)
	}
	args := []string{"sign"}
	if opts.Yes {
		args = append(args, "--yes")
	}
	if opts.Recursive {
		args = append(args, "--recursive")
	}
	if opts.Key != "" {
		args = append(args, "--key", opts.Key)
	}
	args = append(args, refForCosign(ref))
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cosign sign %s: %w", ref, err)
	}
	return nil
}

// VerifyOptions tune one Verify call. Caller usually loads a Policy and
// passes the matching entries; for ad-hoc `installer verify`, a single
// TrustedKey or TrustedKeyless is constructed from CLI flags.
type VerifyOptions struct {
	// TrustedKey forces a keyed verification against this public key.
	TrustedKey *api.TrustedKey
	// TrustedKeyless forces a keyless verification.
	TrustedKeyless *api.TrustedKeyless
}

// Verify runs cosign verify with the given trust entry. Exactly one of
// opts.TrustedKey or opts.TrustedKeyless must be set.
func Verify(ctx context.Context, ref string, opts VerifyOptions) error {
	if (opts.TrustedKey == nil) == (opts.TrustedKeyless == nil) {
		return errors.New("Verify: exactly one of TrustedKey or TrustedKeyless must be set")
	}
	bin := cosignBin()
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("cosign binary %q not found on PATH: %w", bin, err)
	}
	args := []string{"verify"}
	switch {
	case opts.TrustedKey != nil:
		args = append(args, "--key", opts.TrustedKey.PublicKey)
	case opts.TrustedKeyless != nil:
		args = append(args,
			"--certificate-identity", opts.TrustedKeyless.Identity,
			"--certificate-oidc-issuer", opts.TrustedKeyless.Issuer,
		)
	}
	args = append(args, refForCosign(ref))
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// Discard stdout: cosign prints the verified-payload JSON which is
	// noisy for our use case. We surface stderr on failure.
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cosign verify: %w\n%s", err, stderr.String())
	}
	return nil
}

// EnforceVerification consults the loaded policy and verifies ref. If the
// policy is absent, or Enforce is false, returns nil. Otherwise tries every
// matching trust entry; succeeds on first match.
func EnforceVerification(ctx context.Context, ref string) error {
	p, err := LoadPolicy()
	if err != nil {
		return err
	}
	if p == nil || !p.Spec.Enforce {
		return nil
	}
	keys, kls := matchEntries(p, ref)
	if len(keys) == 0 && len(kls) == 0 {
		return fmt.Errorf("signing policy enforces verification but no trust entry matches %s; add a TrustedKey/TrustedKeyless with this repo's prefix", ref)
	}
	var attempts []string
	for _, k := range keys {
		if err := Verify(ctx, ref, VerifyOptions{TrustedKey: &k}); err == nil {
			return nil
		} else {
			attempts = append(attempts, fmt.Sprintf("trustedKey %s: %v", describeKey(k), err))
		}
	}
	for _, k := range kls {
		if err := Verify(ctx, ref, VerifyOptions{TrustedKeyless: &k}); err == nil {
			return nil
		} else {
			attempts = append(attempts, fmt.Sprintf("trustedKeyless %s@%s: %v", k.Identity, k.Issuer, err))
		}
	}
	return fmt.Errorf("no trust entry verified %s:\n  %s", ref, strings.Join(attempts, "\n  "))
}

// LoadPolicy reads the policy file. Returns (nil, nil) if the file is
// absent. The path is taken from EnvPolicyPath if set, else
// DefaultPolicyPath (tilde-expanded).
func LoadPolicy() (*api.SigningPolicy, error) {
	path := os.Getenv(EnvPolicyPath)
	if path == "" {
		expanded, err := expandTilde(DefaultPolicyPath)
		if err != nil {
			return nil, err
		}
		path = expanded
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return api.ParseSigningPolicy(data)
}

// matchEntries returns the subset of trust entries whose Repos prefix-list
// matches ref (or is empty). ref's oci:// prefix is stripped before
// matching.
func matchEntries(p *api.SigningPolicy, ref string) ([]api.TrustedKey, []api.TrustedKeyless) {
	bare := strings.TrimPrefix(ref, "oci://")
	bare = stripTagAndDigest(bare)
	var keys []api.TrustedKey
	for _, k := range p.Spec.TrustedKeys {
		if repoMatches(k.Repos, bare) {
			keys = append(keys, k)
		}
	}
	var kls []api.TrustedKeyless
	for _, k := range p.Spec.TrustedKeyless {
		if repoMatches(k.Repos, bare) {
			kls = append(kls, k)
		}
	}
	return keys, kls
}

func repoMatches(repos []string, bareRef string) bool {
	if len(repos) == 0 {
		return true
	}
	for _, r := range repos {
		r = strings.TrimPrefix(r, "oci://")
		if strings.HasPrefix(bareRef, r) {
			return true
		}
	}
	return false
}

// stripTagAndDigest removes both :tag and @digest suffixes from a bare
// (oci://-less) ref so prefix matching operates on the repo only.
func stripTagAndDigest(ref string) string {
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	if colon := strings.LastIndex(ref, ":"); colon >= 0 && !strings.Contains(ref[colon:], "/") {
		ref = ref[:colon]
	}
	return ref
}

func refForCosign(ref string) string {
	return strings.TrimPrefix(ref, "oci://")
}

func cosignBin() string {
	if b := os.Getenv(EnvCosignBinary); b != "" {
		return b
	}
	return "cosign"
}

func describeKey(k api.TrustedKey) string {
	if k.Description != "" {
		return k.Description
	}
	return k.PublicKey
}

func expandTilde(p string) (string, error) {
	if !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, p[2:]), nil
}

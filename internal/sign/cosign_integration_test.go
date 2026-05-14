package sign_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ipkg "github.com/confighub/installer/internal/pkg"
	"github.com/confighub/installer/internal/sign"
	"github.com/confighub/installer/pkg/api"
)

// TestCosignSignAndVerify exercises the real cosign binary against a local
// registry started by the test. Gated on:
//
//   - cosign binary on PATH
//   - docker daemon on PATH and reachable
//
// Strategy:
//   1. Spin up registry:2 on a free-ish port (5556 — distinct from the
//      manual end-to-end registry on 5555).
//   2. Push a minimal installer artifact via ipkg.Push.
//   3. Generate a cosign key pair with empty password.
//   4. Sign the artifact (keyed).
//   5. Verify with the matching key — must succeed.
//   6. Verify with a different key — must fail.
//   7. EnforceVerification with a policy that lists the matching key —
//      must succeed.
//   8. EnforceVerification with a policy that lists only a different key —
//      must fail.
func TestCosignSignAndVerify(t *testing.T) {
	if _, err := exec.LookPath("cosign"); err != nil {
		t.Skip("cosign not on PATH")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not reachable")
	}

	const (
		registryName     = "installer-cosign-test-registry"
		registryAddr     = "localhost:5556"
		artifactRepoPath = "test/sample"
		artifactTag      = "0.1.0"
	)
	startRegistry(t, registryName, "5556")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", registryName).Run() })

	// 2. Push a minimal package.
	pkgRoot := writeMinimalPackage(t)
	ctx := context.Background()
	ref := "oci://" + registryAddr + "/" + artifactRepoPath + ":" + artifactTag
	if _, err := ipkg.Push(ctx, pkgRoot, ref); err != nil {
		t.Fatalf("Push: %v", err)
	}

	// 3. Generate a key pair (cosign).
	keyDir := t.TempDir()
	t.Setenv("COSIGN_PASSWORD", "")
	runCosign(t, keyDir, "generate-key-pair")
	priv := filepath.Join(keyDir, "cosign.key")
	pub := filepath.Join(keyDir, "cosign.pub")
	otherDir := t.TempDir()
	runCosignDir(t, otherDir, []string{"generate-key-pair"})
	otherPub := filepath.Join(otherDir, "cosign.pub")

	// 4. Sign with the local registry's plain-HTTP option.
	t.Setenv("COSIGN_ALLOW_INSECURE_REGISTRY", "true")
	if err := sign.Sign(ctx, ref, sign.SignOptions{Key: priv, Yes: true}); err != nil {
		t.Fatalf("Sign: %v", err)
	}

	// 5. Verify with the matching public key — must succeed.
	if err := sign.Verify(ctx, ref, sign.VerifyOptions{
		TrustedKey: &api.TrustedKey{PublicKey: pub},
	}); err != nil {
		t.Fatalf("Verify with matching key should succeed: %v", err)
	}

	// 6. Verify with the other key — must fail.
	if err := sign.Verify(ctx, ref, sign.VerifyOptions{
		TrustedKey: &api.TrustedKey{PublicKey: otherPub},
	}); err == nil {
		t.Fatalf("Verify with wrong key should fail")
	}

	// 7. EnforceVerification with a policy that lists the matching key —
	// must succeed.
	policyDir := t.TempDir()
	policyOK := filepath.Join(policyDir, "ok.yaml")
	mustWrite(t, policyOK, fmt.Sprintf(`apiVersion: installer.confighub.com/v1alpha1
kind: SigningPolicy
spec:
  enforce: true
  trustedKeys:
    - publicKey: %s
`, pub))
	t.Setenv(sign.EnvPolicyPath, policyOK)
	if err := sign.EnforceVerification(ctx, ref); err != nil {
		t.Fatalf("EnforceVerification(match) failed: %v", err)
	}

	// 8. EnforceVerification with a policy that lists only the wrong key
	// — must fail.
	policyBad := filepath.Join(policyDir, "bad.yaml")
	mustWrite(t, policyBad, fmt.Sprintf(`apiVersion: installer.confighub.com/v1alpha1
kind: SigningPolicy
spec:
  enforce: true
  trustedKeys:
    - publicKey: %s
`, otherPub))
	t.Setenv(sign.EnvPolicyPath, policyBad)
	err := sign.EnforceVerification(ctx, ref)
	if err == nil || !strings.Contains(err.Error(), "no trust entry verified") {
		t.Fatalf("EnforceVerification(wrong-key) should fail, got %v", err)
	}
}

// --- helpers --------------------------------------------------------------

func startRegistry(t *testing.T, name, hostPort string) {
	t.Helper()
	_ = exec.Command("docker", "rm", "-f", name).Run()
	if err := exec.Command("docker", "run", "-d", "--name", name, "-p", hostPort+":5000", "registry:2").Run(); err != nil {
		t.Skipf("cannot start registry container: %v", err)
	}
	// Wait for the registry to be ready.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://localhost:" + hostPort + "/v2/")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("registry did not come up on :%s", hostPort)
}

func runCosign(t *testing.T, workDir string, args ...string) {
	runCosignDir(t, workDir, args)
}

func runCosignDir(t *testing.T, workDir string, args []string) {
	t.Helper()
	cmd := exec.Command("cosign", args...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "COSIGN_PASSWORD=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cosign %v: %v\n%s", args, err, out)
	}
}

func writeMinimalPackage(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	must := func(p, c string) {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("installer.yaml", `apiVersion: installer.confighub.com/v1alpha1
kind: Package
metadata: {name: signtest, version: 0.1.0}
spec:
  bases:
    - {name: default, path: bases/default, default: true}
`)
	must("bases/default/kustomization.yaml", "resources:\n  - cm.yaml\n")
	must("bases/default/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n")
	return dir
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

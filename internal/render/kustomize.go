package render

import (
	"bytes"
	"fmt"
	"os/exec"
)

// runKustomize shells out to `kustomize build` and returns stdout. The exec
// flags are always passed; kustomize ignores them for kustomizations that
// don't reference exec plugins.
func runKustomize(dir string) ([]byte, error) {
	cmd := exec.Command("kustomize", "build", "--enable-exec", "--enable-alpha-plugins", dir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("kustomize build %s: %w\n%s", dir, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

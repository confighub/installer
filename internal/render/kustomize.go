package render

import (
	"bytes"
	"fmt"
	"os/exec"
)

// runKustomize shells out to `kustomize build <dir>` and returns stdout.
// The kustomize binary must be on PATH.
func runKustomize(dir string) ([]byte, error) {
	cmd := exec.Command("kustomize", "build", dir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("kustomize build %s: %w\n%s", dir, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package installer_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/confighub/installer/pkg/installer"
)

// A program sets up and renders a package with a function chain and an
// AppConfig ConfigMap in a temporary work-dir. It calls
// installer.RunTransformerIfRequested first thing in main, which this
// example's test binary does in TestMain.
func Example() {
	ctx := context.Background()
	pkg, err := collectorApp()
	if err != nil {
		fmt.Println(err)
		return
	}
	defer os.RemoveAll(pkg)
	work, err := os.MkdirTemp("", "installer-example-")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer os.RemoveAll(work)

	inst, err := installer.New(installer.Options{})
	if err != nil {
		fmt.Println(err)
		return
	}
	res, err := inst.Setup(ctx, installer.SetupOptions{
		WorkDir:   work,
		Pull:      pkg,
		Namespace: "demo",
		Inputs:    map[string]string{"app_name": "hello", "image": "nginx:1.27"},
		Render:    true,
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("base:", res.Selection.Spec.Base)
	fmt.Println("configHubURL fact:", res.Facts.Spec.Values["configHubURL"])
	for _, m := range res.Render.Manifests {
		fmt.Println("manifest:", filepath.Base(m))
	}
	fmt.Println("secrets:", len(res.Render.Secrets))

	// The chain's AppConfig/Env group set CONFIGHUB_URL in the env ConfigMap.
	cm, err := os.ReadFile(filepath.Join(work, "out", "manifests", "configmap-demo-collector-app-env.yaml"))
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("CONFIGHUB_URL set:", strings.Contains(string(cm), "CONFIGHUB_URL: https://hub.example.com"))
	// Output:
	// base: default
	// configHubURL fact: https://hub.example.com
	// manifest: clusterrolebinding-demo-collector-app-view.yaml
	// manifest: configmap-demo-collector-app-env.yaml
	// manifest: deployment-demo-hello.yaml
	// manifest: namespace-demo.yaml
	// manifest: serviceaccount-demo-collector-app.yaml
	// secrets: 1
	// CONFIGHUB_URL set: true
}

// collectorApp copies the collector-app example with a collector that needs
// no cub context.
func collectorApp() (string, error) {
	dir, err := os.MkdirTemp("", "collector-app-")
	if err != nil {
		return "", err
	}
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("..", "..", "examples", "collector-app"))); err != nil {
		return "", err
	}
	collector := "#!/bin/sh\nset -eu\n" +
		"printf 'APP_TOKEN=example\\n' > \"$INSTALLER_PACKAGE_DIR/bases/default/collector-app-secret.env.secret\"\n" +
		"printf 'configHubURL: https://hub.example.com\\nimage: %s\\n' \"$INSTALLER_INPUT_IMAGE\"\n"
	return dir, os.WriteFile(filepath.Join(dir, "collector.sh"), []byte(collector), 0o755)
}

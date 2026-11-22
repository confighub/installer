// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package transform

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
)

// EnvRunTransformer is set to "1" by the wrapper render writes for kustomize
// to execute, asking the program it re-executes to run the transform step
// instead of whatever it normally does.
const EnvRunTransformer = "INSTALLER_RUN_TRANSFORMER"

// EnvTransformerLog names a file the transform step appends its log to, its
// error included, instead of writing them to stderr. Render passes it through
// kustomize, which hands an exec function the stderr of the process running
// kustomize: a program rendering in its own process has the log written where
// it says. A standalone kustomize build in out/compose/ doesn't set it.
const EnvTransformerLog = "INSTALLER_TRANSFORMER_LOG"

// Requested reports whether this process was started to run the transform
// step.
func Requested() bool {
	return os.Getenv(EnvRunTransformer) == "1"
}

// Main runs the transform step as a process: a ResourceList on stdin, the
// result on stdout, and an error on stderr with exit status 1. It does not
// return.
func Main() {
	stderr := os.Stderr
	if path := os.Getenv(EnvTransformerLog); path != "" {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "installer transformer: open %s: %v\n", EnvTransformerLog, err)
			os.Exit(1)
		}
		// The functions log through slog's default logger, which writes
		// through the log package's.
		log.SetOutput(f)
		stderr = f
	}
	code := main(os.Stdin, os.Stdout, stderr)
	if stderr != os.Stderr {
		stderr.Close()
	}
	os.Exit(code)
}

func main(stdin io.Reader, stdout, stderr io.Writer) int {
	in, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "installer transformer: read stdin: %v\n", err)
		return 1
	}
	out, err := Run(context.Background(), in)
	if err != nil {
		fmt.Fprintf(stderr, "installer transformer: %v\n", err)
		return 1
	}
	if _, err := stdout.Write(out); err != nil {
		fmt.Fprintf(stderr, "installer transformer: write stdout: %v\n", err)
		return 1
	}
	return 0
}

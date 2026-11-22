// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package transform

import (
	"context"
	"fmt"
	"io"
	"os"
)

// EnvRunTransformer is set to "1" by the wrapper render writes for kustomize
// to execute, asking the program it re-executes to run the transform step
// instead of whatever it normally does.
const EnvRunTransformer = "INSTALLER_RUN_TRANSFORMER"

// Requested reports whether this process was started to run the transform
// step.
func Requested() bool {
	return os.Getenv(EnvRunTransformer) == "1"
}

// Main runs the transform step as a process: a ResourceList on stdin, the
// result on stdout, and an error on stderr with exit status 1. It does not
// return.
func Main() {
	os.Exit(main(os.Stdin, os.Stdout, os.Stderr))
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

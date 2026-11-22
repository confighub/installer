// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package cli

// The lifecycle tests run the installer's commands as a user would, through
// the cobra tree, against a fake ConfigHub (internal/fakehub) and an
// in-memory OCI registry (internal/testregistry). Each compares what the
// command printed, what it left in the work-dir, and what it sent to
// ConfigHub with a golden file in testdata/lifecycle, so a change to any of
// them shows up as a diff. Run with -update to rewrite the golden files after
// an intended change, and review the diff.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/confighub/sdk/core/cubapi"

	"github.com/confighub/installer/internal/fakehub"
	"github.com/confighub/installer/internal/hubclient"
)

var update = flag.Bool("update", false, "rewrite the lifecycle golden files")

// hub is the fake ConfigHub every test's client talks to, through CUB_SERVER.
var hub *fakehub.Server

func TestMain(m *testing.M) {
	// Render runs the function chain as a kustomize exec plugin that executes
	// this binary with the transformer subcommand.
	if len(os.Args) > 1 && os.Args[1] == "transformer" {
		root := NewRoot()
		root.SetArgs(os.Args[1:])
		if err := root.Execute(); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	home, err := os.MkdirTemp("", "installer-cli-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(home)
	hub = fakehub.New()
	defer hub.Close()

	// Nothing a test runs reads the user's own cub configuration, signing
	// policy or installer state, or reaches a real server.
	for k, v := range map[string]string{
		"HOME":       home,
		"CUB_SERVER": hub.URL,
		"CUB_TOKEN":  "test-token",
		"CUB_CONFIG": filepath.Join(home, ".confighub"),
	} {
		os.Setenv(k, v)
	}
	for _, k := range []string{"CUB_PLUGIN", "CUB_CONTEXT", "CUB_SPACE", "ORAS_OCI_INSECURE",
		"INSTALLER_SIGNING_POLICY", "INSTALLER_COSIGN_BIN"} {
		os.Unsetenv(k)
	}
	// Follow-up hints name os.Args[0]; keep them the same on every machine.
	os.Args[0] = "installer"
	return m.Run()
}

// result is what one command did.
type result struct {
	stdout, stderr string
	err            error
}

// runInstaller runs the installer with args, with no terminal on stdin, and
// returns what it printed. The process's stdout and stderr are redirected for
// the duration, since commands print with fmt.Printf.
func runInstaller(t *testing.T, args ...string) result {
	t.Helper()
	// A fresh client per command, as each run of the binary has.
	hubclient.Default = cubapi.MemoizedClient{UserAgent: "installer"}

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	outDone := make(chan struct{})
	errDone := make(chan struct{})
	go func() { _, _ = io.Copy(&stdout, outR); close(outDone) }()
	go func() { _, _ = io.Copy(&stderr, errR); close(errDone) }()

	savedIn, savedOut, savedErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = devNull, outW, errW
	root := NewRoot()
	root.SetOut(outW)
	root.SetErr(errW)
	root.SetArgs(args)
	runErr := root.Execute()
	os.Stdin, os.Stdout, os.Stderr = savedIn, savedOut, savedErr

	outW.Close()
	errW.Close()
	<-outDone
	<-errDone
	return result{stdout: stdout.String(), stderr: stderr.String(), err: runErr}
}

// mustRun runs the installer and fails the test if the command fails.
func mustRun(t *testing.T, args ...string) result {
	t.Helper()
	r := runInstaller(t, args...)
	if r.err != nil {
		t.Fatalf("installer %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), r.err, r.stdout, r.stderr)
	}
	return r
}

// mustFail runs the installer and fails the test unless the command fails.
func mustFail(t *testing.T, args ...string) result {
	t.Helper()
	r := runInstaller(t, args...)
	if r.err == nil {
		t.Fatalf("installer %s: want an error\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), r.stdout, r.stderr)
	}
	return r
}

// normalizer replaces the paths and addresses that differ between runs with
// stable names.
type normalizer struct {
	pairs   [][2]string
	regexps []regexpReplacement
}

type regexpReplacement struct {
	re   *regexp.Regexp
	repl string
}

// addRegexp replaces what re matches with repl, for values that differ between
// runs but can't be named in advance, such as a digest over content that holds
// the test registry's address.
func (n *normalizer) addRegexp(re, repl string) {
	n.regexps = append(n.regexps, regexpReplacement{regexp.MustCompile(re), repl})
}

func (n *normalizer) add(from, to string) {
	if from == "" {
		return
	}
	n.pairs = append(n.pairs, [2]string{from, to})
	// macOS's temporary directories are reached through a symlink, and some
	// paths are printed resolved and some not.
	if resolved, err := filepath.EvalSymlinks(from); err == nil && resolved != from {
		n.pairs = append(n.pairs, [2]string{resolved, to})
	}
}

// logTimestamp is the date and time the standard logger starts a line with.
var logTimestamp = regexp.MustCompile(`(?m)^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} `)

func (n *normalizer) apply(s string) string {
	s = logTimestamp.ReplaceAllString(s, "")
	for _, r := range n.regexps {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	// Longest first, so a path is replaced before a prefix of it.
	pairs := append([][2]string(nil), n.pairs...)
	sort.SliceStable(pairs, func(i, j int) bool { return len(pairs[i][0]) > len(pairs[j][0]) })
	for _, p := range pairs {
		s = strings.ReplaceAll(s, p[0], p[1])
	}
	return s
}

// env is one test's surroundings: its directories and the names they print as.
type env struct {
	t    *testing.T
	norm *normalizer
	repo string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	hub.Reset()
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, norm: &normalizer{}, repo: repo}
	e.norm.add(repo, "$REPO")
	e.norm.add(hub.URL, "$HUB")
	if exe, err := os.Executable(); err == nil {
		e.norm.add(exe, "$EXE")
	}
	return e
}

// dir returns a new empty directory, printed as name.
func (e *env) dir(name string) string {
	e.t.Helper()
	d, err := filepath.EvalSymlinks(e.t.TempDir())
	if err != nil {
		e.t.Fatal(err)
	}
	e.norm.add(d, "$"+name)
	return d
}

// copyExample copies an example package into a new directory, printed as name.
func (e *env) copyExample(example, name string) string {
	e.t.Helper()
	dst := e.dir(name)
	src := filepath.Join(e.repo, "examples", example)
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return dst
}

// edit rewrites a file in place.
func (e *env) edit(path string, fn func(string) string) {
	e.t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(fn(string(data))), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) write(path, content string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// transcript accumulates what a test observed, for one golden file.
type transcript struct {
	e   *env
	buf strings.Builder
}

func (e *env) transcript() *transcript { return &transcript{e: e} }

func (tr *transcript) section(title string) {
	fmt.Fprintf(&tr.buf, "\n===== %s =====\n", title)
}

// run runs the installer, records the command and what it printed, and
// returns the result.
func (tr *transcript) run(args ...string) result {
	tr.e.t.Helper()
	r := runInstaller(tr.e.t, args...)
	tr.record(args, r)
	return r
}

// ok runs the installer, which must succeed, and records it.
func (tr *transcript) ok(args ...string) result {
	tr.e.t.Helper()
	r := runInstaller(tr.e.t, args...)
	tr.record(args, r)
	if r.err != nil {
		tr.e.t.Fatalf("installer %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), r.err, r.stdout, r.stderr)
	}
	return r
}

// fail runs the installer, which must fail, and records it.
func (tr *transcript) fail(args ...string) result {
	tr.e.t.Helper()
	r := runInstaller(tr.e.t, args...)
	tr.record(args, r)
	if r.err == nil {
		tr.e.t.Fatalf("installer %s: want an error\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), r.stdout, r.stderr)
	}
	return r
}

func (tr *transcript) record(args []string, r result) {
	n := tr.e.norm
	tr.section("installer " + n.apply(strings.Join(args, " ")))
	fmt.Fprintf(&tr.buf, "--- stdout\n%s", n.apply(r.stdout))
	if r.stderr != "" {
		fmt.Fprintf(&tr.buf, "--- stderr\n%s", n.apply(r.stderr))
	}
	if r.err != nil {
		fmt.Fprintf(&tr.buf, "--- error\n%s\n", n.apply(r.err.Error()))
	}
}

// tree records every file under dir, with its content, except the paths skip
// matches, which are listed without it.
func (tr *transcript) tree(title, dir string, contentless ...string) {
	tr.e.t.Helper()
	tr.section("tree " + title)
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		tr.e.t.Fatal(err)
	}
	sort.Strings(paths)
	for _, rel := range paths {
		listed := false
		for _, prefix := range contentless {
			if strings.HasPrefix(rel, prefix) {
				listed = true
			}
		}
		if listed {
			fmt.Fprintf(&tr.buf, "--- %s (content not compared)\n", rel)
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			tr.e.t.Fatal(err)
		}
		fmt.Fprintf(&tr.buf, "--- %s\n%s", rel, tr.e.norm.apply(string(data)))
		if len(data) > 0 && data[len(data)-1] != '\n' {
			tr.buf.WriteString("\n")
		}
	}
}

// requests records the requests ConfigHub received since the last call, and
// forgets them.
func (tr *transcript) requests() {
	tr.section("ConfigHub requests")
	for _, r := range hub.Requests() {
		tr.buf.WriteString(tr.formatRequest(r))
	}
	hub.ClearRequests()
}

func (tr *transcript) formatRequest(r fakehub.Request) string {
	n := tr.e.norm
	var b strings.Builder
	keys := make([]string, 0, len(r.Query))
	for k := range r.Query {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var q []string
	for _, k := range keys {
		q = append(q, k+"="+r.Query[k])
	}
	fmt.Fprintf(&b, "%s %s", r.Method, r.Path)
	if len(q) > 0 {
		fmt.Fprintf(&b, " ? %s", strings.Join(q, " & "))
	}
	b.WriteString("\n")
	if len(r.Body) == 0 {
		return b.String()
	}
	if r.Path == "/upload" {
		b.WriteString(tr.formatUploadBody(r.Body))
		return b.String()
	}
	fmt.Fprintf(&b, "  body: %s\n", n.apply(string(r.Body)))
	return b.String()
}

// formatUploadBody prints an upload request with each manifest's content
// reduced to a digest, which the tree section already shows in full, and the
// installer record in full, since it is built only for the upload.
func (tr *transcript) formatUploadBody(body []byte) string {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return "  body: " + string(body) + "\n"
	}
	var record string
	if files, ok := req["Files"].([]any); ok {
		for _, f := range files {
			file := f.(map[string]any)
			content, _ := file["Content"].(string)
			content = tr.e.norm.apply(content)
			if file["Path"] == "installer-record.yaml" {
				record = content
				file["Content"] = "(below)"
				continue
			}
			sum := sha256.Sum256([]byte(content))
			file["Content"] = "sha256:" + hex.EncodeToString(sum[:8])
		}
	}
	out, _ := json.MarshalIndent(req, "  ", "  ")
	s := "  " + tr.e.norm.apply(string(out)) + "\n"
	if record != "" {
		s += "  installer-record.yaml:\n"
		for _, line := range strings.Split(strings.TrimRight(record, "\n"), "\n") {
			s += "    " + line + "\n"
		}
	}
	return s
}

func (tr *transcript) note(format string, args ...any) {
	fmt.Fprintf(&tr.buf, format+"\n", args...)
}

// check compares the transcript with testdata/lifecycle/<name>.golden.
func (tr *transcript) check(name string) {
	tr.e.t.Helper()
	path := filepath.Join("testdata", "lifecycle", name+".golden")
	got := strings.TrimLeft(tr.buf.String(), "\n")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			tr.e.t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			tr.e.t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		tr.e.t.Fatalf("read golden file (run with -update to create it): %v", err)
	}
	if got != string(want) {
		tr.e.t.Errorf("transcript differs from %s (run with -update to accept):\n%s", path, lineDiff(string(want), got))
	}
}

// lineDiff is a minimal line diff: the first differing lines, with context.
func lineDiff(want, got string) string {
	w := strings.Split(want, "\n")
	g := strings.Split(got, "\n")
	i := 0
	for i < len(w) && i < len(g) && w[i] == g[i] {
		i++
	}
	start := max(0, i-5)
	var b strings.Builder
	fmt.Fprintf(&b, "first difference at line %d\n", i+1)
	for j := start; j < i; j++ {
		fmt.Fprintf(&b, "  %s\n", w[j])
	}
	for j := i; j < min(len(w), i+15); j++ {
		fmt.Fprintf(&b, "- %s\n", w[j])
	}
	for j := i; j < min(len(g), i+15); j++ {
		fmt.Fprintf(&b, "+ %s\n", g[j])
	}
	return b.String()
}

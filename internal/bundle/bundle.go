// Package bundle produces deterministic, distribution-ready installer package
// tarballs. The output is the input to `installer push` and is what an OCI
// registry stores as a single layer of a native installer artifact.
//
// Determinism: same source tree → byte-identical .tgz, regardless of mtimes,
// uids, host platform, or walk order. This is what makes signing and
// digest-pinning meaningful.
package bundle

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	gitignore "github.com/monochromegane/go-gitignore"
)

// IgnoreFile is the name of the per-package ignore file (gitignore syntax).
const IgnoreFile = ".installerignore"

// Result is the outcome of a Bundle call.
type Result struct {
	// Digest is the sha256 of the gzipped tar, hex-encoded (no "sha256:" prefix).
	Digest string
	// Files is the list of paths included, in tar order.
	Files []string
	// Size is the size of the .tgz in bytes.
	Size int64
}

// Bundle writes a deterministic .tgz of srcDir to dstFile.
//
// Refuses to include:
//   - any file matching *.env.secret anywhere in the tree
//   - anything under <srcDir>/out/
//   - anything matched by <srcDir>/.installerignore (gitignore syntax)
//   - .git/, .DS_Store (default ignores; not configurable)
//
// dstFile's parent directory is created if needed; the file is replaced if it
// exists.
func Bundle(srcDir, dstFile string) (*Result, error) {
	if srcDir == "" {
		return nil, errors.New("srcDir required")
	}
	if dstFile == "" {
		return nil, errors.New("dstFile required")
	}
	abs, err := filepath.Abs(srcDir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", abs)
	}
	if _, err := os.Stat(filepath.Join(abs, "installer.yaml")); err != nil {
		return nil, fmt.Errorf("no installer.yaml in %s", abs)
	}

	ig, err := loadIgnore(abs)
	if err != nil {
		return nil, err
	}

	files, err := walkAndCollect(abs, ig)
	if err != nil {
		return nil, err
	}

	return writeTarGz(files, dstFile)
}

// fileEntry is a regular file selected for inclusion.
type fileEntry struct {
	rel  string // forward-slash, relative to src
	abs  string
	size int64
	exec bool // include +x bit in mode
}

func walkAndCollect(src string, ig gitignore.IgnoreMatcher) ([]fileEntry, error) {
	var out []fileEntry
	err := filepath.Walk(src, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		relSlash := filepath.ToSlash(rel)

		// Hard refusal: *.env.secret must never ship.
		if strings.HasSuffix(relSlash, ".env.secret") {
			return fmt.Errorf("refusing to bundle %s: *.env.secret files must never be published", relSlash)
		}

		// Hard exclusion: out/ tree is render output, not source.
		if relSlash == "out" || strings.HasPrefix(relSlash, "out/") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Default ignores.
		if relSlash == ".git" || strings.HasPrefix(relSlash, ".git/") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Base(relSlash) == ".DS_Store" {
			return nil
		}

		// User-supplied .installerignore (gitignore syntax).
		if ig != nil && ig.Match(path, info.IsDir()) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Symlinks are rejected — the bundler must produce a self-contained
		// tarball that pulls back identically regardless of host filesystem.
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink not supported in package: %s", relSlash)
		}

		// Directories themselves are not written as tar entries; empty dirs
		// are dropped because they're not load-bearing.
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular file not supported: %s", relSlash)
		}

		out = append(out, fileEntry{
			rel:  relSlash,
			abs:  path,
			size: info.Size(),
			exec: info.Mode()&0o111 != 0,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}

func writeTarGz(files []fileEntry, dst string) (*Result, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(dst)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	hasher := sha256.New()
	counter := &countingWriter{w: io.MultiWriter(f, hasher)}

	gw, err := gzip.NewWriterLevel(counter, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	// Zero gzip header fields so the same input yields the same bytes.
	gw.Name = ""
	gw.Comment = ""
	gw.ModTime = time.Time{}
	gw.OS = 255 // unknown — avoids platform-dependent byte

	tw := tar.NewWriter(gw)
	for _, fe := range files {
		mode := int64(0o644)
		if fe.exec {
			mode = 0o755
		}
		hdr := &tar.Header{
			Name:     fe.rel,
			Mode:     mode,
			Size:     fe.size,
			Typeflag: tar.TypeReg,
			ModTime:  time.Time{},
			Uid:      0,
			Gid:      0,
			Uname:    "",
			Gname:    "",
			Format:   tar.FormatPAX,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, fmt.Errorf("tar header %s: %w", fe.rel, err)
		}
		in, err := os.Open(fe.abs)
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(tw, in); err != nil {
			in.Close()
			return nil, fmt.Errorf("tar copy %s: %w", fe.rel, err)
		}
		in.Close()
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}

	rels := make([]string, len(files))
	for i, fe := range files {
		rels[i] = fe.rel
	}
	return &Result{
		Digest: hex.EncodeToString(hasher.Sum(nil)),
		Files:  rels,
		Size:   counter.n,
	}, nil
}

// loadIgnore returns nil if no .installerignore exists.
func loadIgnore(srcDir string) (gitignore.IgnoreMatcher, error) {
	path := filepath.Join(srcDir, IgnoreFile)
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory; expected a file", path)
	}
	return gitignore.NewGitIgnore(path, srcDir)
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

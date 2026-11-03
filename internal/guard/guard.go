// Package guard holds chatstrata's static gates: the package-boundary table,
// the file-size ratchet, and the escape-hatch allowlist. `go test ./...` is
// the guard run -- there is no separate binary, and nothing runs only in CI --
// so a violation fails locally before it reaches review.
//
// Each gate is a pure function of a tree and its config: CheckBoundaries,
// CheckRatchets and CheckAllow. Every check returns one problem string per
// violation and nil when the tree matches its config. The configs are
// embedded, so a gate always reads the files it ships with, not a copy left
// beside a test binary.
//
// Widen a gate by editing the JSON beside it. A new import edge, a new
// baselined file or a new escape hatch is then a one-line diff that shows in
// review.
package guard

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed imports.json
var importsJSON []byte

//go:embed ratchets.json
var ratchetsJSON []byte

//go:embed allow.json
var allowJSON []byte

// roots are the only trees the gates read. Untracked agent trees (.claude/,
// python-final/, site/) stay outside every walk.
var roots = []string{"cmd", "internal"}

// repoRoot walks up from the working directory to the directory holding
// go.mod. `go test` runs in the package directory, so every gate anchors
// there rather than assuming the shell's directory.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod above the working directory")
		}
		dir = parent
	}
}

// goFiles lists repo-relative .go paths under roots, sorted. tests selects
// _test.go files only; the boundary table and the ratchet read production
// code, so an integration test may cross packages without widening the graph.
func goFiles(root string, tests bool) ([]string, error) {
	var paths []string
	for _, r := range roots {
		base := filepath.Join(root, r)
		if _, err := os.Stat(base); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				name := d.Name()
				if p != base && (strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor") {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") != tests {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			paths = append(paths, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", base, err)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// modulePath reads the module path from go.mod.
func modulePath(root string) (string, error) {
	text, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(text), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(rest), nil
		}
	}
	return "", errors.New("go.mod has no module line")
}

// match reports whether a package name matches a gate pattern. `*` is the
// only wildcard and it spans path separators, so `internal/*` covers every
// internal package and `internal/sources/*` every adapter.
func match(pattern, name string) bool {
	if pattern == name {
		return true
	}
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return false
	}
	if !strings.HasPrefix(name, parts[0]) {
		return false
	}
	name = strings.TrimPrefix(name, parts[0])
	for i, part := range parts[1:] {
		if i == len(parts[1:])-1 {
			return strings.HasSuffix(name, part)
		}
		idx := strings.Index(name, part)
		if idx < 0 {
			return false
		}
		name = name[idx+len(part):]
	}
	return true
}

// matchAny reports whether name matches one of patterns.
func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if match(p, name) {
			return true
		}
	}
	return false
}

// decodeConfig unmarshals a gate config and rejects unknown keys, so a typo
// in the JSON fails loudly instead of silently widening or narrowing a gate.
func decodeConfig(file string, data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	return nil
}

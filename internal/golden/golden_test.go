package golden

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/brandonbosch/chatstrata/internal/cli"
)

const specDir = "../../spec/golden"

type step struct {
	Incremental   bool           `json:"incremental"`
	TruncateLines map[string]int `json:"truncate_lines"`
}

type goldenCase struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Input  string `json:"input"`
	Path   string `json:"path"`
	Steps  []step `json:"steps"`
}

// ported lists the sources the Go implementation has so far; cases for the
// others are skipped until their adapter lands.
var ported = map[string]bool{"claude_code": true}

func TestGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(specDir, "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []goldenCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			if !ported[c.Source] {
				t.Skipf("source %s not ported yet", c.Source)
			}
			actual := runCase(t, c)
			expectedRaw, err := os.ReadFile(filepath.Join(specDir, "expected", c.Name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			compare(t, normalize(t, expectedRaw), normalize(t, mustMarshal(t, actual)))
		})
	}
}

func runCase(t *testing.T, c goldenCase) any {
	t.Helper()
	work := filepath.Join(t.TempDir(), "input")
	if err := os.CopyFS(work, os.DirFS(filepath.Join(specDir, "inputs", c.Input))); err != nil {
		t.Fatal(err)
	}
	originals := map[string][]byte{}
	for _, s := range c.Steps {
		for rel := range s.TruncateLines {
			b, err := os.ReadFile(filepath.Join(work, rel))
			if err != nil {
				t.Fatal(err)
			}
			originals[rel] = b
		}
	}

	db := filepath.Join(t.TempDir(), "archive.duckdb")
	for tick, s := range c.Steps {
		applyTruncation(t, work, originals, s, tick)
		args := []string{"ingest", c.Source, "--path", filepath.Join(work, c.Path), "--db", db}
		if s.Incremental {
			args = append(args, "--incremental")
		}
		var stdout, stderr bytes.Buffer
		if code := cli.Run(context.Background(), args, &stdout, &stderr); code != 0 {
			t.Fatalf("step %d: exit %d\n%s%s", tick, code, stdout.String(), stderr.String())
		}
		if stderr.Len() > 0 {
			t.Errorf("step %d wrote to stderr:\n%s", tick, stderr.String())
		}
	}

	dump, err := Dump(context.Background(), db, [][2]string{{work, "$INPUT"}})
	if err != nil {
		t.Fatal(err)
	}
	return dump
}

// applyTruncation sets each listed file to its first N lines for this step and
// restores the others, bumping the mtime of anything that changed.
func applyTruncation(t *testing.T, work string, originals map[string][]byte, s step, tick int) {
	t.Helper()
	for rel, original := range originals {
		content := original
		if n, ok := s.TruncateLines[rel]; ok {
			lines := bytes.SplitAfter(original, []byte("\n"))
			content = bytes.Join(lines[:min(n, len(lines))], nil)
		}
		target := filepath.Join(work, rel)
		current, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(current, content) {
			continue
		}
		if err := os.WriteFile(target, content, 0o644); err != nil {
			t.Fatal(err)
		}
		mtime := time.Unix(1_800_000_000+int64(tick), 0)
		if err := os.Chtimes(target, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// normalize decodes JSON into generic values, so numbers compare by value
// rather than by how each language prints them.
func normalize(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func compare(t *testing.T, expected, actual any) {
	t.Helper()
	if reflect.DeepEqual(expected, actual) {
		return
	}
	exp, act := pretty(t, expected), pretty(t, actual)
	expLines, actLines := strings.Split(exp, "\n"), strings.Split(act, "\n")
	for i := 0; i < max(len(expLines), len(actLines)); i++ {
		var e, a string
		if i < len(expLines) {
			e = expLines[i]
		}
		if i < len(actLines) {
			a = actLines[i]
		}
		if e != a {
			lo, hi := max(0, i-6), min(len(actLines), i+6)
			t.Fatalf("output differs from expected at line %d:\n  expected: %s\n  actual:   %s\n\nactual around it:\n%s",
				i+1, e, a, strings.Join(actLines[lo:hi], "\n"))
		}
	}
	t.Fatal("output differs from expected")
}

func pretty(t *testing.T, v any) string {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

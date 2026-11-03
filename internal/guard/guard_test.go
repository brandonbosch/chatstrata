package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRepoMatchesGates is the guard run: `go test ./...` fails when the
// checked-in tree stops matching the configs the gates ship with.
func TestRepoMatchesGates(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name  string
		check func(string, []byte) []string
		cfg   []byte
	}{
		{"boundaries", CheckBoundaries, importsJSON},
		{"ratchets", CheckRatchets, ratchetsJSON},
		{"allow", CheckAllow, allowJSON},
	}
	for _, c := range checks {
		if problems := c.check(root, c.cfg); len(problems) > 0 {
			t.Errorf("%s gate:\n%s", c.name, strings.Join(problems, "\n"))
		}
	}
}

// writeTree creates a fake module in a temp directory and returns its root.
// Every gate reads a tree, so each rejection is proved without touching the
// real repository.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	if _, ok := files["go.mod"]; !ok {
		files["go.mod"] = "module example.com/m\n\ngo 1.25.0\n"
	}
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func wantProblems(t *testing.T, got []string, wants ...string) {
	t.Helper()
	if len(got) == 0 {
		t.Fatalf("expected problems containing %q, got none", wants)
	}
	for _, want := range wants {
		found := false
		for _, problem := range got {
			if strings.Contains(problem, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no problem contains %q; got:\n%s", want, strings.Join(got, "\n"))
		}
	}
}

func wantClean(t *testing.T, got []string) {
	t.Helper()
	if len(got) > 0 {
		t.Fatalf("expected no problems, got:\n%s", strings.Join(got, "\n"))
	}
}

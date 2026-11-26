package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brandonbosch/chatstrata/v2/internal/model"
	"github.com/brandonbosch/chatstrata/v2/internal/obslog"
	"github.com/brandonbosch/chatstrata/v2/internal/redact"
	"github.com/brandonbosch/chatstrata/v2/internal/store"
)

func TestObserveRedactsJSONLOnCollect(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "session.jsonl")
	content := `{"text":"alice@example.com"}` + "\n" + `{"cmd":"home::make_private(\nnixos.local)"}` + "\n"
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	policy := redact.DefaultPolicy()
	policy.DisableLocalUsername = true
	engine, err := redact.NewEngine(policy)
	if err != nil {
		t.Fatal(err)
	}
	src := appendSource{}
	key := obslog.Key{Source: src.Name(), Locator: "c1"}
	h := model.Handle{SourceNativeID: "c1", Path: file, Project: "/home/alice/proj"}

	c := observe(src, h, key, store.CollectorState{}, false, false, engine, true)
	if c.err != nil {
		t.Fatal(c.err)
	}
	if c.obs == nil || !c.obs.Redacted {
		t.Fatalf("observation = %+v", c.obs)
	}
	if len(c.obs.Content) != len(content) {
		t.Fatalf("length = %d, want %d", len(c.obs.Content), len(content))
	}
	got := string(c.obs.Content)
	if strings.Contains(got, "alice@example.com") || strings.Contains(got, "nixos.local") {
		t.Fatalf("plaintext logged: %q", got)
	}
	if strings.Contains(got, `\[`) {
		t.Fatalf("redaction split a JSON escape: %q", got)
	}
	if !strings.Contains(got, "[REDACTED:EMAIL]") {
		t.Fatalf("missing email marker: %q", got)
	}
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatalf("line is not valid JSON: %q", line)
		}
	}
	if strings.Contains(c.obs.ProjectHint, "alice") {
		t.Fatalf("project metadata not redacted: %q", c.obs.ProjectHint)
	}
}

func TestLoadPolicyCollect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "redaction.toml")
	if err := os.WriteFile(path, []byte("[redaction]\ncollect = true\nmax_input_bytes = 2048\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := redact.LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Collect || policy.MaxInputBytes != 2048 {
		t.Fatalf("policy = %+v", policy)
	}
}

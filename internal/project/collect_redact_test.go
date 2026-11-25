package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brandonbosch/chatstrata/v2/internal/model"
	"github.com/brandonbosch/chatstrata/v2/internal/obslog"
	"github.com/brandonbosch/chatstrata/v2/internal/redact"
	"github.com/brandonbosch/chatstrata/v2/internal/store"
)

type appendSource struct{}

func (appendSource) Name() string        { return "test" }
func (appendSource) DisplayName() string { return "Test" }
func (appendSource) Version() string     { return "1" }
func (appendSource) Discover(string) ([]model.Handle, error) {
	return nil, nil
}
func (appendSource) Parse(model.Handle, []byte) (*model.Conversation, error) {
	return &model.Conversation{}, nil
}
func (appendSource) AppendOnly() bool { return true }

func TestObserveReRedactsNewAppendWithoutChangingLengths(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "home", "alice", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	first := `{"text":"alice@example.com"}` + "\n"
	if err := os.WriteFile(file, []byte(first), 0o600); err != nil {
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

	start := observe(src, h, key, store.CollectorState{}, false, false, engine, true)
	if start.err != nil {
		t.Fatal(start.err)
	}
	if start.obs == nil || len(start.obs.Content) != len(first) {
		t.Fatalf("first observation = %+v", start.obs)
	}
	if strings.Contains(string(start.obs.Content), "alice@example.com") {
		t.Fatalf("plaintext logged: %q", start.obs.Content)
	}
	if strings.Contains(start.obs.Path, "alice") || strings.Contains(start.obs.ProjectHint, "alice") {
		t.Fatalf("path metadata logged: %q %q", start.obs.Path, start.obs.ProjectHint)
	}

	next := `{"text":"bob@example.com"}` + "\n"
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(next); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got := observe(src, h, key, start.state, true, true, engine, true)
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.obs == nil || got.obs.Kind != obslog.Append {
		t.Fatalf("second observation = %+v", got.obs)
	}
	if got.obs.Offset != start.state.Length {
		t.Fatalf("offset = %d, state length = %d", got.obs.Offset, start.state.Length)
	}
	if len(got.obs.Content) != len(next) {
		t.Fatalf("append length = %d, want %d", len(got.obs.Content), len(next))
	}
	if strings.Contains(string(got.obs.Content), "bob@example.com") {
		t.Fatalf("new plaintext logged: %q", got.obs.Content)
	}
}

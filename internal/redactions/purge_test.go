package redactions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brandonbosch/chatstrata/v2/internal/obslog"
	"github.com/brandonbosch/chatstrata/v2/internal/redact"
)

func testEngine(t *testing.T) *redact.Engine {
	t.Helper()
	p := redact.DefaultPolicy()
	p.DisableLocalUsername = true
	e, err := redact.NewEngine(p)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func appendObservation(t *testing.T, l *obslog.Log, content, path string) {
	t.Helper()
	_, err := l.Append([]*obslog.Observation{{
		Source:  "claude_code",
		Locator: "native-" + string(rune('a'+len(content)%26)),
		Kind:    obslog.Snapshot,
		Content: []byte(content),
		Path:    path,
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPurgeRewritesOwnSegments(t *testing.T) {
	ctx := context.Background()
	logPath := filepath.Join(t.TempDir(), "chatstrata-go.log")
	l, err := obslog.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	appendObservation(t, l, `{"text":"mail alice@example.com","password":"hunter2"}`, "/home/alice/session.jsonl")
	appendObservation(t, l, `{"text":"nothing sensitive"}`, "/home/alice/session.jsonl")
	if err := os.WriteFile(filepath.Join(logPath, "space.key"), []byte("secret-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	stats, err := Purge(ctx, logPath, PurgeOptions{Engine: testEngine(t)})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Redacted != 2 || stats.Segments != 2 || stats.Observations != 2 {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.OldLog == "" {
		t.Fatal("old log was not archived")
	}
	if _, err := os.Stat(stats.OldLog); err != nil {
		t.Fatalf("old log: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(logPath, "space.key")); err != nil || string(b) != "secret-key\n" {
		t.Fatalf("space.key not preserved: %q %v", b, err)
	}

	rewritten, err := obslog.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var contents []string
	var paths []string
	var redacted []bool
	segs, err := rewritten.Segments(rewritten.Identity.Device)
	if err != nil {
		t.Fatal(err)
	}
	for _, seg := range segs {
		if err := rewritten.ReadSegment(seg.Path, true, func(o *obslog.Observation, _ obslog.Location) error {
			contents = append(contents, string(o.Content))
			paths = append(paths, o.Path)
			redacted = append(redacted, o.Redacted)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(contents) != 2 {
		t.Fatalf("records = %d", len(contents))
	}
	if strings.Contains(contents[0], "alice@example.com") || strings.Contains(contents[0], "hunter2") {
		t.Fatalf("plaintext survived: %q", contents[0])
	}
	if !strings.Contains(contents[0], "[REDACTED:EMAIL]") || !strings.Contains(contents[0], "*******") {
		t.Fatalf("placeholders missing: %q", contents[0])
	}
	if !json.Valid([]byte(contents[0])) {
		t.Fatalf("rewritten content is not valid JSON: %q", contents[0])
	}
	if redacted[0] != true || redacted[1] != true {
		t.Fatalf("redacted flags = %v", redacted)
	}
	if strings.Contains(paths[0], "alice") {
		t.Fatalf("path metadata survived: %q", paths[0])
	}
}

func TestPurgeDeleteOld(t *testing.T) {
	ctx := context.Background()
	logPath := filepath.Join(t.TempDir(), "chatstrata-go.log")
	l, err := obslog.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	appendObservation(t, l, `{"text":"mail alice@example.com"}`, "/tmp/session.jsonl")
	stats, err := Purge(ctx, logPath, PurgeOptions{Engine: testEngine(t), DeleteOld: true})
	if err != nil {
		t.Fatal(err)
	}
	if stats.OldLog == "" {
		t.Fatal("no old log path")
	}
	if _, err := os.Stat(stats.OldLog); !os.IsNotExist(err) {
		t.Fatalf("old log still exists: %v", err)
	}
}

func TestPurgeNoMatchesLeavesLog(t *testing.T) {
	ctx := context.Background()
	logPath := filepath.Join(t.TempDir(), "chatstrata-go.log")
	l, err := obslog.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	appendObservation(t, l, `{"text":"nothing sensitive"}`, "/tmp/session.jsonl")
	before, err := os.Stat(filepath.Join(logPath, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	stats, err := Purge(ctx, logPath, PurgeOptions{Engine: testEngine(t)})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Redacted != 0 || stats.OldLog != "" {
		t.Fatalf("stats = %+v", stats)
	}
	after, err := os.Stat(filepath.Join(logPath, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("log was replaced despite no matches")
	}
}

func TestPurgeRefusesMultipleDevices(t *testing.T) {
	ctx := context.Background()
	logPath := filepath.Join(t.TempDir(), "chatstrata-go.log")
	l, err := obslog.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	appendObservation(t, l, `{"text":"alice@example.com"}`, "/tmp/session.jsonl")
	other := &obslog.Observation{
		Space: l.Identity.Space, Device: "other", Seq: 1,
		Source: "claude_code", Locator: "remote", Kind: obslog.Snapshot,
		Content: []byte(`{"text":"remote"}`),
	}
	if err := l.WriteSegment("other", []*obslog.Observation{other}); err != nil {
		t.Fatal(err)
	}
	if _, err := Purge(ctx, logPath, PurgeOptions{Engine: testEngine(t)}); err == nil || !strings.Contains(err.Error(), "more than one device") {
		t.Fatalf("err = %v", err)
	}
}

func TestPurgePreservesAppendOffsets(t *testing.T) {
	ctx := context.Background()
	logPath := filepath.Join(t.TempDir(), "chatstrata-go.log")
	l, err := obslog.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	first := []byte(`{"t":"alice@example.com"}`)
	second := []byte(`{"t":"next line"}`)
	if _, err := l.Append([]*obslog.Observation{
		{Source: "claude_code", Locator: "native", Kind: obslog.Append, Offset: 0, Content: first},
		{Source: "claude_code", Locator: "native", Kind: obslog.Append, Offset: int64(len(first)), Content: second},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Purge(ctx, logPath, PurgeOptions{Engine: testEngine(t)}); err != nil {
		t.Fatal(err)
	}
	rewritten, err := obslog.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var offsets, lengths []int64
	var contents []string
	segs, err := rewritten.Segments(rewritten.Identity.Device)
	if err != nil {
		t.Fatal(err)
	}
	for _, seg := range segs {
		if err := rewritten.ReadSegment(seg.Path, true, func(o *obslog.Observation, _ obslog.Location) error {
			offsets = append(offsets, o.Offset)
			lengths = append(lengths, int64(len(o.Content)))
			contents = append(contents, string(o.Content))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(contents) != 2 {
		t.Fatalf("records = %d", len(contents))
	}
	if offsets[0] != 0 || offsets[1] != int64(len(first)) {
		t.Fatalf("offsets = %v", offsets)
	}
	if lengths[0] != int64(len(first)) || lengths[1] != int64(len(second)) {
		t.Fatalf("lengths = %v", lengths)
	}
	if strings.Contains(contents[0], "alice@example.com") {
		t.Fatalf("plaintext survived: %q", contents[0])
	}
	if contents[1] != string(second) {
		t.Fatalf("second append changed: %q", contents[1])
	}
}

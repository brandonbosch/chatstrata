package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brandonbosch/chatstrata/v2/internal/obslog"
	"github.com/brandonbosch/chatstrata/v2/internal/store"
)

func redactionFixture(t *testing.T) (root, db, log string) {
	t.Helper()
	isolateRedactionConfig(t)
	dir := t.TempDir()
	root = filepath.Join(dir, "projects")
	sessionDir := filepath.Join(root, "proj")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"summary","summary":"Contact alice@example.com"}`,
		`{"type":"user","uuid":"u1","timestamp":"2026-03-14T10:00:00Z","cwd":"/home/alice/proj","message":{"role":"user","content":"contact alice@example.com password = \"hunter2\" from 10.1.2.3"}}`,
		`{"type":"assistant","uuid":"u2","parentUuid":"u1","timestamp":"2026-03-14T10:00:05Z","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}`,
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "session.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db = filepath.Join(dir, "archive.duckdb")
	return root, db, logDir(db)
}

func queryOne(t *testing.T, db, query string) string {
	t.Helper()
	s, err := store.OpenReadOnly(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var got string
	if err := s.Conn().QueryRowContext(context.Background(), query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	return got
}

func logContents(t *testing.T, logPath string) string {
	t.Helper()
	l, err := obslog.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	devices, err := l.Devices()
	if err != nil {
		t.Fatal(err)
	}
	for _, device := range devices {
		segs, err := l.Segments(device)
		if err != nil {
			t.Fatal(err)
		}
		for _, seg := range segs {
			if err := l.ReadSegment(seg.Path, true, func(o *obslog.Observation, _ obslog.Location) error {
				b.Write(o.Content)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return b.String()
}

func assertRedacted(t *testing.T, got string) {
	t.Helper()
	for _, secret := range []string{"alice@example.com", "hunter2", "10.1.2.3"} {
		if strings.Contains(got, secret) {
			t.Errorf("still contains %q: %q", secret, got)
		}
	}
	if !strings.Contains(got, "[REDACTED:") {
		t.Errorf("no placeholder: %q", got)
	}
}

func TestRedactApplyAndPurge(t *testing.T) {
	root, db, logPath := redactionFixture(t)
	run(t, "ingest", "claude_code", "--path", root, "--db", db)

	run(t, "redact", "apply", "--db", db)
	got := queryOne(t, db, `SELECT text FROM content_blocks WHERE text LIKE '%REDACTED%' LIMIT 1`)
	assertRedacted(t, got)
	title := queryOne(t, db, `SELECT title FROM conversations LIMIT 1`)
	assertRedacted(t, title)
	if _, err := os.Stat(filepath.Join(strings.TrimSuffix(db, ".duckdb")+".redactions", "runs")); err != nil {
		t.Fatalf("redaction sidecar: %v", err)
	}

	// Re-ingesting the unchanged source must not restore plaintext.
	run(t, "ingest", "claude_code", "--path", root, "--db", db)
	assertRedacted(t, queryOne(t, db, `SELECT text FROM content_blocks WHERE text LIKE '%REDACTED%' LIMIT 1`))

	// Hard purge rewrites the observation log and rebuilds the projection.
	run(t, "redact", "purge", "--hard", "--yes", "--db", db)
	assertRedacted(t, queryOne(t, db, `SELECT text FROM content_blocks WHERE text LIKE '%REDACTED%' LIMIT 1`))
	assertRedacted(t, logContents(t, logPath))

	// Collection after purge re-applies the policy to the original source
	// bytes instead of logging them again.
	run(t, "ingest", "claude_code", "--path", root, "--db", db)
	assertRedacted(t, logContents(t, logPath))
	assertRedacted(t, queryOne(t, db, `SELECT text FROM content_blocks WHERE text LIKE '%REDACTED%' LIMIT 1`))

	// New bytes in a purged conversation are redacted before they enter the
	// log, not only in the projection.
	session := filepath.Join(root, "proj", "session.jsonl")
	f, err := os.OpenFile(session, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"user","uuid":"u3","parentUuid":"u2","timestamp":"2026-03-14T10:01:00Z","message":{"role":"user","content":"new mail dave@example.com"}}` + "\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	run(t, "ingest", "claude_code", "--path", root, "--db", db)
	contents := logContents(t, logPath)
	if strings.Contains(contents, "dave@example.com") {
		t.Fatalf("new plaintext entered the purged log: %q", contents)
	}
	if got := queryOne(t, db, `SELECT text FROM content_blocks WHERE text LIKE '%dave%' OR text LIKE '%REDACTED:EMAIL%' ORDER BY id DESC LIMIT 1`); strings.Contains(got, "dave@example.com") {
		t.Fatalf("new secret survived projection: %q", got)
	}
}

func TestRedactPurgeRefusesPairedArchive(t *testing.T) {
	root, db, logPath := redactionFixture(t)
	run(t, "ingest", "claude_code", "--path", root, "--db", db)
	if err := os.WriteFile(filepath.Join(logPath, "space.key"), []byte("paired\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"redact", "purge", "--hard", "--yes", "--db", db}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "paired for sync") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRedactPurgeRequiresHardAndYes(t *testing.T) {
	isolateRedactionConfig(t)
	for _, args := range [][]string{
		{"redact", "purge", "--db", filepath.Join(t.TempDir(), "a.duckdb")},
		{"redact", "purge", "--hard", "--db", filepath.Join(t.TempDir(), "a.duckdb")},
	} {
		var stdout, stderr bytes.Buffer
		code := Run(context.Background(), args, &stdout, &stderr)
		if code != 2 || !strings.Contains(stderr.String(), "requires") {
			t.Errorf("%v: code=%d stderr=%q", args, code, stderr.String())
		}
	}
}

func TestRedactOverlaySurvivesReingest(t *testing.T) {
	root, db, _ := redactionFixture(t)
	run(t, "ingest", "claude_code", "--path", root, "--db", db)
	run(t, "redact", "apply", "--db", db)
	first := queryOne(t, db, `SELECT text FROM content_blocks WHERE text LIKE '%REDACTED:EMAIL%' LIMIT 1`)
	assertRedacted(t, first)

	session := filepath.Join(root, "proj", "session.jsonl")
	f, err := os.OpenFile(session, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"user","uuid":"u4","parentUuid":"u2","timestamp":"2026-03-14T10:02:00Z","message":{"role":"user","content":"one more turn"}}` + "\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	run(t, "ingest", "claude_code", "--path", root, "--db", db)
	got := queryOne(t, db, `SELECT text FROM content_blocks WHERE text LIKE '%REDACTED:EMAIL%' LIMIT 1`)
	assertRedacted(t, got)
}

func TestRedactScanIsReadOnly(t *testing.T) {
	root, db, _ := redactionFixture(t)
	run(t, "ingest", "claude_code", "--path", root, "--db", db)
	out := run(t, "redact", "scan", "--db", db)
	if !strings.Contains(out, "Matches:") || strings.Contains(out, "Run:") {
		t.Fatalf("scan output = %q", out)
	}
	if _, err := os.Stat(strings.TrimSuffix(db, ".duckdb") + ".redactions"); !os.IsNotExist(err) {
		t.Fatalf("scan wrote a sidecar: %v", err)
	}
}

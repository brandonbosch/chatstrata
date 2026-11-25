package redactions

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brandonbosch/chatstrata/v2/internal/redact"
	"github.com/brandonbosch/chatstrata/v2/internal/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func seedRedactionRows(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	stmts := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO sources (id, name) VALUES ('claude_code', 'Claude Code')`, nil},
		{`INSERT INTO conversations (id, source_id, source_native_id, title, project, message_count)
		  VALUES ('conv1', 'claude_code', 'native1', 'mail alice@example.com', 'proj', 1)`, nil},
		{`INSERT INTO messages (id, conversation_id, role, sequence_index) VALUES ('msg1', 'conv1', 'user', 0)`, nil},
		{`INSERT INTO content_blocks (id, message_id, block_index, type, text, payload)
		  VALUES ('block1', 'msg1', 0, 'text', 'contact alice@example.com password = "hunter2" from 10.1.2.3', ?::JSON)`,
			[]any{`{"note":"bob@example.com"}`}},
		{`INSERT INTO raw_events (id, source_id, source_native_conversation_id, line_number, payload)
		  VALUES ('raw1', 'claude_code', 'native1', 1, ?::JSON)`,
			[]any{`{"text":"carol@example.com"}`}},
	}
	for _, stmt := range stmts {
		if _, err := s.Conn().ExecContext(ctx, stmt.q, stmt.args...); err != nil {
			t.Fatalf("%s: %v", stmt.q, err)
		}
	}
}

func TestScanApplyOverlay(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedRedactionRows(t, s)
	policy := redact.DefaultPolicy()
	policy.DisableLocalUsername = true
	engine, err := redact.NewEngine(policy)
	if err != nil {
		t.Fatal(err)
	}
	records, stats, err := Scan(ctx, s, engine)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Records != 4 || stats.Findings < 5 {
		t.Fatalf("stats = %+v records = %d", stats, len(records))
	}
	for _, r := range records {
		if len(r.Spans) == 0 {
			t.Fatalf("record has no spans: %+v", r)
		}
	}
	dir := Dir(s.Path)
	if _, err := WriteRun(dir, Header{CreatedAt: time.Time{}, RedactionVersion: engine.Version()}, records); err != nil {
		t.Fatal(err)
	}
	applied, err := Apply(ctx, s, dir)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Applied == 0 || applied.Findings < 4 {
		t.Fatalf("apply = %+v", applied)
	}
	check := func(query string, want ...string) {
		t.Helper()
		var got string
		if err := s.Conn().QueryRowContext(ctx, query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%s = %q, missing %q", query, got, w)
			}
		}
		for _, secret := range []string{"alice@example.com", "bob@example.com", "carol@example.com", "10.1.2.3", "hunter2"} {
			if strings.Contains(got, secret) {
				t.Errorf("%s still contains %q: %q", query, secret, got)
			}
		}
	}
	check(`SELECT text FROM content_blocks WHERE id='block1'`, "[REDACTED:EMAIL]", "[REDACTED:IP]", "[REDACTED:PASSWORD_ASSIGNMENT]")
	check(`SELECT payload::VARCHAR FROM content_blocks WHERE id='block1'`, "[REDACTED:EMAIL]")
	check(`SELECT payload::VARCHAR FROM raw_events WHERE id='raw1'`, "[REDACTED:EMAIL]")
	check(`SELECT title FROM conversations WHERE id='conv1'`, "[REDACTED:EMAIL]")

	var payload string
	if err := s.Conn().QueryRowContext(ctx, `SELECT payload::VARCHAR FROM content_blocks WHERE id='block1'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(payload)) {
		t.Fatalf("payload is not valid JSON: %q", payload)
	}

	again, err := Apply(ctx, s, dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.Applied != 0 || again.Skipped == 0 {
		t.Fatalf("second apply = %+v", again)
	}
	var runs, rows int
	if err := s.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM redaction_runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := s.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM redactions`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if runs != 1 || rows != 4 {
		t.Fatalf("audit rows runs=%d redactions=%d", runs, rows)
	}
}

func TestApplyChainsRuns(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedRedactionRows(t, s)
	dir := Dir(s.Path)
	original := `contact alice@example.com password = "hunter2" from 10.1.2.3`
	first, err := redact.Apply(original, []redact.Span{{Start: 8, End: 25, RuleID: "pii-email", Category: "EMAIL", Priority: 70}})
	if err != nil {
		t.Fatal(err)
	}
	ipStart := strings.Index(first, "10.1.2.3")
	second, err := redact.Apply(first, []redact.Span{{Start: ipStart, End: ipStart + len("10.1.2.3"), RuleID: "pii-ip", Category: "IP", Priority: 60}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = WriteRun(dir, Header{CreatedAt: time.Time{}, RedactionVersion: "rd2-test", RunID: "20260101T000000.000000000Z-a"}, []Record{{
		TargetKind: TargetBlockText, TargetID: "block1", InputSHA256: sha256Hex(original),
		Spans: []redact.Span{{Start: 8, End: 25, RuleID: "pii-email", Category: "EMAIL", Priority: 70}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = WriteRun(dir, Header{CreatedAt: time.Time{}, RedactionVersion: "rd2-test", RunID: "20260101T000000.000000000Z-b"}, []Record{{
		TargetKind: TargetBlockText, TargetID: "block1", InputSHA256: sha256Hex(first),
		Spans: []redact.Span{{Start: ipStart, End: ipStart + len("10.1.2.3"), RuleID: "pii-ip", Category: "IP", Priority: 60}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(ctx, s, dir); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := s.Conn().QueryRowContext(ctx, `SELECT text FROM content_blocks WHERE id='block1'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != second {
		t.Fatalf("chained = %q, want %q", got, second)
	}
}

package redactions

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/brandonbosch/chatstrata/v2/internal/redact"
)

func TestApplyTargetsSelection(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	stmts := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO sources (id, name) VALUES ('test', 'Test')`, nil},
		{`INSERT INTO conversations (id, source_id, source_native_id, title, project, message_count)
		  VALUES ('conv1', 'test', 'c1', 'mail alice@example.com', 'proj', 1),
		         ('conv2', 'test', 'c2', NULL, 'proj', 1)`, nil},
		{`INSERT INTO messages (id, conversation_id, role, sequence_index)
		  VALUES ('msg1', 'conv1', 'user', 0), ('msg2', 'conv2', 'user', 0)`, nil},
		{`INSERT INTO content_blocks (id, message_id, block_index, type, text, payload)
		  VALUES ('block1', 'msg1', 0, 'text', 'alice@example.com', NULL),
		         ('block2', 'msg2', 0, 'text', 'bob@example.com', NULL)`, nil},
		{`INSERT INTO raw_events (id, source_id, source_native_conversation_id, line_number, payload)
		  VALUES ('raw1', 'test', 'c1', 1, ?::JSON), ('raw2', 'test', 'c2', 1, ?::JSON)`,
			[]any{`{"text":"carol@example.com"}`, `{"text":"dave@example.com"}`}},
	}
	for _, stmt := range stmts {
		if _, err := s.Conn().ExecContext(ctx, stmt.q, stmt.args...); err != nil {
			t.Fatalf("%s: %v", stmt.q, err)
		}
	}

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
	if stats.Records != 5 {
		t.Fatalf("records = %d, stats = %+v", len(records), stats)
	}
	dir := Dir(s.Path)
	if _, err := WriteRun(dir, Header{CreatedAt: time.Time{}, RedactionVersion: engine.Version()}, records); err != nil {
		t.Fatal(err)
	}

	selected := map[TargetRef]struct{}{
		{Kind: TargetBlockText, ID: "block1"}: {},
	}
	applied, err := ApplyTargets(ctx, s, dir, selected)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Applied != 1 {
		t.Fatalf("scoped apply = %+v", applied)
	}
	check := func(id, want string) {
		t.Helper()
		var got string
		if err := s.Conn().QueryRowContext(ctx, `SELECT text FROM content_blocks WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", id, got, want)
		}
	}
	check("block1", "[REDACTED:EMAIL]")
	check("block2", "bob@example.com")

	// An empty selection still indexes the run for audit, but reads nothing.
	empty, err := ApplyTargets(ctx, s, dir, map[TargetRef]struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Applied != 0 || empty.Targets != 0 {
		t.Fatalf("empty apply = %+v", empty)
	}
	var indexed int
	if err := s.Conn().QueryRowContext(ctx, `SELECT count(*) FROM redactions`).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if indexed != len(records) {
		t.Fatalf("indexed = %d, records = %d", indexed, len(records))
	}
	// A second replay must not duplicate audit rows.
	if _, err := ApplyTargets(ctx, s, dir, map[TargetRef]struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Conn().QueryRowContext(ctx, `SELECT count(*) FROM redactions`).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if indexed != len(records) {
		t.Fatalf("after second replay indexed = %d, records = %d", indexed, len(records))
	}

	// The full replay finishes the remaining targets.
	full, err := Apply(ctx, s, dir)
	if err != nil {
		t.Fatal(err)
	}
	if full.Applied != 4 {
		t.Fatalf("full apply = %+v", full)
	}
	var payload string
	if err := s.Conn().QueryRowContext(ctx, `SELECT payload::VARCHAR FROM raw_events WHERE id='raw2'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "dave@example.com") || !strings.Contains(payload, "[REDACTED:EMAIL]") {
		t.Fatalf("raw2 = %q", payload)
	}
}

package redactions

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/brandonbosch/chatstrata/v2/internal/redact"
)

func TestJSONEscapeOffsets(t *testing.T) {
	// Decoded: a, newline, b, @, c, emoji, d.
	content := `a\nb\u0040c\ud83d\ude00d`
	escapes, err := jsonEscapes(content)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		decoded int
		end     bool
		want    int
	}{
		{0, false, 0},
		{2, false, 3},  // 'b' follows the \n escape
		{3, true, 4},   // end of 'b'
		{4, false, 10}, // 'c' follows the \u0040 escape
		{4, true, 10},
		{5, false, 11}, // emoji surrogate pair starts
		{9, true, 23},  // surrogate pair ends
		{9, false, 23}, // 'd'
		{10, true, 24},
	}
	for _, c := range cases {
		if got := escapes.rawOffset(c.decoded, c.end); got != c.want {
			t.Errorf("rawOffset(%d, end=%v) = %d, want %d", c.decoded, c.end, got, c.want)
		}
	}
	decoded, err := decodeJSONString(content)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != "a\nb@c\U0001F600d" {
		t.Fatalf("decoded = %q", decoded)
	}
}

func TestScanApplyJSONEscapes(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	// A hostname preceded by a newline escape (the detector must not match the
	// escape letter), an escaped '@', a fully escaped local part, and a
	// double-encoded JSON string.
	payload := `{"cmd":"home::make_private(\nnixos.local)","email":"alice\u0040example.com","esc_email":"\u0061lice@example.com","note":"{\"inner\":\"bob@example.com\"}"}`
	stmts := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO sources (id, name) VALUES ('claude_code', 'Claude Code')`, nil},
		{`INSERT INTO conversations (id, source_id, source_native_id, title, project, message_count)
		  VALUES ('conv1', 'claude_code', 'native1', NULL, 'proj', 1)`, nil},
		{`INSERT INTO messages (id, conversation_id, role, sequence_index) VALUES ('msg1', 'conv1', 'user', 0)`, nil},
		{`INSERT INTO content_blocks (id, message_id, block_index, type, text, payload)
		  VALUES ('block1', 'msg1', 0, 'text', NULL, ?::JSON)`, []any{payload}},
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
	if stats.Records != 1 || stats.Findings < 3 {
		t.Fatalf("stats = %+v, records = %d", stats, len(records))
	}
	dir := Dir(s.Path)
	if _, err := WriteRun(dir, Header{CreatedAt: time.Time{}, RedactionVersion: engine.Version()}, records); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(ctx, s, dir); err != nil {
		t.Fatal(err)
	}

	var got string
	if err := s.Conn().QueryRowContext(ctx, `SELECT payload::VARCHAR FROM content_blocks WHERE id='block1'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(got)) {
		t.Fatalf("payload is not valid JSON: %q", got)
	}
	for _, want := range []string{"[REDACTED:HOSTNAME]", "[REDACTED:EMAIL]", `\n[REDACTED:HOSTNAME]`} {
		if !strings.Contains(got, want) {
			t.Errorf("payload = %q, missing %q", got, want)
		}
	}
	for _, secret := range []string{"nixos.local", "alice@example.com", "bob@example.com"} {
		if strings.Contains(got, secret) {
			t.Errorf("payload still contains %q: %q", secret, got)
		}
	}
	if strings.Contains(got, `\[`) {
		t.Errorf("redaction split a JSON escape: %q", got)
	}
}

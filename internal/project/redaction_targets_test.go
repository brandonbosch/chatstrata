package project

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/brandonbosch/chatstrata/v2/internal/obslog"
	"github.com/brandonbosch/chatstrata/v2/internal/redactions"
	"github.com/brandonbosch/chatstrata/v2/internal/store"
)

func TestRedactionTargets(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	stmts := []string{
		`INSERT INTO sources (id, name) VALUES ('test', 'Test')`,
		`INSERT INTO conversations (id, source_id, source_native_id, title, project, message_count)
		 VALUES ('conv1', 'test', 'c1', 'title', 'proj', 1), ('conv2', 'test', 'c2', NULL, 'proj', 1)`,
		`INSERT INTO messages (id, conversation_id, role, sequence_index)
		 VALUES ('msg1', 'conv1', 'user', 0), ('msg2', 'conv2', 'user', 0)`,
		`INSERT INTO content_blocks (id, message_id, block_index, type, text, payload)
		 VALUES ('block1', 'msg1', 0, 'text', 'a', NULL), ('block2', 'msg2', 0, 'text', 'b', NULL)`,
		`INSERT INTO raw_events (id, source_id, source_native_conversation_id, line_number, payload)
		 VALUES ('raw1', 'test', 'c1', 1, '{}'::JSON), ('raw2', 'test', 'c2', 1, '{}'::JSON)`,
	}
	for _, q := range stmts {
		if _, err := s.Conn().ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	p := &Projector{Store: s}
	got, err := p.redactionTargets(ctx, []obslog.Key{{Source: "test", Locator: "c1"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []redactions.TargetRef{
		{Kind: redactions.TargetConversationTitle, ID: "conv1"},
		{Kind: redactions.TargetBlockText, ID: "block1"},
		{Kind: redactions.TargetBlockPayload, ID: "block1"},
		{Kind: redactions.TargetRawEventPayload, ID: "raw1"},
	}
	for _, ref := range want {
		if _, ok := got[ref]; !ok {
			t.Errorf("missing %+v", ref)
		}
	}
	for _, ref := range []redactions.TargetRef{
		{Kind: redactions.TargetBlockText, ID: "block2"},
		{Kind: redactions.TargetRawEventPayload, ID: "raw2"},
	} {
		if _, ok := got[ref]; ok {
			t.Errorf("selected unrelated %+v", ref)
		}
	}
	if len(got) != len(want) {
		t.Errorf("targets = %d, want %d", len(got), len(want))
	}
}

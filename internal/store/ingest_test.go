package store

import (
	"context"
	"encoding/json"
	"fmt"

	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brandonbosch/chatstrata/internal/model"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "a.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.conn.ExecContext(ctx, `INSERT INTO sources (id, name) VALUES ('src', 'Source')`); err != nil {
		t.Fatal(err)
	}
	return s
}

// testConv builds a conversation of n messages with blocks, parents and one
// raw event per message.
func testConv(n int) *model.Conversation {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	conv := &model.Conversation{
		SourceNativeID: "conv-1",
		Title:          model.Ptr("first"),
		Project:        model.Ptr("/work"),
		StartedAt:      &start,
		RawPath:        "/logs/conv-1.jsonl",
		Metadata:       map[string]any{"event_count": n},
	}
	for i := range n {
		at := start.Add(time.Duration(i) * time.Minute)
		m := model.Message{
			SourceNativeID: model.Ptr(fmt.Sprintf("m%d", i)),
			Role:           model.Role([]string{"user", "assistant"}[i%2]),
			CreatedAt:      &at,
			Metadata:       map[string]any{"cwd": "/work"},
			Blocks:         []model.Block{{Type: model.BlockText, Text: model.Ptr(fmt.Sprintf("message %d", i))}},
		}
		if i > 0 {
			m.ParentSourceNativeID = model.Ptr(fmt.Sprintf("m%d", i-1))
		}
		if i%2 == 1 {
			m.Model = model.Ptr("model-a")
			m.Blocks = append(m.Blocks, model.Block{
				Type: model.BlockToolUse, ToolName: model.Ptr("Bash"), ToolUseID: model.Ptr(fmt.Sprintf("t%d", i)),
				Payload: map[string]any{"input": map[string]any{"command": "ls"}},
			})
		}
		conv.Messages = append(conv.Messages, m)
		conv.RawEvents = append(conv.RawEvents, json.RawMessage(fmt.Sprintf(`{"uuid": "m%d", "n": %d, "cmd": "a < b && c"}`, i, i)))
		conv.EndedAt = &at
	}
	return conv
}

// dump renders every stored row of the archive's conversation tables.
func dump(t *testing.T, s *Store) string {
	t.Helper()
	var b strings.Builder
	for _, q := range []string{
		`SELECT * EXCLUDE (ingested_at) FROM conversations ORDER BY id`,
		`SELECT * FROM messages ORDER BY id`,
		`SELECT * FROM content_blocks ORDER BY id`,
		`SELECT * EXCLUDE (ingested_at) FROM raw_events ORDER BY id`,
	} {
		rows, err := s.conn.QueryContext(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(&b, vals...)
		}
		rows.Close()
		fmt.Fprintln(&b, "--")
	}
	return b.String()
}

func rowid(t *testing.T, s *Store, table, id string) int64 {
	t.Helper()
	var r int64
	if err := s.conn.QueryRowContext(context.Background(),
		`SELECT rowid FROM `+table+` WHERE id = ?`, id).Scan(&r); err != nil {
		t.Fatalf("%s %s: %v", table, id, err)
	}
	return r
}

// TestIngestUpdatesInPlace checks that updating a stored conversation leaves
// exactly the rows a fresh ingest of the new version writes, and that rows
// before the first change are kept rather than rewritten.
func TestIngestUpdatesInPlace(t *testing.T) {
	cases := []struct {
		name   string
		change func(c *model.Conversation)
		want   Action
		kept   int // leading messages and raw events that must not be rewritten
	}{
		{"grown", func(c *model.Conversation) {
			*c = *testConv(13)
			c.Title = model.Ptr("renamed")
		}, Appended, 10},
		{"last message gained a block", func(c *model.Conversation) {
			last := &c.Messages[9]
			last.Blocks = append(last.Blocks, model.Block{Type: model.BlockText, Text: model.Ptr("more")})
			c.RawEvents[9] = json.RawMessage(`{"uuid": "m9", "more": true}`)
		}, Appended, 9},
		{"middle message changed", func(c *model.Conversation) {
			c.Messages[4].Blocks[0].Text = model.Ptr("edited")
		}, Appended, 4},
		{"shrunk", func(c *model.Conversation) {
			c.Messages = c.Messages[:6]
			c.RawEvents = c.RawEvents[:6]
		}, Appended, 6},
		{"parent now resolves", func(c *model.Conversation) {
			// m3's parent only becomes a stored message in this version.
			c.Messages[3].ParentSourceNativeID = model.Ptr("m10")
			*c = *withMessage(c, "m10")
		}, Appended, 3},
		{"raw path moved", func(c *model.Conversation) {
			c.RawPath = "/elsewhere/conv-1.jsonl"
		}, Appended, 0},
		{"project changed", func(c *model.Conversation) {
			c.Project = model.Ptr("/other")
		}, Replaced, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			before := testConv(10)
			after := testConv(10)
			tc.change(after)

			s := openTestStore(t)
			if _, err := s.Ingest(ctx, "src", before, nil); err != nil {
				t.Fatal(err)
			}
			rows, err := buildRows(conversationID("src", before.SourceNativeID), before)
			if err != nil {
				t.Fatal(err)
			}
			var ids []int64
			for i := range tc.kept {
				ids = append(ids, rowid(t, s, "messages", rows.messages[i].id),
					rowid(t, s, "content_blocks", rows.messages[i].blocks[0].id),
					rowid(t, s, "raw_events", rawEventID(rows.convID, i)))
			}

			action, err := s.Ingest(ctx, "src", after, nil)
			if err != nil {
				t.Fatal(err)
			}
			if action != tc.want {
				t.Errorf("action = %v, want %v", action, tc.want)
			}

			fresh := openTestStore(t)
			if _, err := fresh.Ingest(ctx, "src", after, nil); err != nil {
				t.Fatal(err)
			}
			if got, want := dump(t, s), dump(t, fresh); got != want {
				t.Errorf("rows differ from a fresh ingest\n got:\n%s\nwant:\n%s", got, want)
			}

			for i := range tc.kept {
				got := []int64{rowid(t, s, "messages", rows.messages[i].id),
					rowid(t, s, "content_blocks", rows.messages[i].blocks[0].id),
					rowid(t, s, "raw_events", rawEventID(rows.convID, i))}
				if want := ids[3*i : 3*i+3]; fmt.Sprint(got) != fmt.Sprint(want) {
					t.Errorf("message %d was rewritten: rowids %v, were %v", i, got, want)
				}
			}

			if action, err := s.Ingest(ctx, "src", after, nil); err != nil || action != Unchanged {
				t.Errorf("ingesting again = %v, %v; want unchanged", action, err)
			}
		})
	}
}

func withMessage(c *model.Conversation, native string) *model.Conversation {
	at := c.EndedAt.Add(time.Minute)
	c.Messages = append(c.Messages, model.Message{
		SourceNativeID: model.Ptr(native), Role: model.Role("user"), CreatedAt: &at,
		Blocks: []model.Block{{Type: model.BlockText, Text: model.Ptr("late")}},
	})
	c.RawEvents = append(c.RawEvents, json.RawMessage(`{"uuid": "`+native+`"}`))
	c.EndedAt = &at
	return c
}

// TestIngestRecoversInterruptedUpdate simulates an update that stopped after
// clearing the hash and deleting some rows: the next ingest completes it.
func TestIngestRecoversInterruptedUpdate(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, err := s.Ingest(ctx, "src", testConv(10), nil); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE conversations SET content_hash = NULL`,
		`DELETE FROM content_blocks WHERE message_id IN (SELECT id FROM messages WHERE sequence_index >= 7)`,
	} {
		if _, err := s.conn.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	after := testConv(12)
	if _, err := s.Ingest(ctx, "src", after, nil); err != nil {
		t.Fatal(err)
	}
	fresh := openTestStore(t)
	if _, err := fresh.Ingest(ctx, "src", after, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := dump(t, s), dump(t, fresh); got != want {
		t.Errorf("rows differ from a fresh ingest\n got:\n%s\nwant:\n%s", got, want)
	}
}

// TestGrowingSessionsKeepArchiveSmall is the regression test for the archive
// growing without bound. Sessions that keep growing, re-ingested after every
// few turns as the daemon does, must leave about as many used blocks as
// storing their final version once. Rewriting whole conversations left their
// deleted rows in used blocks, so the archive grew with every update. (Free
// blocks are DuckDB's own churn from checkpoints and level off; the file
// size includes them, so it isn't compared.)
func TestGrowingSessionsKeepArchiveSmall(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	const sessions, active, size, step, rounds = 20, 10, 150, 20, 25
	conv := func(i, round int) *model.Conversation {
		n := size
		if i < active {
			n += round * step
		}
		c := testConv(n)
		c.SourceNativeID = fmt.Sprintf("conv-%d", i)
		return c
	}
	write := func(path string, rounds []int) (used int64) {
		for _, round := range rounds {
			s, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.conn.ExecContext(ctx, `INSERT INTO sources (id, name) VALUES ('src', 'Source') ON CONFLICT DO NOTHING`); err != nil {
				t.Fatal(err)
			}
			for i := range sessions {
				if round > 0 && i >= active {
					continue // only the active sessions changed
				}
				if _, err := s.Ingest(ctx, "src", conv(i, round), nil); err != nil {
					t.Fatal(err)
				}
			}
			s.Close() // checkpoints, as every CLI run and daemon cycle does
		}
		s, err := OpenReadOnly(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.conn.QueryRowContext(ctx, `SELECT used_blocks FROM pragma_database_size()`).Scan(&used); err != nil {
			t.Fatal(err)
		}
		return used
	}

	all := make([]int, rounds+1)
	for i := range all {
		all[i] = i
	}
	grown := write(filepath.Join(dir, "grown.duckdb"), all)
	once := write(filepath.Join(dir, "once.duckdb"), []int{rounds})
	t.Logf("used blocks after %d rounds of updates: %d; final version stored once: %d", rounds, grown, once)
	if grown > once*5/4 {
		t.Errorf("updates left %d used blocks, more than 1.25x the %d of storing the final version once", grown, once)
	}
}

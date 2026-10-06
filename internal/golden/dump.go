// Package golden produces the canonical form of an archive described in
// spec/README.md, so the Go implementation can be checked against the output
// the Python implementation recorded in spec/golden/expected.
//
// It is the Go twin of scripts/golden.py's dump.
package golden

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/brandonbosch/chatstrata/internal/store"
)

// Dump returns the canonical projection of the archive at path. Each
// replacement pair rewrites a path prefix (e.g. a temp dir to "$INPUT") in
// every string value.
func Dump(ctx context.Context, path string, replacements [][2]string) (any, error) {
	s, err := store.OpenReadOnly(ctx, path)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	db := s.Conn()

	convRows, err := db.QueryContext(ctx, `
		SELECT id, source_id, source_native_id, title, project, started_at, ended_at,
		       message_count, raw_path, metadata::VARCHAR
		FROM conversations ORDER BY source_id, source_native_id`)
	if err != nil {
		return nil, err
	}
	type convRow struct {
		id, source, native  string
		title, project, raw sql.NullString
		started, ended      sql.NullTime
		count               int
		metadata            sql.NullString
	}
	var convs []convRow
	for convRows.Next() {
		var c convRow
		if err := convRows.Scan(&c.id, &c.source, &c.native, &c.title, &c.project, &c.started,
			&c.ended, &c.count, &c.raw, &c.metadata); err != nil {
			convRows.Close()
			return nil, err
		}
		convs = append(convs, c)
	}
	convRows.Close()

	conversations := []any{}
	for _, c := range convs {
		messages, err := dumpMessages(ctx, db, c.id)
		if err != nil {
			return nil, err
		}
		rawEvents, err := dumpRawEvents(ctx, db, c.source, c.native)
		if err != nil {
			return nil, err
		}
		metadata, err := jsonColumn(c.metadata)
		if err != nil {
			return nil, err
		}
		conversations = append(conversations, map[string]any{
			"source_id":        c.source,
			"source_native_id": c.native,
			"title":            nullString(c.title),
			"project":          nullString(c.project),
			"started_at":       timestamp(c.started),
			"ended_at":         timestamp(c.ended),
			"message_count":    c.count,
			"raw_path":         nullString(c.raw),
			"metadata":         metadata,
			"messages":         messages,
			"raw_events":       rawEvents,
		})
	}
	return replacePaths(map[string]any{"conversations": conversations}, replacements), nil
}

func dumpMessages(ctx context.Context, db *sql.Conn, convID string) ([]any, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT m.id, m.sequence_index, m.source_native_id, p.source_native_id, m.role, m.model,
		       m.created_at, m.metadata::VARCHAR
		FROM messages m
		LEFT JOIN messages p ON p.id = m.parent_message_id
		WHERE m.conversation_id = ?
		ORDER BY m.sequence_index`, convID)
	if err != nil {
		return nil, err
	}
	type msgRow struct {
		id                    string
		seq                   int
		native, parent, model sql.NullString
		role                  string
		created               sql.NullTime
		metadata              sql.NullString
	}
	var msgs []msgRow
	for rows.Next() {
		var m msgRow
		if err := rows.Scan(&m.id, &m.seq, &m.native, &m.parent, &m.role, &m.model, &m.created, &m.metadata); err != nil {
			rows.Close()
			return nil, err
		}
		msgs = append(msgs, m)
	}
	rows.Close()

	out := []any{}
	for _, m := range msgs {
		blocks, err := dumpBlocks(ctx, db, m.id)
		if err != nil {
			return nil, err
		}
		metadata, err := jsonColumn(m.metadata)
		if err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"sequence_index":          m.seq,
			"source_native_id":        nullString(m.native),
			"parent_source_native_id": nullString(m.parent),
			"role":                    m.role,
			"model":                   nullString(m.model),
			"created_at":              timestamp(m.created),
			"metadata":                metadata,
			"blocks":                  blocks,
		})
	}
	return out, nil
}

func dumpBlocks(ctx context.Context, db *sql.Conn, messageID string) ([]any, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT block_index, type, text, tool_name, tool_use_id, payload::VARCHAR
		FROM content_blocks WHERE message_id = ? ORDER BY block_index`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var index int
		var typ string
		var text, toolName, toolUseID, payload sql.NullString
		if err := rows.Scan(&index, &typ, &text, &toolName, &toolUseID, &payload); err != nil {
			return nil, err
		}
		p, err := jsonColumn(payload)
		if err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"block_index": index,
			"type":        typ,
			"text":        nullString(text),
			"tool_name":   nullString(toolName),
			"tool_use_id": nullString(toolUseID),
			"payload":     p,
		})
	}
	return out, rows.Err()
}

func dumpRawEvents(ctx context.Context, db *sql.Conn, source, native string) ([]any, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT line_number, raw_path, payload::VARCHAR FROM raw_events
		WHERE source_id = ? AND source_native_conversation_id = ?
		ORDER BY line_number, raw_path`, source, native)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var line sql.NullInt64
		var rawPath, payload sql.NullString
		if err := rows.Scan(&line, &rawPath, &payload); err != nil {
			return nil, err
		}
		p, err := jsonColumn(payload)
		if err != nil {
			return nil, err
		}
		var lineValue any
		if line.Valid {
			lineValue = line.Int64
		}
		out = append(out, map[string]any{"line_number": lineValue, "raw_path": nullString(rawPath), "payload": p})
	}
	return out, rows.Err()
}

func jsonColumn(s sql.NullString) (any, error) {
	if !s.Valid {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal([]byte(s.String), &v); err != nil {
		return nil, fmt.Errorf("decode JSON column: %w", err)
	}
	return v, nil
}

func nullString(s sql.NullString) any {
	if s.Valid {
		return s.String
	}
	return nil
}

// timestamp formats RFC 3339 in UTC with trailing fractional zeros trimmed.
func timestamp(t sql.NullTime) any {
	if !t.Valid {
		return nil
	}
	return t.Time.UTC().Format(time.RFC3339Nano)
}

func replacePaths(v any, replacements [][2]string) any {
	switch x := v.(type) {
	case string:
		for _, r := range replacements {
			x = strings.ReplaceAll(x, r[0], r[1])
		}
		return x
	case []any:
		for i := range x {
			x[i] = replacePaths(x[i], replacements)
		}
		return x
	case map[string]any:
		for k := range x {
			x[k] = replacePaths(x[k], replacements)
		}
		return x
	}
	return v
}

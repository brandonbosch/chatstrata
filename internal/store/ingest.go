package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	duckdb "github.com/duckdb/duckdb-go/v2"

	"github.com/brandonbosch/chatstrata/internal/model"
)

type Action int

const (
	Unchanged Action = iota
	Inserted
	Replaced
)

// hashVersion changes whenever the stored form of a conversation changes, so
// a new version re-ingests everything once.
const hashVersion = "go-2"

// EnsureSource registers a source, or refreshes its version and timestamp.
func (s *Store) EnsureSource(ctx context.Context, src model.Source) error {
	_, err := s.conn.ExecContext(ctx, `
		INSERT INTO sources (id, name, adapter_version, config, last_ingested)
		VALUES (?, ?, ?, NULL, now())
		ON CONFLICT (id) DO UPDATE SET
			adapter_version = excluded.adapter_version,
			config = excluded.config,
			last_ingested = now()`,
		src.Name(), src.DisplayName(), src.Version())
	if err != nil {
		return fmt.Errorf("register source %s: %w", src.Name(), err)
	}
	return nil
}

// Ingest stores a conversation, replacing any earlier version of it.
//
// An unchanged conversation (same content hash) only has its mtime refreshed.
// Otherwise the old rows are deleted and the new ones inserted. Ids are
// deterministic, so a replaced conversation keeps the ids of everything that
// didn't change.
func (s *Store) Ingest(ctx context.Context, source string, conv *model.Conversation, mtime *float64) (Action, error) {
	hash, err := contentHash(conv)
	if err != nil {
		return Unchanged, err
	}

	var existingID, storedHash sql.NullString
	err = s.conn.QueryRowContext(ctx, `
		SELECT id, content_hash FROM conversations
		WHERE source_id = ? AND source_native_id = ?`, source, conv.SourceNativeID).
		Scan(&existingID, &storedHash)
	if err != nil && err != sql.ErrNoRows {
		return Unchanged, fmt.Errorf("look up %s: %w", conv.SourceNativeID, err)
	}

	action := Inserted
	if existingID.Valid {
		if storedHash.String == hash {
			_, err := s.conn.ExecContext(ctx,
				`UPDATE conversations SET source_file_mtime = ?, raw_path = ? WHERE id = ?`,
				mtime, conv.RawPath, existingID.String)
			if err != nil {
				return Unchanged, fmt.Errorf("refresh %s: %w", conv.SourceNativeID, err)
			}
			return Unchanged, nil
		}
		if err := s.deleteConversation(ctx, source, conv.SourceNativeID, existingID.String); err != nil {
			return Unchanged, err
		}
		action = Replaced
	}

	if err := s.insertConversation(ctx, source, conv, hash, mtime); err != nil {
		return Unchanged, fmt.Errorf("store %s: %w", conv.SourceNativeID, err)
	}
	return action, nil
}

// deleteConversation removes a conversation and everything under it. Each
// statement runs in its own transaction: DuckDB checks foreign keys against
// committed data, so deleting children and parents in one transaction fails.
// If this is interrupted, the conversation row (and its stored mtime) is the
// last thing to go, so the next ingest re-parses it.
func (s *Store) deleteConversation(ctx context.Context, source, nativeID, convID string) error {
	const inConversation = `message_id IN (SELECT id FROM messages WHERE conversation_id = ?)`
	steps := []struct {
		sql  string
		args []any
	}{
		{`UPDATE conversations SET source_file_mtime = NULL, content_hash = NULL WHERE id = ?`, []any{convID}},
		{`DELETE FROM message_embeddings WHERE ` + inConversation, []any{convID}},
		{`DELETE FROM attachments WHERE ` + inConversation, []any{convID}},
		{`DELETE FROM content_blocks WHERE ` + inConversation, []any{convID}},
		{`DELETE FROM messages WHERE conversation_id = ?`, []any{convID}},
		{`DELETE FROM raw_events WHERE source_id = ? AND source_native_conversation_id = ?`, []any{source, nativeID}},
		{`DELETE FROM conversations WHERE id = ?`, []any{convID}},
	}
	for _, step := range steps {
		if _, err := s.conn.ExecContext(ctx, step.sql, step.args...); err != nil {
			return fmt.Errorf("remove old version of %s: %w", nativeID, err)
		}
	}
	return nil
}

func (s *Store) insertConversation(ctx context.Context, source string, conv *model.Conversation, hash string, mtime *float64) (err error) {
	convID := conversationID(source, conv.SourceNativeID)
	metadata, err := json.Marshal(nonNil(conv.Metadata))
	if err != nil {
		return fmt.Errorf("encode metadata: %w", err)
	}

	if _, err := s.conn.ExecContext(ctx, "BEGIN TRANSACTION"); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			s.conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()

	if _, err = s.conn.ExecContext(ctx, `
		INSERT INTO conversations (
			id, source_id, source_native_id, title, project, started_at, ended_at,
			message_count, content_hash, raw_path, metadata, source_file_mtime
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::JSON, ?)`,
		convID, source, conv.SourceNativeID, conv.Title, conv.Project,
		utc(conv.StartedAt), utc(conv.EndedAt), len(conv.Messages), hash,
		conv.RawPath, string(metadata), mtime); err != nil {
		return err
	}

	err = s.conn.Raw(func(dc any) error {
		return appendRows(dc.(driver.Conn), source, convID, conv)
	})
	if err != nil {
		return err
	}
	_, err = s.conn.ExecContext(ctx, "COMMIT")
	return err
}

// appendRows bulk-loads messages, content blocks and raw events with DuckDB
// appenders, which is far faster than one INSERT per row.
func appendRows(dc driver.Conn, source, convID string, conv *model.Conversation) error {
	nativeIDs := make([]*string, len(conv.Messages))
	for i, m := range conv.Messages {
		nativeIDs[i] = m.SourceNativeID
	}
	ids := messageIDs(convID, nativeIDs)
	// Parents resolve to messages in the same conversation only.
	byNative := make(map[string]string, len(ids))
	for i, native := range nativeIDs {
		if native != nil {
			if _, dup := byNative[*native]; !dup {
				byNative[*native] = ids[i]
			}
		}
	}

	messages, err := duckdb.NewAppenderFromConn(dc, "", "messages")
	if err != nil {
		return err
	}
	blocks, err := duckdb.NewAppenderFromConn(dc, "", "content_blocks")
	if err != nil {
		messages.Close()
		return err
	}
	if err := appendMessages(messages, blocks, convID, ids, byNative, conv.Messages); err != nil {
		// Closing flushes into the transaction, which the caller rolls back.
		messages.Close()
		blocks.Close()
		return err
	}
	// Messages must be flushed before their blocks, which reference them.
	if err := messages.Close(); err != nil {
		blocks.Close()
		return fmt.Errorf("flush messages: %w", err)
	}
	if err := blocks.Close(); err != nil {
		return fmt.Errorf("flush content blocks: %w", err)
	}

	events, err := duckdb.NewAppenderFromConn(dc, "", "raw_events")
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for line, raw := range conv.RawEvents {
		if err := events.AppendRow(rawEventID(convID, line), source, conv.SourceNativeID,
			conv.RawPath, int32(line), raw, now); err != nil {
			events.Close()
			return fmt.Errorf("append raw event %d: %w", line, err)
		}
	}
	if err := events.Close(); err != nil {
		return fmt.Errorf("flush raw events: %w", err)
	}
	return nil
}

func appendMessages(messages, blocks *duckdb.Appender, convID string, ids []string, byNative map[string]string, msgs []model.Message) error {
	for i, m := range msgs {
		var parent any
		if m.ParentSourceNativeID != nil {
			if id, ok := byNative[*m.ParentSourceNativeID]; ok {
				parent = id
			}
		}
		metadata, err := jsonValue(m.Metadata, true)
		if err != nil {
			return err
		}
		if err := messages.AppendRow(ids[i], convID, opt(m.SourceNativeID), parent, string(m.Role),
			opt(m.Model), utc(m.CreatedAt), int32(i), metadata); err != nil {
			return fmt.Errorf("append message %d: %w", i, err)
		}
		for j, b := range m.Blocks {
			payload, err := jsonValue(b.Payload, false)
			if err != nil {
				return err
			}
			if err := blocks.AppendRow(blockID(ids[i], j), ids[i], int32(j), string(b.Type),
				opt(b.Text), opt(b.ToolName), opt(b.ToolUseID), payload); err != nil {
				return fmt.Errorf("append block %d of message %d: %w", j, i, err)
			}
		}
	}
	return nil
}

// contentHash identifies the stored form of a conversation.
// Raw events are hashed byte for byte: json.Marshal would compact them, and
// differently formatted copies of an event (original bytes versus a legacy
// re-serialization) must not count as the same stored conversation.
func contentHash(conv *model.Conversation) (string, error) {
	shallow := *conv
	shallow.RawEvents = nil
	b, err := json.Marshal(&shallow)
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", conv.SourceNativeID, err)
	}
	h := sha256.New()
	h.Write([]byte(hashVersion + "\x00"))
	h.Write(b)
	for _, raw := range conv.RawEvents {
		h.Write([]byte{0})
		h.Write(raw)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// jsonValue encodes a JSON column value as raw JSON, which the appender stores
// verbatim. An empty map is NULL unless keepEmpty is set (Python stores empty
// metadata as {} but empty block payloads as NULL).
func jsonValue(m map[string]any, keepEmpty bool) (any, error) {
	if len(m) == 0 {
		if !keepEmpty {
			return nil, nil
		}
		return json.RawMessage("{}"), nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode JSON column: %w", err)
	}
	return json.RawMessage(b), nil
}

func nonNil(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func opt(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// utc normalizes to UTC at microsecond precision, DuckDB's TIMESTAMPTZ resolution.
func utc(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Truncate(time.Microsecond)
}

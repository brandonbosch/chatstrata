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

	"github.com/brandonbosch/chatstrata/v2/internal/model"
)

type Action int

const (
	Unchanged Action = iota
	Inserted
	Replaced
	// Appended means only rows past the stored ones were written (and any
	// stored rows after the first difference rewritten).
	Appended
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
// A changed one keeps the stored messages and raw events that are already
// exactly what it would write (the usual case: a transcript that grew), and
// rewrites only the rows after the first difference. Rewriting whole
// conversations leaves dead space in the DuckDB file that it never gives
// back, so an archive that rewrote its active sessions on every sync grew
// without bound. Ids are deterministic, so the result is the same rows a
// fresh insert would write.
//
// A change to an indexed conversation column (project, started_at) still
// replaces the whole conversation: DuckDB rejects updating indexed columns of
// a row other rows reference.
func (s *Store) Ingest(ctx context.Context, source string, conv *model.Conversation, mtime *float64) (Action, error) {
	hash, err := contentHash(conv)
	if err != nil {
		return Unchanged, err
	}

	var existingID, storedHash, storedProject sql.NullString
	var storedStarted sql.NullTime
	err = s.conn.QueryRowContext(ctx, `
		SELECT id, content_hash, project, started_at FROM conversations
		WHERE source_id = ? AND source_native_id = ?`, source, conv.SourceNativeID).
		Scan(&existingID, &storedHash, &storedProject, &storedStarted)
	if err != nil && err != sql.ErrNoRows {
		return Unchanged, fmt.Errorf("look up %s: %w", conv.SourceNativeID, err)
	}

	rows, err := buildRows(conversationID(source, conv.SourceNativeID), conv)
	if err != nil {
		return Unchanged, fmt.Errorf("store %s: %w", conv.SourceNativeID, err)
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
		if sameString(storedProject, conv.Project) && sameTime(storedStarted, conv.StartedAt) {
			if err := s.updateConversation(ctx, source, conv, rows, hash, mtime); err != nil {
				return Unchanged, err
			}
			return Appended, nil
		}
		if err := s.deleteConversation(ctx, source, conv.SourceNativeID, existingID.String); err != nil {
			return Unchanged, err
		}
		action = Replaced
	}

	if err := s.insertConversation(ctx, source, conv, rows, hash, mtime); err != nil {
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

// updateConversation brings a stored conversation up to date in place: it
// keeps the leading messages and raw events that are stored exactly as rows
// would store them, deletes the stored rows after them, and appends the rest.
//
// The deletes run first, each in its own transaction (see
// deleteConversation), after clearing the stored hash, so an interrupted
// update is redone by the next ingest. Appending and updating the
// conversation row then commit together.
func (s *Store) updateConversation(ctx context.Context, source string, conv *model.Conversation, rows *convRows, hash string, mtime *float64) (err error) {
	keepMsgs, storedMsgs, err := s.storedMessagePrefix(ctx, rows)
	if err != nil {
		return fmt.Errorf("compare %s: %w", conv.SourceNativeID, err)
	}
	keepEvents, storedEvents, err := s.storedEventPrefix(ctx, source, conv, rows)
	if err != nil {
		return fmt.Errorf("compare %s: %w", conv.SourceNativeID, err)
	}

	if keepMsgs < storedMsgs || keepEvents < storedEvents {
		const tail = `message_id IN (SELECT id FROM messages WHERE conversation_id = ? AND sequence_index >= ?)`
		steps := []struct {
			sql  string
			args []any
		}{
			{`UPDATE conversations SET source_file_mtime = NULL, content_hash = NULL WHERE id = ?`, []any{rows.convID}},
			{`DELETE FROM message_embeddings WHERE ` + tail, []any{rows.convID, keepMsgs}},
			{`DELETE FROM attachments WHERE ` + tail, []any{rows.convID, keepMsgs}},
			{`DELETE FROM content_blocks WHERE ` + tail, []any{rows.convID, keepMsgs}},
			{`DELETE FROM messages WHERE conversation_id = ? AND sequence_index >= ?`, []any{rows.convID, keepMsgs}},
			{`DELETE FROM raw_events WHERE source_id = ? AND source_native_conversation_id = ? AND line_number >= ?`,
				[]any{source, conv.SourceNativeID, keepEvents}},
		}
		for _, step := range steps {
			if _, err := s.conn.ExecContext(ctx, step.sql, step.args...); err != nil {
				return fmt.Errorf("remove changed rows of %s: %w", conv.SourceNativeID, err)
			}
		}
	}

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
	err = s.conn.Raw(func(dc any) error {
		return appendRows(dc.(driver.Conn), source, conv, rows, keepMsgs, keepEvents)
	})
	if err != nil {
		return fmt.Errorf("store %s: %w", conv.SourceNativeID, err)
	}
	// Only unindexed columns: Ingest replaces the conversation when an
	// indexed one changed.
	if _, err = s.conn.ExecContext(ctx, `
		UPDATE conversations SET
			title = ?, ended_at = ?, message_count = ?, content_hash = ?,
			raw_path = ?, metadata = ?::JSON, source_file_mtime = ?
		WHERE id = ?`,
		conv.Title, utc(conv.EndedAt), len(conv.Messages), hash,
		conv.RawPath, string(metadata), mtime, rows.convID); err != nil {
		return fmt.Errorf("update %s: %w", conv.SourceNativeID, err)
	}
	_, err = s.conn.ExecContext(ctx, "COMMIT")
	return err
}

// storedMessagePrefix returns how many leading messages (with their blocks)
// are stored exactly as rows would store them, and how many are stored.
func (s *Store) storedMessagePrefix(ctx context.Context, rows *convRows) (keep, stored int, err error) {
	q, err := s.conn.QueryContext(ctx, `
		SELECT m.sequence_index, m.id, m.source_native_id, m.parent_message_id,
		       m.role, m.model, m.created_at, m.metadata::VARCHAR,
		       cb.id, cb.type, cb.text, cb.tool_name, cb.tool_use_id, cb.payload::VARCHAR
		FROM messages m
		LEFT JOIN content_blocks cb ON cb.message_id = m.id
		WHERE m.conversation_id = ?
		ORDER BY m.sequence_index, cb.block_index`, rows.convID)
	if err != nil {
		return 0, 0, err
	}
	defer q.Close()

	matching := true
	var cur *messageRow
	finish := func() {
		if cur == nil {
			return
		}
		if matching && stored < len(rows.messages) && cur.equal(&rows.messages[stored]) {
			keep++
		} else {
			matching = false
		}
		stored++
		cur = nil
	}
	for q.Next() {
		var seq int
		var m messageRow
		var created sql.NullTime
		var blockID, blockType sql.NullString
		var b blockRow
		if err := q.Scan(&seq, &m.id, &m.nativeID, &m.parent, &m.role, &m.model, &created, &m.metadata,
			&blockID, &blockType, &b.text, &b.toolName, &b.toolUseID, &b.payload); err != nil {
			return 0, 0, err
		}
		if cur == nil || seq != stored {
			finish()
			if seq != stored { // a gap: nothing after it can be kept
				matching = false
			}
			if created.Valid {
				t := created.Time.UTC()
				m.createdAt = &t
			}
			cur = &m
		}
		if blockID.Valid {
			b.id, b.typ = blockID.String, blockType.String
			cur.blocks = append(cur.blocks, b)
		}
	}
	finish()
	return keep, stored, q.Err()
}

// storedEventPrefix returns how many leading raw events are stored exactly as
// rows would store them, and how many are stored.
func (s *Store) storedEventPrefix(ctx context.Context, source string, conv *model.Conversation, rows *convRows) (keep, stored int, err error) {
	q, err := s.conn.QueryContext(ctx, `
		SELECT line_number, id, raw_path, payload::VARCHAR FROM raw_events
		WHERE source_id = ? AND source_native_conversation_id = ?
		ORDER BY line_number`, source, conv.SourceNativeID)
	if err != nil {
		return 0, 0, err
	}
	defer q.Close()
	matching := true
	for q.Next() {
		var line sql.NullInt64
		var id string
		var path, payload sql.NullString
		if err := q.Scan(&line, &id, &path, &payload); err != nil {
			return 0, 0, err
		}
		matching = matching && line.Valid && int(line.Int64) == stored && stored < len(conv.RawEvents) &&
			id == rawEventID(rows.convID, stored) && path.Valid && path.String == conv.RawPath && payload.Valid
		if matching {
			// The driver stores a JSON column as json.Marshal renders it
			// (compacted), so compare with that rather than the original bytes.
			want, err := json.Marshal(conv.RawEvents[stored])
			if err != nil {
				return 0, 0, fmt.Errorf("encode raw event %d: %w", stored, err)
			}
			matching = payload.String == string(want)
		}
		if matching {
			keep++
		}
		stored++
	}
	return keep, stored, q.Err()
}

func (s *Store) insertConversation(ctx context.Context, source string, conv *model.Conversation, rows *convRows, hash string, mtime *float64) (err error) {
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
		rows.convID, source, conv.SourceNativeID, conv.Title, conv.Project,
		utc(conv.StartedAt), utc(conv.EndedAt), len(conv.Messages), hash,
		conv.RawPath, string(metadata), mtime); err != nil {
		return err
	}

	err = s.conn.Raw(func(dc any) error {
		return appendRows(dc.(driver.Conn), source, conv, rows, 0, 0)
	})
	if err != nil {
		return err
	}
	_, err = s.conn.ExecContext(ctx, "COMMIT")
	return err
}

// convRows are the message and content block rows of a conversation, as they
// are stored. Comparing them with stored rows decides what an update keeps.
type convRows struct {
	convID   string
	messages []messageRow
}

type messageRow struct {
	id        string
	nativeID  sql.NullString
	parent    sql.NullString
	role      string
	model     sql.NullString
	createdAt *time.Time // UTC, at DuckDB's precision
	metadata  sql.NullString
	blocks    []blockRow
}

type blockRow struct {
	id, typ            string
	text, toolName     sql.NullString
	toolUseID, payload sql.NullString
}

func (m *messageRow) equal(o *messageRow) bool {
	if m.id != o.id || m.nativeID != o.nativeID || m.parent != o.parent || m.role != o.role ||
		m.model != o.model || m.metadata != o.metadata || len(m.blocks) != len(o.blocks) {
		return false
	}
	if (m.createdAt == nil) != (o.createdAt == nil) || m.createdAt != nil && !m.createdAt.Equal(*o.createdAt) {
		return false
	}
	for i := range m.blocks {
		if m.blocks[i] != o.blocks[i] {
			return false
		}
	}
	return true
}

func buildRows(convID string, conv *model.Conversation) (*convRows, error) {
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

	r := &convRows{convID: convID, messages: make([]messageRow, len(conv.Messages))}
	for i, m := range conv.Messages {
		row := &r.messages[i]
		row.id = ids[i]
		row.nativeID = nullString(m.SourceNativeID)
		if m.ParentSourceNativeID != nil {
			if id, ok := byNative[*m.ParentSourceNativeID]; ok {
				row.parent = sql.NullString{String: id, Valid: true}
			}
		}
		row.role = string(m.Role)
		row.model = nullString(m.Model)
		if m.CreatedAt != nil {
			t := m.CreatedAt.UTC().Truncate(time.Microsecond)
			row.createdAt = &t
		}
		var err error
		if row.metadata, err = jsonText(m.Metadata, true); err != nil {
			return nil, err
		}
		row.blocks = make([]blockRow, len(m.Blocks))
		for j, b := range m.Blocks {
			payload, err := jsonText(b.Payload, false)
			if err != nil {
				return nil, err
			}
			row.blocks[j] = blockRow{
				id: blockID(ids[i], j), typ: string(b.Type),
				text: nullString(b.Text), toolName: nullString(b.ToolName),
				toolUseID: nullString(b.ToolUseID), payload: payload,
			}
		}
	}
	return r, nil
}

// appendRows bulk-loads messages from index fromMsg on (with their content
// blocks) and raw events from line fromEvent on, with DuckDB appenders, which
// is far faster than one INSERT per row.
func appendRows(dc driver.Conn, source string, conv *model.Conversation, rows *convRows, fromMsg, fromEvent int) error {
	messages, err := duckdb.NewAppenderFromConn(dc, "", "messages")
	if err != nil {
		return err
	}
	blocks, err := duckdb.NewAppenderFromConn(dc, "", "content_blocks")
	if err != nil {
		messages.Close()
		return err
	}
	if err := appendMessages(messages, blocks, rows, fromMsg); err != nil {
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
	for line := fromEvent; line < len(conv.RawEvents); line++ {
		if err := events.AppendRow(rawEventID(rows.convID, line), source, conv.SourceNativeID,
			conv.RawPath, int32(line), conv.RawEvents[line], now); err != nil {
			events.Close()
			return fmt.Errorf("append raw event %d: %w", line, err)
		}
	}
	if err := events.Close(); err != nil {
		return fmt.Errorf("flush raw events: %w", err)
	}
	return nil
}

func appendMessages(messages, blocks *duckdb.Appender, rows *convRows, from int) error {
	for i := from; i < len(rows.messages); i++ {
		m := &rows.messages[i]
		if err := messages.AppendRow(m.id, rows.convID, value(m.nativeID), value(m.parent), m.role,
			value(m.model), timeValue(m.createdAt), int32(i), jsonColumn(m.metadata)); err != nil {
			return fmt.Errorf("append message %d: %w", i, err)
		}
		for j, b := range m.blocks {
			if err := blocks.AppendRow(b.id, m.id, int32(j), b.typ, value(b.text), value(b.toolName),
				value(b.toolUseID), jsonColumn(b.payload)); err != nil {
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

// jsonText encodes a JSON column value. An empty map is NULL unless keepEmpty
// is set (Python stores empty metadata as {} but empty block payloads as
// NULL).
func jsonText(m map[string]any, keepEmpty bool) (sql.NullString, error) {
	if len(m) == 0 {
		if !keepEmpty {
			return sql.NullString{}, nil
		}
		return sql.NullString{String: "{}", Valid: true}, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("encode JSON column: %w", err)
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}

// jsonColumn is a JSON column value as raw JSON, which the appender stores
// verbatim.
func jsonColumn(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return json.RawMessage(s.String)
}

func nonNil(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func nullString(p *string) sql.NullString {
	if p == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *p, Valid: true}
}

// value is a nullable column value for an appender.
func value(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return s.String
}

func timeValue(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}

// utc normalizes to UTC at microsecond precision, DuckDB's TIMESTAMPTZ resolution.
func utc(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Truncate(time.Microsecond)
}

func sameString(stored sql.NullString, p *string) bool {
	return stored == nullString(p)
}

func sameTime(stored sql.NullTime, t *time.Time) bool {
	if t == nil || !stored.Valid {
		return t == nil && !stored.Valid
	}
	return stored.Time.Equal(t.UTC().Truncate(time.Microsecond))
}

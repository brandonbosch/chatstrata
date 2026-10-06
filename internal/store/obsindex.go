package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/brandonbosch/chatstrata/internal/obslog"
)

// IndexedObservation is an observation's index row: everything but content.
type IndexedObservation struct {
	obslog.Observation
	Location obslog.Location
}

// IsSegmentIndexed reports whether catch-up already read a segment file.
func (s *Store) IsSegmentIndexed(ctx context.Context, segment string) (bool, error) {
	var n int
	err := s.conn.QueryRowContext(ctx, `SELECT count(*) FROM indexed_segments WHERE segment = ?`, segment).Scan(&n)
	return n > 0, err
}

// IndexSegment records a segment's observations and marks the segment read,
// in one transaction. Observations already indexed (the same device and
// sequence number delivered twice) are ignored. It returns the keys of the
// conversations that gained observations.
func (s *Store) IndexSegment(ctx context.Context, segment string, obs []IndexedObservation) (keys []obslog.Key, err error) {
	if _, err := s.conn.ExecContext(ctx, "BEGIN TRANSACTION"); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			s.conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()
	stmt, err := s.conn.PrepareContext(ctx, `
		INSERT OR IGNORE INTO observations (
			device_id, device_seq, source_id, scope, locator, kind, byte_offset, length,
			content_sha256, observed_at, path, project_hint, legacy, segment, position
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	seen := map[obslog.Key]bool{}
	for _, o := range obs {
		res, err := stmt.ExecContext(ctx, o.Device, o.Seq, o.Source, o.Scope, o.Locator, string(o.Kind),
			o.Offset, o.Length, nullIfEmpty(o.SHA256), o.ObservedAt.UTC(), nullIfEmpty(o.Path),
			nullIfEmpty(o.ProjectHint), o.Legacy, o.Location.Segment, o.Location.Position)
		if err != nil {
			return nil, fmt.Errorf("index observation %s/%d: %w", o.Device, o.Seq, err)
		}
		if n, _ := res.RowsAffected(); n > 0 && !seen[o.Key()] {
			seen[o.Key()] = true
			keys = append(keys, o.Key())
		}
	}
	if _, err = s.conn.ExecContext(ctx, `INSERT OR IGNORE INTO indexed_segments (segment) VALUES (?)`, segment); err != nil {
		return nil, err
	}
	_, err = s.conn.ExecContext(ctx, "COMMIT")
	return keys, err
}

// ObservationsFor returns a conversation's indexed observations, ordered by
// device and sequence number.
func (s *Store) ObservationsFor(ctx context.Context, key obslog.Key) ([]IndexedObservation, error) {
	rows, err := s.conn.QueryContext(ctx, `
		SELECT device_id, device_seq, kind, byte_offset, length, content_sha256, observed_at,
		       path, project_hint, legacy, segment, position
		FROM observations
		WHERE source_id = ? AND scope = ? AND locator = ?
		ORDER BY device_id, device_seq`, key.Source, key.Scope, key.Locator)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IndexedObservation
	for rows.Next() {
		o := IndexedObservation{}
		o.Source, o.Scope, o.Locator = key.Source, key.Scope, key.Locator
		var kind string
		var sha, path, hint sql.NullString
		var observed sql.NullTime
		if err := rows.Scan(&o.Device, &o.Seq, &kind, &o.Offset, &o.Length, &sha, &observed,
			&path, &hint, &o.Legacy, &o.Location.Segment, &o.Location.Position); err != nil {
			return nil, err
		}
		o.Kind = obslog.Kind(kind)
		o.SHA256, o.Path, o.ProjectHint = sha.String, path.String, hint.String
		o.ObservedAt = observed.Time
		out = append(out, o)
	}
	return out, rows.Err()
}

// HasLegacyObservation reports whether identical legacy content for a
// conversation was already imported, so re-running an import is harmless.
func (s *Store) HasLegacyObservation(ctx context.Context, key obslog.Key, sha string) (bool, error) {
	var n int
	err := s.conn.QueryRowContext(ctx, `
		SELECT count(*) FROM observations
		WHERE source_id = ? AND scope = ? AND locator = ? AND legacy AND content_sha256 = ?`,
		key.Source, key.Scope, key.Locator, sha).Scan(&n)
	return n > 0, err
}

// IsTombstoned reports whether any device deleted the conversation.
func (s *Store) IsTombstoned(ctx context.Context, key obslog.Key) (bool, error) {
	var n int
	err := s.conn.QueryRowContext(ctx, `
		SELECT count(*) FROM observations
		WHERE source_id = ? AND scope = ? AND locator = ? AND kind = 'tombstone'`,
		key.Source, key.Scope, key.Locator).Scan(&n)
	return n > 0, err
}

// CollectorState is what this device last logged for one conversation.
type CollectorState struct {
	Length int64
	SHA256 string
	Mtime  *float64
}

// CollectorStates returns this device's collector state for a source.
func (s *Store) CollectorStates(ctx context.Context, source string) (map[obslog.Key]CollectorState, error) {
	rows, err := s.conn.QueryContext(ctx, `
		SELECT scope, locator, length, content_sha256, mtime FROM collector_state WHERE source_id = ?`, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[obslog.Key]CollectorState{}
	for rows.Next() {
		var key obslog.Key
		var st CollectorState
		var mtime sql.NullFloat64
		key.Source = source
		if err := rows.Scan(&key.Scope, &key.Locator, &st.Length, &st.SHA256, &mtime); err != nil {
			return nil, err
		}
		if mtime.Valid {
			st.Mtime = &mtime.Float64
		}
		out[key] = st
	}
	return out, rows.Err()
}

// SetCollectorState records what this device has logged for a conversation.
func (s *Store) SetCollectorState(ctx context.Context, key obslog.Key, st CollectorState) error {
	_, err := s.conn.ExecContext(ctx, `
		INSERT OR REPLACE INTO collector_state (source_id, scope, locator, length, content_sha256, mtime)
		VALUES (?, ?, ?, ?, ?, ?)`, key.Source, key.Scope, key.Locator, st.Length, st.SHA256, st.Mtime)
	return err
}

// Divergence is one branch of a conversation that devices saw differently.
type Divergence struct {
	ForkAfter     *string
	BranchMessage string
	Devices       []string
}

// ReplaceDivergences sets the divergence rows of a conversation.
func (s *Store) ReplaceDivergences(ctx context.Context, source, nativeID string, divs []Divergence) error {
	convID := conversationID(source, nativeID)
	if _, err := s.conn.ExecContext(ctx, `DELETE FROM divergences WHERE conversation_id = ?`, convID); err != nil {
		return err
	}
	for _, d := range divs {
		devices := append([]string(nil), d.Devices...)
		sort.Strings(devices)
		b, _ := json.Marshal(devices)
		if _, err := s.conn.ExecContext(ctx, `
			INSERT INTO divergences (conversation_id, fork_after, branch_message, devices)
			VALUES (?, ?, ?, ?::JSON)`, convID, d.ForkAfter, d.BranchMessage, string(b)); err != nil {
			return err
		}
	}
	return nil
}

// RemoveConversation deletes a conversation from the projection, as a
// tombstone requires. Its observations stay in the log.
func (s *Store) RemoveConversation(ctx context.Context, source, nativeID string) (bool, error) {
	var id string
	err := s.conn.QueryRowContext(ctx,
		`SELECT id FROM conversations WHERE source_id = ? AND source_native_id = ?`, source, nativeID).Scan(&id)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := s.deleteConversation(ctx, source, nativeID, id); err != nil {
		return false, err
	}
	_, err = s.conn.ExecContext(ctx, `DELETE FROM divergences WHERE conversation_id = ?`, id)
	return true, err
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// TombstonedLocators returns the conversations of a source that were deleted
// from the archive, which the collector must not record again.
func (s *Store) TombstonedLocators(ctx context.Context, source string) (map[obslog.Key]bool, error) {
	rows, err := s.conn.QueryContext(ctx, `
		SELECT DISTINCT scope, locator FROM observations WHERE source_id = ? AND kind = 'tombstone'`, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[obslog.Key]bool{}
	for rows.Next() {
		k := obslog.Key{Source: source}
		if err := rows.Scan(&k.Scope, &k.Locator); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// Checkpoint writes the write-ahead log into the database file.
func (s *Store) Checkpoint(ctx context.Context) error {
	_, err := s.conn.ExecContext(ctx, "CHECKPOINT")
	return err
}

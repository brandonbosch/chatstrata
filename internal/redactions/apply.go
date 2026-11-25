package redactions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/brandonbosch/chatstrata/v2/internal/redact"
	"github.com/brandonbosch/chatstrata/v2/internal/store"
)

// ApplyStats counts what an overlay replay did.
type ApplyStats struct {
	Runs     int
	Targets  int
	Applied  int
	Skipped  int
	Findings int
}

type targetKey struct {
	kind TargetKind
	id   string
}

// Apply replays every run in dir into the projection. Records are applied in
// hash chains: a run whose input hash matches the row changes it, then later
// runs whose input is that output are applied to the result. Stale records
// (source changed since the scan) are left in the overlay and skipped.
func Apply(ctx context.Context, s *store.Store, dir string) (ApplyStats, error) {
	var stats ApplyStats
	runs, err := ReadRuns(dir)
	if err != nil {
		return stats, err
	}
	if len(runs) == 0 {
		return stats, nil
	}
	stats.Runs = len(runs)
	if err := indexRuns(ctx, s, runs); err != nil {
		return stats, err
	}

	groups := map[targetKey][]Record{}
	for _, run := range runs {
		for _, record := range run.Records {
			key := targetKey{kind: record.TargetKind, id: record.TargetID}
			groups[key] = append(groups[key], record)
		}
	}
	stats.Targets = len(groups)

	keys := make([]targetKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].kind != keys[j].kind {
			return keys[i].kind < keys[j].kind
		}
		return keys[i].id < keys[j].id
	})

	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		records := groups[key]
		text, ok, err := readTarget(ctx, s, key)
		if err != nil {
			return stats, err
		}
		if !ok {
			stats.Skipped += len(records)
			continue
		}
		applied := make([]bool, len(records))
		for range 100 {
			hash := sha256Hex(text)
			var spans []redact.Span
			var used []int
			for i, record := range records {
				if !applied[i] && record.InputSHA256 == hash {
					spans = append(spans, record.Spans...)
					used = append(used, i)
				}
			}
			if len(used) == 0 {
				break
			}
			next, err := redact.Apply(text, spans)
			if err != nil {
				return stats, fmt.Errorf("apply %s %s: %w", key.kind, key.id, err)
			}
			for _, i := range used {
				applied[i] = true
			}
			if next == text {
				break
			}
			if err := writeTarget(ctx, s, key, next); err != nil {
				return stats, err
			}
			stats.Applied++
			stats.Findings += len(spans)
			text = next
		}
		for _, ok := range applied {
			if !ok {
				stats.Skipped++
			}
		}
	}
	return stats, nil
}

func indexRuns(ctx context.Context, s *store.Store, runs []Run) error {
	for _, run := range runs {
		if _, err := s.Conn().ExecContext(ctx, `
			INSERT OR IGNORE INTO redaction_runs (run_id, created_at, redaction_version, records)
			VALUES (?, ?, ?, ?)`,
			run.Header.RunID, run.Header.CreatedAt.UTC(), run.Header.RedactionVersion, len(run.Records)); err != nil {
			return fmt.Errorf("record redaction run %s: %w", run.Header.RunID, err)
		}
		for _, record := range run.Records {
			spans, err := json.Marshal(record.Spans)
			if err != nil {
				return fmt.Errorf("encode redaction spans: %w", err)
			}
			if _, err := s.Conn().ExecContext(ctx, `
				INSERT OR IGNORE INTO redactions (run_id, target_kind, target_id, input_sha256, spans, created_at)
				VALUES (?, ?, ?, ?, ?::JSON, ?)`,
				run.Header.RunID, string(record.TargetKind), record.TargetID, record.InputSHA256, string(spans), run.Header.CreatedAt.UTC()); err != nil {
				return fmt.Errorf("record redaction %s %s: %w", record.TargetKind, record.TargetID, err)
			}
		}
	}
	return nil
}

func readTarget(ctx context.Context, s *store.Store, key targetKey) (string, bool, error) {
	var text sql.NullString
	var err error
	switch key.kind {
	case TargetBlockText:
		err = s.Conn().QueryRowContext(ctx, `SELECT text FROM content_blocks WHERE id = ?`, key.id).Scan(&text)
	case TargetBlockPayload:
		err = s.Conn().QueryRowContext(ctx, `SELECT payload::VARCHAR FROM content_blocks WHERE id = ?`, key.id).Scan(&text)
	case TargetRawEventPayload:
		err = s.Conn().QueryRowContext(ctx, `SELECT payload::VARCHAR FROM raw_events WHERE id = ?`, key.id).Scan(&text)
	case TargetConversationTitle:
		err = s.Conn().QueryRowContext(ctx, `SELECT title FROM conversations WHERE id = ?`, key.id).Scan(&text)
	default:
		return "", false, fmt.Errorf("unknown redaction target %s", key.kind)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read %s %s: %w", key.kind, key.id, err)
	}
	return text.String, text.Valid, nil
}

func writeTarget(ctx context.Context, s *store.Store, key targetKey, text string) error {
	var query string
	switch key.kind {
	case TargetBlockText:
		query = `UPDATE content_blocks SET text = ? WHERE id = ?`
	case TargetBlockPayload:
		if !json.Valid([]byte(text)) {
			return fmt.Errorf("redaction produced invalid JSON for content block %s", key.id)
		}
		query = `UPDATE content_blocks SET payload = ?::JSON WHERE id = ?`
	case TargetRawEventPayload:
		if !json.Valid([]byte(text)) {
			return fmt.Errorf("redaction produced invalid JSON for raw event %s", key.id)
		}
		query = `UPDATE raw_events SET payload = ?::JSON WHERE id = ?`
	case TargetConversationTitle:
		query = `UPDATE conversations SET title = ? WHERE id = ?`
	default:
		return fmt.Errorf("unknown redaction target %s", key.kind)
	}
	if _, err := s.Conn().ExecContext(ctx, query, text, key.id); err != nil {
		return fmt.Errorf("write %s %s: %w", key.kind, key.id, err)
	}
	return nil
}

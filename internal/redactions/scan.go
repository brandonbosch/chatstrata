package redactions

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"

	"github.com/brandonbosch/chatstrata/v2/internal/redact"
	"github.com/brandonbosch/chatstrata/v2/internal/store"
)

// Stats counts what one scan found.
type Stats struct {
	Rows     int
	Records  int
	Findings int
	Counts   map[string]int
}

// Scan walks the redactable text columns of the projection and returns one
// record per target that has findings. It does not write anything.
func Scan(ctx context.Context, s *store.Store, engine *redact.Engine) ([]Record, Stats, error) {
	var stats Stats
	var out []Record
	queries := []struct {
		kind TargetKind
		sql  string
		json bool
	}{
		{TargetBlockText, `
			SELECT cb.id, cb.text, c.project, c.raw_path
			FROM content_blocks cb
			JOIN messages m ON m.id = cb.message_id
			JOIN conversations c ON c.id = m.conversation_id
			WHERE cb.text IS NOT NULL AND cb.text <> ''
			ORDER BY cb.id`, false},
		{TargetBlockPayload, `
			SELECT cb.id, cb.payload::VARCHAR, c.project, c.raw_path
			FROM content_blocks cb
			JOIN messages m ON m.id = cb.message_id
			JOIN conversations c ON c.id = m.conversation_id
			WHERE cb.payload IS NOT NULL
			ORDER BY cb.id`, true},
		{TargetRawEventPayload, `
			SELECT re.id, re.payload::VARCHAR, c.project, c.raw_path
			FROM raw_events re
			LEFT JOIN conversations c
			  ON c.source_id = re.source_id
			 AND c.source_native_id = re.source_native_conversation_id
			WHERE re.payload IS NOT NULL
			ORDER BY re.id`, true},
		{TargetConversationTitle, `
			SELECT c.id, c.title, c.project, c.raw_path
			FROM conversations c
			WHERE c.title IS NOT NULL AND c.title <> ''
			ORDER BY c.id`, false},
	}
	for _, q := range queries {
		records, err := scanQuery(ctx, s, engine, q.kind, q.sql, q.json, &stats)
		if err != nil {
			return nil, stats, err
		}
		out = append(out, records...)
	}
	if stats.Counts == nil {
		stats.Counts = map[string]int{}
	}
	return out, stats, nil
}

func scanQuery(ctx context.Context, s *store.Store, engine *redact.Engine, kind TargetKind, query string, jsonTarget bool, stats *Stats) ([]Record, error) {
	rows, err := s.Conn().QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", kind, err)
	}
	defer func() { _ = rows.Close() }()
	var out []Record
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var id, text string
		var project, path sql.NullString
		if err := rows.Scan(&id, &text, &project, &path); err != nil {
			return nil, fmt.Errorf("scan %s: %w", kind, err)
		}
		if text == "" {
			continue
		}
		stats.Rows++
		scope := redact.Scope{Project: project.String, Path: path.String}
		var spans []redact.Span
		if jsonTarget {
			spans, err = scanJSONValue(engine, scope, text)
			if err != nil {
				return nil, fmt.Errorf("redact %s %s: %w", kind, id, err)
			}
		} else {
			var result redact.Result
			result, err = engine.RedactScope(scope, text)
			if err != nil {
				return nil, fmt.Errorf("redact %s %s: %w", kind, id, err)
			}
			spans = make([]redact.Span, len(result.Findings))
			for i, f := range result.Findings {
				spans[i] = f.Span()
			}
		}
		if len(spans) == 0 {
			continue
		}
		for _, span := range spans {
			stats.Findings++
			if stats.Counts == nil {
				stats.Counts = map[string]int{}
			}
			stats.Counts[span.RuleID]++
		}
		out = append(out, Record{
			TargetKind:  kind,
			TargetID:    id,
			InputSHA256: sha256Hex(text),
			Spans:       spans,
		})
		stats.Records++
	}
	return out, rows.Err()
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

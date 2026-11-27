package label

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/brandonbosch/chatstrata/v2/internal/store"
)

// ReplayStats reports what replaying chatstrata-go.labels/ into DuckDB did.
type ReplayStats struct {
	Runs    int
	Applied int
	Skipped int
}

// Replay projects every run file in dir into DuckDB in creation order.
// Only answers whose target_id currently exists in the archive are kept, and
// latest (pack, target_id, question) wins. When full is false, runs already
// recorded in label_runs are skipped so normal catch-up stays cheap.
func Replay(ctx context.Context, s *store.Store, dir string, full bool) (ReplayStats, error) {
	var stats ReplayStats
	runs, err := ReadRuns(dir)
	if err != nil {
		return stats, err
	}
	if len(runs) == 0 {
		return stats, nil
	}

	var pending []Run
	if full {
		pending = runs
	} else {
		seen, err := indexedRunIDs(ctx, s)
		if err != nil {
			return stats, err
		}
		for _, r := range runs {
			if !seen[r.Header.RunID] {
				pending = append(pending, r)
			}
		}
	}
	if len(pending) == 0 {
		return stats, nil
	}

	existing := map[string]map[string]struct{}{}
	for _, r := range pending {
		for _, rec := range r.Records {
			if _, ok := existing[rec.TargetKind]; ok {
				continue
			}
			ids, err := loadExistingTargets(ctx, s, rec.TargetKind)
			if err != nil {
				return stats, err
			}
			existing[rec.TargetKind] = ids
		}
	}

	for _, run := range pending {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		applied, skipped, err := projectRun(ctx, s, run, existing)
		if err != nil {
			return stats, err
		}
		stats.Runs++
		stats.Applied += applied
		stats.Skipped += skipped
	}
	return stats, nil
}

func indexedRunIDs(ctx context.Context, s *store.Store) (map[string]bool, error) {
	rows, err := s.Conn().QueryContext(ctx, `SELECT id FROM label_runs`)
	if err != nil {
		return nil, fmt.Errorf("read label_runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func loadExistingTargets(ctx context.Context, s *store.Store, kind string) (map[string]struct{}, error) {
	var query string
	switch kind {
	case TargetConversation:
		query = `SELECT id FROM conversations`
	case TargetMessage:
		query = `SELECT id FROM messages`
	case TargetToolCall:
		query = `SELECT id FROM content_blocks WHERE type = 'tool_use'`
	default:
		return nil, fmt.Errorf("unknown label target_kind %q", kind)
	}
	rows, err := s.Conn().QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("load existing %s targets: %w", kind, err)
	}
	defer func() { _ = rows.Close() }()
	ids := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = struct{}{}
	}
	return ids, rows.Err()
}

func projectRun(ctx context.Context, s *store.Store, run Run, existing map[string]map[string]struct{}) (applied, skipped int, err error) {
	tx, err := s.Conn().BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("project label run %s: %w", run.Header.RunID, err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := upsertRunTx(ctx, tx, run.Header); err != nil {
		return 0, 0, err
	}
	stmt, err := tx.PrepareContext(ctx, upsertLabelSQL)
	if err != nil {
		return 0, 0, fmt.Errorf("prepare label upsert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, rec := range run.Records {
		if _, ok := existing[rec.TargetKind][rec.TargetID]; !ok {
			skipped++
			continue
		}
		n, err := execAnswerStmt(ctx, stmt, run.Header, rec)
		if err != nil {
			return 0, 0, err
		}
		applied += n
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit label run %s: %w", run.Header.RunID, err)
	}
	return applied, skipped, nil
}

const upsertRunSQL = `
INSERT OR REPLACE INTO label_runs (
	id, pack, pack_version, backend, model,
	started_at, finished_at, targets, failures, input_tokens, redaction_version
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

const upsertLabelSQL = `
INSERT OR REPLACE INTO labels (
	pack, target_kind, target_id, question, answer_type,
	value, choice, confidence, probabilities,
	pack_version, model, run_id, created_at, target_fingerprint
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?::JSON, ?, ?, ?, ?, ?)`

// UpsertRunHeader records or updates one run in label_runs.
func UpsertRunHeader(ctx context.Context, s *store.Store, h RunHeader) error {
	var finished any
	if !h.FinishedAt.IsZero() {
		finished = h.FinishedAt.UTC()
	}
	_, err := s.Conn().ExecContext(ctx, upsertRunSQL,
		h.RunID, h.Pack, h.PackVersion, h.Backend, nullableString(h.Model),
		h.StartedAt.UTC(), finished, h.Targets, h.Failures, h.InputTokens, nullableString(h.RedactionVersion))
	if err != nil {
		return fmt.Errorf("upsert label_runs %s: %w", h.RunID, err)
	}
	return nil
}

func upsertRunTx(ctx context.Context, tx *sql.Tx, h RunHeader) error {
	var finished any
	if !h.FinishedAt.IsZero() {
		finished = h.FinishedAt.UTC()
	}
	_, err := tx.ExecContext(ctx, upsertRunSQL,
		h.RunID, h.Pack, h.PackVersion, h.Backend, nullableString(h.Model),
		h.StartedAt.UTC(), finished, h.Targets, h.Failures, h.InputTokens, nullableString(h.RedactionVersion))
	if err != nil {
		return fmt.Errorf("upsert label_runs %s: %w", h.RunID, err)
	}
	return nil
}

// UpsertAnswer writes all question answers from one target into DuckDB.
func UpsertAnswer(ctx context.Context, s *store.Store, h RunHeader, rec AnswerRecord) (int, error) {
	tx, err := s.Conn().BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, upsertLabelSQL)
	if err != nil {
		return 0, err
	}
	defer func() { _ = stmt.Close() }()
	n, err := execAnswerStmt(ctx, stmt, h, rec)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

func execAnswerStmt(ctx context.Context, stmt *sql.Stmt, h RunHeader, rec AnswerRecord) (int, error) {
	questions := make([]string, 0, len(rec.Answers))
	for q := range rec.Answers {
		questions = append(questions, q)
	}
	sort.Strings(questions)

	model := rec.Model
	if model == "" {
		model = h.Model
	}
	n := 0
	for _, q := range questions {
		ans := rec.Answers[q]
		var val, choice, conf, probs any
		switch ans.Type {
		case "noul":
			if ans.Noul != nil {
				val = *ans.Noul
			}
		case "choice":
			if ans.Choice != nil {
				choice = *ans.Choice
			}
			if ans.Confidence != nil {
				conf = *ans.Confidence
			}
			if len(ans.Probabilities) > 0 {
				b, err := json.Marshal(ans.Probabilities)
				if err != nil {
					return n, fmt.Errorf("marshal probabilities for %s.%s: %w", rec.TargetID, q, err)
				}
				probs = string(b)
			}
		case "score":
			if ans.Score != nil {
				val = *ans.Score
			}
			if ans.Confidence != nil {
				conf = *ans.Confidence
			}
			if len(ans.Probabilities) > 0 {
				b, err := json.Marshal(ans.Probabilities)
				if err != nil {
					return n, fmt.Errorf("marshal probabilities for %s.%s: %w", rec.TargetID, q, err)
				}
				probs = string(b)
			}
		default:
			continue
		}
		if _, err := stmt.ExecContext(ctx,
			h.Pack, rec.TargetKind, rec.TargetID, q, ans.Type,
			val, choice, conf, probs,
			h.PackVersion, nullableString(model), h.RunID,
			rec.CreatedAt.UTC(), nullableString(rec.TargetFingerprint)); err != nil {
			return n, fmt.Errorf("upsert label %s/%s/%s: %w", h.Pack, rec.TargetID, q, err)
		}
		n++
	}
	return n, nil
}

// Clear removes matching runs from the durable sidecar log and refreshes the
// DuckDB projection for the affected pack so rebuild stays consistent.
func Clear(ctx context.Context, s *store.Store, dir string, filter ClearFilter) (int, error) {
	removed, err := DeleteRuns(dir, filter)
	if err != nil {
		return 0, err
	}
	tx, err := s.Conn().BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if filter.Pack != "" && filter.PackVersion == "" && filter.RunID == "" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM labels WHERE pack = ?`, filter.Pack); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM label_runs WHERE pack = ?`, filter.Pack); err != nil {
			return 0, err
		}
		return len(removed), tx.Commit()
	}

	// Narrower filter (version or run): remove matching rows, then replay
	// remaining runs for the affected pack(s) so any earlier runs resurface.
	if filter.RunID != "" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM labels WHERE run_id = ?`, filter.RunID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM label_runs WHERE id = ?`, filter.RunID); err != nil {
			return 0, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `DELETE FROM labels WHERE pack = ? AND pack_version = ?`, filter.Pack, filter.PackVersion); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM label_runs WHERE pack = ? AND pack_version = ?`, filter.Pack, filter.PackVersion); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if _, err := Replay(ctx, s, dir, true); err != nil {
		return len(removed), fmt.Errorf("replay remaining label runs after clear: %w", err)
	}
	return len(removed), nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

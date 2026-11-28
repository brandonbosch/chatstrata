package label

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/brandonbosch/chatstrata/v2/internal/store"
)

// SummaryRow is one aggregated row returned by Summarize.
type SummaryRow struct {
	Group    string   `json:"group"`
	Question string   `json:"question"`
	Answer   string   `json:"answer"`
	Count    int64    `json:"n"`
	SharePct *float64 `json:"share_pct,omitempty"`
	Mean     *float64 `json:"mean,omitempty"`
	AvgConf  *float64 `json:"avg_conf,omitempty"`
}

var summaryDimensions = map[string]map[string]string{
	TargetConversation: {
		"source":  "c.source_id",
		"project": "coalesce(c.project, '(none)')",
		"month":   "strftime(date_trunc('month', coalesce(c.started_at, c.ingested_at)), '%Y-%m')",
		"quarter": "strftime(coalesce(c.started_at, c.ingested_at), '%Y') || '-Q' || CAST(quarter(coalesce(c.started_at, c.ingested_at)) AS VARCHAR)",
	},
	TargetMessage: {
		"source":  "c.source_id",
		"project": "coalesce(c.project, '(none)')",
		"month":   "strftime(date_trunc('month', m.created_at), '%Y-%m')",
		"quarter": "strftime(m.created_at, '%Y') || '-Q' || CAST(quarter(m.created_at) AS VARCHAR)",
	},
	TargetToolCall: {
		"source":  "c.source_id",
		"tool":    "cb.tool_name",
		"model":   "coalesce(m.model, '(unknown)')",
		"project": "coalesce(c.project, '(none)')",
		"month":   "strftime(date_trunc('month', m.created_at), '%Y-%m')",
		"quarter": "strftime(m.created_at, '%Y') || '-Q' || CAST(quarter(m.created_at) AS VARCHAR)",
	},
}

var summaryJoins = map[string]string{
	TargetConversation: `JOIN conversations c ON c.id = l.target_id`,
	TargetMessage: `
		JOIN messages m ON m.id = l.target_id
		JOIN conversations c ON c.id = m.conversation_id`,
	TargetToolCall: `
		JOIN content_blocks cb ON cb.id = l.target_id
		JOIN messages m ON m.id = cb.message_id
		JOIN conversations c ON c.id = m.conversation_id`,
}

// Summarize aggregates a pack's labels, optionally grouped by a dimension.
func Summarize(ctx context.Context, s *store.Store, pack *Pack, by string, minConfidence float64) ([]SummaryRow, error) {
	groupExpr := "'all'"
	if by != "" {
		dims := summaryDimensions[pack.TargetKind]
		expr, ok := dims[by]
		if !ok {
			allowed := make([]string, 0, len(dims))
			for k := range dims {
				allowed = append(allowed, k)
			}
			sort.Strings(allowed)
			return nil, fmt.Errorf("--by must be one of: %s", strings.Join(allowed, ", "))
		}
		groupExpr = expr
	}

	query := fmt.Sprintf(`
		WITH l AS (
			SELECT l.*, %s AS grp
			FROM labels l
			%s
			WHERE l.pack = ?
			  AND (l.confidence IS NULL OR l.confidence >= ?)
		)
		SELECT CAST(coalesce(grp, '(none)') AS VARCHAR) AS grp, question, choice AS answer, COUNT(*) AS n,
		       ROUND(100.0 * COUNT(*) / SUM(COUNT(*)) OVER (PARTITION BY grp, question), 1) AS share_pct,
		       NULL::DOUBLE AS mean, ROUND(AVG(confidence), 2) AS avg_conf
		FROM l WHERE answer_type = 'choice'
		GROUP BY grp, question, choice
		UNION ALL
		SELECT CAST(coalesce(grp, '(none)') AS VARCHAR), question, 'yes (p > 0.5)', COUNT(*),
		       ROUND(100.0 * AVG(CASE WHEN value > 0.5 THEN 1 ELSE 0 END), 1),
		       ROUND(AVG(value), 2), NULL
		FROM l WHERE answer_type = 'noul'
		GROUP BY grp, question
		UNION ALL
		SELECT CAST(coalesce(grp, '(none)') AS VARCHAR), question, 'score', COUNT(*),
		       NULL, ROUND(AVG(value), 2), ROUND(AVG(confidence), 2)
		FROM l WHERE answer_type = 'score'
		GROUP BY grp, question
		ORDER BY 1, 2, 4 DESC`, groupExpr, summaryJoins[pack.TargetKind])

	rows, err := s.Conn().QueryContext(ctx, query, pack.Name, minConfidence)
	if err != nil {
		return nil, fmt.Errorf("summarize %s: %w", pack.Name, err)
	}
	defer func() { _ = rows.Close() }()

	var out []SummaryRow
	for rows.Next() {
		var r SummaryRow
		var share, mean, conf sql.NullFloat64
		if err := rows.Scan(&r.Group, &r.Question, &r.Answer, &r.Count, &share, &mean, &conf); err != nil {
			return nil, err
		}
		if share.Valid {
			v := share.Float64
			r.SharePct = &v
		}
		if mean.Valid {
			v := mean.Float64
			r.Mean = &v
		}
		if conf.Valid {
			v := conf.Float64
			r.AvgConf = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListRuns returns recorded label runs from label_runs, newest first.
func ListRuns(ctx context.Context, s *store.Store, packFilter string) ([]RunHeader, error) {
	query := `
		SELECT id, pack, pack_version, backend, model,
		       started_at, finished_at, targets, failures, input_tokens, redaction_version
		FROM label_runs`
	var args []any
	if packFilter != "" {
		query += ` WHERE pack = ?`
		args = append(args, packFilter)
	}
	query += ` ORDER BY started_at DESC, id DESC`

	rows, err := s.Conn().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list label_runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []RunHeader
	for rows.Next() {
		var h RunHeader
		var model, redVer sql.NullString
		var finished sql.NullTime
		var started time.Time
		if err := rows.Scan(&h.RunID, &h.Pack, &h.PackVersion, &h.Backend, &model,
			&started, &finished, &h.Targets, &h.Failures, &h.InputTokens, &redVer); err != nil {
			return nil, err
		}
		h.Model = model.String
		h.RedactionVersion = redVer.String
		h.StartedAt = started
		if finished.Valid {
			h.FinishedAt = finished.Time
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

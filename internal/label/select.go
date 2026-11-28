package label

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/brandonbosch/chatstrata/v2/internal/redact"
	"github.com/brandonbosch/chatstrata/v2/internal/store"
)

// DefaultMinAge is the default settle window for conversation packs so active
// sessions are not labeled mid-flight (ADR 0007).
const DefaultMinAge = 30 * time.Minute

// TargetFilters narrows which targets SelectTargets returns.
type TargetFilters struct {
	Source          string
	Projects        []string
	ExcludeProjects []string
	Tool            string
	Since           *time.Time
	Until           *time.Time
	MinAge          time.Duration
	Limit           int
	Force           bool
	Now             time.Time // test hook; defaults to time.Now().UTC()
}

// Target is one redacted, ready-to-send item selected for a pack.
type Target struct {
	ID          string
	SourceID    string
	Project     string
	RawPath     string
	Fingerprint string
	State       map[string]any
}

// Selection is the pre-flight result of target selection and egress redaction.
type Selection struct {
	Targets           []Target
	ExcludedByProject int
	RedactionVersion  string
	RedactionFindings int
	RedactionCounts   map[string]int
}

// CheckDependencies verifies that every pack in pack.DependsOn already has at
// least one row in labels.
func CheckDependencies(ctx context.Context, s *store.Store, pack *Pack) error {
	for _, dep := range pack.DependsOn {
		var n int
		if err := s.Conn().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM labels WHERE pack = ?`, dep).Scan(&n); err != nil {
			return fmt.Errorf("check pack dependency %q: %w", dep, err)
		}
		if n == 0 {
			return fmt.Errorf("pack %q depends on %q, which has no labels yet; run `chatstrata label run %s` first",
				pack.Name, dep, dep)
		}
	}
	return nil
}

// SelectTargets queries pack.TargetSQL with filters and incremental freshness
// skipping, enforces gate's project boundary, and redacts every string value
// in each target's state (fail-closed).
func SelectTargets(ctx context.Context, s *store.Store, pack *Pack, gate *redact.Gate, filters TargetFilters) (*Selection, error) {
	if gate == nil {
		return nil, errors.New("label selection requires a redaction gate")
	}
	if err := CheckDependencies(ctx, s, pack); err != nil {
		return nil, err
	}
	if filters.Tool != "" && pack.TargetKind != TargetToolCall {
		return nil, fmt.Errorf("--tool only applies to tool_call packs, not %q", pack.Name)
	}

	cols, err := probeColumns(ctx, s, pack.TargetSQL)
	if err != nil {
		return nil, fmt.Errorf("validate target_sql for %q: %w", pack.Name, err)
	}
	if !cols["target_id"] || !cols["source_id"] || !cols["created_at"] {
		return nil, fmt.Errorf("pack %q target_sql must return target_id, source_id, and created_at", pack.Name)
	}

	var clauses []string
	var params []any
	if filters.Source != "" {
		clauses = append(clauses, "t.source_id = ?")
		params = append(params, filters.Source)
	}
	if len(filters.Projects) > 0 && cols["project"] {
		clauses = append(clauses, "t.project IN ("+placeholders(len(filters.Projects))+")")
		for _, p := range filters.Projects {
			params = append(params, p)
		}
	}
	if len(filters.ExcludeProjects) > 0 && cols["project"] {
		clauses = append(clauses, "(t.project IS NULL OR t.project NOT IN ("+placeholders(len(filters.ExcludeProjects))+"))")
		for _, p := range filters.ExcludeProjects {
			params = append(params, p)
		}
	}
	if filters.Tool != "" {
		if !cols["tool_name"] {
			return nil, fmt.Errorf("pack %q target_sql has no tool_name column", pack.Name)
		}
		clauses = append(clauses, "t.tool_name = ?")
		params = append(params, filters.Tool)
	}
	if filters.Since != nil {
		clauses = append(clauses, "t.created_at >= ?")
		params = append(params, filters.Since.UTC())
	}
	if filters.Until != nil {
		clauses = append(clauses, "t.created_at < ?")
		params = append(params, filters.Until.UTC())
	}
	if filters.MinAge > 0 && pack.TargetKind == TargetConversation {
		now := filters.Now
		if now.IsZero() {
			now = time.Now().UTC()
		}
		cutoff := now.Add(-filters.MinAge).UTC()
		ageCol := "t.created_at"
		if cols["ended_at"] {
			ageCol = "t.ended_at"
		}
		clauses = append(clauses, ageCol+" <= ?")
		params = append(params, cutoff)
	}
	if !filters.Force {
		if pack.TargetKind == TargetConversation && cols["target_fingerprint"] {
			clauses = append(clauses, `NOT EXISTS (
				SELECT 1 FROM labels l
				WHERE l.pack = ? AND l.pack_version = ? AND l.target_id = t.target_id
				  AND coalesce(l.target_fingerprint, '') = coalesce(CAST(t.target_fingerprint AS VARCHAR), '')
			)`)
			params = append(params, pack.Name, pack.Version())
		} else {
			clauses = append(clauses, `NOT EXISTS (
				SELECT 1 FROM labels l
				WHERE l.pack = ? AND l.pack_version = ? AND l.target_id = t.target_id
			)`)
			params = append(params, pack.Name, pack.Version())
		}
	}

	where := "TRUE"
	if len(clauses) > 0 {
		where = strings.Join(clauses, " AND ")
	}
	query := fmt.Sprintf("SELECT * FROM (%s) t WHERE %s ORDER BY t.created_at, t.target_id",
		strings.TrimSuffix(pack.TargetSQL, ";"), where)
	if filters.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", filters.Limit)
	}

	rows, err := s.Conn().QueryContext(ctx, query, params...)
	if err != nil {
		return nil, fmt.Errorf("select targets for %q: %w", pack.Name, err)
	}
	defer func() { _ = rows.Close() }()

	colNames, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	sel := &Selection{
		RedactionVersion: gate.Version(),
		RedactionCounts:  map[string]int{},
	}
	for rows.Next() {
		rawVals := make([]any, len(colNames))
		ptrs := make([]any, len(colNames))
		for i := range rawVals {
			ptrs[i] = &rawVals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("scan target row: %w", err)
		}
		row := make(map[string]any, len(colNames))
		for i, name := range colNames {
			row[name] = normalizeSQLValue(rawVals[i])
		}

		project := stringVal(row["project"])
		if err := gate.CheckProject(project); err != nil {
			if errors.Is(err, redact.ErrProjectDenied) {
				sel.ExcludedByProject++
				continue
			}
			return nil, err
		}
		rawPath := stringVal(row["raw_path"])
		state := pack.BuildState(row)
		redactedState, report, err := redactState(gate, redact.Scope{Project: project, Path: rawPath}, state)
		if err != nil {
			return nil, fmt.Errorf("redact target %s: %w", stringVal(row["target_id"]), err)
		}
		sel.RedactionFindings += report.Findings
		for rule, n := range report.Counts {
			sel.RedactionCounts[rule] += n
		}
		sel.Targets = append(sel.Targets, Target{
			ID:          stringVal(row["target_id"]),
			SourceID:    stringVal(row["source_id"]),
			Project:     project,
			RawPath:     rawPath,
			Fingerprint: stringVal(row["target_fingerprint"]),
			State:       redactedState,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return sel, nil
}

// redactState passes every string field in state through gate.RedactFieldsScope
// (fail-closed) and preserves non-string scalar fields. Any nested non-scalar
// values are serialized as JSON strings so no unredacted text can slip through.
func redactState(gate *redact.Gate, scope redact.Scope, state map[string]any) (map[string]any, redact.Report, error) {
	strFields := make(map[string]string, len(state))
	scalars := make(map[string]any, len(state))
	for k, v := range state {
		switch x := v.(type) {
		case string:
			strFields[k] = x
		case []byte:
			strFields[k] = string(x)
		case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
			scalars[k] = x
		default:
			b, err := json.Marshal(x)
			if err != nil {
				return nil, redact.Report{}, fmt.Errorf("serialize field %q: %w", k, err)
			}
			strFields[k] = string(b)
		}
	}
	redacted, report, err := gate.RedactFieldsScope(scope, strFields)
	if err != nil {
		return nil, redact.Report{}, err
	}
	out := make(map[string]any, len(state))
	for k, v := range scalars {
		out[k] = v
	}
	for k, v := range redacted {
		out[k] = v
	}
	return out, report, nil
}

func probeColumns(ctx context.Context, s *store.Store, targetSQL string) (map[string]bool, error) {
	q := fmt.Sprintf("SELECT * FROM (%s) t WHERE FALSE", strings.TrimSuffix(targetSQL, ";"))
	rows, err := s.Conn().QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	names, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	cols := make(map[string]bool, len(names))
	for _, n := range names {
		cols[n] = true
	}
	return cols, nil
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func normalizeSQLValue(v any) any {
	switch x := v.(type) {
	case []byte:
		return string(x)
	default:
		return x
	}
}

func stringVal(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// EstimateTokens estimates total input tokens (~4 chars/token) across all
// targets plus the pack's questions.
func EstimateTokens(pack *Pack, targets []Target) int64 {
	if len(targets) == 0 {
		return 0
	}
	qb, _ := json.Marshal(pack.Questions)
	qChars := int64(len(qb))
	var stateChars int64
	for _, t := range targets {
		sb, _ := json.Marshal(t.State)
		stateChars += int64(len(sb))
	}
	return (stateChars + qChars*int64(len(targets))) / 4
}

// EstimateCostUSD estimates USD cost for input tokens at pricePerMTok.
func EstimateCostUSD(tokens int64, pricePerMTok float64) float64 {
	if tokens <= 0 || pricePerMTok <= 0 {
		return 0
	}
	return float64(tokens) / 1_000_000.0 * pricePerMTok
}

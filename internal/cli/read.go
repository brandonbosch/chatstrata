package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/brandonbosch/chatstrata/internal/store"
)

// openForReading opens the archive read-only. A missing archive is reported
// once, as Python does for an empty one, and yields a nil store.
func openForReading(e *env, db string) (*store.Store, error) {
	dbPath, err := store.ResolvePath(db)
	if err != nil {
		return nil, err
	}
	s, err := store.OpenReadOnly(e.ctx, dbPath)
	if errors.Is(err, store.ErrNoArchive) {
		fmt.Fprintf(e.stdout, "No archive at %s yet. Try `chatstrata ingest claude_code`.\n", dbPath)
		return nil, nil
	}
	return s, err
}

// Read-only enforcement, ported from chatstrata/mcp/safety.py.
var (
	mutatingKeywords = map[string]bool{
		"INSERT": true, "UPDATE": true, "DELETE": true, "DROP": true, "ALTER": true,
		"CREATE": true, "TRUNCATE": true, "COPY": true, "EXPORT": true, "ATTACH": true,
		"DETACH": true, "LOAD": true, "INSTALL": true, "SET": true, "GRANT": true,
		"REVOKE": true, "BEGIN": true, "COMMIT": true, "ROLLBACK": true, "VACUUM": true,
		"CHECKPOINT": true,
	}
	sqlComment = regexp.MustCompile(`(?s)--[^\n]*|/\*.*?\*/`)
)

func validateReadOnly(query string) error {
	stripped := strings.TrimSpace(sqlComment.ReplaceAllString(query, " "))
	if stripped == "" {
		return errors.New("Empty query.")
	}
	if strings.Contains(strings.TrimRight(stripped, ";"), ";") {
		return errors.New("Multi-statement queries are not allowed.")
	}
	first := strings.ToUpper(strings.Fields(stripped)[0])
	if mutatingKeywords[first] {
		return fmt.Errorf("%s queries are not allowed. Only SELECT/WITH/DESCRIBE/SHOW/PRAGMA are permitted.", first)
	}
	return nil
}

const (
	queryTimeout   = 30 * time.Second
	maxRows        = 500
	maxResultBytes = 512_000
)

func runQuery(e *env, args []string) error {
	fs := newFlagSet(e, "query", `query SQL [--db PATH] [--json]`)
	db := fs.String("db", "", "Override the database path.")
	asJSON := fs.Bool("json", false, "Output rows as JSON.")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return usageError{"expected exactly one SQL argument"}
	}
	if err := validateReadOnly(positional[0]); err != nil {
		return usageError{err.Error()}
	}
	s, err := openForReading(e, *db)
	if s == nil || err != nil {
		return err
	}
	defer s.Close()

	ctx, cancel := context.WithTimeout(e.ctx, queryTimeout)
	defer cancel()
	rows, err := s.Conn().QueryContext(ctx, positional[0])
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("query exceeded %s timeout", queryTimeout)
		}
		return err
	}
	defer rows.Close()
	cols, values, truncated, err := collect(rows)
	if err != nil {
		return err
	}

	if *asJSON {
		return writeJSONRows(e.stdout, cols, values)
	}
	if len(cols) > 0 {
		fmt.Fprintln(e.stdout, strings.Join(cols, "\t"))
		dashes := make([]string, len(cols))
		for i, c := range cols {
			dashes[i] = strings.Repeat("-", max(3, len([]rune(c))))
		}
		fmt.Fprintln(e.stdout, strings.Join(dashes, "\t"))
	}
	for _, row := range values {
		cells := make([]string, len(row))
		for i, v := range row {
			cells[i] = formatValue(v)
		}
		fmt.Fprintln(e.stdout, strings.Join(cells, "\t"))
	}
	if truncated {
		fmt.Fprintln(e.stdout, "\nResults truncated. Refine the query or add LIMIT.")
	}
	return nil
}

// collect reads up to maxRows rows and trims the result to maxResultBytes,
// the same limits the MCP server applies.
func collect(rows *sql.Rows) ([]string, [][]any, bool, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, false, err
	}
	var values [][]any
	truncated := false
	for rows.Next() {
		if len(values) == maxRows {
			truncated = true
			break
		}
		row := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range row {
			ptrs[i] = &row[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, false, err
		}
		values = append(values, row)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, false, err
	}

	size := 0
	for _, row := range values {
		for _, v := range row {
			size += len(formatValue(v)) + 4
		}
	}
	if size > maxResultBytes {
		keep := max(1, int(float64(len(values))*float64(maxResultBytes)/float64(size)*0.9))
		values = values[:keep]
		truncated = true
	}
	return cols, values, truncated, nil
}

func writeJSONRows(w io.Writer, cols []string, values [][]any) error {
	// Build each object by hand to keep the query's column order.
	var b strings.Builder
	b.WriteString("[")
	for i, row := range values {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n  {")
		for j, col := range cols {
			if j > 0 {
				b.WriteString(",")
			}
			key, _ := json.Marshal(col)
			val, err := json.Marshal(jsonValue(row[j]))
			if err != nil {
				return err
			}
			fmt.Fprintf(&b, "\n    %s: %s", key, val)
		}
		b.WriteString("\n  }")
	}
	if len(values) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("]\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// jsonValue mirrors json.dumps(default=str): values JSON can't hold become strings.
func jsonValue(v any) any {
	switch x := v.(type) {
	case time.Time:
		return pyTime(x)
	case []byte:
		return string(x)
	}
	return v
}

// formatValue renders a cell the way Python's str() does.
func formatValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case time.Time:
		return pyTime(x)
	case float64:
		return pyFloat(x)
	case float32:
		return pyFloat(float64(x))
	case bool:
		if x {
			return "True"
		}
		return "False"
	case []byte:
		return string(x)
	case map[string]any, []any:
		b, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprint(x)
		}
		return string(b)
	}
	return fmt.Sprint(v)
}

// pyTime formats like str(datetime) for a TIMESTAMPTZ value, which Python's
// DuckDB client returns in the session time zone, the machine's local zone.
func pyTime(t time.Time) string {
	t = t.In(time.Local)
	s := t.Format("2006-01-02 15:04:05")
	if us := t.Nanosecond() / 1000; us != 0 {
		s += fmt.Sprintf(".%06d", us)
	}
	return s + t.Format("-07:00")
}

// pyFloat formats like repr(float).
func pyFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	if abs := math.Abs(f); abs != 0 && (abs < 1e-4 || abs >= 1e16) {
		return strconv.FormatFloat(f, 'e', -1, 64)
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

func runSearch(e *env, args []string) error {
	fs := newFlagSet(e, "search", `search QUERY [--db PATH] [--source NAME] [--since YYYY-MM-DD] [--until YYYY-MM-DD] [--limit N] [--json]`)
	db := fs.String("db", "", "Override the database path.")
	source := fs.String("source", "", "Filter to a specific source (e.g. claude_code).")
	since := fs.String("since", "", "Only include messages after this date (YYYY-MM-DD).")
	until := fs.String("until", "", "Only include messages before this date (YYYY-MM-DD).")
	limit := fs.Int("limit", 20, "Maximum number of results.")
	asJSON := fs.Bool("json", false, "Output results as JSON.")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return usageError{"expected exactly one QUERY"}
	}
	query := positional[0]

	var conditions []string
	var params []any
	if *source != "" {
		conditions = append(conditions, "c.source_id = ?")
		params = append(params, *source)
	}
	for _, bound := range []struct {
		value, op, flag string
	}{{*since, ">=", "--since"}, {*until, "<", "--until"}} {
		if bound.value == "" {
			continue
		}
		t, err := time.Parse("2006-01-02", bound.value)
		if err != nil {
			return usageError{fmt.Sprintf("%s must be YYYY-MM-DD", bound.flag)}
		}
		conditions = append(conditions, "m.created_at "+bound.op+" ?")
		params = append(params, t)
	}

	s, err := openForReading(e, *db)
	if s == nil || err != nil {
		return err
	}
	defer s.Close()

	results, err := searchFTS(e.ctx, s, query, conditions, params, *limit)
	substring := err != nil
	if substring {
		// No index or no FTS extension: fall back to substring search.
		results, err = searchSubstring(e.ctx, s, query, conditions, params, *limit)
		if err != nil {
			return err
		}
	}

	if len(results) == 0 && !*asJSON {
		// Ingest and sync keep the full-text index current, so an empty FTS
		// result means nothing matched. Only point at a fix when search had
		// to fall back to plain substring matching.
		switch {
		case !substring:
			fmt.Fprintln(e.stdout, "No results.")
		case s.LoadFTS(e.ctx):
			fmt.Fprintln(e.stdout, "No results (substring match). Run `chatstrata reindex` for full-text search.")
		default:
			fmt.Fprintln(e.stdout, "No results (substring match). Run `chatstrata reindex --install-fts` once for full-text search.")
		}
		return nil
	}
	if *asJSON {
		out := make([]map[string]any, len(results))
		for i, r := range results {
			var created any
			if r.createdAt.Valid {
				created = pyTime(r.createdAt.Time)
			}
			out[i] = map[string]any{
				"score": r.score, "conversation_id": r.conversationID, "title": nullable(r.title),
				"source": r.source, "project": nullable(r.project), "role": r.role,
				"created_at": created, "snippet": snippet(r.text.String, query),
			}
		}
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Fprintf(e.stdout, "  (keyword search, %d results)\n\n", len(results))
	for i, r := range results {
		if i > 0 {
			fmt.Fprintln(e.stdout)
		}
		title := "(untitled)"
		if r.title.Valid && r.title.String != "" {
			title = r.title.String
		}
		ts := "?"
		if r.createdAt.Valid {
			ts = pyTime(r.createdAt.Time)[:19]
		}
		fmt.Fprintf(e.stdout, "  [%s] %s\n", r.source, title)
		fmt.Fprintf(e.stdout, "  %s @ %s  (score: %.2f)\n", r.role, ts, r.score)
		fmt.Fprintf(e.stdout, "  %s\n", snippet(r.text.String, query))
	}
	return nil
}

type searchResult struct {
	score          float64
	conversationID string
	title          sql.NullString
	source         string
	project        sql.NullString
	role           string
	createdAt      sql.NullTime
	text           sql.NullString
}

func nullable(s sql.NullString) any {
	if s.Valid {
		return s.String
	}
	return nil
}

const searchColumns = `c.id, c.title, c.source_id, c.project, m.role, m.created_at, cb.text
	FROM content_blocks cb
	JOIN messages m ON m.id = cb.message_id
	JOIN conversations c ON c.id = m.conversation_id`

func searchFTS(ctx context.Context, s *store.Store, query string, conditions []string, params []any, limit int) ([]searchResult, error) {
	where := strings.Join(append([]string{"score IS NOT NULL"}, conditions...), " AND ")
	sqlText := `SELECT fts_main_content_blocks.match_bm25(cb.id, ?) AS score, ` + searchColumns +
		` WHERE ` + where + ` ORDER BY score DESC LIMIT ?`
	return runSearchQuery(ctx, s, sqlText, append(append([]any{query}, params...), limit))
}

func searchSubstring(ctx context.Context, s *store.Store, query string, conditions []string, params []any, limit int) ([]searchResult, error) {
	where := strings.Join(append([]string{"cb.text IS NOT NULL", "lower(cb.text) LIKE ?"}, conditions...), " AND ")
	sqlText := `SELECT 1.0 AS score, ` + searchColumns +
		` WHERE ` + where + ` ORDER BY m.sequence_index, cb.block_index LIMIT ?`
	args := append(append([]any{"%" + strings.ToLower(query) + "%"}, params...), limit)
	return runSearchQuery(ctx, s, sqlText, args)
}

func runSearchQuery(ctx context.Context, s *store.Store, sqlText string, args []any) ([]searchResult, error) {
	rows, err := s.Conn().QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []searchResult
	for rows.Next() {
		var r searchResult
		if err := rows.Scan(&r.score, &r.conversationID, &r.title, &r.source, &r.project,
			&r.role, &r.createdAt, &r.text); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// snippet returns text around the first occurrence of the query's first term,
// ported from chatstrata/core/search.py. Offsets are in characters.
func snippet(text, query string, contextChars ...int) string {
	n := 120
	if len(contextChars) > 0 {
		n = contextChars[0]
	}
	if text == "" {
		return ""
	}
	first := query
	if fields := strings.Fields(query); len(fields) > 0 {
		first = fields[0]
	}
	runes := []rune(text)
	idx := indexRunesFold(runes, []rune(first))
	if idx == -1 {
		if len(runes) > n*2 {
			return string(runes[:n*2]) + "..."
		}
		return text
	}
	start := max(0, idx-n)
	end := min(len(runes), idx+len([]rune(first))+n)
	prefix, suffix := "", ""
	if start > 0 {
		prefix = "..."
	}
	if end < len(runes) {
		suffix = "..."
	}
	return prefix + string(runes[start:end]) + suffix
}

func indexRunesFold(haystack, needle []rune) int {
	if len(needle) == 0 {
		return 0
	}
outer:
	for i := 0; i+len(needle) <= len(haystack); i++ {
		for j, r := range needle {
			if unicode.ToLower(haystack[i+j]) != unicode.ToLower(r) {
				continue outer
			}
		}
		return i
	}
	return -1
}

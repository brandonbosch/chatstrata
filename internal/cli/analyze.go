package cli

import (
	"fmt"
	"strings"

	"github.com/brandonbosch/chatstrata"
)

// analyze runs the queries in chatstrata/analysis/queries, shared unchanged
// with the Python app, and prints them the way Python's `analyze` does.
var analyzeCommands = []struct {
	name, summary string
	run           func(e *env, name string, args []string) error
}{
	{"activity", "Messages over time, grouped by period", runAnalyzeActivity},
	{"tools", "Tool usage frequency", runAnalyzeTools},
	{"conversations", "Conversation length statistics", runAnalyzeConversations},
	{"models", "Model usage breakdown", runAnalyzeSimple},
	{"projects", "Per-project conversation counts (Claude Code)", runAnalyzeSimple},
}

func runAnalyze(e *env, args []string) error {
	if len(args) > 0 {
		for _, c := range analyzeCommands {
			if c.name == args[0] {
				return c.run(e, c.name, args[1:])
			}
		}
	}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(e.stdout, "Usage: chatstrata analyze COMMAND [--db PATH] [--json]\n\nAnalyze your conversation archive.\n\nCommands:")
		for _, c := range analyzeCommands {
			fmt.Fprintf(e.stdout, "  %-15s %s\n", c.name, c.summary)
		}
		return nil
	}
	return usageError{fmt.Sprintf("unknown analyze command %q", args[0])}
}

func loadQuery(name string) (string, error) {
	b, err := chatstrata.AnalysisQueries.ReadFile("chatstrata/analysis/queries/" + name + ".sql")
	if err != nil {
		return "", fmt.Errorf("load query %s: %w", name, err)
	}
	return string(b), nil
}

// fillTemplate substitutes {name} placeholders, as Python's str.format does
// for these queries.
func fillTemplate(sql string, values map[string]string) string {
	for k, v := range values {
		sql = strings.ReplaceAll(sql, "{"+k+"}", v)
	}
	return sql
}

// sourceFilter is Python's build_source_filter.
func sourceFilter(source, column string) (string, []any) {
	if source == "" {
		return "", nil
	}
	return "AND " + column + " = ?", []any{source}
}

func runAnalyzeActivity(e *env, name string, args []string) error {
	fs := newFlagSet(e, "analyze activity", "analyze activity [--by day|week|month] [--source NAME] [--db PATH] [--json]")
	by := fs.String("by", "month", "Time granularity: day, week or month.")
	source := fs.String("source", "", "Filter to a specific source.")
	db := fs.String("db", "", "Override the database path.")
	asJSON := fs.Bool("json", false, "Output as JSON.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	switch *by {
	case "day", "week", "month":
	default:
		return usageError{"--by must be day, week or month"}
	}
	tmpl, err := loadQuery(name)
	if err != nil {
		return err
	}
	filter, params := sourceFilter(*source, "c.source_id")
	return runAnalysisQuery(e, *db, fillTemplate(tmpl, map[string]string{"granularity": *by, "source_filter": filter}), params, *asJSON)
}

func runAnalyzeTools(e *env, name string, args []string) error {
	fs := newFlagSet(e, "analyze tools", "analyze tools [--source NAME] [--db PATH] [--json]")
	source := fs.String("source", "", "Filter to a specific source (e.g. claude_code).")
	db := fs.String("db", "", "Override the database path.")
	asJSON := fs.Bool("json", false, "Output as JSON.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	tmpl, err := loadQuery(name)
	if err != nil {
		return err
	}
	filter, params := sourceFilter(*source, "source_id")
	return runAnalysisQuery(e, *db, fillTemplate(tmpl, map[string]string{"source_filter": filter}), params, *asJSON)
}

func runAnalyzeConversations(e *env, name string, args []string) error {
	fs := newFlagSet(e, "analyze conversations", "analyze conversations [--longest N | --shortest N] [--db PATH] [--json]")
	longest := fs.Int("longest", 0, "Show N longest conversations.")
	shortest := fs.Int("shortest", 0, "Show N shortest conversations.")
	db := fs.String("db", "", "Override the database path.")
	asJSON := fs.Bool("json", false, "Output as JSON.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *longest > 0 && *shortest > 0 {
		return usageError{"Specify --longest or --shortest, not both."}
	}
	order, limit := "DESC", 20
	switch {
	case *shortest > 0:
		order, limit = "ASC", *shortest
	case *longest > 0:
		limit = *longest
	}
	tmpl, err := loadQuery(name)
	if err != nil {
		return err
	}
	sql := fillTemplate(tmpl, map[string]string{"order": order, "limit": fmt.Sprint(limit)})
	return runAnalysisQuery(e, *db, sql, nil, *asJSON)
}

func runAnalyzeSimple(e *env, name string, args []string) error {
	fs := newFlagSet(e, "analyze "+name, "analyze "+name+" [--db PATH] [--json]")
	db := fs.String("db", "", "Override the database path.")
	asJSON := fs.Bool("json", false, "Output as JSON.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	sql, err := loadQuery(name)
	if err != nil {
		return err
	}
	return runAnalysisQuery(e, *db, sql, nil, *asJSON)
}

func runAnalysisQuery(e *env, db, sql string, params []any, asJSON bool) error {
	s, err := openForReading(e, db)
	if s == nil || err != nil {
		return err
	}
	defer s.Close()
	rows, err := s.Conn().QueryContext(e.ctx, sql, params...)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	var values [][]any
	for rows.Next() {
		row := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range row {
			ptrs[i] = &row[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		values = append(values, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if asJSON {
		return writeJSONRows(e.stdout, cols, values)
	}
	writeTable(e, cols, values)
	return nil
}

// writeTable prints left-aligned columns separated by two spaces, as the
// Python analyze commands do.
func writeTable(e *env, cols []string, values [][]any) {
	if len(values) == 0 {
		fmt.Fprintln(e.stdout, "No data.")
		return
	}
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = len([]rune(c))
		for _, row := range values {
			widths[i] = max(widths[i], len([]rune(formatValue(row[i]))))
		}
	}
	line := func(cells []string) {
		for i := range cells {
			cells[i] += strings.Repeat(" ", widths[i]-len([]rune(cells[i])))
		}
		fmt.Fprintln(e.stdout, strings.Join(cells, "  "))
	}
	line(append([]string(nil), cols...))
	dashes := make([]string, len(cols))
	for i, w := range widths {
		dashes[i] = strings.Repeat("-", w)
	}
	fmt.Fprintln(e.stdout, strings.Join(dashes, "  "))
	for _, row := range values {
		cells := make([]string, len(row))
		for i, v := range row {
			cells[i] = formatValue(v)
		}
		line(cells)
	}
}

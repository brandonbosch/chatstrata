package cli

import (
	"context"
	"fmt"
	"strings"
)

// schema describes every table and view with its row count, as the Python
// chatstrata://schema resource does.
func (a archiveReader) schema(ctx context.Context) (string, error) {
	s, err := a.open(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = s.Close() }()
	conn := s.Conn()
	rows, err := conn.QueryContext(ctx, `SELECT table_name, table_type FROM information_schema.tables
		WHERE table_schema = 'main' ORDER BY table_type, table_name`)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	type table struct{ name, kind string }
	var tables []table
	for rows.Next() {
		var t table
		if err := rows.Scan(&t.name, &t.kind); err != nil {
			return "", err
		}
		tables = append(tables, t)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}

	columnLines := func(table string) ([]string, error) {
		cols, err := conn.QueryContext(ctx, `SELECT column_name, data_type, is_nullable
			FROM information_schema.columns WHERE table_name = ? ORDER BY ordinal_position`, table)
		if err != nil {
			return nil, err
		}
		defer func() { _ = cols.Close() }()
		var lines []string
		for cols.Next() {
			var name, dtype, nullable string
			if err := cols.Scan(&name, &dtype, &nullable); err != nil {
				return nil, err
			}
			line := fmt.Sprintf("    %-30s %s", name, dtype)
			if nullable == "YES" {
				line += " (nullable)"
			}
			lines = append(lines, line)
		}
		if err := cols.Err(); err != nil {
			return nil, err
		}
		return lines, nil
	}

	var schemaParts, statsParts []string
	for _, t := range tables {
		label := "TABLE"
		if t.kind == "VIEW" {
			label = "VIEW"
		}
		lines, err := columnLines(t.name)
		if err != nil {
			return "", err
		}
		schemaParts = append(schemaParts, fmt.Sprintf("%s %s:\n%s", label, t.name, strings.Join(lines, "\n")))

		var n int64
		if err := conn.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM "%s"`, t.name)).Scan(&n); err != nil {
			statsParts = append(statsParts, fmt.Sprintf("  %s: (unable to count)", t.name))
		} else {
			statsParts = append(statsParts, fmt.Sprintf("  %s: %s rows", t.name, thousands(n)))
		}
	}
	return fmt.Sprintf(`# ChatStrata Database Schema

%s

## Row Counts
%s

## Key Relationships
- conversations.source_id -> sources.id
- messages.conversation_id -> conversations.id
- content_blocks.message_id -> messages.id
- attachments.message_id -> messages.id
- tool_calls is a VIEW over content_blocks WHERE type = 'tool_use'

## Full-Text Search (BM25)
Use: fts_main_content_blocks.match_bm25(cb.id, 'your search terms')
Returns a float score (higher = more relevant), NULL for non-matches.
Filter with: WHERE fts_main_content_blocks.match_bm25(cb.id, 'term') IS NOT NULL
Order by score DESC for relevance ranking.

## Example Queries
%s
`, strings.Join(schemaParts, "\n\n"), strings.Join(statsParts, "\n"), exampleQueries), nil
}

// thousands formats n with comma separators, like Python's f"{n:,}".
func thousands(n int64) string {
	s := fmt.Sprint(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

package cli

import (
	"database/sql"
	"fmt"

	"github.com/brandonbosch/chatstrata/internal/store"
)

func runStats(e *env, args []string) error {
	fs := newFlagSet(e, "stats", "stats [--db PATH]")
	db := fs.String("db", "", "Override the database path.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	s, err := openForReading(e, *db)
	if s == nil || err != nil {
		return err
	}
	defer s.Close()

	fmt.Fprintf(e.stdout, "Database: %s\n\n", s.Path)
	rows, err := s.Conn().QueryContext(e.ctx, `
		SELECT s.id, s.name, COUNT(c.id) AS conversations,
		       MIN(c.started_at) AS earliest, MAX(c.ended_at) AS latest
		FROM sources s
		LEFT JOIN conversations c ON c.source_id = s.id
		GROUP BY s.id, s.name
		ORDER BY s.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	printed := false
	for rows.Next() {
		var id, name string
		var n int
		var earliest, latest sql.NullTime
		if err := rows.Scan(&id, &name, &n, &earliest, &latest); err != nil {
			return err
		}
		if !printed {
			fmt.Fprintln(e.stdout, "Sources:")
			printed = true
		}
		fmt.Fprintf(e.stdout, "  %-20s %-25s %6d conversations  %s  to  %s\n",
			id, name, n, timeOrDash(earliest), timeOrDash(latest))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !printed {
		fmt.Fprintln(e.stdout, "No data ingested yet. Try `chatstrata ingest claude_code`.")
		return nil
	}

	var conversations, messages, blocks, toolCalls int
	if err := s.Conn().QueryRowContext(e.ctx, `
		SELECT
			(SELECT COUNT(*) FROM conversations),
			(SELECT COUNT(*) FROM messages),
			(SELECT COUNT(*) FROM content_blocks),
			(SELECT COUNT(*) FROM content_blocks WHERE type = 'tool_use')`).
		Scan(&conversations, &messages, &blocks, &toolCalls); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "\nTotals: conversations=%d, messages=%d, content_blocks=%d, tool_calls=%d\n",
		conversations, messages, blocks, toolCalls)
	return nil
}

func timeOrDash(t sql.NullTime) string {
	if !t.Valid {
		return "-"
	}
	return pyTime(t.Time)
}

func runDoctor(e *env, args []string) error {
	fs := newFlagSet(e, "doctor", "doctor [--db PATH]")
	db := fs.String("db", "", "Override the database path.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	s, err := openForReading(e, *db)
	if s == nil || err != nil {
		return err
	}
	defer s.Close()

	issues := 0
	rows, err := s.Conn().QueryContext(e.ctx, `
		SELECT s.id FROM sources s
		LEFT JOIN conversations c ON c.source_id = s.id
		GROUP BY s.id HAVING COUNT(c.id) = 0
		ORDER BY s.id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		fmt.Fprintf(e.stdout, "  ⚠ source '%s' has no conversations\n", id)
		issues++
	}
	rows.Close()

	checks := []struct{ sql, message string }{
		{`SELECT COUNT(*) FROM conversations c
		  WHERE NOT EXISTS (SELECT 1 FROM messages m WHERE m.conversation_id = c.id)`,
			"conversations have no messages"},
		{`SELECT COUNT(*) FROM messages m
		  WHERE NOT EXISTS (SELECT 1 FROM content_blocks cb WHERE cb.message_id = m.id)`,
			"messages have no content blocks"},
	}
	for _, c := range checks {
		var n int
		if err := s.Conn().QueryRowContext(e.ctx, c.sql).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			fmt.Fprintf(e.stdout, "  ⚠ %d %s\n", n, c.message)
			issues++
		}
	}

	if issues == 0 {
		fmt.Fprintln(e.stdout, "✓ All checks passed.")
	} else {
		fmt.Fprintf(e.stdout, "\n%d issue(s) found.\n", issues)
	}
	return nil
}

func runReindex(e *env, args []string) error {
	fs := newFlagSet(e, "reindex", "reindex [--db PATH] [--install-fts]")
	db := fs.String("db", "", "Override the database path.")
	install := fs.Bool("install-fts", false, "Download DuckDB's full-text search extension if it isn't installed (one network request).")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	dbPath, err := store.ResolvePath(*db)
	if err != nil {
		return err
	}
	s, err := store.Open(e.ctx, dbPath)
	if err != nil {
		return err
	}
	defer s.Close()

	if !s.LoadFTS(e.ctx) {
		if !*install {
			fmt.Fprintln(e.stderr, "DuckDB's full-text search extension isn't installed, so `search` falls back to substring matching.")
			fmt.Fprintln(e.stderr, "Run `chatstrata reindex --install-fts` to download it once (the only network request search needs).")
			return exitError{1}
		}
		fmt.Fprintln(e.stdout, "Downloading DuckDB full-text search extension...")
		if err := s.InstallFTS(e.ctx); err != nil {
			return err
		}
	}

	var n int
	if err := s.Conn().QueryRowContext(e.ctx, `SELECT COUNT(*) FROM content_blocks WHERE text IS NOT NULL`).Scan(&n); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "Rebuilding search index over %d content blocks...\n", n)
	if err := s.RebuildFTS(e.ctx); err != nil {
		return fmt.Errorf("rebuild search index: %w", err)
	}
	fmt.Fprintln(e.stdout, "Done. Search index is up to date.")
	return nil
}

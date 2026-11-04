package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// DuckDB never returns the space of deleted rows: they stay in the file's
// blocks until it is rewritten. Updates that rewrite rows (a changed indexed
// column, a hashVersion bump, an edited or reordered conversation) leave
// that dead space behind, so the store counts the rows it deletes and
// Compact copies the live rows into a fresh file.

// deletedKey is the meta row counting rows deleted since the archive was
// created or last compacted. An archive from before the count existed has
// no such row.
const deletedKey = "compaction_deleted_rows"

// minDeleted keeps small archives, where dead rows cost little, from
// compacting over a handful of rewrites.
const minDeleted = 10_000

// legacySize is the file size at which an archive from before the count
// existed, whose dead space is unknown, is compacted once. A variable so
// tests can lower it.
var legacySize int64 = 64 << 20

// ErrCompactUnsupported is returned on Windows, where a file can't be
// renamed over while it is open, so the archive can't be swapped in place.
var ErrCompactUnsupported = errors.New("compaction isn't supported on Windows; run `chatstrata rebuild` instead")

// exec runs a statement and adds the rows a DELETE removed to the count of
// dead rows, which Close records.
func (s *Store) exec(ctx context.Context, query string, args ...any) error {
	res, err := s.conn.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if strings.HasPrefix(query, "DELETE") {
		if n, err := res.RowsAffected(); err == nil {
			s.deleted += n
		}
	}
	return nil
}

// saveDeleted adds this session's deleted rows to the archive's count. An
// archive without a count keeps none until it is compacted.
func (s *Store) saveDeleted(ctx context.Context) error {
	if s.deleted == 0 {
		return nil
	}
	_, err := s.conn.ExecContext(ctx, `UPDATE meta SET value = CAST(CAST(value AS BIGINT) + ? AS VARCHAR) WHERE key = ?`,
		s.deleted, deletedKey)
	if err == nil {
		s.deleted = 0
	}
	return err
}

// CompactionDue reports whether the archive carries enough dead rows to be
// worth rewriting: at least as many as the live rows of the same tables,
// so at least half of what it wrote is dead. An archive from before the
// count existed is compacted once, if it is large enough to matter.
func (s *Store) CompactionDue(ctx context.Context) (bool, error) {
	if runtime.GOOS == "windows" {
		return false, nil
	}
	var v sql.NullString
	err := s.conn.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, deletedKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		fi, err := os.Stat(s.Path)
		return err == nil && fi.Size() >= legacySize, err
	}
	if err != nil {
		return false, err
	}
	stored, err := strconv.ParseInt(v.String, 10, 64)
	if err != nil {
		return false, fmt.Errorf("meta %s: %w", deletedKey, err)
	}
	deleted := stored + s.deleted
	if deleted < minDeleted {
		return false, nil
	}
	var live int64
	err = s.conn.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM conversations) + (SELECT count(*) FROM messages)
		+ (SELECT count(*) FROM content_blocks) + (SELECT count(*) FROM raw_events)`).Scan(&live)
	return deleted >= live, err
}

// Compact rewrites the archive into a fresh file holding only its live rows,
// swaps that file in, and closes the store, which must not be used again.
// It returns the file's size before and after.
//
// The fresh file is created by the same migrations as a new archive, then
// filled table by table in foreign-key order (COPY FROM DATABASE copies
// children before parents and fails) and checked row for row. The swap
// happens while this store still holds DuckDB's write lock, so no other
// process can open the old file meanwhile: it is checkpointed, renamed over,
// and only then closed. Closing a checkpointed connection that wrote nothing
// since leaves the new file's write-ahead log alone, so a writer that opens
// the new file straight away is safe (TestSwapLeavesNewFilesWALAlone). The
// full-text index is rebuilt on the new file rather than copied.
//
// If anything fails before the rename, the archive is untouched and the
// temporary file is removed; a crash leaves it behind for the next
// compaction to remove.
func (s *Store) Compact(ctx context.Context) (before, after int64, err error) {
	if runtime.GOOS == "windows" {
		return 0, 0, ErrCompactUnsupported
	}
	path, err := filepath.EvalSymlinks(s.Path)
	if err != nil {
		return 0, 0, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0, 0, err
	}
	before = fi.Size()
	tmp := path + ".compact"
	if err := removeFiles(tmp, tmp+".wal"); err != nil {
		return 0, 0, err
	}
	swapped := false
	defer func() {
		if !swapped {
			_ = removeFiles(tmp, tmp+".wal")
		}
	}()

	if err := s.saveDeleted(ctx); err != nil {
		return 0, 0, err
	}
	hadIndex, _, _, _ := s.FTSCoverage(ctx)
	fresh, err := Open(ctx, tmp)
	if err != nil {
		return 0, 0, err
	}
	if err := fresh.Close(); err != nil {
		return 0, 0, err
	}
	if err := s.copyInto(ctx, tmp); err != nil {
		return 0, 0, fmt.Errorf("compact: %w", err)
	}
	if err := os.Chmod(tmp, fi.Mode().Perm()); err != nil {
		return 0, 0, err
	}
	if err := syncFile(tmp); err != nil {
		return 0, 0, err
	}
	if err := compactHook(); err != nil {
		return 0, 0, err
	}

	if err := s.Checkpoint(ctx); err != nil {
		return 0, 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return 0, 0, err
	}
	swapped = true
	_ = syncFile(filepath.Dir(path))
	if err := s.Close(); err != nil {
		return 0, 0, err
	}

	// The search index is rebuildable; without it search falls back to
	// substring matching and the next catch-up rebuilds it.
	if hadIndex {
		if n, err := Open(ctx, path); err == nil {
			if n.LoadFTS(ctx) {
				_ = n.RebuildFTS(ctx)
			}
			_ = n.Close()
		}
	}
	if fi, err := os.Stat(path); err == nil {
		after = fi.Size()
	}
	return before, after, nil
}

// compactHook lets tests fail a compaction just before the swap.
var compactHook = func() error { return nil }

// copyInto copies every table of the main schema into the freshly migrated
// archive at dst, parents before children, and checks the row counts.
func (s *Store) copyInto(ctx context.Context, dst string) (err error) {
	if _, err := s.conn.ExecContext(ctx, "ATTACH "+sqlString(dst)+" AS compacted"); err != nil {
		return err
	}
	defer func() {
		if _, derr := s.conn.ExecContext(context.WithoutCancel(ctx), "DETACH compacted"); err == nil {
			err = derr
		}
	}()
	tables, err := s.tablesInFKOrder(ctx)
	if err != nil {
		return err
	}
	// The migrations seed rows (meta); the copy replaces them.
	for i := len(tables) - 1; i >= 0; i-- {
		if _, err := s.conn.ExecContext(ctx, "DELETE FROM compacted.main."+ident(tables[i])); err != nil {
			return fmt.Errorf("table %s isn't in a new archive: %w", tables[i], err)
		}
	}
	for _, t := range tables {
		cols, err := s.columns(ctx, t)
		if err != nil {
			return err
		}
		if _, err := s.conn.ExecContext(ctx, fmt.Sprintf("INSERT INTO compacted.main.%s (%s) SELECT %s FROM main.%s",
			ident(t), cols, cols, ident(t))); err != nil {
			return fmt.Errorf("copy %s: %w", t, err)
		}
		var src, dstN int64
		if err := s.conn.QueryRowContext(ctx, fmt.Sprintf("SELECT (SELECT count(*) FROM main.%s), (SELECT count(*) FROM compacted.main.%s)",
			ident(t), ident(t))).Scan(&src, &dstN); err != nil {
			return err
		}
		if src != dstN {
			return fmt.Errorf("copy %s: %d rows, copied %d", t, src, dstN)
		}
	}
	_, err = s.conn.ExecContext(ctx, `INSERT OR REPLACE INTO compacted.main.meta (key, value) VALUES (?, '0')`, deletedKey)
	return err
}

// tablesInFKOrder lists the main schema's tables so that every table comes
// after the tables its foreign keys reference.
func (s *Store) tablesInFKOrder(ctx context.Context) ([]string, error) {
	db := `(SELECT current_database())`
	tables, err := s.strings(ctx, `SELECT table_name FROM duckdb_tables()
		WHERE database_name = `+db+` AND schema_name = 'main' ORDER BY table_name`)
	if err != nil {
		return nil, err
	}
	fks, err := s.strings(ctx, `SELECT table_name || chr(0) || referenced_table FROM duckdb_constraints()
		WHERE database_name = `+db+` AND schema_name = 'main' AND constraint_type = 'FOREIGN KEY'`)
	if err != nil {
		return nil, err
	}
	refs := map[string][]string{}
	for _, fk := range fks {
		if t, ref, _ := strings.Cut(fk, "\x00"); ref != t {
			refs[t] = append(refs[t], ref)
		}
	}
	var order []string
	state := map[string]int{} // 1 visiting, 2 done
	var visit func(string) error
	visit = func(t string) error {
		switch state[t] {
		case 1:
			return fmt.Errorf("foreign keys form a cycle at %s", t)
		case 2:
			return nil
		}
		state[t] = 1
		for _, r := range refs[t] {
			if err := visit(r); err != nil {
				return err
			}
		}
		state[t] = 2
		order = append(order, t)
		return nil
	}
	for _, t := range tables {
		if err := visit(t); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// columns lists a main-schema table's columns, quoted, in table order.
func (s *Store) columns(ctx context.Context, table string) (string, error) {
	cols, err := s.strings(ctx, `SELECT column_name FROM duckdb_columns()
		WHERE database_name = (SELECT current_database()) AND schema_name = 'main' AND table_name = ?
		ORDER BY column_index`, table)
	for i, c := range cols {
		cols[i] = ident(c)
	}
	return strings.Join(cols, ", "), err
}

// strings runs a query returning one text column.
func (s *Store) strings(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func ident(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func removeFiles(paths ...string) error {
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// syncFile flushes a file, or a directory's entries, to disk.
func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

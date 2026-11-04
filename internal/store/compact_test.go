package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/brandonbosch/chatstrata/v2/internal/model"
)

func skipOnWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("compaction isn't supported on Windows")
	}
}

// bloated builds an archive at path of 20 conversations, then, in rounds of
// separate sessions (each closing, and so checkpointing, the archive),
// replaces 3 of them: a changed project forces a full rewrite. That leaves
// dead rows among live ones, the way a daemon's occasional rewrites do. It
// returns the file's size after the first session, which held the same
// amount of data with nothing deleted yet.
func bloated(t *testing.T, path string, rounds int) (freshSize int64) {
	t.Helper()
	ctx := context.Background()
	for r := -1; r < rounds; r++ {
		s, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if r == -1 {
			if _, err := s.conn.ExecContext(ctx, `INSERT INTO sources (id, name) VALUES ('src', 'Source')`); err != nil {
				t.Fatal(err)
			}
		}
		for i := range 20 {
			if r >= 0 && (i-3*r%20+20)%20 >= 3 {
				continue // only 3 conversations change per round
			}
			c := testConv(150)
			c.SourceNativeID = fmt.Sprintf("conv-%d", i)
			c.Project = model.Ptr(fmt.Sprintf("/work/%d", r))
			if _, err := s.Ingest(ctx, "src", c, nil); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if r == -1 {
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			freshSize = fi.Size()
		}
	}
	return freshSize
}

// catalog lists the archive's tables, indexes, views and constraints, so a
// compacted file can be compared with a new archive's schema.
func catalog(t *testing.T, s *Store) string {
	t.Helper()
	rows, err := s.conn.QueryContext(context.Background(), `
		SELECT 'table ' || schema_name || '.' || table_name || ' ' || sql FROM duckdb_tables() WHERE database_name = current_database()
		UNION ALL SELECT 'index ' || index_name || ' ' || sql FROM duckdb_indexes() WHERE database_name = current_database()
		UNION ALL SELECT 'view ' || view_name || ' ' || sql FROM duckdb_views() WHERE database_name = current_database() AND NOT internal
		UNION ALL SELECT 'constraint ' || table_name || ' ' || constraint_text FROM duckdb_constraints() WHERE database_name = current_database()
		ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var b strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		b.WriteString(line + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func meta(t *testing.T, s *Store, key string) (string, bool) {
	t.Helper()
	var v string
	err := s.conn.QueryRowContext(context.Background(), `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if err != nil {
		return "", false
	}
	return v, true
}

func usedBlocks(t *testing.T, s *Store) int64 {
	t.Helper()
	var n int64
	if err := s.conn.QueryRowContext(context.Background(), `SELECT used_blocks FROM pragma_database_size()`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCompactKeepsEveryRowAndDropsDeadSpace(t *testing.T) {
	skipOnWindows(t)
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.duckdb")
	freshSize := bloated(t, path, 12)

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if due, err := s.CompactionDue(ctx); err != nil || !due {
		t.Fatalf("CompactionDue after 12 rounds of rewrites = %v, %v; want due", due, err)
	}
	wantRows, wantUsed := dump(t, s), usedBlocks(t, s)
	before, after, err := s.Compact(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("compacted %d -> %d bytes; the same data written once: %d bytes", before, after, freshSize)
	if after > freshSize*6/5 {
		t.Errorf("compacted file is %d bytes, more than 1.2x the %d of writing the same data once", after, freshSize)
	}
	if before < freshSize*3/2 {
		t.Fatalf("fixture isn't bloated: %d bytes against %d written once", before, freshSize)
	}
	if _, err := os.Stat(path + ".compact"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temporary file left behind: %v", err)
	}

	c, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if got := dump(t, c); got != wantRows {
		t.Error("compacted archive's rows differ from the original's")
	}
	if got := usedBlocks(t, c); got > wantUsed {
		t.Errorf("compacted archive uses %d blocks, more than the original's %d", got, wantUsed)
	}
	if v, _ := meta(t, c, deletedKey); v != "0" {
		t.Errorf("dead-row count after compaction = %q, want 0", v)
	}
	if v, _ := meta(t, c, "schema_version"); v == "" {
		t.Error("schema version lost")
	}
	if due, err := c.CompactionDue(ctx); err != nil || due {
		t.Errorf("CompactionDue right after compaction = %v, %v", due, err)
	}

	fresh, err := Open(ctx, filepath.Join(dir, "fresh.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fresh.Close() }()
	if got, want := catalog(t, c), catalog(t, fresh); got != want {
		t.Errorf("compacted schema differs from a new archive's\n got:\n%s\nwant:\n%s", got, want)
	}

	// Still a working archive: a further update lands normally.
	if _, err := c.Ingest(ctx, "src", testConv(201), nil); err != nil {
		t.Fatal(err)
	}
}

func TestCompactionDue(t *testing.T) {
	skipOnWindows(t)
	ctx := context.Background()
	dir := t.TempDir()

	// A new archive starts with a count of zero and isn't due.
	s := openTestStore(t)
	if v, ok := meta(t, s, deletedKey); !ok || v != "0" {
		t.Fatalf("new archive's dead-row count = %q, %v; want 0", v, ok)
	}
	if due, _ := s.CompactionDue(ctx); due {
		t.Error("a new archive is due")
	}

	// Pure appends delete nothing.
	path := filepath.Join(dir, "a.duckdb")
	bloated(t, path, 1)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for n := 201; n < 205; n++ {
		c := testConv(n)
		c.SourceNativeID, c.Project = "conv-0", model.Ptr("/work/0")
		if _, err := s.Ingest(ctx, "src", c, nil); err != nil {
			t.Fatal(err)
		}
	}
	if s.deleted != 0 {
		t.Errorf("appends counted %d deleted rows", s.deleted)
	}

	// The count covers rows deleted this session, then is saved on close.
	prior, _ := meta(t, s, deletedKey)
	c := testConv(200)
	c.SourceNativeID, c.Project = "conv-1", model.Ptr("/elsewhere")
	if _, err := s.Ingest(ctx, "src", c, nil); err != nil {
		t.Fatal(err)
	}
	session := s.deleted
	if session == 0 {
		t.Fatal("a full rewrite counted no deleted rows")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var priorN int64
	_, _ = fmt.Sscan(prior, &priorN)
	if v, _ := meta(t, s, deletedKey); v != fmt.Sprint(priorN+session) {
		t.Errorf("saved count = %s, want %d + %d", v, priorN, session)
	}
	if due, _ := s.CompactionDue(ctx); due {
		t.Error("due with far fewer dead rows than live ones")
	}
	if _, err := s.conn.ExecContext(ctx, `UPDATE meta SET value = '1000000' WHERE key = ?`, deletedKey); err != nil {
		t.Fatal(err)
	}
	if due, _ := s.CompactionDue(ctx); !due {
		t.Error("not due with more dead rows than live ones")
	}

	// An archive from before the count: due once, only if it is large.
	if _, err := s.conn.ExecContext(ctx, `DELETE FROM meta WHERE key = ?`, deletedKey); err != nil {
		t.Fatal(err)
	}
	if due, _ := s.CompactionDue(ctx); due {
		t.Error("a small archive without a count is due")
	}
	defer func(size int64) { legacySize = size }(legacySize)
	legacySize = 1
	if due, _ := s.CompactionDue(ctx); !due {
		t.Error("a large archive without a count isn't due")
	}
	// Its first save keeps no count, so it stays unknown until compacted.
	s.deleted = 5
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if v, ok := meta(t, s, deletedKey); ok {
		t.Errorf("an archive without a count gained one (%q) before compacting", v)
	}
	if _, _, err := s.Compact(ctx); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := meta(t, s, deletedKey); v != "0" {
		t.Errorf("count after compacting a legacy archive = %q, want 0", v)
	}
	if due, _ := s.CompactionDue(ctx); due {
		t.Error("still due after the one-time compaction")
	}
}

func TestFailedCompactionLeavesArchiveUntouched(t *testing.T) {
	skipOnWindows(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "a.duckdb")
	bloated(t, path, 3)

	// A crash in an earlier compaction left its temporary files behind.
	for _, p := range []string{path + ".compact", path + ".compact.wal"} {
		if err := os.WriteFile(p, []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	compactHook = func() error { return errors.New("simulated crash before the swap") }
	defer func() { compactHook = func() error { return nil } }()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	want := dump(t, s)
	if _, _, err := s.Compact(ctx); err == nil || !strings.Contains(err.Error(), "simulated crash") {
		t.Fatalf("Compact = %v, want the simulated failure", err)
	}
	_ = s.Close()
	for _, p := range []string{path + ".compact", path + ".compact.wal"} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind: %v", filepath.Base(p), err)
		}
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if dump(t, s) != want {
		t.Error("a failed compaction changed the archive")
	}
}

// TestSwapLeavesNewFilesWALAlone pins the DuckDB behaviour Compact relies
// on: once the old file is checkpointed and renamed over, a writer in
// another process may open the new file at once, and closing the old
// connection must neither delete nor replay that writer's write-ahead log.
func TestSwapLeavesNewFilesWALAlone(t *testing.T) {
	skipOnWindows(t)
	if p := os.Getenv("CHATSTRATA_SWAP_CHILD"); p != "" {
		s, err := Open(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.conn.ExecContext(context.Background(), "SET wal_autocheckpoint = '1GB'"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.conn.ExecContext(context.Background(), `INSERT INTO meta VALUES ('child', 'wrote')`); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p+".ready", nil, 0o600); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 1000; i++ {
			if _, err := os.Stat(p + ".stop"); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		_ = s.Close()
		return
	}

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "a.duckdb")
	bloated(t, path, 2)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	// The same sequence Compact follows: checkpoint the old file, rename a
	// new one over it while the old connection still holds the lock, then
	// close the old connection, with a writer in another process in between.
	tmp := path + ".other"
	o, err := Open(ctx, tmp)
	if err != nil {
		t.Fatal(err)
	}
	_ = o.Close()
	if err := s.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	var childOut strings.Builder
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSwapLeavesNewFilesWALAlone$")
	child.Env = append(os.Environ(), "CHATSTRATA_SWAP_CHILD="+path)
	child.Stdout, child.Stderr = &childOut, &childOut
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	for i := 0; ; i++ {
		if _, err := os.Stat(path + ".ready"); err == nil {
			break
		}
		if i > 1000 {
			t.Fatalf("child writer never started:\n%s", childOut.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	walBefore, _ := os.ReadFile(path + ".wal")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	walAfter, _ := os.ReadFile(path + ".wal")
	if len(walBefore) == 0 || string(walBefore) != string(walAfter) {
		t.Errorf("closing the old connection changed the new file's WAL (%d bytes before, %d after)", len(walBefore), len(walAfter))
	}
	if err := os.WriteFile(path+".stop", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatalf("child writer: %v\n%s", err, childOut.String())
	}
	n, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = n.Close() }()
	if v, _ := meta(t, n, "child"); v != "wrote" {
		t.Errorf("the child's write to the new file was lost (got %q)", v)
	}
}

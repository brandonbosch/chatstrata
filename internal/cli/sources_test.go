package cli

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A database source is compared by content, not by the file's mtime: SQLite
// can keep new rows in its write-ahead log without touching the main file.
func TestIncrementalIngestSeesDatabaseChangesWithoutMtime(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "opencode.db")
	script, err := os.ReadFile("../../spec/golden/inputs/opencode/opencode.sql")
	if err != nil {
		t.Fatal(err)
	}
	src, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if _, err := src.Exec(string(script)); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "archive.duckdb")
	run(t, "ingest", "opencode", "--path", dbPath, "--db", archive, "--incremental")
	before, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := src.Exec(`
		INSERT INTO message VALUES ('msg_late', 'ses_test001', 1700000100000, 1700000100000,
			'{"role": "user", "time": {"created": 1700000100000}}');
		INSERT INTO part VALUES ('prt_late', 'msg_late', 'ses_test001', 1700000100000, 1700000100000,
			'{"type": "text", "text": "one more thing: LATEPROBE"}')`); err != nil {
		t.Fatal(err)
	}
	mtime := before.ModTime().Add(-time.Hour)
	if err := os.Chtimes(dbPath, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	run(t, "ingest", "opencode", "--path", dbPath, "--db", archive, "--incremental")

	out := run(t, "query", "SELECT count(*) AS n FROM content_blocks WHERE text LIKE '%LATEPROBE%'", "--db", archive)
	if strings.TrimSpace(out) != "n\n---\n1" {
		t.Errorf("new row not ingested:\n%s", out)
	}
}

// A database session that loses rows (a revert, a rewind) loses them in the
// archive too: a device's newer snapshot replaces its older one rather than
// standing beside it as a divergent version.
func TestSnapshotSourceFollowsRewrites(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "opencode.db")
	script, err := os.ReadFile("../../spec/golden/inputs/opencode/opencode.sql")
	if err != nil {
		t.Fatal(err)
	}
	src, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if _, err := src.Exec(string(script)); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "archive.duckdb")
	count := func() string {
		t.Helper()
		return strings.TrimSpace(run(t, "query",
			"SELECT (SELECT count(*) FROM messages) || '/' || (SELECT count(*) FROM divergences) AS n", "--db", archive))
	}
	run(t, "ingest", "opencode", "--path", dbPath, "--db", archive)
	first := count()

	if _, err := src.Exec(`DELETE FROM part WHERE message_id = 'msg_asst001'; DELETE FROM message WHERE id = 'msg_asst001'`); err != nil {
		t.Fatal(err)
	}
	run(t, "ingest", "opencode", "--path", dbPath, "--db", archive)
	if got := count(); got != "n\n---\n1/0" {
		t.Errorf("messages/divergences after the revert = %q (before: %q), want 1/0", got, first)
	}
	// A rebuild from the log agrees.
	run(t, "rebuild", "--db", archive)
	if got := count(); got != "n\n---\n1/0" {
		t.Errorf("after rebuild = %q, want 1/0", got)
	}
}

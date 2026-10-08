package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/brandonbosch/chatstrata/internal/golden"
	"github.com/brandonbosch/chatstrata/internal/store"
)

// fixture copies the Claude Code golden inputs to a temp dir and returns the
// transcripts root and an archive path.
func fixture(t *testing.T) (root, db string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(filepath.Join(dir, "in"), os.DirFS("../../spec/golden/inputs/claude_code")); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "in", "projects"), filepath.Join(dir, "archive.duckdb")
}

func run(t *testing.T, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), args, &stdout, &stderr); code != 0 || stderr.Len() > 0 {
		t.Fatalf("chatstrata %v: exit %d\n%s%s", args, code, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func dump(t *testing.T, db string) any {
	t.Helper()
	d, err := golden.Dump(context.Background(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func countObservations(t *testing.T, db string) int {
	t.Helper()
	s, err := store.OpenReadOnly(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err := s.Conn().QueryRowContext(context.Background(), `SELECT count(*) FROM observations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRebuildReproducesTheArchive(t *testing.T) {
	root, db := fixture(t)
	run(t, "ingest", "claude_code", "--path", root, "--db", db)
	before := dump(t, db)
	observations := countObservations(t, db)

	if err := os.Remove(db); err != nil {
		t.Fatal(err)
	}
	run(t, "rebuild", "--db", db)
	if after := dump(t, db); !reflect.DeepEqual(before, after) {
		t.Fatal("rebuilt archive differs from the original")
	}

	// The rebuilt collector state means unchanged files aren't logged again.
	run(t, "ingest", "claude_code", "--path", root, "--db", db)
	if n := countObservations(t, db); n != observations {
		t.Errorf("observations grew from %d to %d after rebuild", observations, n)
	}
}

// A crash after the log is written but before the projection is updated must
// lose nothing: the next run applies the unindexed segment.
func TestCrashAfterLogWriteIsRecovered(t *testing.T) {
	root, db := fixture(t)
	reference := filepath.Join(t.TempDir(), "reference.duckdb")
	run(t, "ingest", "claude_code", "--path", root, "--db", reference)
	want := dump(t, reference)

	e := &env{ctx: context.Background(), stdout: io.Discard, stderr: io.Discard}
	a, err := openArchive(e, db)
	if err != nil {
		t.Fatal(err)
	}
	handles, err := sources["claude_code"].Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.proj.Collect(e.ctx, sources["claude_code"], handles, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	a.Close() // "crash": the segment is on disk, nothing is projected

	run(t, "stats", "--db", db) // a reader doesn't change anything
	run(t, "ingest", "claude_code", "--path", root, "--db", db)
	if got := dump(t, db); !reflect.DeepEqual(got, want) {
		t.Fatal("archive after recovery differs from a clean ingest")
	}
}

// A crash after the log is written but before the collector state is saved
// makes the next run log the same bytes again, which must not change the
// archive.
func TestRelogAfterLostCollectorStateIsHarmless(t *testing.T) {
	root, db := fixture(t)
	run(t, "ingest", "claude_code", "--path", root, "--db", db)
	want := dump(t, db)

	ctx := context.Background()
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Conn().ExecContext(ctx, `DELETE FROM collector_state`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	run(t, "ingest", "claude_code", "--path", root, "--db", db)
	if got := dump(t, db); !reflect.DeepEqual(got, want) {
		t.Fatal("re-logging the same bytes changed the archive")
	}
}

// A search that matches nothing says so plainly; it only suggests a reindex
// when search had to fall back to substring matching.
func TestSearchWithNoMatches(t *testing.T) {
	root, db := fixture(t)
	run(t, "ingest", "claude_code", "--path", root, "--db", db)
	const query = "qqqwwweee123"

	if got := run(t, "search", query, "--json", "--db", db); strings.TrimSpace(got) != "[]" {
		t.Errorf("--json with no matches = %q, want []", got)
	}

	fts := ftsAvailable(t, db)
	got := run(t, "search", query, "--db", db)
	if fts {
		if strings.TrimSpace(got) != "No results." {
			t.Errorf("no matches with a current index = %q, want just \"No results.\"", got)
		}
		// Without an index, search falls back and points at reindex.
		s, err := store.Open(context.Background(), db)
		if err != nil {
			t.Fatal(err)
		}
		s.LoadFTS(context.Background())
		if _, err := s.Conn().ExecContext(context.Background(), "PRAGMA drop_fts_index('content_blocks')"); err != nil {
			t.Fatal(err)
		}
		s.Close()
		got = run(t, "search", query, "--db", db)
		if !strings.Contains(got, "`chatstrata reindex`") {
			t.Errorf("no matches without an index = %q, want a reindex hint", got)
		}
	} else if !strings.Contains(got, "reindex --install-fts") {
		t.Errorf("no matches without the FTS extension = %q, want an --install-fts hint", got)
	}
}

// ingest --auto, which the Python app's scheduled ingest runs, collects every
// installed source like the daemon does: here only Claude Code is installed.
func TestIngestAutoCollectsInstalledSources(t *testing.T) {
	root, db := fixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("HERMES_HOME", filepath.Join(home, "no-hermes"))
	if err := os.CopyFS(filepath.Join(home, ".claude", "projects"), os.DirFS(root)); err != nil {
		t.Fatal(err)
	}

	out := run(t, "ingest", "--auto", "--no-embed", "--db", db)
	if !strings.Contains(out, "claude_code") || !strings.Contains(out, "Ingested: 2") {
		t.Fatalf("ingest --auto:\n%s", out)
	}
	want := filepath.Join(t.TempDir(), "want.duckdb")
	run(t, "ingest", "claude_code", "--path", filepath.Join(home, ".claude", "projects"), "--db", want)
	if !reflect.DeepEqual(dump(t, want), dump(t, db)) {
		t.Fatal("ingest --auto differs from ingesting the source directly")
	}
	if out := run(t, "ingest", "--auto", "--db", db); !strings.Contains(out, "Ingested: 0") {
		t.Fatalf("second ingest --auto changed something:\n%s", out)
	}
}

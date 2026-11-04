package cli

import (
	"bytes"
	"context"
	"log"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/brandonbosch/chatstrata/v2/internal/store"
)

// markDead records n dead rows in the archive, as many rewrites would.
func markDead(t *testing.T, db, n string) {
	t.Helper()
	s, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.Conn().ExecContext(context.Background(),
		`UPDATE meta SET value = ? WHERE key = 'compaction_deleted_rows'`, n); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonCompactsWhenDue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("compaction isn't supported on Windows")
	}
	t.Setenv("HOME", t.TempDir()) // no installed sources to collect
	a := newDevice(t, map[string][]byte{sample: transcript(t, sample, 0), noCwd: transcript(t, noCwd, 0)})
	run(t, "ingest", "claude_code", "--path", a.root, "--db", a.db)
	if ftsAvailable(t, a.db) {
		run(t, "reindex", "--db", a.db)
	}
	want := dump(t, a.db)

	cycle := func() string {
		t.Helper()
		var out bytes.Buffer
		e := &env{ctx: context.Background(), stdout: &out, stderr: &out}
		if err := daemonRun(e, a.db, log.New(&out, "", 0)); err != nil {
			t.Fatalf("daemon run: %v\n%s", err, out.String())
		}
		return out.String()
	}

	if out := cycle(); strings.Contains(out, "compact") {
		t.Fatalf("daemon compacted a healthy archive:\n%s", out)
	}
	markDead(t, a.db, "1000000")
	if out := cycle(); !strings.Contains(out, "archive: compacted") {
		t.Fatalf("daemon didn't compact an archive that was due:\n%s", out)
	}
	if out := cycle(); strings.Contains(out, "compact") {
		t.Fatalf("daemon compacted again right after compacting:\n%s", out)
	}
	if !reflect.DeepEqual(dump(t, a.db), want) {
		t.Fatal("compaction changed the archive's contents")
	}
	if ftsAvailable(t, a.db) {
		if out := run(t, "search", "refactor", "--db", a.db); strings.Contains(out, "reindex") || strings.Contains(out, "No results") {
			t.Errorf("search after compaction:\n%s", out)
		}
	}
}

func TestCompactCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("compaction isn't supported on Windows")
	}
	a := newDevice(t, map[string][]byte{sample: transcript(t, sample, 0)})
	run(t, "ingest", "claude_code", "--path", a.root, "--db", a.db)
	want := dump(t, a.db)
	out := run(t, "compact", "--db", a.db)
	if !strings.Contains(out, "Done.") || !strings.Contains(out, " MB -> ") {
		t.Errorf("compact output:\n%s", out)
	}
	if !reflect.DeepEqual(dump(t, a.db), want) {
		t.Fatal("compact changed the archive's contents")
	}
	run(t, "ingest", "claude_code", "--path", a.root, "--db", a.db) // still a working archive
}

package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/brandonbosch/chatstrata/v2/internal/store"
)

// Rebuilding over an existing archive, rather than a deleted one, swaps the
// rebuilt file in.
func TestRebuildReplacesAnExistingArchive(t *testing.T) {
	root, db := fixture(t)
	run(t, "ingest", "claude_code", "--path", root, "--db", db)
	before := dump(t, db)
	run(t, "rebuild", "--db", db)
	if after := dump(t, db); !reflect.DeepEqual(before, after) {
		t.Fatal("rebuilt archive differs from the original")
	}
	if _, err := os.Stat(db + ".rebuild"); !os.IsNotExist(err) {
		t.Errorf("%s.rebuild left behind: %v", db, err)
	}
}

// While another process, like the daemon, has the archive open for writing,
// rebuild must refuse instead of swapping the file out from under it or
// deleting its write-ahead log.
func TestRebuildRefusesWhileAnotherProcessWrites(t *testing.T) {
	ctx := context.Background()
	if p := os.Getenv("CHATSTRATA_REBUILD_WRITER"); p != "" {
		s, err := store.Open(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range []string{"SET wal_autocheckpoint = '1GB'", `INSERT INTO meta VALUES ('writer', 'wrote')`} {
			if _, err := s.Conn().ExecContext(ctx, q); err != nil {
				t.Fatal(err)
			}
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
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}

	root, db := fixture(t)
	run(t, "ingest", "claude_code", "--path", root, "--db", db)
	var out strings.Builder
	child := exec.Command(os.Args[0], "-test.run=^TestRebuildRefusesWhileAnotherProcessWrites$")
	child.Env = append(os.Environ(), "CHATSTRATA_REBUILD_WRITER="+db)
	child.Stdout, child.Stderr = &out, &out
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	for i := 0; ; i++ {
		if _, err := os.Stat(db + ".ready"); err == nil {
			break
		}
		if i > 1000 {
			_ = child.Process.Kill()
			t.Fatalf("writer never started:\n%s", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	fileBefore, err := os.Stat(db)
	if err != nil {
		t.Fatal(err)
	}
	walBefore, _ := os.ReadFile(db + ".wal")

	var stdout, stderr bytes.Buffer
	if code := Run(ctx, []string{"rebuild", "--db", db}, &stdout, &stderr); code == 0 {
		t.Error("rebuild succeeded while another process held the archive")
	}
	if !strings.Contains(stderr.String(), "open for writing") {
		t.Errorf("rebuild error doesn't explain the conflict:\n%s", stderr.String())
	}
	if walAfter, _ := os.ReadFile(db + ".wal"); len(walBefore) == 0 || !bytes.Equal(walBefore, walAfter) {
		t.Errorf("rebuild changed the writer's WAL (%d bytes before, %d after)", len(walBefore), len(walAfter))
	}
	if fileAfter, err := os.Stat(db); err != nil || !os.SameFile(fileBefore, fileAfter) {
		t.Errorf("rebuild replaced the archive the writer had open: %v", err)
	}

	if err := os.WriteFile(db+".stop", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatalf("writer: %v\n%s", err, out.String())
	}
	s, err := store.OpenReadOnly(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var v string
	_ = s.Conn().QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'writer'`).Scan(&v)
	if v != "wrote" {
		t.Errorf("the writer's change was lost (got %q)", v)
	}
}

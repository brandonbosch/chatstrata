package store

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// heldWriter is the child half of the cross-process tests: with
// CHATSTRATA_WRITER_CHILD set it opens that archive, leaves a write in its
// write-ahead log, signals <path>.ready, and keeps the archive open until
// <path>.stop appears. It reports whether it ran as the child.
func heldWriter(t *testing.T) bool {
	t.Helper()
	p := os.Getenv("CHATSTRATA_WRITER_CHILD")
	if p == "" {
		return false
	}
	ctx := context.Background()
	s, err := Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"SET wal_autocheckpoint = '1GB'", `INSERT INTO meta VALUES ('child', 'wrote')`} {
		if _, err := s.conn.ExecContext(ctx, q); err != nil {
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
	return true
}

// startWriter runs test (which must call heldWriter first) in a child
// process holding path open for writing, and waits until it has written.
// The returned function lets it close the archive and waits for it to exit.
func startWriter(t *testing.T, test, path string) (stop func()) {
	t.Helper()
	var out strings.Builder
	child := exec.Command(os.Args[0], "-test.run=^"+test+"$")
	child.Env = append(os.Environ(), "CHATSTRATA_WRITER_CHILD="+path)
	child.Stdout, child.Stderr = &out, &out
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	for i := 0; ; i++ {
		if _, err := os.Stat(path + ".ready"); err == nil {
			break
		}
		if i > 1000 {
			_ = child.Process.Kill()
			t.Fatalf("child writer never started:\n%s", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	return func() {
		if err := os.WriteFile(path+".stop", nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := child.Wait(); err != nil {
			t.Fatalf("child writer: %v\n%s", err, out.String())
		}
	}
}

func metaValue(t *testing.T, path, key string) string {
	t.Helper()
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var v string
	_ = s.conn.QueryRowContext(context.Background(), `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	return v
}

// TestReplaceWithLeavesNewFilesWALAlone pins the DuckDB behaviour ReplaceWith
// relies on: once the old file is checkpointed and renamed over, a writer in
// another process may open the new file at once, and closing the old
// connection must neither delete nor replay that writer's write-ahead log.
func TestReplaceWithLeavesNewFilesWALAlone(t *testing.T) {
	if heldWriter(t) {
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows closes the old connection before the rename")
	}
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.duckdb")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dir, "a.duckdb.new")
	n, err := Open(ctx, tmp)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}

	old, err := OpenForReplace(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	// ReplaceWith's sequence, split to let the writer in after the rename.
	if err := old.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	stop := startWriter(t, "TestReplaceWithLeavesNewFilesWALAlone", path)
	walBefore, _ := os.ReadFile(path + ".wal")
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	walAfter, _ := os.ReadFile(path + ".wal")
	if len(walBefore) == 0 || string(walBefore) != string(walAfter) {
		t.Errorf("closing the old connection changed the new file's WAL (%d bytes before, %d after)", len(walBefore), len(walAfter))
	}
	stop()
	if v := metaValue(t, path, "child"); v != "wrote" {
		t.Errorf("the child's write to the new file was lost (got %q)", v)
	}
}

// OpenForReplace must refuse while another process writes to the archive,
// leaving that writer's WAL alone.
func TestOpenForReplaceFailsWhileAnotherProcessWrites(t *testing.T) {
	if heldWriter(t) {
		return
	}
	path := filepath.Join(t.TempDir(), "a.duckdb")
	stop := startWriter(t, "TestOpenForReplaceFailsWhileAnotherProcessWrites", path)
	walBefore, _ := os.ReadFile(path + ".wal")
	if s, err := OpenForReplace(context.Background(), path); err == nil {
		_ = s.Close()
		t.Fatal("OpenForReplace succeeded while another process held the archive")
	}
	if walAfter, _ := os.ReadFile(path + ".wal"); len(walBefore) == 0 || string(walBefore) != string(walAfter) {
		t.Errorf("the writer's WAL changed (%d bytes before, %d after)", len(walBefore), len(walAfter))
	}
	stop()
	if v := metaValue(t, path, "child"); v != "wrote" {
		t.Errorf("the child's write was lost (got %q)", v)
	}
}

func TestReplaceWithSwapsTheFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.duckdb")
	tmp := filepath.Join(dir, "a.duckdb.new")
	n, err := Open(ctx, tmp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.conn.ExecContext(ctx, `INSERT INTO meta VALUES ('new', 'yes')`); err != nil {
		t.Fatal(err)
	}
	if err := n.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
	old, err := OpenForReplace(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.ReplaceWith(ctx, tmp); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("%s still exists after the swap: %v", tmp, err)
	}
	if v := metaValue(t, path, "new"); v != "yes" {
		t.Errorf("archive after the swap isn't the new file (got %q)", v)
	}
}

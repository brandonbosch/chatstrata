package cli

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brandonbosch/chatstrata/internal/devsync"
	"github.com/brandonbosch/chatstrata/internal/relay"
	"github.com/brandonbosch/chatstrata/internal/store"
)

const projects = "../../spec/golden/inputs/claude_code/projects"

// device is one simulated machine: its own transcripts and its own archive.
type device struct {
	root string
	db   string
}

func newDevice(t *testing.T, files map[string][]byte) device {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "projects")
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return device{root: root, db: filepath.Join(dir, "archive.duckdb")}
}

func transcript(t *testing.T, rel string, lines int) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(projects, rel))
	if err != nil {
		t.Fatal(err)
	}
	if lines <= 0 {
		return b
	}
	parts := bytes.SplitAfter(b, []byte("\n"))
	return bytes.Join(parts[:lines], nil)
}

func runErr(args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), args, &stdout, &stderr); code != 0 {
		return stdout.String(), &runError{stderr.String()}
	}
	return stdout.String(), nil
}

type runError struct{ msg string }

func (e *runError) Error() string { return e.msg }

func pairingCode(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if code, ok := strings.CutPrefix(strings.TrimSpace(line), "chatstrata join "); ok {
			return code
		}
	}
	t.Fatalf("no pairing code in:\n%s", out)
	return ""
}

// withoutPaths drops raw_path, which names where a file lived on the
// device that projected it and so legitimately differs between machines.
func withoutPaths(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if k != "raw_path" {
				out[k] = withoutPaths(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = withoutPaths(x[i])
		}
		return out
	}
	return v
}

const (
	sample = "-Users-example-myproj/sample_session.jsonl"
	noCwd  = "-Users-example-other/no_cwd_session.jsonl"
)

func TestDevicesConvergeThroughRelay(t *testing.T) {
	relayDir := t.TempDir()
	var failPuts atomic.Int32
	handler := (&relay.Server{Dir: relayDir}).Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" && failPuts.Add(-1) >= 0 {
			http.Error(w, "simulated outage", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer srv.Close()

	// A has the full session; B has another session and an older, shorter
	// copy of A's (as a synced ~/.claude would give it).
	a := newDevice(t, map[string][]byte{sample: transcript(t, sample, 0)})
	b := newDevice(t, map[string][]byte{
		noCwd:  transcript(t, noCwd, 0),
		sample: transcript(t, sample, 3),
	})

	run(t, "ingest", "claude_code", "--path", a.root, "--db", a.db)
	run(t, "ingest", "claude_code", "--path", b.root, "--db", b.db)
	code := pairingCode(t, run(t, "pair", "--relay", srv.URL, "--db", a.db))

	// An outage mid-sync fails the run but leaves nothing half-done.
	failPuts.Store(1)
	if _, err := runErr("sync", "--db", a.db); err == nil {
		t.Fatal("sync succeeded through a relay outage")
	}
	run(t, "sync", "--db", a.db)
	run(t, "join", code, "--db", b.db) // uploads B's segments, downloads A's
	run(t, "sync", "--db", a.db)       // downloads B's

	wantA, wantB := dump(t, a.db), dump(t, b.db)
	if !reflect.DeepEqual(withoutPaths(wantA), withoutPaths(wantB)) {
		t.Fatal("devices disagree after sync")
	}
	if n := len(wantA.(map[string]any)["conversations"].([]any)); n != 2 {
		t.Fatalf("%d conversations after sync, want 2", n)
	}

	// A third, empty device restores everything from the relay alone; the
	// other devices play no part.
	c := newDevice(t, nil)
	run(t, "join", code, "--db", c.db)
	if got := dump(t, c.db); !reflect.DeepEqual(withoutPaths(got), withoutPaths(wantA)) {
		t.Fatal("a new device restored from the relay differs")
	}

	// The relay holds only ciphertext.
	filepath.Walk(relayDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		content, _ := os.ReadFile(path)
		if bytes.Contains(content, []byte("auth module")) || bytes.Contains(content, []byte("CHATSTRATA-SEG")) {
			t.Errorf("plaintext on the relay in %s", path)
		}
		return nil
	})

	// Syncing again moves nothing and changes nothing.
	out := run(t, "sync", "--db", b.db)
	if !strings.Contains(out, "Uploaded: 0 segments  Downloaded: 0 segments") {
		t.Errorf("second sync moved data: %s", out)
	}
	if !reflect.DeepEqual(dump(t, b.db), wantB) {
		t.Error("idle sync changed the archive")
	}
}

func TestWrongKeyIsRejected(t *testing.T) {
	srv := httptest.NewServer((&relay.Server{Dir: t.TempDir()}).Handler())
	defer srv.Close()
	a := newDevice(t, map[string][]byte{sample: transcript(t, sample, 0)})
	run(t, "ingest", "claude_code", "--path", a.root, "--db", a.db)
	run(t, "pair", "--relay", srv.URL, "--db", a.db)
	run(t, "sync", "--db", a.db)

	// A device that pairs on its own makes a new key and space, so it can't
	// see A's space at all; forging A's space id with another key is refused.
	other := newDevice(t, nil)
	otherCode := pairingCode(t, run(t, "pair", "--relay", srv.URL, "--db", other.db))
	aCode := pairingCode(t, run(t, "pair", "--db", a.db))
	forged := forgeSpace(t, otherCode, aCode)
	intruder := newDevice(t, nil)
	if _, err := runErr("join", forged, "--db", intruder.db); err == nil || !strings.Contains(err.Error(), "wrong token") {
		t.Fatalf("forged code: err = %v, want a wrong-token refusal", err)
	}
}

// forgeSpace builds a pairing code for victim's space but with attacker's key.
func forgeSpace(t *testing.T, attacker, victim string) string {
	t.Helper()
	att, err := devsync.ParsePairingCode(attacker)
	if err != nil {
		t.Fatal(err)
	}
	vic, err := devsync.ParsePairingCode(victim)
	if err != nil {
		t.Fatal(err)
	}
	return (&devsync.Space{ID: vic.ID, Key: att.Key, Relay: vic.Relay}).PairingCode()
}

// The daemon's periodic run collects from the default transcript location and
// syncs, so devices running only the daemon converge.
func TestDaemonRunsCollectAndSync(t *testing.T) {
	srv := httptest.NewServer((&relay.Server{Dir: t.TempDir()}).Handler())
	defer srv.Close()

	home := func(files map[string][]byte) (homeDir, db string) {
		homeDir = t.TempDir()
		for rel, content := range files {
			path := filepath.Join(homeDir, ".claude", "projects", rel)
			os.MkdirAll(filepath.Dir(path), 0o755)
			if err := os.WriteFile(path, content, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return homeDir, filepath.Join(homeDir, "archive.duckdb")
	}
	homeA, dbA := home(map[string][]byte{sample: transcript(t, sample, 0)})
	homeB, dbB := home(map[string][]byte{noCwd: transcript(t, noCwd, 0)})

	cycle := func(homeDir, db string) {
		t.Helper()
		t.Setenv("HOME", homeDir)
		var out bytes.Buffer
		e := &env{ctx: context.Background(), stdout: &out, stderr: &out}
		if err := daemonRun(e, db, log.New(&out, "", 0)); err != nil {
			t.Fatalf("daemon run: %v\n%s", err, out.String())
		}
		if strings.Contains(out.String(), "sync skipped") {
			t.Fatalf("daemon couldn't sync:\n%s", out.String())
		}
	}

	t.Setenv("HOME", homeA)
	code := pairingCode(t, run(t, "pair", "--relay", srv.URL, "--db", dbA))
	t.Setenv("HOME", homeB)
	run(t, "join", code, "--db", dbB)

	cycle(homeA, dbA) // collects and uploads A's session
	cycle(homeB, dbB) // collects B's, uploads it, downloads A's
	cycle(homeA, dbA) // downloads B's

	a, b := dump(t, dbA), dump(t, dbB)
	if n := len(a.(map[string]any)["conversations"].([]any)); n != 2 {
		t.Fatalf("A has %d conversations after daemon runs, want 2", n)
	}
	if !reflect.DeepEqual(withoutPaths(a), withoutPaths(b)) {
		t.Fatal("devices disagree after daemon runs")
	}
}

func TestDaemonStopsOnInterrupt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int)
	go func() {
		var out bytes.Buffer
		done <- Run(ctx, []string{"daemon", "--interval", "1h", "--db", filepath.Join(t.TempDir(), "a.duckdb")}, &out, &out)
	}()
	time.Sleep(500 * time.Millisecond)
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("daemon exited %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon didn't stop on interrupt")
	}
}

// Content that arrives by sync is searchable straight away, without a
// manual reindex, when the FTS extension is installed.
func TestSearchFindsSyncedContentWithoutReindex(t *testing.T) {
	srv := httptest.NewServer((&relay.Server{Dir: t.TempDir()}).Handler())
	defer srv.Close()
	a := newDevice(t, map[string][]byte{sample: transcript(t, sample, 0)})
	b := newDevice(t, map[string][]byte{noCwd: transcript(t, noCwd, 0)})

	run(t, "ingest", "claude_code", "--path", b.root, "--db", b.db)
	if !ftsAvailable(t, b.db) {
		t.Skip("DuckDB FTS extension not installed (run `chatstrata reindex --install-fts` once)")
	}
	run(t, "reindex", "--db", b.db) // B has a search index before anything syncs

	run(t, "ingest", "claude_code", "--path", a.root, "--db", a.db)
	code := pairingCode(t, run(t, "pair", "--relay", srv.URL, "--db", a.db))
	run(t, "sync", "--db", a.db)
	run(t, "join", code, "--db", b.db)

	// Found straight away: by the index, or by the substring matching that
	// covers blocks the index doesn't hold yet.
	out := run(t, "search", "refactor", "--db", b.db)
	if !strings.Contains(out, "Refactor the user auth module") {
		t.Errorf("synced content not found without a reindex:\n%s", out)
	}
}

func ftsAvailable(t *testing.T, db string) bool {
	t.Helper()
	s, err := store.OpenReadOnly(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	return s.LoadFTS(context.Background())
}

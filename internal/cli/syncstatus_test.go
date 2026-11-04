package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brandonbosch/chatstrata/v2/internal/devsync"
	"github.com/brandonbosch/chatstrata/v2/internal/relay"
)

// backdate moves a device's sync record into the past, as if its last
// attempts had happened that long ago.
func backdate(t *testing.T, db string, by time.Duration) {
	t.Helper()
	root := logDir(db)
	st, err := devsync.LoadStatus(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, ts := range []*time.Time{&st.LastAttempt, &st.LastSuccess, &st.FailingSince} {
		if !ts.IsZero() {
			*ts = ts.Add(-by)
		}
	}
	b, _ := json.Marshal(st)
	if err := os.WriteFile(filepath.Join(root, "sync-state.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestDeadRelayIsReported walks a relay through working, briefly away, gone
// for over a day, and replaced, checking what `devices`, `doctor` and the
// daemon say at each step.
func TestDeadRelayIsReported(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // the daemon finds no sources to collect
	srv := httptest.NewServer((&relay.Server{Dir: t.TempDir()}).Handler())
	a := newDevice(t, map[string][]byte{sample: transcript(t, sample, 0)})
	run(t, "ingest", "claude_code", "--path", a.root, "--db", a.db)

	// Paired but never synced.
	run(t, "pair", "--relay", srv.URL, "--db", a.db)
	if out := run(t, "doctor", "--db", a.db); !strings.Contains(out, "no successful sync recorded yet") {
		t.Errorf("doctor before the first sync:\n%s", out)
	}
	if out := run(t, "devices", "--db", a.db); !strings.Contains(out, "Last sync:   none recorded yet") {
		t.Errorf("devices before the first sync:\n%s", out)
	}

	// Working.
	run(t, "sync", "--db", a.db)
	if out := run(t, "devices", "--db", a.db); !strings.Contains(out, "Last sync:   less than a minute ago") {
		t.Errorf("devices after a sync:\n%s", out)
	}
	if out := run(t, "doctor", "--db", a.db); !strings.Contains(out, "All checks passed") || strings.Contains(out, "sync") {
		t.Errorf("doctor after a sync:\n%s", out)
	}

	// Briefly away: noted, not an issue.
	srv.Close()
	if _, err := runErr("sync", "--db", a.db); err == nil {
		t.Fatal("sync against a stopped relay succeeded")
	}
	out := run(t, "doctor", "--db", a.db)
	if !strings.Contains(out, "ℹ sync has failed for less than a minute") || !strings.Contains(out, "All checks passed") {
		t.Errorf("doctor with the relay briefly away:\n%s", out)
	}

	// Gone for two days: an issue everywhere.
	backdate(t, a.db, 50*time.Hour)
	out = run(t, "doctor", "--db", a.db)
	if !strings.Contains(out, "⚠ sync has failed for 2 days") || !strings.Contains(out, "1 issue(s) found") {
		t.Errorf("doctor with the relay gone for two days:\n%s", out)
	}
	if out := run(t, "devices", "--db", a.db); !strings.Contains(out, "Last sync:   ⚠ failing for 2 days (last success 2 days ago, ") {
		t.Errorf("devices with the relay gone for two days:\n%s", out)
	}
	var logged bytes.Buffer
	e := &env{ctx: context.Background(), stdout: &logged, stderr: &logged}
	if err := daemonRun(e, a.db, log.New(&logged, "", 0)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logged.String(), "WARNING: sync has failed for 2 days") {
		t.Errorf("daemon log with the relay gone for two days:\n%s", logged.String())
	}

	// Replaced: back to normal.
	replacement := httptest.NewServer((&relay.Server{Dir: t.TempDir()}).Handler())
	defer replacement.Close()
	run(t, "sync", "--set-relay", replacement.URL, "--db", a.db)
	if out := run(t, "doctor", "--db", a.db); strings.Contains(out, "sync") || !strings.Contains(out, "All checks passed") {
		t.Errorf("doctor after moving to a working relay:\n%s", out)
	}
}

func TestDoctorNotesSyncThatHasNotRunLately(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // doctor checks the sources installed in HOME
	srv := httptest.NewServer((&relay.Server{Dir: t.TempDir()}).Handler())
	defer srv.Close()
	a := newDevice(t, map[string][]byte{sample: transcript(t, sample, 0)})
	run(t, "ingest", "claude_code", "--path", a.root, "--db", a.db)
	run(t, "pair", "--relay", srv.URL, "--db", a.db)
	run(t, "sync", "--db", a.db)
	backdate(t, a.db, 3*24*time.Hour)

	out := run(t, "doctor", "--db", a.db)
	if !strings.Contains(out, "ℹ last successful sync was 3 days ago; is the daemon running?") || !strings.Contains(out, "All checks passed") {
		t.Errorf("doctor when sync hasn't run for days:\n%s", out)
	}
}

func TestDoctorSaysNothingAboutSyncWhenNotPaired(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // doctor checks the sources installed in HOME
	a := newDevice(t, map[string][]byte{sample: transcript(t, sample, 0)})
	run(t, "ingest", "claude_code", "--path", a.root, "--db", a.db)
	if out := run(t, "doctor", "--db", a.db); strings.Contains(out, "sync") {
		t.Errorf("doctor on an unpaired device:\n%s", out)
	}
}

func TestStoppingIsNotRecordedAsAFailure(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	recordSync(&env{ctx: ctx, stdout: &out, stderr: &out}, root, errors.New("context canceled"))
	if st, err := devsync.LoadStatus(root); err != nil || st != (devsync.Status{}) {
		t.Fatalf("a cancelled attempt was recorded: %+v, %v", st, err)
	}
}

func TestDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Second: "less than a minute",
		time.Minute:      "1 minute",
		45 * time.Minute: "45 minutes",
		time.Hour:        "1 hour",
		47 * time.Hour:   "47 hours",
		50 * time.Hour:   "2 days",
	} {
		if got := duration(d); got != want {
			t.Errorf("duration(%s) = %q, want %q", d, got, want)
		}
	}
}

package devsync

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecordSyncTracksHowLongSyncHasFailed(t *testing.T) {
	root := t.TempDir()
	t0 := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	record := func(at time.Time, err error) Status {
		t.Helper()
		if err := RecordSync(root, at, err); err != nil {
			t.Fatal(err)
		}
		st, err := LoadStatus(root)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}

	if st, err := LoadStatus(root); err != nil || st != (Status{}) {
		t.Fatalf("before any attempt: %+v, %v; want a zero status", st, err)
	}

	st := record(t0, nil)
	if !st.LastSuccess.Equal(t0) || !st.FailingSince.IsZero() {
		t.Fatalf("after a success: %+v", st)
	}

	t1 := t0.Add(time.Hour)
	record(t1, errors.New("relay down"))
	st = record(t1.Add(30*time.Minute), errors.New("relay still down"))
	if !st.FailingSince.Equal(t1) || !st.LastSuccess.Equal(t0) || st.LastError != "relay still down" {
		t.Fatalf("after two failures: %+v; want failing since the first, last error the latest", st)
	}
	if st.Stale(t1.Add(StaleAfter - time.Minute)) {
		t.Error("stale before failing for StaleAfter")
	}
	if !st.Stale(t1.Add(StaleAfter)) {
		t.Error("not stale after failing for StaleAfter")
	}

	st = record(t1.Add(2*time.Hour), nil)
	if !st.FailingSince.IsZero() || st.LastError != "" || st.Stale(t1.Add(48*time.Hour)) {
		t.Fatalf("a success must clear the failure: %+v", st)
	}

	st = record(t1.Add(3*time.Hour), errors.New(strings.Repeat("x", 1000)))
	if len(st.LastError) > maxErrorLen+3 {
		t.Errorf("stored error is %d bytes, want it bounded", len(st.LastError))
	}
	fi, err := os.Stat(filepath.Join(root, statusFile))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600 like the other sync files", fi.Mode().Perm())
	}
}

func TestRecordSyncReplacesAnUnreadableRecord(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, statusFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	if err := RecordSync(root, at, nil); err != nil {
		t.Fatal(err)
	}
	if st, err := LoadStatus(root); err != nil || !st.LastSuccess.Equal(at) {
		t.Fatalf("%+v, %v", st, err)
	}
}

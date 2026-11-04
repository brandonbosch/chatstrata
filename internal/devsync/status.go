package devsync

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// StaleAfter is how long sync may keep failing before it is reported as a
// problem rather than a relay that is briefly away (a laptop closed for the
// night, a restart).
const StaleAfter = 24 * time.Hour

// Status is this device's record of its sync attempts, kept in
// sync-state.json next to sync.json. Its times come from this device's clock
// and are only ever displayed; nothing orders or merges by them (ADR 0005).
type Status struct {
	LastAttempt time.Time `json:"last_attempt,omitzero"`
	LastSuccess time.Time `json:"last_success,omitzero"`
	// FailingSince is the first failed attempt after the last success; it is
	// zero while sync works.
	FailingSince time.Time `json:"failing_since,omitzero"`
	LastError    string    `json:"last_error,omitempty"`
}

const statusFile = "sync-state.json"

// maxErrorLen bounds the stored error, which can include a relay's response.
const maxErrorLen = 300

// LoadStatus reads the sync record of the log at root. A device that never
// attempted a sync has a zero Status.
func LoadStatus(root string) (Status, error) {
	var st Status
	b, err := os.ReadFile(filepath.Join(root, statusFile))
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, fmt.Errorf("read %s: %w", statusFile, err)
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, fmt.Errorf("read %s: %w", statusFile, err)
	}
	return st, nil
}

// RecordSync adds the outcome of a sync attempt made at now (nil for
// success) to the record of the log at root.
func RecordSync(root string, now time.Time, syncErr error) error {
	st, err := LoadStatus(root)
	if err != nil {
		st = Status{} // an unreadable record is replaced, not fatal
	}
	st.LastAttempt = now
	if syncErr == nil {
		st.LastSuccess, st.FailingSince, st.LastError = now, time.Time{}, ""
	} else {
		if st.FailingSince.IsZero() {
			st.FailingSince = now
		}
		st.LastError = syncErr.Error()
		if len(st.LastError) > maxErrorLen {
			st.LastError = st.LastError[:maxErrorLen] + "..."
		}
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	if err := writePrivate(filepath.Join(root, statusFile), append(b, '\n')); err != nil {
		return fmt.Errorf("write %s: %w", statusFile, err)
	}
	return nil
}

// Stale reports whether sync has kept failing for at least StaleAfter.
func (s Status) Stale(now time.Time) bool {
	return !s.FailingSince.IsZero() && now.Sub(s.FailingSince) >= StaleAfter
}

package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/brandonbosch/chatstrata/v2/internal/devsync"
)

// recordSync notes the outcome of a sync attempt, so `devices`, `doctor` and
// the daemon can tell a relay that has been gone for days from one that is
// briefly away. An attempt cut short because the command or daemon is
// stopping says nothing about the relay and isn't recorded.
func recordSync(e *env, root string, syncErr error) {
	if syncErr != nil && e.ctx.Err() != nil {
		return
	}
	if err := devsync.RecordSync(root, time.Now(), syncErr); err != nil {
		_, _ = fmt.Fprintf(e.stderr, "warning: %s\n", err)
	}
}

// syncFailure is the daemon's log line for a failed sync: routine while the
// relay has been away briefly, a warning once it has been failing for long.
func syncFailure(root string, syncErr error) string {
	st, err := devsync.LoadStatus(root)
	now := time.Now()
	if err != nil || !st.Stale(now) {
		return fmt.Sprintf("sync skipped: %s", syncErr)
	}
	return fmt.Sprintf("WARNING: sync has failed for %s (since %s); check that the relay is running and reachable: %s",
		duration(now.Sub(st.FailingSince)), st.FailingSince.Local().Format("2006-01-02 15:04"), syncErr)
}

// printLastSync adds the device's sync record to `devices`.
func printLastSync(e *env, root string) {
	st, err := devsync.LoadStatus(root)
	if err != nil {
		_, _ = fmt.Fprintf(e.stdout, "Last sync:   unknown (%s)\n", err)
		return
	}
	mark := ""
	if st.Stale(time.Now()) {
		mark = "⚠ "
	}
	_, _ = fmt.Fprintf(e.stdout, "Last sync:   %s%s\n", mark, syncSummary(st, time.Now()))
}

func syncSummary(st devsync.Status, now time.Time) string {
	last := "never succeeded"
	if !st.LastSuccess.IsZero() {
		last = fmt.Sprintf("%s ago, %s", duration(now.Sub(st.LastSuccess)), st.LastSuccess.Local().Format("2006-01-02 15:04"))
	}
	switch {
	case st.LastAttempt.IsZero():
		return "none recorded yet"
	case st.FailingSince.IsZero():
		return last
	case st.LastSuccess.IsZero():
		return fmt.Sprintf("failing for %s (never succeeded): %s", duration(now.Sub(st.FailingSince)), st.LastError)
	default:
		return fmt.Sprintf("failing for %s (last success %s): %s", duration(now.Sub(st.FailingSince)), last, st.LastError)
	}
}

// checkSync is doctor's sync check. Sync that has kept failing is an issue;
// sync that simply hasn't run lately (no daemon, a machine that was off) is
// only noted.
func checkSync(e *env, root string) int {
	if _, err := devsync.Load(root, ""); errors.Is(err, devsync.ErrNotPaired) {
		return 0
	}
	st, err := devsync.LoadStatus(root)
	if err != nil {
		_, _ = fmt.Fprintf(e.stdout, "  ⚠ %s\n", err)
		return 1
	}
	now := time.Now()
	switch {
	case st.Stale(now):
		_, _ = fmt.Fprintf(e.stdout, "  ⚠ sync has failed for %s; check that the relay is running and reachable (`chatstrata devices`): %s\n",
			duration(now.Sub(st.FailingSince)), st.LastError)
		return 1
	case !st.FailingSince.IsZero():
		_, _ = fmt.Fprintf(e.stdout, "  ℹ sync has failed for %s (an issue after %s): %s\n",
			duration(now.Sub(st.FailingSince)), duration(devsync.StaleAfter), st.LastError)
	case st.LastSuccess.IsZero():
		_, _ = fmt.Fprintln(e.stdout, "  ℹ no successful sync recorded yet; run `chatstrata sync`")
	case now.Sub(st.LastSuccess) >= devsync.StaleAfter:
		_, _ = fmt.Fprintf(e.stdout, "  ℹ last successful sync was %s ago; is the daemon running? (`chatstrata schedule status`)\n",
			duration(now.Sub(st.LastSuccess)))
	}
	return 0
}

// duration renders an elapsed time coarsely: "5 minutes", "3 hours", "2 days".
func duration(d time.Duration) string {
	unit := func(n int, name string) string {
		if n == 1 {
			return "1 " + name
		}
		return fmt.Sprintf("%d %ss", n, name)
	}
	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < time.Hour:
		return unit(int(d/time.Minute), "minute")
	case d < 48*time.Hour:
		return unit(int(d/time.Hour), "hour")
	default:
		return unit(int(d/(24*time.Hour)), "day")
	}
}

package cli

import (
	"fmt"
	"log"
)

func runCompact(e *env, args []string) error {
	fs := newFlagSet(e, "compact", "compact [--db PATH]\n\nRewrite the archive without the space its deleted rows still take. The daemon\ndoes this by itself when enough of the archive is dead; this runs it now.")
	db := fs.String("db", "", "Override the database path.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	a, err := openArchive(e, *db)
	if err != nil {
		return err
	}
	defer func() { _ = a.Close() }()
	_, _ = fmt.Fprintf(e.stdout, "Compacting %s...\n", a.store.Path)
	before, after, err := a.store.Compact(e.ctx)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(e.stdout, "Done. %s -> %s.\n", megabytes(before), megabytes(after))
	return nil
}

// compactIfDue compacts the archive when enough of it is dead space. It runs
// in the daemon, which holds the archive anyway; a compaction closes it. A
// failed compaction leaves the archive as it was and is retried next run.
func compactIfDue(e *env, a *archive, logger *log.Logger) {
	due, err := a.store.CompactionDue(e.ctx)
	if err != nil {
		logger.Printf("compaction check failed: %s", err)
		return
	}
	if !due {
		return
	}
	before, after, err := a.store.Compact(e.ctx)
	if err != nil {
		logger.Printf("compaction failed, archive unchanged: %s", err)
		return
	}
	logger.Printf("archive: compacted %s -> %s", megabytes(before), megabytes(after))
}

func megabytes(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/1e6) }

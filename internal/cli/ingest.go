package cli

import (
	"fmt"
	"slices"
)

func runIngest(e *env, args []string) error {
	fs := newFlagSet(e, "ingest", "ingest SOURCE_NAME [--path PATH] [--db PATH] [--incremental] [--limit N] [--dry-run]")
	path := fs.String("path", "", "Override the default path for this source.")
	db := fs.String("db", "", "Override the database path.")
	limit := fs.Int("limit", 0, "Ingest at most N conversations.")
	dryRun := fs.Bool("dry-run", false, "Discover only; do not write to the database.")
	incremental := fs.Bool("incremental", false, "Skip conversations whose source file has not changed since last ingest.")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return usageError{"expected exactly one SOURCE_NAME"}
	}
	name := positional[0]

	src, ok := sources[name]
	if !ok {
		if slices.Contains(notYetPorted, name) {
			fmt.Fprintf(e.stderr, "Source %q is not in the Go version yet; use the Python chatstrata for it.\n", name)
		} else {
			fmt.Fprintf(e.stderr, "Unknown source: %s\nRun `chatstrata sources` to see available adapters.\n", name)
		}
		return exitError{1}
	}

	handles, err := src.Discover(*path)
	if err != nil {
		fmt.Fprintf(e.stderr, "! %s\n", err)
		return exitError{1}
	}
	if len(handles) == 0 {
		fmt.Fprintf(e.stdout, "No conversations found for source '%s'.\n", name)
		return nil
	}
	if *limit > 0 && *limit < len(handles) {
		handles = handles[:*limit]
	}

	if *dryRun {
		fmt.Fprintf(e.stdout, "Would ingest %d conversations from %s:\n", len(handles), name)
		for _, h := range handles[:min(20, len(handles))] {
			fmt.Fprintf(e.stdout, "  %s  (%s)\n", h.SourceNativeID, h.Path)
		}
		if len(handles) > 20 {
			fmt.Fprintf(e.stdout, "  ... and %d more\n", len(handles)-20)
		}
		return nil
	}

	a, err := openArchive(e, *db)
	if err != nil {
		return err
	}
	defer a.Close()

	if err := a.store.EnsureSource(e.ctx, src); err != nil {
		return err
	}
	collected, err := a.proj.Collect(e.ctx, src, handles, *incremental, e.stderr)
	if err != nil {
		return err
	}
	stats, err := a.proj.CatchUp(e.ctx, false, e.stderr)
	if err != nil {
		return err
	}
	skipped := len(handles) - stats.Stored - collected.Failed - stats.Failed
	fmt.Fprintf(e.stdout, "\nDone. Ingested: %d  Skipped: %d  Failed: %d\n",
		stats.Stored, max(0, skipped), collected.Failed+stats.Failed)
	fmt.Fprintf(e.stdout, "Database: %s\n", a.store.Path)
	return nil
}

package cli

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"sync"

	"github.com/brandonbosch/chatstrata/internal/model"
	"github.com/brandonbosch/chatstrata/internal/store"
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

	dbPath, err := store.ResolvePath(*db)
	if err != nil {
		return err
	}
	s, err := store.Open(e.ctx, dbPath)
	if err != nil {
		return err
	}
	defer s.Close()

	result, err := ingestSource(e, s, src, handles, *incremental)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "\nDone. Ingested: %d  Skipped: %d  Failed: %d\n", result.ingested, result.skipped, result.failed)
	fmt.Fprintf(e.stdout, "Database: %s\n", dbPath)
	return nil
}

type ingestResult struct {
	ingested, skipped, failed int
}

type parsed struct {
	handle model.Handle
	conv   *model.Conversation
	mtime  *float64
	err    error
}

// ingestSource parses transcripts in parallel and writes them from this
// goroutine, the archive's only writer.
func ingestSource(e *env, s *store.Store, src model.Source, handles []model.Handle, incremental bool) (ingestResult, error) {
	var result ingestResult
	if err := s.EnsureSource(e.ctx, src); err != nil {
		return result, err
	}
	var stored map[string]float64
	if incremental {
		var err error
		if stored, err = s.StoredMtimes(e.ctx, src.Name()); err != nil {
			return result, err
		}
	}

	work := make(chan model.Handle)
	out := make(chan parsed)
	var wg sync.WaitGroup
	for range runtime.NumCPU() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for h := range work {
				p := parsed{handle: h, mtime: fileMtime(h.Path)}
				p.conv, p.err = src.Parse(h)
				out <- p
			}
		}()
	}

	go func() {
		defer close(work)
		for _, h := range handles {
			if incremental && h.Path != "" {
				if mtime := fileMtime(h.Path); mtime != nil {
					if prev, ok := stored[h.SourceNativeID]; ok && prev == *mtime {
						out <- parsed{handle: h} // nil conv, nil err: skipped
						continue
					}
				}
			}
			select {
			case work <- h:
			case <-e.ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(out)
	}()

	var writeErr error
	for p := range out {
		if writeErr != nil {
			continue // drain so the workers can exit
		}
		switch {
		case p.err != nil:
			result.failed++
			fmt.Fprintf(e.stderr, "  ! failed to parse %s: %s\n", p.handle.SourceNativeID, p.err)
		case p.conv == nil:
			result.skipped++
		case len(p.conv.Messages) == 0:
			// Sessions with no messages are not stored, as in Python.
		default:
			action, err := s.Ingest(e.ctx, src.Name(), p.conv, p.mtime)
			if err != nil {
				if errors.Is(err, e.ctx.Err()) {
					writeErr = err
					continue
				}
				result.failed++
				fmt.Fprintf(e.stderr, "  ! failed to store %s: %s\n", p.handle.SourceNativeID, err)
				continue
			}
			if action == store.Unchanged {
				result.skipped++
			} else {
				result.ingested++
			}
		}
	}
	return result, writeErr
}

func fileMtime(path string) *float64 {
	if path == "" {
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	m := float64(fi.ModTime().UnixNano()) / 1e9
	return &m
}

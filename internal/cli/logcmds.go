package cli

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/brandonbosch/chatstrata/internal/obslog"
	"github.com/brandonbosch/chatstrata/internal/store"
)

func runRebuild(e *env, args []string) error {
	fs := newFlagSet(e, "rebuild", "rebuild [--db PATH]")
	db := fs.String("db", "", "Override the database path.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	dbPath, err := store.ResolvePath(*db)
	if err != nil {
		return err
	}
	logPath := logDir(dbPath)
	if _, err := os.Stat(filepath.Join(logPath, "identity.json")); err != nil {
		return fmt.Errorf("no observation log at %s to rebuild from", logPath)
	}

	// Build next to the archive, then swap it in, so a failed rebuild leaves
	// the current archive untouched.
	tmp := dbPath + ".rebuild"
	for _, p := range []string{tmp, tmp + ".wal"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	fmt.Fprintf(e.stdout, "Rebuilding %s from %s...\n", dbPath, logPath)
	s, err := store.Open(e.ctx, tmp)
	if err != nil {
		return err
	}
	l, err := obslog.Open(logPath)
	if err != nil {
		s.Close()
		return err
	}
	a := &archive{store: s, log: l}
	a.proj = newProjector(s, l)
	stats, err := a.proj.CatchUp(e.ctx, true, e.stderr)
	if err == nil {
		err = s.Checkpoint(e.ctx)
	}
	if cerr := s.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("rebuild: %w", err)
	}

	// A stale write-ahead log next to the old file would be replayed into the
	// new one, so it goes first.
	if err := os.Remove(dbPath + ".wal"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(tmp, dbPath); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "Done. Segments: %d  Conversations: %d  Kept in log only (source not in the Go version yet): %d  Failed: %d\n",
		stats.Segments, stats.Stored, stats.Unported, stats.Failed)
	if !stats.Reindexed {
		fmt.Fprintln(e.stdout, "Run `chatstrata reindex` to rebuild the search index.")
	}
	return nil
}

func runImportLegacy(e *env, args []string) error {
	fs := newFlagSet(e, "import-legacy", "import-legacy [PYTHON_ARCHIVE] [--db PATH]\n\nPYTHON_ARCHIVE defaults to the Python app's archive: $CHATSTRATA_DB, else chatstrata.duckdb in the data directory.")
	db := fs.String("db", "", "Override the database path.")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) > 1 {
		return usageError{"expected at most one path, the Python-era chatstrata.duckdb"}
	}
	var legacyPath string
	if len(positional) == 1 {
		legacyPath = positional[0]
	} else if legacyPath, err = pythonArchivePath(); err != nil {
		return err
	}
	legacyPath, err = filepath.Abs(legacyPath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(legacyPath); err != nil {
		return fmt.Errorf("Python archive: %w", err)
	}

	a, err := openArchive(e, *db)
	if err != nil {
		return err
	}
	defer a.Close()
	if own, _ := filepath.Abs(a.store.Path); own == legacyPath {
		return usageError{"that is this archive; pass the Python archive (chatstrata.duckdb)"}
	}

	obs, already, err := readLegacy(e.ctx, a, legacyPath)
	if err != nil {
		return err
	}
	if _, err := a.log.Append(obs); err != nil {
		return err
	}
	stats, err := a.proj.CatchUp(e.ctx, false, e.stderr)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "Done. Imported: %d conversations  Already imported: %d  Kept in log only (source not in the Go version yet): %d  Failed: %d\n",
		len(obs), already, stats.Unported, stats.Failed)
	fmt.Fprintf(e.stdout, "Database: %s\n", a.store.Path)
	return nil
}

// readLegacy turns each conversation of a Python archive into a snapshot
// observation built from its raw_events: the transcript lines as Python
// stored them (re-serialized JSON), so history whose source files are gone
// survives. Conversations already imported with identical content are skipped.
func readLegacy(ctx context.Context, a *archive, path string) ([]*obslog.Observation, int, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, 0, fmt.Errorf("open %s: %w", path, err)
	}
	legacy, err := sql.Open("duckdb", path+"?access_mode=READ_ONLY")
	if err != nil {
		return nil, 0, err
	}
	defer legacy.Close()

	convs, err := legacy.QueryContext(ctx, `
		SELECT source_id, source_native_id, raw_path, project FROM conversations
		ORDER BY source_id, source_native_id`)
	if err != nil {
		return nil, 0, fmt.Errorf("read %s (is the Python app writing to it?): %w", path, err)
	}
	type conv struct {
		source, native   string
		rawPath, project sql.NullString
	}
	var list []conv
	for convs.Next() {
		var c conv
		if err := convs.Scan(&c.source, &c.native, &c.rawPath, &c.project); err != nil {
			convs.Close()
			return nil, 0, err
		}
		list = append(list, c)
	}
	convs.Close()

	var out []*obslog.Observation
	already := 0
	for _, c := range list {
		rows, err := legacy.QueryContext(ctx, `
			SELECT payload::VARCHAR FROM raw_events
			WHERE source_id = ? AND source_native_conversation_id = ?
			ORDER BY line_number`, c.source, c.native)
		if err != nil {
			return nil, 0, err
		}
		var b strings.Builder
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				rows.Close()
				return nil, 0, err
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
		rows.Close()
		if b.Len() == 0 {
			continue
		}
		content := []byte(b.String())
		sum := sha256.Sum256(content)
		key := obslog.Key{Source: c.source, Locator: c.native}
		dup, err := a.store.HasLegacyObservation(ctx, key, hex.EncodeToString(sum[:]))
		if err != nil {
			return nil, 0, err
		}
		if dup {
			already++
			continue
		}
		out = append(out, &obslog.Observation{
			Source:      c.source,
			Locator:     c.native,
			Kind:        obslog.Snapshot,
			Legacy:      true,
			Path:        c.rawPath.String,
			ProjectHint: c.project.String,
			Content:     content,
		})
	}
	return out, already, nil
}

// pythonArchivePath is where the Python app keeps its archive by default
// (chatstrata/core/db.py): $CHATSTRATA_DB, else chatstrata.duckdb in the same
// data directory the Go app uses.
func pythonArchivePath() (string, error) {
	if p := os.Getenv("CHATSTRATA_DB"); p != "" {
		if rest, ok := strings.CutPrefix(p, "~/"); ok {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			p = filepath.Join(home, rest)
		}
		return p, nil
	}
	dir, err := store.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "chatstrata.duckdb"), nil
}

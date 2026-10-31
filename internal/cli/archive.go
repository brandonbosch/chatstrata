package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/brandonbosch/chatstrata/internal/model"
	"github.com/brandonbosch/chatstrata/internal/obslog"
	"github.com/brandonbosch/chatstrata/internal/project"
	"github.com/brandonbosch/chatstrata/internal/store"
)

// archive is an open archive for writing: the observation log, which is the
// source of truth, and the DuckDB projection built from it.
type archive struct {
	store *store.Store
	log   *obslog.Log
	proj  *project.Projector
}

// logDir is where the observation log of a projection lives: next to it,
// named after it (chatstrata-go.duckdb -> chatstrata-go.log).
func logDir(dbPath string) string {
	return strings.TrimSuffix(dbPath, filepath.Ext(dbPath)) + ".log"
}

// openArchive opens the projection (taking DuckDB's write lock first, so two
// writers can't share a log) and its log, and applies any segments the
// projection hasn't seen yet.
func openArchive(e *env, db string) (*archive, error) {
	dbPath, err := store.ResolvePath(db)
	if err != nil {
		return nil, err
	}
	return openArchiveAt(e, dbPath, logDir(dbPath))
}

func openArchiveAt(e *env, dbPath, logPath string) (*archive, error) {
	s, err := store.Open(e.ctx, dbPath)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "conflicting lock") {
			return nil, fmt.Errorf("%w\nClose other DuckDB connections, including idle Python/notebook analysis sessions. Use `chatstrata query` or MCP for analysis; they close the archive after each request", err)
		}
		return nil, err
	}
	l, err := obslog.Open(logPath)
	if err != nil {
		s.Close()
		return nil, err
	}
	a := &archive{store: s, log: l, proj: newProjector(s, l)}
	if _, err := a.proj.CatchUp(e.ctx, false, e.stderr); err != nil {
		s.Close()
		return nil, err
	}
	return a, nil
}

func (a *archive) Close() error { return a.store.Close() }

func newProjector(s *store.Store, l *obslog.Log) *project.Projector {
	srcs := make(map[string]model.Source, len(sources))
	for name, src := range sources {
		srcs[name] = src
	}
	return &project.Projector{Store: s, Log: l, Sources: srcs}
}

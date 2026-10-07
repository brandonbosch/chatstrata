// Package store owns the DuckDB archive: opening it, applying the shared
// schema migrations, and writing parsed conversations.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	_ "github.com/duckdb/duckdb-go/v2" // registers the "duckdb" driver

	"github.com/brandonbosch/chatstrata"
	"github.com/brandonbosch/chatstrata/internal/store/migrations"
)

// Store is an open archive. Writes go through a single connection because
// explicit transactions and appenders are tied to one connection.
type Store struct {
	DB   *sql.DB
	conn *sql.Conn
	Path string
}

// DefaultPath returns the Go implementation's archive location. During the
// rewrite it is deliberately a different file from the Python archive
// (chatstrata.duckdb in the same directory), so running both side by side
// never mixes their data. CHATSTRATA_GO_DB overrides it.
func DefaultPath() (string, error) {
	if env := os.Getenv("CHATSTRATA_GO_DB"); env != "" {
		return expandHome(env)
	}
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "chatstrata-go.duckdb"), nil
}

// DataDir matches platformdirs.user_data_dir("chatstrata") used by Python.
func DataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "chatstrata"), nil
	case "windows":
		if appData := os.Getenv("LOCALAPPDATA"); appData != "" {
			return filepath.Join(appData, "chatstrata"), nil
		}
		return filepath.Join(home, "AppData", "Local", "chatstrata"), nil
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "chatstrata"), nil
	}
	return filepath.Join(home, ".local", "share", "chatstrata"), nil
}

// ResolvePath returns the --db override if given, else the default.
func ResolvePath(override string) (string, error) {
	if override != "" {
		return expandHome(override)
	}
	return DefaultPath()
}

func expandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand %s: %w", p, err)
	}
	return filepath.Join(home, p[1:]), nil
}

// Open opens (creating if needed) the archive for reading and writing and
// applies pending migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create archive directory: %w", err)
	}
	s, err := open(ctx, path, path)
	if err != nil {
		return nil, err
	}
	if err := s.migrate(ctx); err != nil {
		s.Close()
		return nil, err
	}
	s.LoadFTS(ctx)
	return s, nil
}

// ErrNoArchive is returned by OpenReadOnly when the archive doesn't exist yet.
var ErrNoArchive = errors.New("no archive")

// OpenReadOnly opens an existing archive without taking the write lock.
func OpenReadOnly(ctx context.Context, path string) (*Store, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w at %s", ErrNoArchive, path)
	}
	s, err := open(ctx, path, path+"?access_mode=READ_ONLY")
	if err != nil {
		return nil, err
	}
	s.LoadFTS(ctx)
	return s, nil
}

func open(ctx context.Context, path, dsn string) (*Store, error) {
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return &Store{DB: db, conn: conn, Path: path}, nil
}

func (s *Store) Close() error {
	s.conn.Close()
	return s.DB.Close()
}

// Conn returns the store's single connection, for queries that must see
// connection-local state such as loaded extensions.
func (s *Store) Conn() *sql.Conn { return s.conn }

// LoadFTS loads the full-text search extension if it is installed. It never
// downloads anything; see InstallFTS.
func (s *Store) LoadFTS(ctx context.Context) bool {
	_, err := s.conn.ExecContext(ctx, "LOAD fts")
	return err == nil
}

// InstallFTS downloads the DuckDB full-text search extension. This is the
// one network call search needs, so it only happens when the user asks.
func (s *Store) InstallFTS(ctx context.Context) error {
	if _, err := s.conn.ExecContext(ctx, "INSTALL fts"); err != nil {
		return fmt.Errorf("install fts extension: %w", err)
	}
	if _, err := s.conn.ExecContext(ctx, "LOAD fts"); err != nil {
		return fmt.Errorf("load fts extension: %w", err)
	}
	return nil
}

// RebuildFTS recreates the full-text index over content_blocks.text, with the
// same settings as the Python implementation.
func (s *Store) RebuildFTS(ctx context.Context) error {
	_, err := s.conn.ExecContext(ctx,
		`PRAGMA create_fts_index('content_blocks', 'id', 'text', stemmer='porter', stopwords='english', overwrite=1)`)
	return err
}

// FTSCoverage reports how much of content_blocks the full-text index covers:
// whether an index exists, how many blocks it holds, and how many blocks
// with text it doesn't hold yet. LoadFTS must have succeeded.
func (s *Store) FTSCoverage(ctx context.Context) (exists bool, indexed, pending int64, err error) {
	var n int
	if err := s.conn.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema = 'fts_main_content_blocks' AND table_name = 'docs'`).Scan(&n); err != nil || n == 0 {
		return false, 0, 0, err
	}
	err = s.conn.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM fts_main_content_blocks.docs),
		       (SELECT count(*) FROM content_blocks cb WHERE cb.text IS NOT NULL
		          AND NOT EXISTS (SELECT 1 FROM fts_main_content_blocks.docs d WHERE d.name = cb.id))`).Scan(&indexed, &pending)
	return true, indexed, pending, err
}

// FTS rebuild policy. DuckDB can't update a full-text index in place, and a
// rebuild costs time proportional to the whole archive, so it waits until
// the blocks not yet indexed are a fair share of it: the index grows
// geometrically and the cost per new block stays constant. Search covers
// the blocks in between with substring matching.
const (
	ftsMinPending   = 2000
	ftsPendingShare = 0.05
)

// FTSNeedsRebuild applies that policy: no index yet, or enough new blocks.
func FTSNeedsRebuild(exists bool, indexed, pending int64) bool {
	if !exists {
		return true
	}
	return pending >= max(ftsMinPending, int64(float64(indexed)*ftsPendingShare))
}

type migration struct {
	version int
	name    string
	fsys    fs.FS
	path    string
}

func (s *Store) SchemaVersion(ctx context.Context) int {
	var v string
	if err := s.conn.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&v); err != nil {
		return 0
	}
	n, _ := strconv.Atoi(v)
	return n
}

// migrate applies the migrations shared with the Python implementation, then
// the Go-only ones, recording the version the same way, so either
// implementation can open an archive the other created.
func (s *Store) migrate(ctx context.Context) error {
	type source struct {
		fsys fs.FS
		dir  string
	}
	var all []migration
	for _, src := range []source{{chatstrata.Migrations, "chatstrata/core/migrations"}, {migrations.FS, "."}} {
		entries, err := fs.ReadDir(src.fsys, src.dir)
		if err != nil {
			return fmt.Errorf("read migrations: %w", err)
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".sql") {
				continue
			}
			prefix, _, ok := strings.Cut(e.Name(), "_")
			v, err := strconv.Atoi(prefix)
			if !ok || err != nil {
				return fmt.Errorf("migration %s: name must start with a number", e.Name())
			}
			all = append(all, migration{version: v, name: e.Name(), fsys: src.fsys, path: path.Join(src.dir, e.Name())})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].version < all[j].version })
	for i := 1; i < len(all); i++ {
		if all[i].version == all[i-1].version {
			return fmt.Errorf("migrations %s and %s share version %d", all[i-1].name, all[i].name, all[i].version)
		}
	}

	current := s.SchemaVersion(ctx)
	for _, m := range all {
		if m.version <= current {
			continue
		}
		body, err := fs.ReadFile(m.fsys, m.path)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", m.name, err)
		}
		if _, err := s.conn.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("apply migration %s: %w", m.name, err)
		}
		if _, err := s.conn.ExecContext(ctx,
			`INSERT OR REPLACE INTO meta (key, value) VALUES ('schema_version', ?)`, strconv.Itoa(m.version)); err != nil {
			return fmt.Errorf("record migration %s: %w", m.name, err)
		}
	}
	return nil
}

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
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	_ "github.com/duckdb/duckdb-go/v2" // registers the "duckdb" driver

	"github.com/brandonbosch/chatstrata"
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
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "chatstrata-go.duckdb"), nil
}

// dataDir matches platformdirs.user_data_dir("chatstrata") used by Python.
func dataDir() (string, error) {
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

type migration struct {
	version int
	name    string
}

func (s *Store) SchemaVersion(ctx context.Context) int {
	var v string
	if err := s.conn.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&v); err != nil {
		return 0
	}
	n, _ := strconv.Atoi(v)
	return n
}

// migrate applies the migrations shared with the Python implementation and
// records the version the same way, so either implementation can open an
// archive the other created.
func (s *Store) migrate(ctx context.Context) error {
	const dir = "chatstrata/core/migrations"
	entries, err := fs.ReadDir(chatstrata.Migrations, dir)
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	var migrations []migration
	for _, e := range entries {
		prefix, _, ok := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(prefix)
		if !ok || err != nil {
			return fmt.Errorf("migration %s: name must start with a number", e.Name())
		}
		migrations = append(migrations, migration{version: v, name: e.Name()})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })

	current := s.SchemaVersion(ctx)
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		body, err := fs.ReadFile(chatstrata.Migrations, dir+"/"+m.name)
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

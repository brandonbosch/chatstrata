package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Runs only in builds that embed the extension:
//
//	go run ./internal/tools/fetchfts && go test -tags fts_embed ./internal/store/
func TestEmbeddedFTSLoadsWithoutInstall(t *testing.T) {
	if !FTSEmbedded() {
		t.Skip("build doesn't embed the fts extension (-tags fts_embed)")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "data"))

	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "a.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// An empty extension directory, so only the embedded copy can load.
	if _, err := s.conn.ExecContext(ctx, "SET extension_directory = "+sqlString(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	path, err := ftsExtensionFile()
	if err != nil || path == "" {
		t.Fatalf("extension file: %q, %v", path, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.ExecContext(ctx, "LOAD "+sqlString(path)); err != nil {
		t.Fatalf("load embedded extension: %v", err)
	}
	if err := s.RebuildFTS(ctx); err != nil {
		t.Fatalf("build index with embedded extension: %v", err)
	}
	if exists, _, _, err := s.FTSCoverage(ctx); err != nil || !exists {
		t.Fatalf("index after rebuild: exists=%v err=%v", exists, err)
	}
}

// Command fetchfts downloads DuckDB's signed full-text search extension for
// the DuckDB version and platform this module links, into
// internal/store/fts/, so a release build with -tags fts_embed carries it
// and search never needs a network request. Release builds run it on each
// native runner:
//
//	go run ./internal/tools/fetchfts
//	go build -tags fts_embed ./cmd/chatstrata
package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
)

const dest = "internal/store/fts/fts.duckdb_extension.gz"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fetchfts:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return err
	}
	defer db.Close()
	var version, platform string
	if err := db.QueryRowContext(ctx, "SELECT version()").Scan(&version); err != nil {
		return fmt.Errorf("read duckdb version: %w", err)
	}
	if err := db.QueryRowContext(ctx, "PRAGMA platform").Scan(&platform); err != nil {
		return fmt.Errorf("read duckdb platform: %w", err)
	}
	url := fmt.Sprintf("https://extensions.duckdb.org/%s/%s/fts.duckdb_extension.gz", version, platform)

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("download %s: %w", url, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	fmt.Printf("fts %s %s: %d bytes -> %s\n", version, platform, n, dest)
	return nil
}

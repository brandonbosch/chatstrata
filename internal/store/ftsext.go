package store

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// ftsExtensionFile writes the embedded fts extension to the data directory
// the first time a process needs it and returns its path, or "" when this
// build embeds none. The directory is named after the content hash, so a
// new release never loads an older build's file. DuckDB checks the
// extension's signature when it loads it.
var ftsExtensionFile = sync.OnceValues(func() (string, error) {
	if len(embeddedFTS) == 0 {
		return "", nil
	}
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(embeddedFTS)
	path := filepath.Join(dir, "extensions", hex.EncodeToString(sum[:8]), "fts.duckdb_extension")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if err := writeFTSExtension(path); err != nil {
		return "", fmt.Errorf("write fts extension: %w", err)
	}
	return path, nil
})

func writeFTSExtension(path string) error {
	zr, err := gzip.NewReader(bytes.NewReader(embeddedFTS))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".fts-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, zr); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Rename is atomic, so concurrent processes (daemon, serve) never load
	// a half-written file.
	return os.Rename(tmp.Name(), path)
}

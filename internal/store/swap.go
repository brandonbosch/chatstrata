package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// OpenForReplace opens the archive at path for writing without migrating it
// or loading extensions, only to hold DuckDB's write lock until ReplaceWith
// swaps a new file in. It fails while another process has the archive open
// for writing. A missing archive is created, so nothing else can create one
// in its place meanwhile.
//
// Inside one process DuckDB shares an open database between connections to
// the same path, so this lock only keeps other processes out.
func OpenForReplace(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create archive directory: %w", err)
	}
	return open(ctx, path, path)
}

// ReplaceWith renames the closed, fully written archive file at src over
// this store's file and closes the store, which must not be used again.
//
// The rename happens while this store still holds DuckDB's write lock, so no
// other process can open the old file meanwhile: it is checkpointed, renamed
// over, and only then closed. Closing a checkpointed connection that wrote
// nothing since leaves the new file's write-ahead log alone, so a writer that
// opens the new file straight away is safe. The old file's WAL is never
// removed by path: after the checkpoint there is none, and the path may
// already be the new file's.
//
// Windows can't rename over an open file, so there the store is closed just
// before the rename, leaving a brief window in which another writer could
// open the old file.
//
// If it fails before the rename, the archive is untouched and the store is
// closed.
func (s *Store) ReplaceWith(ctx context.Context, src string) (err error) {
	renamed := false
	defer func() {
		if !renamed {
			_ = s.Close()
		}
	}()
	dst, err := filepath.EvalSymlinks(s.Path)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(dst); err == nil {
		if err := os.Chmod(src, fi.Mode().Perm()); err != nil {
			return err
		}
	}
	if err := syncFile(src); err != nil {
		return err
	}
	if err := s.Checkpoint(ctx); err != nil {
		return fmt.Errorf("checkpoint %s: %w", dst, err)
	}
	if runtime.GOOS == "windows" {
		renamed = true
		if err := s.Close(); err != nil {
			return err
		}
		return os.Rename(src, dst)
	}
	if err := os.Rename(src, dst); err != nil {
		return err
	}
	renamed = true
	_ = syncFile(filepath.Dir(dst))
	return s.Close()
}

// syncFile flushes a file, or a directory's entries, to disk.
func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

package label

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RunWriter appends completed answers to a crash-safe staging JSONL file and
// promotes them into an immutable .jsonl.zst run on Finish.
type RunWriter struct {
	runsDir     string
	stagingPath string
	file        *os.File
	enc         *json.Encoder
	header      RunHeader
	records     []AnswerRecord
	sinceSync   int
	closed      bool
}

// OpenRunWriter creates a staging file under <dir>/runs/ for a live run.
func OpenRunWriter(dir string, header RunHeader) (*RunWriter, error) {
	if header.RunID == "" {
		header.RunID = NewRunID()
	}
	if header.StartedAt.IsZero() {
		header.StartedAt = time.Now().UTC()
	}
	header.Type = "run"
	runsDir := filepath.Join(dir, "runs")
	if err := os.MkdirAll(runsDir, 0o700); err != nil {
		return nil, fmt.Errorf("create label log: %w", err)
	}
	staging := filepath.Join(runsDir, ".staging-"+header.RunID+".jsonl")
	f, err := os.OpenFile(staging, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create label staging file: %w", err)
	}
	enc := json.NewEncoder(f)
	if err := enc.Encode(header); err != nil {
		_ = f.Close()
		_ = os.Remove(staging)
		return nil, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(staging)
		return nil, err
	}
	return &RunWriter{
		runsDir:     runsDir,
		stagingPath: staging,
		file:        f,
		enc:         enc,
		header:      header,
	}, nil
}

// Append writes one answer record to the staging log.
func (w *RunWriter) Append(rec AnswerRecord) error {
	if w.closed {
		return errors.New("label run writer is closed")
	}
	rec.Type = "answer"
	rec.RunID = w.header.RunID
	if rec.TargetKind == "" {
		rec.TargetKind = w.header.TargetKind
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	if err := w.enc.Encode(rec); err != nil {
		return err
	}
	w.records = append(w.records, rec)
	w.sinceSync++
	if w.sinceSync >= 20 {
		if err := w.file.Sync(); err != nil {
			return err
		}
		w.sinceSync = 0
	}
	return nil
}

// Finish promotes all appended answers into an immutable .jsonl.zst file and
// removes the staging file. If no answers were appended, the staging file is
// removed and an empty path with nil error is returned.
func (w *RunWriter) Finish(header RunHeader) (string, error) {
	if w.closed {
		return "", nil
	}
	w.closed = true
	if w.file != nil {
		_ = w.file.Sync()
		_ = w.file.Close()
	}
	if len(w.records) == 0 {
		_ = os.Remove(w.stagingPath)
		return "", nil
	}
	header.RunID = w.header.RunID
	if header.StartedAt.IsZero() {
		header.StartedAt = w.header.StartedAt
	}
	if err := normalizeRun(&header, w.records); err != nil {
		return "", err
	}
	path, err := writeCompressedRun(w.runsDir, header, w.records)
	if err != nil {
		return "", err
	}
	_ = os.Remove(w.stagingPath)
	return path, nil
}

// Package redactions owns the append-only archive redaction overlay and the
// hard-purge rewrite of the observation log (ADR 0008).
//
// A run file stores payload-free records: target identity, a hash of the text
// the spans were found in, and byte spans. It never stores matched text or a
// redacted copy of the transcript.
package redactions

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/brandonbosch/chatstrata/v2/internal/redact"
)

// Dir is the sidecar next to a DuckDB archive: chatstrata-go.duckdb ->
// chatstrata-go.redactions.
func Dir(dbPath string) string {
	return strings.TrimSuffix(dbPath, filepath.Ext(dbPath)) + ".redactions"
}

// TargetKind names one redactable text column in the projection.
type TargetKind string

const (
	TargetBlockText         TargetKind = "content_blocks.text"
	TargetBlockPayload      TargetKind = "content_blocks.payload"
	TargetRawEventPayload   TargetKind = "raw_events.payload"
	TargetConversationTitle TargetKind = "conversations.title"
)

func (k TargetKind) valid() bool {
	switch k {
	case TargetBlockText, TargetBlockPayload, TargetRawEventPayload, TargetConversationTitle:
		return true
	}
	return false
}

// Header is one run's metadata, written as the first JSON line.
type Header struct {
	Type             string    `json:"type"`
	RunID            string    `json:"run_id"`
	CreatedAt        time.Time `json:"created_at"`
	RedactionVersion string    `json:"redaction_version"`
	Records          int       `json:"records"`
}

// Record is one target's spans. InputSHA256 identifies the exact text the
// spans apply to; a later run may chain from the previous run's output.
type Record struct {
	Type        string        `json:"type"`
	RunID       string        `json:"run_id"`
	TargetKind  TargetKind    `json:"target_kind"`
	TargetID    string        `json:"target_id"`
	InputSHA256 string        `json:"input_sha256"`
	Spans       []redact.Span `json:"spans"`
}

// Run is a decoded run file.
type Run struct {
	Header  Header
	Records []Record
}

// ErrNoRecords means there was nothing to write.
var ErrNoRecords = errors.New("no redaction records")

// WriteRun writes one immutable run file. The header's RunID is generated when
// empty. The file is synced and renamed into place.
func WriteRun(dir string, header Header, records []Record) (string, error) {
	if len(records) == 0 {
		return "", ErrNoRecords
	}
	if header.RunID == "" {
		header.RunID = newRunID()
	}
	if header.CreatedAt.IsZero() {
		header.CreatedAt = time.Now().UTC()
	}
	if header.RedactionVersion == "" {
		return "", errors.New("redaction run needs a version")
	}
	header.Type = "run"
	header.Records = len(records)
	for i := range records {
		records[i].Type = "redaction"
		records[i].RunID = header.RunID
		if !records[i].TargetKind.valid() || records[i].TargetID == "" || !validSHA(records[i].InputSHA256) || len(records[i].Spans) == 0 {
			return "", fmt.Errorf("redaction record %d is invalid", i)
		}
	}

	runs := filepath.Join(dir, "runs")
	if err := os.MkdirAll(runs, 0o700); err != nil {
		return "", fmt.Errorf("create redaction log: %w", err)
	}
	tmp, err := os.CreateTemp(runs, ".tmp-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	zw, err := zstd.NewWriter(tmp)
	if err != nil {
		_ = tmp.Close()
		return "", err
	}
	enc := json.NewEncoder(zw)
	for _, value := range append([]any{header}, recordsAsAny(records)...) {
		if err := enc.Encode(value); err != nil {
			_ = zw.Close()
			_ = tmp.Close()
			return "", err
		}
	}
	if err := zw.Close(); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	path := filepath.Join(runs, header.RunID+".jsonl.zst")
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	if d, err := os.Open(runs); err == nil {
		defer func() { _ = d.Close() }()
		_ = d.Sync()
	}
	return path, nil
}

func recordsAsAny(records []Record) []any {
	out := make([]any, len(records))
	for i := range records {
		out[i] = records[i]
	}
	return out
}

// ReadRuns reads every run file in lexical order (fixed-width timestamps make
// that creation order). A missing directory is an empty log.
func ReadRuns(dir string) ([]Run, error) {
	runsDir := filepath.Join(dir, "runs")
	entries, err := os.ReadDir(runsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl.zst") {
			paths = append(paths, filepath.Join(runsDir, e.Name()))
		}
	}
	sort.Strings(paths)
	out := make([]Run, 0, len(paths))
	for _, path := range paths {
		run, err := readRun(path)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, nil
}

func readRun(path string) (Run, error) {
	f, err := os.Open(path)
	if err != nil {
		return Run{}, err
	}
	defer func() { _ = f.Close() }()
	zr, err := zstd.NewReader(f)
	if err != nil {
		return Run{}, err
	}
	defer zr.Close()
	dec := json.NewDecoder(bufio.NewReader(zr))
	var header Header
	if err := dec.Decode(&header); err != nil {
		return Run{}, fmt.Errorf("%s: read header: %w", path, err)
	}
	if header.Type != "run" || header.RunID == "" || header.RedactionVersion == "" {
		return Run{}, fmt.Errorf("%s: invalid run header", path)
	}
	var run Run
	run.Header = header
	for {
		var record Record
		err := dec.Decode(&record)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Run{}, fmt.Errorf("%s: read record: %w", path, err)
		}
		if record.Type != "redaction" || record.RunID != header.RunID || !record.TargetKind.valid() ||
			record.TargetID == "" || !validSHA(record.InputSHA256) || len(record.Spans) == 0 {
			return Run{}, fmt.Errorf("%s: invalid redaction record", path)
		}
		run.Records = append(run.Records, record)
	}
	if header.Records != len(run.Records) {
		return Run{}, fmt.Errorf("%s: header says %d records, read %d", path, header.Records, len(run.Records))
	}
	return run, nil
}

func newRunID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(b[:])
}

func validSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

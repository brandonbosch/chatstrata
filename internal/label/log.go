// Package label runs external classifier packs (TypeSafe Jev) over archive
// targets behind the local redaction gate, persists durable append-only runs
// under chatstrata-go.labels/, and projects them into DuckDB (ADR 0007).
package label

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
)

// Dir is the sidecar next to a DuckDB archive:
// chatstrata-go.duckdb -> chatstrata-go.labels.
func Dir(dbPath string) string {
	return strings.TrimSuffix(dbPath, filepath.Ext(dbPath)) + ".labels"
}

// TargetKind names the archive entity a pack classifies.
const (
	TargetConversation = "conversation"
	TargetMessage      = "message"
	TargetToolCall     = "tool_call"
)

func validTargetKind(kind string) bool {
	switch kind {
	case TargetConversation, TargetMessage, TargetToolCall:
		return true
	}
	return false
}

// Answer is one typed classifier response in TypeSafe System One wire format.
type Answer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Choice        *string            `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

// RunHeader is the first JSON line of an immutable label run file.
type RunHeader struct {
	Type             string    `json:"type"`
	RunID            string    `json:"run_id"`
	Pack             string    `json:"pack"`
	PackVersion      string    `json:"pack_version"`
	TargetKind       string    `json:"target_kind"`
	Backend          string    `json:"backend"`
	Model            string    `json:"model,omitempty"`
	RedactionVersion string    `json:"redaction_version,omitempty"`
	StartedAt        time.Time `json:"started_at"`
	FinishedAt       time.Time `json:"finished_at,omitempty"`
	Targets          int       `json:"targets"`
	Failures         int       `json:"failures"`
	InputTokens      int64     `json:"input_tokens"`
}

// AnswerRecord is one labeled target's answers inside a run file.
type AnswerRecord struct {
	Type              string            `json:"type"`
	RunID             string            `json:"run_id"`
	TargetKind        string            `json:"target_kind"`
	TargetID          string            `json:"target_id"`
	TargetFingerprint string            `json:"target_fingerprint,omitempty"`
	Model             string            `json:"model,omitempty"`
	InputTokens       int64             `json:"input_tokens,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	Answers           map[string]Answer `json:"answers"`
}

// Run is a decoded run file from chatstrata-go.labels/runs/.
type Run struct {
	Path    string
	Header  RunHeader
	Records []AnswerRecord
}

// ErrNoRecords means a run produced no answers to persist.
var ErrNoRecords = errors.New("no label records")

// NewRunID generates a lexically sortable run ID: fixed-width UTC timestamp
// plus 4 random bytes.
func NewRunID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(b[:])
}

// WriteRun writes one immutable compressed run file atomically (temp + sync +
// rename + parent directory sync).
func WriteRun(dir string, header RunHeader, records []AnswerRecord) (string, error) {
	if len(records) == 0 {
		return "", ErrNoRecords
	}
	if err := normalizeRun(&header, records); err != nil {
		return "", err
	}
	runsDir := filepath.Join(dir, "runs")
	if err := os.MkdirAll(runsDir, 0o700); err != nil {
		return "", fmt.Errorf("create label log: %w", err)
	}
	return writeCompressedRun(runsDir, header, records)
}

func normalizeRun(header *RunHeader, records []AnswerRecord) error {
	if header.RunID == "" {
		header.RunID = NewRunID()
	}
	if header.Pack == "" || header.PackVersion == "" {
		return errors.New("label run requires pack and pack_version")
	}
	if !validTargetKind(header.TargetKind) {
		return fmt.Errorf("label run has invalid target_kind %q", header.TargetKind)
	}
	if header.Backend == "" {
		header.Backend = "typesafe"
	}
	if header.StartedAt.IsZero() {
		header.StartedAt = time.Now().UTC()
	}
	if header.FinishedAt.IsZero() {
		header.FinishedAt = time.Now().UTC()
	}
	header.Type = "run"
	header.Targets = len(records)
	for i := range records {
		records[i].Type = "answer"
		records[i].RunID = header.RunID
		if records[i].TargetKind == "" {
			records[i].TargetKind = header.TargetKind
		}
		if records[i].CreatedAt.IsZero() {
			records[i].CreatedAt = header.FinishedAt
		}
		if !validTargetKind(records[i].TargetKind) || records[i].TargetID == "" || len(records[i].Answers) == 0 {
			return fmt.Errorf("label answer record %d is invalid", i)
		}
	}
	return nil
}

func writeCompressedRun(runsDir string, header RunHeader, records []AnswerRecord) (string, error) {
	tmp, err := os.CreateTemp(runsDir, ".tmp-*")
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
	if err := enc.Encode(header); err != nil {
		_ = zw.Close()
		_ = tmp.Close()
		return "", err
	}
	for i := range records {
		if err := enc.Encode(records[i]); err != nil {
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
	path := filepath.Join(runsDir, header.RunID+".jsonl.zst")
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	if d, err := os.Open(runsDir); err == nil {
		defer func() { _ = d.Close() }()
		_ = d.Sync()
	}
	return path, nil
}

// ReadRuns reads every finalized run file in lexical (creation) order. A
// missing directory is an empty log.
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
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl.zst") && !strings.HasPrefix(e.Name(), ".") {
			paths = append(paths, filepath.Join(runsDir, e.Name()))
		}
	}
	sort.Strings(paths)
	runs := make([]Run, 0, len(paths))
	for _, p := range paths {
		run, err := readRun(p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		runs = append(runs, run)
	}
	return runs, nil
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

	var run Run
	run.Path = path
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		raw := sc.Bytes()
		if len(raw) == 0 {
			continue
		}
		if line == 1 {
			if err := json.Unmarshal(raw, &run.Header); err != nil {
				return Run{}, fmt.Errorf("header: %w", err)
			}
			if run.Header.Type != "run" || run.Header.RunID == "" || run.Header.Pack == "" {
				return Run{}, errors.New("invalid label run header")
			}
			continue
		}
		var rec AnswerRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return Run{}, fmt.Errorf("line %d: %w", line, err)
		}
		if rec.Type != "answer" || rec.TargetID == "" || len(rec.Answers) == 0 {
			return Run{}, fmt.Errorf("line %d: invalid answer record", line)
		}
		run.Records = append(run.Records, rec)
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return Run{}, err
	}
	if line == 0 {
		return Run{}, errors.New("empty label run file")
	}
	return run, nil
}

// ClearFilter scopes which runs to delete.
type ClearFilter struct {
	Pack        string
	PackVersion string
	RunID       string
}

func (f ClearFilter) matches(h RunHeader) bool {
	if f.Pack != "" && h.Pack != f.Pack {
		return false
	}
	if f.PackVersion != "" && h.PackVersion != f.PackVersion {
		return false
	}
	if f.RunID != "" && h.RunID != f.RunID {
		return false
	}
	return true
}

// DeleteRuns removes matching run files from dir and returns their run IDs.
func DeleteRuns(dir string, filter ClearFilter) ([]string, error) {
	if filter.Pack == "" && filter.PackVersion == "" && filter.RunID == "" {
		return nil, errors.New("clear requires a pack, version, or run filter")
	}
	runs, err := ReadRuns(dir)
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, r := range runs {
		if !filter.matches(r.Header) {
			continue
		}
		if err := os.Remove(r.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, fmt.Errorf("remove %s: %w", r.Path, err)
		}
		removed = append(removed, r.Header.RunID)
	}
	return removed, nil
}

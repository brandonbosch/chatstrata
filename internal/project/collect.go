package project

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/brandonbosch/chatstrata/v2/internal/model"
	"github.com/brandonbosch/chatstrata/v2/internal/obslog"
	"github.com/brandonbosch/chatstrata/v2/internal/redact"
	"github.com/brandonbosch/chatstrata/v2/internal/store"
)

// CollectResult counts what the collector did.
type CollectResult struct {
	Logged    int // files with new observations
	Unchanged int
	Failed    int
}

// PendingCollection counts local conversations with bytes not yet in the log.
// It uses the collector's comparison rules without writing observations, so
// doctor can detect a stalled collector even when the database is readable.
func PendingCollection(ctx context.Context, s *store.Store, src model.Source, handles []model.Handle) (int, error) {
	states, err := s.CollectorStates(ctx, src.Name())
	if err != nil {
		return 0, err
	}
	deleted, err := s.TombstonedLocators(ctx, src.Name())
	if err != nil {
		return 0, err
	}
	redacted, err := s.RedactedLocators(ctx, src.Name())
	if err != nil {
		return 0, err
	}
	pending := 0
	for _, h := range handles {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		key := obslog.Key{Source: src.Name(), Locator: h.SourceNativeID}
		if deleted[key] {
			continue
		}
		if redacted[key] {
			// Comparing a purged conversation needs the policy engine; doctor
			// cannot do it from the read-only store alone.
			continue
		}
		prev, known := states[key]
		c := observe(src, h, key, prev, known, true, nil, false)
		if c.err != nil {
			return 0, fmt.Errorf("inspect local conversation: %w", c.err)
		}
		if c.obs != nil {
			pending++
		}
	}
	return pending, nil
}

type collected struct {
	key   obslog.Key
	obs   *obslog.Observation
	state store.CollectorState
	err   error
	path  string
	skip  bool
}

// Collect compares each discovered file with what this device last logged
// and appends what changed to the log: new bytes of an append-only file as an
// append, anything else as a snapshot. It then updates the collector state.
// It doesn't project; call CatchUp afterwards.
func (p *Projector) Collect(ctx context.Context, src model.Source, handles []model.Handle, incremental bool, errs io.Writer) (CollectResult, error) {
	var result CollectResult
	states, err := p.Store.CollectorStates(ctx, src.Name())
	if err != nil {
		return result, err
	}
	deleted, err := p.Store.TombstonedLocators(ctx, src.Name())
	if err != nil {
		return result, err
	}
	redacted, err := p.Store.RedactedLocators(ctx, src.Name())
	if err != nil {
		return result, err
	}

	work := make(chan model.Handle)
	out := make(chan collected)
	var wg sync.WaitGroup
	for range runtime.NumCPU() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for h := range work {
				key := obslog.Key{Source: src.Name(), Locator: h.SourceNativeID}
				prev, known := states[key]
				out <- observe(src, h, key, prev, known, incremental, p.Redactor, redacted[key] || p.RedactOnCollect)
			}
		}()
	}
	go func() {
		defer close(work)
		for _, h := range handles {
			if deleted[obslog.Key{Source: src.Name(), Locator: h.SourceNativeID}] {
				continue
			}
			select {
			case work <- h:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(out)
	}()

	var batch []*obslog.Observation
	var batchStates []collected
	var unchangedStates []collected
	for c := range out {
		switch {
		case c.err != nil:
			result.Failed++
			_, _ = fmt.Fprintf(errs, "  ! failed to read %s: %s\n", c.path, c.err)
		case c.obs != nil:
			batch = append(batch, c.obs)
			batchStates = append(batchStates, c)
		default:
			result.Unchanged++
			if !c.skip {
				unchangedStates = append(unchangedStates, c)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	// The log is written first. If we stop before the state below is saved,
	// the next run logs the same bytes again, which the projection ignores.
	if _, err := p.Log.Append(batch); err != nil {
		return result, err
	}
	for _, c := range append(batchStates, unchangedStates...) {
		if err := p.Store.SetCollectorState(ctx, c.key, c.state); err != nil {
			return result, err
		}
	}
	result.Logged = len(batch)
	return result, nil
}

func observe(src model.Source, h model.Handle, key obslog.Key, prev store.CollectorState, known, incremental bool, redactor *redact.Engine, redactContent bool) collected {
	c := collected{key: key, path: h.Path}
	fi, err := os.Stat(h.Path)
	if err != nil {
		c.err = err
		return c
	}
	mtime := float64(fi.ModTime().UnixNano()) / 1e9
	// The mtime shortcut only holds for one file per conversation. A shared
	// database's mtime can stay put while new rows sit in its write-ahead
	// log, and its handles carry their content already, so those are
	// compared by content.
	if incremental && known && h.Content == nil && prev.Mtime != nil && *prev.Mtime == mtime {
		c.skip = true
		return c
	}
	data := h.Content
	if data == nil {
		if data, err = os.ReadFile(h.Path); err != nil {
			c.err = err
			return c
		}
	}
	if src.AppendOnly() {
		data = completeLines(data)
	}
	path, project := h.Path, h.Project
	if redactContent {
		if redactor == nil {
			c.err = errors.New("observations for this conversation are already redacted; no redaction policy is available")
			return c
		}
		scope := redact.Scope{Project: h.Project, Path: h.Path}
		redacted, err := redactPreserving(redactor, scope, string(data))
		if err != nil {
			c.err = err
			return c
		}
		data = []byte(redacted)
		if path, err = redactPreserving(redactor, scope, path); err != nil {
			c.err = err
			return c
		}
		if project, err = redactPreserving(redactor, scope, project); err != nil {
			c.err = err
			return c
		}
	}
	sum := sha256.Sum256(data)
	c.state = store.CollectorState{Length: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Mtime: &mtime}
	if len(data) == 0 {
		c.skip = true
		return c
	}

	obs := &obslog.Observation{
		Source:      key.Source,
		Scope:       key.Scope,
		Locator:     key.Locator,
		Path:        path,
		ProjectHint: project,
		Redacted:    redactContent,
	}
	switch {
	case known && prev.Length == int64(len(data)) && prev.SHA256 == c.state.SHA256:
		return c // nothing new; only the mtime changes
	case src.AppendOnly() && known && prev.Length < int64(len(data)) && sha256Hex(data[:prev.Length]) == prev.SHA256:
		obs.Kind, obs.Offset, obs.Content = obslog.Append, prev.Length, data[prev.Length:]
	case src.AppendOnly() && !known:
		obs.Kind, obs.Content = obslog.Append, data
	default:
		obs.Kind, obs.Content = obslog.Snapshot, data
	}
	c.obs = obs
	return c
}

// completeLines drops a trailing line that is still being written. A final
// line without a newline counts as complete only if it is a whole JSON value.
func completeLines(data []byte) []byte {
	i := bytes.LastIndexByte(data, '\n')
	tail := data[i+1:]
	if len(bytes.TrimSpace(tail)) == 0 || json.Valid(tail) {
		return data
	}
	return data[:i+1]
}

func redactPreserving(engine *redact.Engine, scope redact.Scope, text string) (string, error) {
	if text == "" {
		return text, nil
	}
	spans, err := redactionSpans(engine, scope, text)
	if err != nil || len(spans) == 0 {
		return text, err
	}
	return redact.ApplyPreserving(text, spans)
}

// redactionSpans finds the policy's spans in one source payload. JSON
// documents and JSONL are scanned by decoded string value, so a marker can
// never split an escape sequence: the length-preserving rewrite would
// otherwise leave an observation the source parser cannot read.
func redactionSpans(engine *redact.Engine, scope redact.Scope, text string) ([]redact.Span, error) {
	if json.Valid([]byte(text)) {
		return redact.JSONSpans(engine, scope, text)
	}
	if spans, ok, err := jsonLineSpans(engine, scope, text); ok || err != nil {
		return spans, err
	}
	result, err := engine.RedactScope(scope, text)
	if err != nil {
		return nil, err
	}
	spans := make([]redact.Span, len(result.Findings))
	for i, finding := range result.Findings {
		spans[i] = finding.Span()
	}
	return spans, nil
}

// jsonLineSpans handles JSONL: every non-empty line must be a JSON value. A
// file with any other shape is not treated as JSON.
func jsonLineSpans(engine *redact.Engine, scope redact.Scope, text string) ([]redact.Span, bool, error) {
	var spans []redact.Span
	start := 0
	for {
		lineEnd := len(text)
		next := len(text)
		if i := strings.IndexByte(text[start:], '\n'); i >= 0 {
			lineEnd = start + i
			next = lineEnd + 1
		}
		line := text[start:lineEnd]
		if strings.TrimSpace(line) != "" {
			if !json.Valid([]byte(line)) {
				return nil, false, nil
			}
			lineSpans, err := redact.JSONSpans(engine, scope, line)
			if err != nil {
				return nil, false, err
			}
			for _, s := range lineSpans {
				s.Start += start
				s.End += start
				spans = append(spans, s)
			}
		}
		if next >= len(text) {
			break
		}
		start = next
	}
	return spans, true, nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

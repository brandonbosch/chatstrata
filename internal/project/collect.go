package project

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"

	"github.com/brandonbosch/chatstrata/internal/model"
	"github.com/brandonbosch/chatstrata/internal/obslog"
	"github.com/brandonbosch/chatstrata/internal/store"
)

// CollectResult counts what the collector did.
type CollectResult struct {
	Logged    int // files with new observations
	Unchanged int
	Failed    int
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
				out <- observe(src, h, key, prev, known, incremental)
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
			fmt.Fprintf(errs, "  ! failed to read %s: %s\n", c.path, c.err)
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

func observe(src model.Source, h model.Handle, key obslog.Key, prev store.CollectorState, known, incremental bool) collected {
	c := collected{key: key, path: h.Path}
	fi, err := os.Stat(h.Path)
	if err != nil {
		c.err = err
		return c
	}
	mtime := float64(fi.ModTime().UnixNano()) / 1e9
	if incremental && known && prev.Mtime != nil && *prev.Mtime == mtime {
		c.skip = true
		return c
	}
	data, err := os.ReadFile(h.Path)
	if err != nil {
		c.err = err
		return c
	}
	if src.AppendOnly() {
		data = completeLines(data)
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
		Path:        h.Path,
		ProjectHint: h.Project,
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

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

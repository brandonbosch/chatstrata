package project

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brandonbosch/chatstrata/v2/internal/model"
	"github.com/brandonbosch/chatstrata/v2/internal/obslog"
	"github.com/brandonbosch/chatstrata/v2/internal/sources/claudecode"
	"github.com/brandonbosch/chatstrata/v2/internal/store"
)

// BenchmarkCollection uses 100 synthetic sessions with 20 user/assistant
// exchanges each. Corpus generation is excluded; full ingestion includes
// opening/migrating DuckDB, writing the log, and projection. The no-op case
// measures an already-open collector, as used during a daemon cycle.
func BenchmarkCollection(b *testing.B) {
	root := b.TempDir()
	for session := range 100 {
		dir := filepath.Join(root, fmt.Sprintf("-Users-example-project%d", session%12))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatal(err)
		}
		var text strings.Builder
		for turn := range 40 {
			role := "user"
			if turn%2 == 1 {
				role = "assistant"
			}
			fmt.Fprintf(&text, `{"type":%q,"uuid":"m-%d-%d","timestamp":"2026-01-01T00:00:%02dZ","message":{"role":%q,"content":"Synthetic migration benchmark message %d"}}`+"\n", role, session, turn, turn, role, turn)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("session-%d.jsonl", session)), []byte(text.String()), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	src := claudecode.Source{}
	handles, err := src.Discover(root)
	if err != nil {
		b.Fatal(err)
	}
	if len(handles) != 100 {
		b.Fatalf("synthetic corpus discovered %d sessions, want 100", len(handles))
	}
	ctx := context.Background()
	open := func() *Projector {
		b.Helper()
		dir := b.TempDir()
		s, err := store.Open(ctx, filepath.Join(dir, "archive.duckdb"))
		if err != nil {
			b.Fatal(err)
		}
		l, err := obslog.Open(filepath.Join(dir, "archive.log"))
		if err != nil {
			s.Close()
			b.Fatal(err)
		}
		return &Projector{Store: s, Log: l, Sources: map[string]model.Source{src.Name(): src}}
	}
	collect := func(p *Projector) {
		b.Helper()
		if _, err := p.Collect(ctx, src, handles, true, io.Discard); err != nil {
			b.Fatal(err)
		}
		if _, err := p.CatchUp(ctx, false, io.Discard); err != nil {
			b.Fatal(err)
		}
	}
	b.Run("full_100x20", func(b *testing.B) {
		for b.Loop() {
			p := open()
			collect(p)
			p.Store.Close()
		}
	})
	b.Run("unchanged_100x20", func(b *testing.B) {
		p := open()
		defer p.Store.Close()
		collect(p)
		b.ResetTimer()
		for b.Loop() {
			collect(p)
		}
	})
}

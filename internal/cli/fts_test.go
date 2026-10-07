package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brandonbosch/chatstrata/internal/store"
)

func TestFTSRebuildPolicy(t *testing.T) {
	for _, c := range []struct {
		exists           bool
		indexed, pending int64
		want             bool
	}{
		{false, 0, 0, true},      // no index yet
		{true, 1000, 1, false},   // a little new content waits
		{true, 1000, 2000, true}, // the floor
		{true, 1_000_000, 40_000, false},
		{true, 1_000_000, 50_000, true}, // 5% of a large archive
	} {
		if got := store.FTSNeedsRebuild(c.exists, c.indexed, c.pending); got != c.want {
			t.Errorf("FTSNeedsRebuild(%v, %d, %d) = %v", c.exists, c.indexed, c.pending, got)
		}
	}
}

// A small ingest doesn't rebuild the index, yet search still finds what it
// added, next to the ranked results.
func TestSearchCoversContentNotYetIndexed(t *testing.T) {
	root, db := fixture(t)
	run(t, "ingest", "claude_code", "--path", root, "--db", db)
	if !ftsAvailable(t, db) {
		t.Skip("DuckDB FTS extension not installed (run `chatstrata reindex --install-fts` once)")
	}
	coverage := func() (int64, int64) {
		t.Helper()
		s, err := store.OpenReadOnly(context.Background(), db)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		s.LoadFTS(context.Background())
		_, indexed, pending, err := s.FTSCoverage(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return indexed, pending
	}
	indexed, pending := coverage()
	if indexed == 0 || pending != 0 {
		t.Fatalf("after the first ingest: indexed %d, pending %d", indexed, pending)
	}

	// Many short sessions that all mention "refactor", and one probe.
	dir := filepath.Join(root, "-Users-example-more")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 30 {
		text := fmt.Sprintf("refactor step %d", i)
		if i == 29 {
			text = "refactor FRESHPROBE"
		}
		line := fmt.Sprintf(`{"type":"user","uuid":"u%d","sessionId":"s%d","timestamp":"2026-05-01T10:00:%02dZ","message":{"role":"user","content":%q}}`+"\n", i, i, i, text)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("s%d.jsonl", i)), []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run(t, "ingest", "claude_code", "--path", root, "--db", db)
	if _, pending := coverage(); pending != 30 {
		t.Fatalf("pending after a small ingest = %d, want 30 (no rebuild)", pending)
	}

	if out := run(t, "search", "FRESHPROBE", "--db", db); !strings.Contains(out, "refactor FRESHPROBE") {
		t.Errorf("new content not found:\n%s", out)
	}
	// Ranked results and new content share the slots: with a limit of 8,
	// at least 2 go to the 30 new matches, the rest to the index.
	out := run(t, "search", "refactor", "--limit", "8", "--db", db)
	if n := strings.Count(out, "refactor step"); n < 2 || !strings.Contains(out, "Refactor the user auth module") {
		t.Errorf("limit 8: %d new matches, ranked results present: %v\n%s", n, strings.Contains(out, "Refactor the user auth module"), out)
	}

	run(t, "reindex", "--db", db)
	if _, pending := coverage(); pending != 0 {
		t.Errorf("pending after reindex = %d", pending)
	}
}

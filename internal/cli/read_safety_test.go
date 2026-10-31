package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/brandonbosch/chatstrata/v2/internal/store"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestQueriesCannotReadOutsideArchive(t *testing.T) {
	root, db := fixture(t)
	run(t, "ingest", "claude_code", "--path", root, "--db", db)
	cs := connectMCP(t, db)
	sentinel := filepath.Join(t.TempDir(), "private.txt")
	const secret = "SYNTHETIC_FILE_MUST_NOT_REACH_AGENT"
	if err := os.WriteFile(sentinel, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		fmt.Fprintln(w, "value\nsynthetic")
	}))
	defer srv.Close()
	for _, sql := range []string{
		"SELECT content FROM read_text('" + sentinel + "')",
		"SELECT * FROM read_csv('" + sentinel + "')",
		"SELECT * FROM glob('" + filepath.Dir(sentinel) + "/*')",
		"SELECT * FROM read_csv('" + srv.URL + "/data.csv')",
		"SELECT * FROM query('SELECT content FROM read_text(''" + sentinel + "'')')",
	} {
		out := callText(t, cs, "query", map[string]any{"sql": sql})
		var result map[string]any
		if err := json.Unmarshal([]byte(out), &result); err != nil || result["error"] == nil || strings.Contains(out, secret) {
			t.Errorf("MCP permitted external read %q: %s", sql, out)
		}
		if out, err := runErr("query", sql, "--db", db); err == nil || strings.Contains(out, secret) {
			t.Errorf("CLI permitted external read %q: %s", sql, out)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("queries made %d network requests", requests.Load())
	}
	// Enforcement must survive bypassing the SQL keyword validator entirely.
	s, err := store.OpenReadOnly(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Conn().ExecContext(context.Background(), "SET enable_external_access = true"); err == nil {
		t.Fatal("read connection allowed re-enabling external access")
	}
	out := callText(t, cs, "query", map[string]any{"sql": "PRAGMA table_info('conversations')"})
	if !strings.Contains(out, `"row_count"`) || strings.Contains(out, `"error"`) {
		t.Errorf("safe metadata query failed: %s", out)
	}
}

func TestMCPResponseBudget(t *testing.T) {
	root, db := fixture(t)
	run(t, "ingest", "claude_code", "--path", root, "--db", db)
	cs := connectMCP(t, db)
	for _, tc := range []struct {
		name, sql string
		wantRows  int
	}{
		{"huge single row", "SELECT repeat('x', 600000) AS value", 0},
		{"escaped single row", "SELECT repeat(chr(34), 300000) AS value", 0},
		{"keep small prefix", "SELECT CASE WHEN i = 0 THEN 'small' ELSE repeat('x', 600000) END AS value FROM range(2) t(i) ORDER BY i", 1},
		{"row limit", "SELECT i FROM range(501) t(i)", 500},
		{"cumulative bytes", "SELECT repeat('x', 2000) AS value FROM range(500)", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := callText(t, cs, "query", map[string]any{"sql": tc.sql})
			var result queryResult
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatal(err)
			}
			if len(out) > maxResultBytes || !result.Truncated || result.Note == "" {
				t.Fatalf("budget not enforced: %d bytes, %d rows, truncated=%v", len(out), result.RowCount, result.Truncated)
			}
			if tc.wantRows >= 0 && result.RowCount != tc.wantRows {
				t.Errorf("rows=%d, want %d", result.RowCount, tc.wantRows)
			}
			if tc.wantRows < 0 && (result.RowCount == 0 || result.RowCount >= 500) {
				t.Errorf("did not retain bounded row prefix: %d", result.RowCount)
			}
			cli := run(t, "query", tc.sql, "--db", db, "--json")
			if len(cli) > maxResultBytes || !json.Valid([]byte(cli)) {
				t.Errorf("CLI response not bounded valid JSON: %d bytes", len(cli))
			}
		})
	}
}

func TestConcurrentMCPQueries(t *testing.T) {
	root, db := fixture(t)
	run(t, "ingest", "claude_code", "--path", root, "--db", db)
	cs := connectMCP(t, db)
	var wg sync.WaitGroup
	errors := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "query", Arguments: map[string]any{"sql": "SELECT count(*) AS n FROM conversations"}})
			if err != nil {
				errors <- err
				return
			}
			if len(res.Content) != 1 {
				errors <- fmt.Errorf("unexpected query content")
				return
			}
			text, ok := res.Content[0].(*mcp.TextContent)
			if !ok || !strings.Contains(text.Text, `"n": 2`) || strings.Contains(text.Text, `"error"`) {
				errors <- fmt.Errorf("concurrent query failed: %v", res.Content)
			}
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

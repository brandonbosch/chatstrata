package golden

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/brandonbosch/chatstrata/internal/cli"
)

// TestLegacyImport checks that import-legacy of a Python-era archive gives
// the same archive as ingesting the sources, for every adapter. The Python
// archive is built from the golden output: the conversations and raw_events
// tables are all import-legacy reads.
func TestLegacyImport(t *testing.T) {
	for _, name := range []string{"claude_code", "codex_cli", "omp", "claude_export", "opencode", "hermes_agent"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(specDir, "expected", name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var expected struct {
				Conversations []map[string]any `json:"conversations"`
			}
			if err := json.Unmarshal(raw, &expected); err != nil {
				t.Fatal(err)
			}

			dir := t.TempDir()
			legacy := filepath.Join(dir, "chatstrata.duckdb")
			writeLegacyArchive(t, legacy, expected.Conversations)
			db := filepath.Join(dir, "chatstrata-go.duckdb")
			args := []string{"import-legacy", legacy, "--db", db}
			if name == "claude_code" {
				// Without a path it reads the Python app's archive.
				t.Setenv("CHATSTRATA_DB", legacy)
				args = []string{"import-legacy", "--db", db}
			}
			var stdout, stderr bytes.Buffer
			if code := cli.Run(context.Background(), args, &stdout, &stderr); code != 0 || stderr.Len() > 0 {
				t.Fatalf("import-legacy: exit %d\n%s%s", code, stdout.String(), stderr.String())
			}

			actual, err := Dump(context.Background(), db, nil)
			if err != nil {
				t.Fatal(err)
			}
			want := normalize(t, raw)
			if name == "opencode" {
				// The Python archive keeps OpenCode's session row only in the
				// conversations table, so a legacy import has no title.
				for _, c := range want.(map[string]any)["conversations"].([]any) {
					c.(map[string]any)["title"] = nil
				}
			}
			compare(t, want, normalize(t, mustMarshal(t, actual)))
		})
	}
}

// writeLegacyArchive creates the parts of a Python-era archive that
// import-legacy reads.
func writeLegacyArchive(t *testing.T, path string, convs []map[string]any) {
	t.Helper()
	db, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`CREATE TABLE conversations (source_id VARCHAR, source_native_id VARCHAR, raw_path VARCHAR, project VARCHAR)`,
		`CREATE TABLE raw_events (source_id VARCHAR, source_native_conversation_id VARCHAR, line_number INTEGER, payload JSON)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range convs {
		source, native := c["source_id"].(string), c["source_native_id"].(string)
		if _, err := db.Exec(`INSERT INTO conversations VALUES (?, ?, ?, ?)`,
			source, native, c["raw_path"], c["project"]); err != nil {
			t.Fatal(err)
		}
		for _, ev := range c["raw_events"].([]any) {
			e := ev.(map[string]any)
			payload, err := json.Marshal(e["payload"])
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO raw_events VALUES (?, ?, ?, ?)`,
				source, native, int(e["line_number"].(float64)), string(payload)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

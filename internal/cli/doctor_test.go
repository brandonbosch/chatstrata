package cli

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// doctor warns about sources whose storage it finds but can't make sense
// of, which is how a tool changing its format shows up.
func TestDoctorChecksSources(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HERMES_HOME", "")
	db := filepath.Join(t.TempDir(), "archive.duckdb")
	projects := filepath.Join(home, ".claude", "projects")

	write := func(rel string, content []byte) {
		t.Helper()
		path := filepath.Join(projects, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(sample, transcript(t, sample, 0))

	// Transcripts on this machine that were never ingested.
	run(t, "ingest", "codex_cli", "--path", "../../spec/golden/inputs/codex_cli/sessions", "--db", db) // creates the archive
	if out := run(t, "doctor", "--db", db); !strings.Contains(out, "source 'claude_code': 1 conversations on this machine, none in the archive") {
		t.Errorf("not-ingested source not reported:\n%s", out)
	}
	run(t, "ingest", "claude_code", "--db", db)
	if out := run(t, "doctor", "--db", db); !strings.Contains(out, "All checks passed") {
		t.Errorf("healthy archive:\n%s", out)
	}

	// Sessions in a shape the adapter doesn't understand produce no messages.
	for i := range 4 {
		write(fmt.Sprintf("-Users-example-new/new-format-%d.jsonl", i),
			[]byte(`{"kind":"turn","speaker":"human","body":"hello"}`+"\n"))
	}
	run(t, "ingest", "claude_code", "--db", db)
	if out := run(t, "doctor", "--db", db); !strings.Contains(out, "4 of the 5 most recently collected conversations produced no messages") {
		t.Errorf("format change not reported:\n%s", out)
	}

	// An OpenCode database in a layout this version doesn't know.
	ocPath := filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	if err := os.MkdirAll(filepath.Dir(ocPath), 0o755); err != nil {
		t.Fatal(err)
	}
	oc, err := sql.Open("sqlite3", ocPath)
	if err != nil {
		t.Fatal(err)
	}
	defer oc.Close()
	if _, err := oc.Exec(`CREATE TABLE session_v3 (id TEXT); CREATE TABLE session_entry (id TEXT)`); err != nil {
		t.Fatal(err)
	}
	out := run(t, "doctor", "--db", db)
	for _, want := range []string{
		"has none of the session tables this version reads",
		"has session tables from a newer layout (session_v3)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out)
		}
	}
}

func TestOpenCodeCheckNotesUnknownKinds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.db")
	script, err := os.ReadFile("../../spec/golden/inputs/opencode_v2/opencode.sql")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(string(script)); err != nil {
		t.Fatal(err)
	}
	src := sources["opencode"].(interface {
		Check(string) ([]string, []string)
	})
	if problems, notes := src.Check(path); len(problems)+len(notes) != 0 {
		t.Errorf("fixture: problems %v, notes %v", problems, notes)
	}
	if _, err := db.Exec(`INSERT INTO session_message VALUES ('m9','ses_test001','handoff',99,0,0,'{}');
		INSERT INTO part VALUES ('p9','msg_user001','ses_test001',0,0,'{"type":"subtask"}')`); err != nil {
		t.Fatal(err)
	}
	_, notes := src.Check(path)
	if got := strings.Join(notes, "\n"); !strings.Contains(got, "1.x part types it doesn't know: subtask") || !strings.Contains(got, "2.x message types it doesn't know: handoff") {
		t.Errorf("notes = %v", notes)
	}
}

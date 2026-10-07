package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAnalyze(t *testing.T) {
	root, db := fixture(t)
	run(t, "ingest", "claude_code", "--path", root, "--db", db)

	out := run(t, "analyze", "tools", "--db", db)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if !strings.HasPrefix(lines[0], "tool_name") || !strings.HasPrefix(lines[1], "---") || len(lines) < 3 {
		t.Errorf("tools table:\n%s", out)
	}

	var rows []map[string]any
	if err := json.Unmarshal([]byte(run(t, "analyze", "activity", "--by", "day", "--json", "--db", db)), &rows); err != nil || len(rows) == 0 {
		t.Fatalf("activity --json: %v %v", err, rows)
	}
	if rows[0]["source"] != "claude_code" || rows[0]["messages"] == nil {
		t.Errorf("activity row = %v", rows[0])
	}

	if out := run(t, "analyze", "tools", "--source", "nothing", "--db", db); out != "No data.\n" {
		t.Errorf("empty result = %q", out)
	}
	if _, err := runErr("analyze", "conversations", "--longest", "1", "--shortest", "1", "--db", db); err == nil {
		t.Error("--longest with --shortest accepted")
	}
	if _, err := runErr("analyze", "activity", "--by", "year", "--db", db); err == nil {
		t.Error("--by year accepted")
	}
}

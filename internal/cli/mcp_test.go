package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connect runs the MCP server for an archive in-process and returns a
// connected client session.
func connectMCP(t *testing.T, db string) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	server := newMCPServer(archiveReader{dbPath: db, wait: time.Second})
	ss, err := server.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func callText(t *testing.T, cs *mcp.ClientSession, tool string, args any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("%s returned %d content items", tool, len(res.Content))
	}
	return res.Content[0].(*mcp.TextContent).Text
}

func TestMCPServer(t *testing.T) {
	root, db := fixture(t)
	run(t, "ingest", "claude_code", "--path", root, "--db", db)
	cs := connectMCP(t, db)

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "get_schema,query" {
		t.Errorf("tools = %v", names)
	}

	var res struct {
		Columns  []string         `json:"columns"`
		Rows     []map[string]any `json:"rows"`
		RowCount int              `json:"row_count"`
		Error    string           `json:"error"`
	}
	out := callText(t, cs, "query", map[string]any{"sql": "SELECT source_id, count(*) AS n FROM conversations GROUP BY 1"})
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.Error != "" || res.RowCount != 1 || res.Rows[0]["source_id"] != "claude_code" || res.Rows[0]["n"] != float64(2) {
		t.Errorf("query result:\n%s", out)
	}
	// Columns keep the query's order, as Python's dict(zip(...)) does.
	if !strings.Contains(out, `"source_id": "claude_code",`) || strings.Index(out, `"source_id"`) > strings.Index(out, `"n"`) {
		t.Errorf("row keys out of column order:\n%s", out)
	}

	for sql, want := range map[string]string{
		"DELETE FROM conversations":   "DELETE queries are not allowed",
		"SELECT 1; SELECT 2":          "Multi-statement",
		"SELECT * FROM no_such_table": "DuckDB error:",
	} {
		res.Error = ""
		if err := json.Unmarshal([]byte(callText(t, cs, "query", map[string]any{"sql": sql})), &res); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(res.Error, want) {
			t.Errorf("%q: error %q, want %q", sql, res.Error, want)
		}
	}

	schema := callText(t, cs, "get_schema", map[string]any{})
	for _, want := range []string{"TABLE conversations:", "VIEW tool_calls:", "conversations: 2 rows", "## Example Queries"} {
		if !strings.Contains(schema, want) {
			t.Errorf("schema lacks %q", want)
		}
	}
	resource, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "chatstrata://schema"})
	if err != nil || len(resource.Contents) != 1 || resource.Contents[0].Text != schema {
		t.Errorf("schema resource differs from get_schema (err %v)", err)
	}
}

func TestMCPServerWithoutArchive(t *testing.T) {
	cs := connectMCP(t, filepath.Join(t.TempDir(), "missing.duckdb"))
	out := callText(t, cs, "query", map[string]any{"sql": "SELECT 1"})
	if !strings.Contains(out, "Run `chatstrata ingest <source>` first") {
		t.Errorf("missing archive: %s", out)
	}
}

func TestMCPConfig(t *testing.T) {
	out := run(t, "mcp", "config", "claude-code", "--command", "/opt/chatstrata", "--db", "/data/my archive.duckdb", "--scope", "project")
	want := "claude mcp add --transport stdio --scope project --env 'CHATSTRATA_GO_DB=/data/my archive.duckdb' chatstrata -- /opt/chatstrata serve\n"
	if out != want {
		t.Errorf("claude-code:\n%s\nwant:\n%s", out, want)
	}
	var desktop struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(run(t, "mcp", "config", "claude-desktop", "--command", "/opt/chatstrata")), &desktop); err != nil {
		t.Fatal(err)
	}
	if s := desktop.MCPServers["chatstrata"]; s.Command != "/opt/chatstrata" || strings.Join(s.Args, " ") != "serve" || s.Env != nil {
		t.Errorf("claude-desktop = %+v", s)
	}
	if out := run(t, "mcp", "config", "codex", "--command", "/opt/chatstrata"); !strings.Contains(out, "[mcp_servers.chatstrata]\ncommand = \"/opt/chatstrata\"\nargs = [\"serve\"]\n") {
		t.Errorf("codex:\n%s", out)
	}
	if _, err := runErr("mcp", "config", "vscode"); err == nil {
		t.Error("unknown client accepted")
	}
}

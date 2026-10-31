// Command smokemcp checks the actual release binary's stdio MCP transport.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "smoke MCP:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 4 {
		return fmt.Errorf("usage: smokemcp BINARY DATABASE ISOLATED_HOME")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.Command(os.Args[1], "serve", "--db", os.Args[2])
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Args[3], "XDG_DATA_HOME=" + filepath.Join(os.Args[3], "data")}
	cmd.Stderr = os.Stderr
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "release-smoke", Version: "1"}, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	defer cs.Close()
	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		return err
	}
	names := map[string]bool{}
	for _, tool := range list.Tools {
		names[tool.Name] = true
	}
	if !names["get_schema"] || !names["query"] {
		return fmt.Errorf("missing archive tools")
	}
	call := func(name string, args map[string]any) (string, error) {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			return "", err
		}
		if len(res.Content) != 1 {
			return "", fmt.Errorf("%s returned unexpected content", name)
		}
		text, ok := res.Content[0].(*mcp.TextContent)
		if !ok {
			return "", fmt.Errorf("%s did not return text", name)
		}
		return text.Text, nil
	}
	schema, err := call("get_schema", map[string]any{})
	if err != nil || !strings.Contains(schema, "TABLE conversations:") {
		return fmt.Errorf("schema discovery failed: %v", err)
	}
	resource, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "chatstrata://schema"})
	if err != nil || len(resource.Contents) != 1 || resource.Contents[0].Text != schema {
		return fmt.Errorf("schema resource differs: %v", err)
	}
	out, err := call("query", map[string]any{"sql": "SELECT count(*) AS n FROM conversations"})
	var result struct {
		Rows []struct {
			N int `json:"n"`
		} `json:"rows"`
	}
	if err != nil || json.Unmarshal([]byte(out), &result) != nil || len(result.Rows) != 1 || result.Rows[0].N != 2 {
		return fmt.Errorf("archive query failed: %v, %s", err, out)
	}
	sentinel := filepath.Join(os.Args[3], "synthetic-private.txt")
	if err := os.WriteFile(sentinel, []byte("SYNTHETIC_PRIVATE_FILE"), 0o600); err != nil {
		return err
	}
	for _, sql := range []string{"DELETE FROM conversations", "SELECT content FROM read_text('" + strings.ReplaceAll(sentinel, "'", "''") + "')"} {
		out, err := call("query", map[string]any{"sql": sql})
		var result map[string]any
		if err != nil || json.Unmarshal([]byte(out), &result) != nil || result["error"] == nil || strings.Contains(out, "SYNTHETIC_PRIVATE_FILE") {
			return fmt.Errorf("query boundary failed: %v", err)
		}
	}
	fmt.Println("smoke: stdio MCP schema, query, and access restrictions passed")
	return nil
}

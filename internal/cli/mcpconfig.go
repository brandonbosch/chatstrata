package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/brandonbosch/chatstrata/internal/store"
)

func runMCP(e *env, args []string) error {
	if len(args) == 0 || args[0] != "config" {
		return usageError{"usage: chatstrata mcp config CLIENT (claude-code, claude-desktop or codex)"}
	}
	return runMCPConfig(e, args[1:])
}

// runMCPConfig prints how to register `chatstrata serve` with an MCP client.
// Unlike the Python version there's nothing to pick between uvx and an
// installed script: the server is this binary, named by its absolute path so
// clients that don't search PATH (Claude Desktop) find it.
func runMCPConfig(e *env, args []string) error {
	fs := newFlagSet(e, "mcp config", "mcp config claude-code|claude-desktop|codex [--name NAME] [--db PATH] [--scope local|project|user] [--command PATH]")
	name := fs.String("name", "chatstrata", "Server name in the client's config. The Python app registers \"chatstrata\" too: remove that entry first, or pick another name to keep both.")
	db := fs.String("db", "", "Set CHATSTRATA_GO_DB for the MCP server.")
	scope := fs.String("scope", "user", "Claude Code MCP scope: local, project or user.")
	command := fs.String("command", "", "Command that runs chatstrata (default: this binary).")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return usageError{"expected one CLIENT: claude-code, claude-desktop or codex"}
	}
	if !serverName.MatchString(*name) {
		return usageError{"--name may only use letters, digits, '-' and '_'"}
	}
	switch *scope {
	case "local", "project", "user":
	default:
		return usageError{"--scope must be local, project or user"}
	}
	cmd := *command
	if cmd == "" {
		if cmd, err = os.Executable(); err != nil {
			return fmt.Errorf("locate the chatstrata binary (pass --command): %w", err)
		}
		if resolved, err := filepath.EvalSymlinks(cmd); err == nil {
			cmd = resolved
		}
	}
	var env map[string]string
	if *db != "" {
		path, err := store.ResolvePath(*db)
		if err != nil {
			return err
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		env = map[string]string{"CHATSTRATA_GO_DB": path}
	}

	switch positional[0] {
	case "claude-desktop":
		spec := map[string]any{"type": "stdio", "command": cmd, "args": []string{"serve"}}
		if env != nil {
			spec["env"] = env
		}
		b, err := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{*name: spec}}, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(e.stdout, string(b))
	case "claude-code":
		parts := []string{"claude", "mcp", "add", "--transport", "stdio", "--scope", *scope}
		if env != nil {
			parts = append(parts, "--env", "CHATSTRATA_GO_DB="+env["CHATSTRATA_GO_DB"])
		}
		parts = append(parts, *name, "--", cmd, "serve")
		fmt.Fprintln(e.stdout, shellJoin(parts))
	case "codex":
		fmt.Fprintln(e.stdout, "# Add to ~/.codex/config.toml")
		fmt.Fprintf(e.stdout, "[mcp_servers.%s]\n", *name)
		fmt.Fprintf(e.stdout, "command = %s\n", tomlString(cmd))
		fmt.Fprintln(e.stdout, `args = ["serve"]`)
		if env != nil {
			fmt.Fprintf(e.stdout, "env = { CHATSTRATA_GO_DB = %s }\n", tomlString(env["CHATSTRATA_GO_DB"]))
		}
	default:
		return usageError{fmt.Sprintf("unknown client %q: use claude-code, claude-desktop or codex", positional[0])}
	}
	return nil
}

// serverName is what all three clients accept as a server name (and what a
// bare TOML key allows).
var serverName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9@%+=:,./_-]+$`)

// shellJoin quotes arguments the way Python's shlex.join does.
func shellJoin(parts []string) string {
	out := make([]string, len(parts))
	for i, p := range parts {
		if p != "" && shellSafe.MatchString(p) {
			out[i] = p
		} else {
			out[i] = "'" + strings.ReplaceAll(p, "'", `'"'"'`) + "'"
		}
	}
	return strings.Join(out, " ")
}

// tomlString writes a TOML basic string; JSON string escaping is valid TOML.
func tomlString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

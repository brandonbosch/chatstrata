# ChatStrata MCP Server

A single-tool MCP server that lets any LLM query your conversation archive via SQL.

## Design

One tool, maximum flexibility — the LLM writes DuckDB SQL, the server executes it, returns JSON results.

| MCP primitive | Name | Purpose |
|---|---|---|
| **Tool** | `query` | Execute read-only SQL against the DuckDB archive |
| **Resource** | `chatstrata://schema` | Live schema introspection (tables, columns, row counts, example queries) |

Full-text search (BM25) is a native DuckDB SQL function, so it works through the same `query` tool — no separate search tool needed.

## Install

```bash
uv tool install "chatstrata[mcp]"
# or: pipx install "chatstrata[mcp]"
```

For local development from a clone:

```bash
uv pip install -e ".[mcp]"
```

## Claude Code

Use Claude Code's MCP command to add chatstrata as a user-scoped stdio server:

```bash
claude mcp add --transport stdio --scope user chatstrata -- uvx --from "chatstrata[mcp]" chatstrata-mcp
```

Or generate the command:

```bash
chatstrata mcp config claude-code
```

If you installed the tool directly and know `chatstrata-mcp` is on your PATH:

```bash
chatstrata mcp config claude-code --runner installed
```

Start a new session after adding the config. Approve the server when prompted.

For a project-scoped `.mcp.json`, use:

```json
{
  "mcpServers": {
    "chatstrata": {
      "type": "stdio",
      "command": "uvx",
      "args": ["--from", "chatstrata[mcp]", "chatstrata-mcp"]
    }
  }
}
```

If `chatstrata-mcp` is on your PATH, this also works:

```json
{
  "mcpServers": {
    "chatstrata": {
      "type": "stdio",
      "command": "chatstrata-mcp",
      "args": []
    }
  }
}
```

## Claude Desktop

Add to your Claude Desktop config (`~/Library/Application Support/Claude/claude_desktop_config.json` on macOS):

```json
{
  "mcpServers": {
    "chatstrata": {
      "type": "stdio",
      "command": "uvx",
      "args": ["--from", "chatstrata[mcp]", "chatstrata-mcp"]
    }
  }
}
```

Generate the same JSON with:

```bash
chatstrata mcp config claude-desktop
```

Restart Claude Desktop after saving.

## Database selection

By default, the MCP server reads the same platform-specific database path used
by the `chatstrata` CLI. Check it with:

```bash
chatstrata paths
```

To point an MCP client at a specific archive, set `CHATSTRATA_DB` in the server
environment. The config helper can include it:

```bash
chatstrata mcp config claude-desktop --db /absolute/path/to/chatstrata.duckdb
chatstrata mcp config claude-code --db /absolute/path/to/chatstrata.duckdb
```

## Remote Access (Tailscale / Mobile)

Run the server with HTTP transport on the machine that has your DuckDB database:

```bash
chatstrata serve --transport streamable-http --host 0.0.0.0 --port 8462
```

Then connect from any MCP client on your Tailscale network:

```json
{
  "mcpServers": {
    "chatstrata": {
      "url": "http://<tailscale-hostname>:8462/mcp"
    }
  }
}
```

## Safety

- Database is opened in read-only mode — mutations are rejected at the engine level
- Results capped at 500 rows / 512 KB to prevent context window blowup
- 30-second query timeout
- Multi-statement queries blocked

## Example Prompts

Once connected, you can ask things like:

- "What topics have I discussed most in the last month?"
- "Which tools do I use most frequently in Claude Code?"
- "Find conversations where I talked about authentication"
- "How has my prompting style changed over time?"
- "What projects have I been working on?"

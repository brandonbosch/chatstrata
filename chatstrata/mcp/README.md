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
uv pip install -e ".[mcp]"
```

## Claude Code

Add to `.mcp.json` at your project root (or `~/.claude/.mcp.json` for global):

```json
{
  "mcpServers": {
    "chatstrata": {
      "command": "chatstrata-mcp",
      "args": []
    }
  }
}
```

If `chatstrata-mcp` isn't on your PATH, use the full path to the binary:

```json
{
  "mcpServers": {
    "chatstrata": {
      "command": "/path/to/your/venv/bin/chatstrata-mcp",
      "args": []
    }
  }
}
```

Start a new session after adding the config. Approve the server when prompted.

## Claude Desktop

Add to your Claude Desktop config (`~/Library/Application Support/Claude/claude_desktop_config.json` on macOS):

```json
{
  "mcpServers": {
    "chatstrata": {
      "command": "/path/to/your/venv/bin/chatstrata-mcp",
      "args": []
    }
  }
}
```

Restart Claude Desktop after saving.

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

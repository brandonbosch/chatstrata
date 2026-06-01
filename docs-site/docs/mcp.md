---
title: MCP Server
description: Connect chatstrata to Claude Code, Claude Desktop, and other MCP clients.
---

# MCP Server

chatstrata ships an MCP server that lets an LLM query your local conversation
archive through a single read-only SQL tool. The server exposes:

| MCP primitive | Name | Purpose |
|---|---|---|
| Tool | `query` | Run read-only DuckDB SQL against your archive |
| Resource | `chatstrata://schema` | Inspect tables, columns, row counts, and example queries |

The server opens the database read-only, rejects mutating SQL, caps result size,
and blocks multi-statement queries.

## Install

```bash
uv tool install "chatstrata[embeddings,mcp]"
# or: pipx install "chatstrata[embeddings,mcp]"
```

Create and populate the archive before connecting an MCP client:

```bash
chatstrata init
chatstrata ingest --auto
chatstrata paths
```

If you installed only `chatstrata[mcp]`, run `chatstrata ingest --auto --no-embed` or install the embeddings extra before auto ingest.

## Claude Code

Add chatstrata as a user-scoped stdio server:

```bash
claude mcp add --transport stdio --scope user chatstrata -- uvx --from "chatstrata[mcp]" chatstrata-mcp
```

Or generate the command:

```bash
chatstrata mcp config claude-code
```

Start a new Claude Code session after adding the server. Check `/mcp` and
approve the server if prompted.

## Claude Desktop

Add this to Claude Desktop's MCP config:

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

Restart Claude Desktop after saving the config.

## Database Selection

By default, the MCP server uses the same platform-specific database path as the
CLI. Check it with:

```bash
chatstrata paths
```

To point the MCP server at a specific archive, include `CHATSTRATA_DB` in the
generated client setup:

```bash
chatstrata mcp config claude-code --db /absolute/path/to/chatstrata.duckdb
chatstrata mcp config claude-desktop --db /absolute/path/to/chatstrata.duckdb
```

## Remote Access

For HTTP-based MCP clients, run the server on the machine that has your archive:

```bash
chatstrata serve --transport streamable-http --host 0.0.0.0 --port 8462
```

Then connect to:

```text
http://<host>:8462/mcp
```

Use this only on a trusted network such as Tailscale.

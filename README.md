# chatstrata

<p align="center">
  <img src="docs/images/chatstrata.png" alt="chatstrata" width="360">
</p>

A personal, queryable archive of your AI conversations across providers.

Every conversation you've had with Claude, ChatGPT, or any other LLM is a record of
how you think, what you're working on, and how that's changed over time. Most of
that record lives scattered across browser exports, hidden JSONL files, and SaaS
dashboards you don't fully control. chatstrata pulls it into one place, normalizes
it, and lets you actually query and analyze it.

The name is from "strata" — layers of conversation deposited over time, with the
deeper layers telling you who you were.

## Why this exists

LLM providers collect rich data about how you interact with their models and use
it (in aggregate) to improve the experience for everyone. chatstrata is the same
idea, but for an audience of one: **you**. Your conversations, on your machine,
queryable on your terms.

Concretely, with chatstrata you can:

- Find every conversation where you discussed a topic, across providers.
- See how your prompting has changed over months or years.
- Audit every bash command you ran through Claude Code, grouped by project.
- Build a corpus that helps you brief a new model on who you are and what you care about.
- Identify abandoned projects, dropped threads, recurring patterns.

## Status

**Alpha.** Published on [PyPI](https://pypi.org/project/chatstrata/). Includes
adapters for Claude Code, claude.ai exports, Codex CLI, and OpenCode, plus
scheduled background sync so your archive stays current without manual runs. The
architecture is built so that adding more sources (ChatGPT exports, Cursor, etc.)
is the work of one adapter — see [docs/adapter-guide.md](docs/adapter-guide.md).

## Quickstart

Requires Python 3.10+. We recommend [uv](https://docs.astral.sh/uv/) — it's the
fastest way to get started (no virtualenv wrangling, no dependency conflicts).

### 1. Install

```bash
uv tool install "chatstrata[embeddings]"
```

<details>
<summary>Alternative: pipx</summary>

```bash
pipx install "chatstrata[embeddings]"
```
</details>

### 2. Initialize and ingest

```bash
chatstrata init                  # create the local DuckDB archive
chatstrata ingest --auto         # auto-detect transcripts and generate embeddings
```

### 3. Set up background sync

Never think about syncing again — chatstrata can keep your archive up to date
automatically in the background:

```bash
chatstrata schedule install              # sync every 15 minutes (default)
chatstrata schedule install --interval 1h   # or pick your own interval
chatstrata schedule status               # check that it's running
```

On macOS this installs a launchd agent that runs `chatstrata ingest --auto`
on the configured interval. Logs go to `~/Library/Logs/chatstrata/`. To stop
background sync:

```bash
chatstrata schedule uninstall
```

> **Linux:** systemd scheduling is not yet implemented. Use cron as a workaround:
> `*/15 * * * * chatstrata ingest --auto`

### 4. Query your archive

```bash
chatstrata stats
chatstrata query "SELECT model, COUNT(*) FROM messages GROUP BY model"
```

The default database lives at a platform-appropriate user data directory
(e.g. `~/.local/share/chatstrata/chatstrata.duckdb` on Linux). Override with
`CHATSTRATA_DB` or `--db`. Run `chatstrata paths` to see the exact paths for
your machine.

## MCP server

chatstrata ships an [MCP](https://modelcontextprotocol.io) server that exposes
your archive to MCP-aware clients (Claude Desktop, etc.) through a single
read-only `query` tool plus a `chatstrata://schema` resource. The client can
then write and run SQL against your conversations directly.

### 1. Install with MCP support

```bash
uv tool install "chatstrata[embeddings,mcp]"
# or: pipx install "chatstrata[embeddings,mcp]"
```

### 2. Create and populate the archive

The MCP server reads an existing database; make sure you've ingested something
first:

```bash
chatstrata init
chatstrata ingest --auto
chatstrata paths               # note the database path for the next step
```

If you installed without the `embeddings` extra, use `chatstrata ingest --auto --no-embed`
or install the extra before running auto mode.

### 3. Point your MCP client at chatstrata

The installed `chatstrata-mcp` executable speaks MCP over stdio. If you use
`uvx`, clients can run the published package without needing the absolute path
to that executable.

For Claude Code, run:

```bash
claude mcp add --transport stdio --scope user chatstrata -- uvx --from "chatstrata[mcp]" chatstrata-mcp
```

Or ask chatstrata to print the command:

```bash
chatstrata mcp config claude-code
```

For Claude Desktop, add an entry to its `mcpServers` config (Settings →
Developer → Edit Config):

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

You can generate that JSON with:

```bash
chatstrata mcp config claude-desktop
```

If `CHATSTRATA_DB` is omitted, the server falls back to the default platform
path. To pin the MCP server to a specific database, pass `--db` when generating
the setup snippet:

```bash
chatstrata mcp config claude-desktop --db /absolute/path/to/chatstrata.duckdb
```

Restart the client. The `chatstrata` server should appear with a `query` tool
available; ask it something like "what topics have I discussed most this month?"
and it will query your archive.

## Data model

chatstrata normalizes every conversation into the same shape regardless of source:

- **conversations** — one per session/thread
- **messages** — one per turn (user, assistant, system)
- **content_blocks** — one per content unit within a message (text, tool_use, tool_result, thinking, attachment)
- **tool_calls** — denormalized view of tool_use blocks for easy querying
- **raw_events** — the source data, line-for-line, for re-parsing without re-ingestion

See [docs/schema.md](docs/schema.md) for the full schema.

## Auto ingest

`chatstrata ingest --auto` scans every installed adapter's default location,
ingests the sources it finds, and then generates missing message embeddings.
For each source, the first auto run does a full ingest; later auto runs switch
that source to incremental mode and skip unchanged files.

Embedding uses a local sentence-transformers model. The first embedding run may
download the model if it is not already cached; transcript content is not sent
to chatstrata or a hosted inference API.

Use explicit source commands when you need a custom path or a provider export
that has no default location:

```bash
chatstrata ingest claude_export --path ~/Downloads/claude-export/
chatstrata ingest claude_code --path ~/alternate/.claude/projects --incremental
```

## Adding a source

Each source (Claude Code, ChatGPT export, etc.) is an adapter that implements a
small protocol: `discover()` finds available conversations, `parse()` turns them
into the canonical record types. See [docs/adapter-guide.md](docs/adapter-guide.md)
for the worked example using Claude Code.

Adapters can be contributed as PRs to this repo or as standalone pip packages
that register via entry points.

## Privacy

Your transcript content stays on your machine. Standard ingestion and querying
make no network calls. Semantic search uses local embeddings; the first run may
download the configured sentence-transformers model if it is not already cached.
DuckDB's VSS extension is optional, and chatstrata only installs that extension
when `CHATSTRATA_INSTALL_DUCKDB_VSS=1` is set.

If you want to share queries or notebooks publicly, an optional redaction layer
(`uv tool install "chatstrata[redact]"`) wraps Microsoft Presidio with
chatstrata-specific recognizers for API keys, file paths, and other things that
commonly appear in LLM transcripts. See [docs/redaction.md](docs/redaction.md).

## Contributing

Contributions welcome. Especially valuable: new source adapters. See
[CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache 2.0. See [LICENSE](LICENSE).

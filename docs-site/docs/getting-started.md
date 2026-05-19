---
title: Getting Started
description: Install chatstrata, ingest your first conversations, and run your first queries.
---

# Getting Started

This guide gets you from zero to querying your conversation archive in under five minutes.

## Prerequisites

- Python 3.10 or later.
- [uv](https://docs.astral.sh/uv/) or [pipx](https://pipx.pypa.io/).
- No separate DuckDB install. The embedded DuckDB Python package is installed
  with chatstrata.

## Install

```bash
uv tool install chatstrata
# or: pipx install chatstrata
```

For optional features, install the relevant extras:

```bash
# Semantic search via sentence-transformers
uv tool install "chatstrata[embeddings]"

# PII redaction via Presidio
uv tool install "chatstrata[redact]"

# Everything
uv tool install "chatstrata[embeddings,redact,mcp]"
```

For local development from a clone, use:

```bash
git clone https://github.com/brandonbosch/chatstrata.git
cd chatstrata
uv venv
uv pip install -e ".[dev,redact,embeddings]"
```

## Initialize your archive

```bash
chatstrata init
```

This creates the local DuckDB file, applies schema migrations, and shows which
conversation sources are detectable on your machine. The database is just a
single local file; there is no server to run.

## Check installed sources

```bash
chatstrata sources
```

This lists every adapter chatstrata can find via Python entry points. Out of the box you'll see:

```
  claude_code          Claude Code  (v0.1.0)
  claude_export        claude.ai Export  (v0.1.0)
  codex_cli            Codex CLI  (v0.1.0)
  opencode             OpenCode  (v0.1.0)
```

Third-party adapters installed as pip packages appear here automatically.

## Ingest your conversations

Start with Claude Code — it reads directly from `~/.claude/projects/`:

```bash
chatstrata ingest claude_code
```

For a claude.ai data export (download from your account settings), point to the extracted directory:

```bash
chatstrata ingest claude_export --path ~/Downloads/claude-export/
```

For Codex CLI sessions:

```bash
chatstrata ingest codex_cli
```

For OpenCode sessions (reads from the local SQLite database):

```bash
chatstrata ingest opencode
```

Use `--dry-run` to preview what would be ingested without writing anything:

```bash
chatstrata ingest claude_code --dry-run
```

On subsequent runs, add `--incremental` to skip conversations whose source files haven't changed:

```bash
chatstrata ingest claude_code --incremental
```

## See what's there

```bash
chatstrata stats
```

This shows a summary: how many conversations and messages per source, date ranges, and totals.

## Your first queries

Run arbitrary SQL against the DuckDB database:

```bash
# Messages by role
chatstrata query "SELECT role, COUNT(*) FROM messages GROUP BY role"

# Most-used tools
chatstrata query "SELECT tool_name, COUNT(*) as n FROM tool_calls GROUP BY tool_name ORDER BY n DESC LIMIT 10"

# Conversations per project
chatstrata query "SELECT project, COUNT(*) FROM conversations WHERE project IS NOT NULL GROUP BY project ORDER BY 2 DESC"
```

Add `--json` for machine-readable output.

## Search

Keyword search uses BM25 full-text indexing. After ingesting, rebuild the search index:

```bash
chatstrata reindex
chatstrata search "database migration"
```

Filter by source, date range, or limit results:

```bash
chatstrata search "auth module" --source claude_code --since 2026-01-01 --limit 5
```

For semantic search (requires the `[embeddings]` extra), first generate embeddings, then search:

```bash
chatstrata embed --source claude_code
chatstrata search --semantic "how to handle errors"
chatstrata search --hybrid "refactor login flow"
```

## Bundled analysis

```bash
chatstrata analyze activity --by month
chatstrata analyze tools --source claude_code
chatstrata analyze models
chatstrata analyze projects
chatstrata analyze conversations --longest 5
```

## Where the database lives

By default, chatstrata stores its DuckDB file at the `platformdirs`-derived user data directory:

| Platform | Default path |
|----------|-------------|
| Linux | `~/.local/share/chatstrata/chatstrata.duckdb` |
| macOS | `~/Library/Application Support/chatstrata/chatstrata.duckdb` |
| Windows | `C:\Users\<you>\AppData\Local\chatstrata\chatstrata.duckdb` |

Override with the `CHATSTRATA_DB` environment variable or the `--db` flag on any command:

```bash
export CHATSTRATA_DB=~/my-archive.duckdb
chatstrata stats
```

Run `chatstrata paths` at any time to see the database path, data directory, and
optional config path for your machine.

## Next steps

- [The Canonical Schema](schema.md) — understand the tables and design principles
- [Querying and Analysis](querying.md) — DuckDB query patterns, search modes, analyze commands
- [Writing a Source Adapter](adapters.md) — contribute support for a new provider
- [Privacy and Redaction](privacy.md) — detect and remove sensitive data before sharing
- [CLI Reference](cli.md) — every command with flags and examples

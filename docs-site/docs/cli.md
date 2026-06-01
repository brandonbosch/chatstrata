---
title: CLI Reference
description: Every chatstrata command, with flags and examples.
---

# CLI Reference

`chatstrata` is a Click-based CLI with commands organized into core database operations, search, analysis, embeddings, redaction, and MCP setup. Every command that touches the database accepts a `--db` flag to override the default path. The default database location is determined by `platformdirs` (e.g., `~/.local/share/chatstrata/chatstrata.duckdb` on Linux) and can also be set via the `CHATSTRATA_DB` environment variable.

Most commands that produce tabular output support a `--json` flag for machine-readable output.

```bash
# Check the installed version
chatstrata --version
```

## Core commands

### sources

List all installed source adapters. Each adapter knows how to discover and parse conversations from a specific provider.

```bash
chatstrata sources
```

No flags. Output shows adapter name, display name, and version.

### ingest

Import conversations from a source adapter into the database. For first-time setup, `chatstrata ingest --auto` is the recommended path: it checks every installed adapter's default location, ingests detected sources, and generates embeddings for eligible messages.

| Flag | Description |
|------|-------------|
| `--path` | Override the default discovery path for this source |
| `--db` | Override the database path |
| `--limit N` | Ingest at most N conversations |
| `--dry-run` | Discover conversations but do not write to the database |
| `--incremental` | Skip conversations whose source file has not changed since the last ingest |
| `--auto` | Detect available default sources, ingest them, and generate embeddings |
| `--no-embed` | With `--auto`, skip embedding generation |
| `--model MODEL` | Embedding model for `--auto` |
| `--min-tokens N` | Minimum message size for `--auto` embeddings |
| `--batch-size N` | Embedding batch size for `--auto` |

```bash
# Auto-detect local app conversations, ingest them, and generate embeddings
chatstrata ingest --auto

# Preview detected default sources without writing
chatstrata ingest --auto --dry-run

# Auto-ingest without requiring the embeddings extra
chatstrata ingest --auto --no-embed

# Ingest all Claude Code conversations
chatstrata ingest claude_code

# Re-ingest only changed files
chatstrata ingest claude_code --incremental

# Preview what would be ingested without writing
chatstrata ingest claude_code --dry-run

# Ingest from a non-default path, limited to 50 conversations
chatstrata ingest claude_code --path ~/custom/claude --limit 50

# Ingest OpenCode sessions from the default SQLite database
chatstrata ingest opencode

# Ingest OpenCode from a non-default database
chatstrata ingest opencode --path ~/alternate/opencode.db
```

Auto mode does a full ingest for sources that are not yet in the database. On later runs, it switches those sources to incremental ingest and skips unchanged file-backed conversations. Sources that do not have a default discoverable location, such as an extracted claude.ai export, still need a manual `--path`.

### query

Run arbitrary SQL against the DuckDB database and print results.

| Flag | Description |
|------|-------------|
| `--db` | Override the database path |
| `--json` | Output rows as JSON |

```bash
# Count messages by role
chatstrata query "SELECT role, COUNT(*) FROM messages GROUP BY role"

# Get results as JSON for piping to jq
chatstrata query --json "SELECT title, started_at FROM conversations ORDER BY started_at DESC LIMIT 5"
```

### stats

Print a summary of what is in the database: sources, conversation counts, date ranges, and totals.

| Flag | Description |
|------|-------------|
| `--db` | Override the database path |

```bash
chatstrata stats
```

### doctor

Run sanity checks on the database. Reports empty sources, conversations with no messages, and messages with no content blocks.

| Flag | Description |
|------|-------------|
| `--db` | Override the database path |

```bash
chatstrata doctor
```

### migrate

Apply pending schema migrations or check migration status. Migrations run automatically on most commands, but this gives you explicit control.

| Flag | Description |
|------|-------------|
| `--db` | Override the database path |
| `--status` | Show current schema version and pending migrations without applying |

```bash
# Check if migrations are needed
chatstrata migrate --status

# Apply pending migrations
chatstrata migrate
```

### reindex

Rebuild the full-text search index over all content blocks. Run this after ingesting new data if keyword search is not returning expected results.

| Flag | Description |
|------|-------------|
| `--db` | Override the database path |

```bash
chatstrata reindex
```

## Search

### search

Search your conversation archive by keyword (default), semantic similarity, or a hybrid of both. Keyword search uses DuckDB's built-in full-text search. Semantic and hybrid modes require pre-computed embeddings (see [embed](#embed)).

| Flag | Description |
|------|-------------|
| `--source` | Filter to a specific source (e.g. `claude_code`) |
| `--since YYYY-MM-DD` | Only include messages after this date |
| `--until YYYY-MM-DD` | Only include messages before this date |
| `--limit N` | Maximum number of results (default: 20) |
| `--json` | Output results as JSON |
| `--semantic` | Use semantic similarity search (requires embeddings) |
| `--hybrid` | Combine keyword and semantic search via reciprocal rank fusion |
| `--model` | Embedding model for `--semantic` / `--hybrid` (default: `all-MiniLM-L6-v2`) |

```bash
# Basic keyword search
chatstrata search "auth module"

# Semantic search (finds conceptually similar messages)
chatstrata search --semantic "how to handle authentication"

# Hybrid search filtered by source and date range
chatstrata search --hybrid "refactor login" --source claude_code --since 2025-01-01

# JSON output for scripting
chatstrata search --json "deployment pipeline" --limit 10
```

## Analysis

The `analyze` subcommand group provides five built-in analytical views over your archive. All subcommands support `--db` and `--json`.

### analyze activity

Show message counts over time, grouped by day, week, or month.

| Flag | Description |
|------|-------------|
| `--by` | Time granularity: `day`, `week`, or `month` (default: `month`) |
| `--source` | Filter to a specific source |
| `--db` | Override the database path |
| `--json` | Output as JSON |

```bash
# Monthly activity summary
chatstrata analyze activity

# Daily breakdown for Claude Code conversations
chatstrata analyze activity --by day --source claude_code
```

### analyze tools

Show tool usage frequency across your conversations.

| Flag | Description |
|------|-------------|
| `--source` | Filter to a specific source |
| `--db` | Override the database path |
| `--json` | Output as JSON |

```bash
chatstrata analyze tools
chatstrata analyze tools --source claude_code --json
```

### analyze conversations

Show conversation length statistics, optionally ranked by longest or shortest.

| Flag | Description |
|------|-------------|
| `--longest N` | Show the N longest conversations |
| `--shortest N` | Show the N shortest conversations |
| `--db` | Override the database path |
| `--json` | Output as JSON |

Only one of `--longest` or `--shortest` can be specified at a time. Without either flag, conversations are listed longest-first (default top 20).

```bash
# Top 10 longest conversations
chatstrata analyze conversations --longest 10

# Shortest 5 conversations as JSON
chatstrata analyze conversations --shortest 5 --json
```

### analyze models

Show a breakdown of which AI models appear in your conversations.

| Flag | Description |
|------|-------------|
| `--db` | Override the database path |
| `--json` | Output as JSON |

```bash
chatstrata analyze models
```

### analyze projects

Show per-project conversation counts. Particularly useful for Claude Code sessions where conversations are associated with project directories.

| Flag | Description |
|------|-------------|
| `--db` | Override the database path |
| `--json` | Output as JSON |

```bash
chatstrata analyze projects --json
```

## MCP

### mcp config

Print setup snippets for MCP clients. This command does not edit client config
files; it gives you the command or JSON to paste into the target client.

Requires the `[mcp]` extra when the client actually starts the server:
`uv tool install "chatstrata[mcp]"`.

| Argument / Flag | Description |
|------|-------------|
| `claude-code` | Print a `claude mcp add` command |
| `claude-desktop` | Print a Claude Desktop `mcpServers` JSON block |
| `--runner uvx` | Launch with `uvx --from "chatstrata[mcp]" chatstrata-mcp` (default) |
| `--runner installed` | Launch `chatstrata-mcp` from PATH |
| `--db PATH` | Set `CHATSTRATA_DB` for the MCP server |
| `--scope local\|project\|user` | Claude Code MCP scope (default: `user`) |

```bash
# Claude Code setup command
chatstrata mcp config claude-code

# Claude Desktop JSON
chatstrata mcp config claude-desktop

# Pin the server to a specific database
chatstrata mcp config claude-desktop --db /absolute/path/to/chatstrata.duckdb

# Use an already-installed chatstrata-mcp executable
chatstrata mcp config claude-code --runner installed
```

## Embeddings

### embed

Generate vector embeddings for messages and store them in the `message_embeddings` table. This is a prerequisite for `search --semantic` and `search --hybrid`. Messages already embedded with the same model are skipped automatically.

`chatstrata ingest --auto` runs this embedding step automatically unless `--no-embed` is set.

Requires the `[embeddings]` extras: `uv tool install "chatstrata[embeddings]"`.

| Flag | Description |
|------|-------------|
| `--source` | Filter to a specific source |
| `--since YYYY-MM-DD` | Only embed messages after this date |
| `--model` | Embedding model name (default: `all-MiniLM-L6-v2`) |
| `--min-tokens N` | Skip messages shorter than N tokens (default: 50) |
| `--batch-size N` | Batch size for embedding (default: 64) |
| `--db` | Override the database path |

```bash
# Embed all un-embedded messages
chatstrata embed

# Embed only Claude Code messages with a lower token threshold
chatstrata embed --source claude_code --min-tokens 30

# Use a different model with a larger batch size
chatstrata embed --model all-MiniLM-L6-v2 --batch-size 128 --since 2025-01-01
```

## Redaction

The `redact` subcommand group detects and redacts PII using a Presidio-backed engine. Requires the `[redact]` extras: `uv tool install "chatstrata[redact]"`.

All redact subcommands accept a `--mode` flag with these options:

| Mode | Behavior |
|------|----------|
| `detect_only` | Find entities without modifying text |
| `tag` | Wrap entities in `<PII:type>...</PII:type>` tags |
| `mask` | Replace with `[TYPE_N]` placeholders (default) |
| `remove` | Delete entity text entirely |
| `hash` | Replace with a stable hash of the entity |

The `--allow-entity` flag can be specified multiple times to include entity types that are excluded by default (e.g., `DATE_TIME`, `ORGANIZATION`, `PERSON`).

### redact text

Redact PII from a single text string.

| Flag | Description |
|------|-------------|
| `--mode` | Redaction mode (default: `mask`) |
| `--json` | Output as JSON (includes original text, redacted text, entity details, and mapping) |
| `--allow-entity` | Entity types to include that are off by default (repeatable) |

```bash
# Mask PII in a string
chatstrata redact text "My API key is sk-ant-api03-abc123 and my email is user@example.com"

# Detect only, output as JSON
chatstrata redact text --mode detect_only --json "Call me at 555-0123"

# Include person names in detection
chatstrata redact text --allow-entity PERSON "Contact Alice at alice@example.com"
```

### redact query

Run a SQL query and automatically redact PII from all string columns in the results.

| Flag | Description |
|------|-------------|
| `--mode` | Redaction mode (default: `mask`) |
| `--db` | Override the database path |
| `--json` | Output as JSON |
| `--allow-entity` | Entity types to include that are off by default (repeatable) |

```bash
# View content blocks with PII redacted
chatstrata redact query "SELECT text FROM content_blocks LIMIT 10"

# Tag PII instead of masking, output as JSON
chatstrata redact query --mode tag --json "SELECT role, text FROM content_blocks WHERE text LIKE '%@%' LIMIT 5"
```

### redact interactive

Interactively walk through detected PII entities one at a time, choosing to redact or skip each one. Supports bulk decisions per entity type.

| Flag | Description |
|------|-------------|
| `--db` | Override the database path |
| `--mode` | Redaction mode for confirmed entities (default: `mask`) |
| `--sql` | SQL to select text to review; must return a `text` column (default: first 100 content blocks) |
| `--allow-entity` | Entity types to include that are off by default (repeatable) |

During review, each entity prompts for a decision: `[r]edact`, `[s]kip`, `[R]edact all` of that type, `[S]kip all` of that type, or `[q]uit`.

```bash
# Review the first 100 content blocks
chatstrata redact interactive

# Review a specific set of blocks
chatstrata redact interactive --sql "SELECT id, text FROM content_blocks WHERE text LIKE '%password%' LIMIT 20"
```

## Related

- [Schema](schema.md) -- database tables and column definitions
- [Ingestion pipeline](ingestion.md) -- how conversations flow from source adapters into the database
- [Source adapters](adapters.md) -- details on each adapter's discovery and parsing logic
- [Querying and search](querying.md) -- deeper dive into full-text and semantic search

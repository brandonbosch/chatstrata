---
title: chatstrata
description: A personal, queryable archive of your AI conversations across providers.
---

# chatstrata

![chatstrata](images/chatstrata.png){ width="360" }

Every conversation you have with an LLM is a record of how you think, what you're working on, and how that's changed over time. Most of that record is scattered across browser exports, hidden JSONL files, and SaaS dashboards you don't fully control.

chatstrata pulls it into one place, normalizes it into a [source-agnostic schema](schema.md), and lets you actually query and analyze it. DuckDB is the query engine — analytical by design, so the queries that matter run fast.

Your data stays on your machine. chatstrata makes no network calls during ingestion or querying.

## What you can do with this

```sql
-- Every bash command you've run through Claude Code, grouped by project
SELECT project, tool_name, COUNT(*) as uses
FROM tool_calls
WHERE tool_name = 'Bash'
GROUP BY project, tool_name
ORDER BY uses DESC;
```

```sql
-- How your prompting has changed over months
SELECT DATE_TRUNC('month', m.created_at) AS month,
       AVG(LENGTH(cb.text)) AS avg_prompt_length
FROM messages m
JOIN content_blocks cb ON cb.message_id = m.id
WHERE m.role = 'user' AND cb.type = 'text'
GROUP BY month ORDER BY month;
```

```bash
# Search for that thing you discussed with Claude last week
chatstrata search "auth module" --since 2026-05-07
```

Find abandoned threads. See which models you've used most. Audit every tool call across every provider. Build a corpus that helps you brief a new model on who you are and what you care about.

## Quick start

```bash
uv tool install "chatstrata[embeddings]"

chatstrata init
chatstrata ingest --auto
chatstrata stats
chatstrata query "SELECT role, COUNT(*) FROM messages GROUP BY role"
```

See [Getting Started](getting-started.md) for the full setup guide.

## How it's structured

chatstrata normalizes every conversation — regardless of source — into the same shape:

- **conversations** — one per session or thread
- **messages** — one per turn (user, assistant, system, tool)
- **content_blocks** — one per content unit within a message (text, tool_use, tool_result, thinking, image, attachment)
- **tool_calls** — a convenience view for querying tool usage with project context
- **raw_events** — the source data, line-for-line, preserved so improved parsers can re-derive without re-ingesting

The [canonical schema](schema.md) page has the full design rationale.

## Source adapters

chatstrata ships with four built-in adapters: [Claude Code, claude.ai exports, Codex CLI, and OpenCode](sources.md). Adding a new source is the work of one adapter — implement `discover()` and `parse()`, register an entry point, and chatstrata auto-discovers it.

Adapters can live in this repo or be published as standalone pip packages. Growing the supported-sources list is the project's primary growth vector. See [Writing a Source Adapter](adapters.md) if you're interested in contributing one.

## Documentation

| Page | What it covers |
|------|----------------|
| [Getting Started](getting-started.md) | Install, ingest, first queries |
| [The Canonical Schema](schema.md) | Tables, design principles, source-agnosticism |
| [Ingestion Pipeline](ingestion.md) | Discover, parse, normalize, persist |
| [Querying and Analysis](querying.md) | SQL, full-text search, semantic search, analyze commands |
| [MCP Server](mcp.md) | Query your archive from Claude Code, Claude Desktop, and other MCP clients |
| [Built-in Source Adapters](sources.md) | Claude Code, claude.ai export, Codex CLI, OpenCode |
| [Writing a Source Adapter](adapters.md) | The contribution path for new sources |
| [Privacy and Redaction](privacy.md) | PII detection, Presidio engine, custom recognizers |
| [Schema Migrations](migrations.md) | How the database schema evolves |
| [CLI Reference](cli.md) | Every command, with flags and examples |

## License

Apache 2.0.

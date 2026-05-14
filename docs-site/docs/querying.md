---
title: Querying and Analysis
description: SQL queries, full-text search, semantic search, and bundled analysis commands.
---

# Querying and Analysis

Ingestion gets your conversations into a DuckDB database. Querying is where the value lives. Because DuckDB is an analytical engine -- not a transactional one -- you get window functions, JSON path expressions, and fast aggregations over your entire conversation history out of the box. chatstrata layers three search modes on top (keyword, semantic, hybrid) and ships bundled analysis commands for the questions you will ask most.

## Raw SQL queries

The `query` command passes arbitrary SQL straight to DuckDB and prints the result as a tab-separated table or JSON:

```bash
chatstrata query "SELECT role, COUNT(*) FROM messages GROUP BY role"
chatstrata query --json "SELECT model, COUNT(*) FROM messages WHERE model IS NOT NULL GROUP BY model ORDER BY count(*) DESC"
```

DuckDB's `date_trunc`, JSON path operators, and window functions make it natural to ask questions that would be painful in SQLite. A few examples worth trying:

```sql title="Messages per week over time"
SELECT
    date_trunc('week', m.created_at) AS week,
    COUNT(*) AS messages,
    COUNT(DISTINCT m.conversation_id) AS conversations
FROM messages m
WHERE m.created_at IS NOT NULL
GROUP BY week
ORDER BY week;
```

```sql title="Conversation length distribution"
SELECT
    CASE
        WHEN message_count < 5 THEN 'very short (< 5)'
        WHEN message_count < 20 THEN 'short (5-19)'
        WHEN message_count < 50 THEN 'medium (20-49)'
        ELSE 'long (50+)'
    END AS length_bucket,
    COUNT(*) AS conversations
FROM conversations
GROUP BY length_bucket
ORDER BY conversations DESC;
```

```sql title="Average user prompt length over time"
SELECT
    date_trunc('week', m.created_at) AS week,
    AVG(length(cb.text) - length(replace(cb.text, ' ', ''))) AS avg_word_count
FROM content_blocks cb
JOIN messages m ON m.id = cb.message_id
WHERE m.role = 'user'
  AND cb.type = 'text'
  AND cb.text IS NOT NULL
GROUP BY week
ORDER BY week;
```

DuckDB's JSON path expressions work directly on the `payload` column in `content_blocks`. This is how you reach into structured data like tool call inputs without needing extra tables:

```sql title="Every bash command run through Claude Code, grouped by project"
SELECT
    c.project,
    cb.payload->'input'->>'command' AS command,
    COUNT(*) AS times_run
FROM content_blocks cb
JOIN messages m ON m.id = cb.message_id
JOIN conversations c ON c.id = m.conversation_id
WHERE cb.type = 'tool_use'
  AND cb.tool_name = 'Bash'
  AND c.source_id = 'claude_code'
GROUP BY c.project, command
ORDER BY times_run DESC
LIMIT 50;
```

```sql title="How often does the model use explicit thinking, by month"
SELECT
    date_trunc('month', m.created_at) AS month,
    SUM(CASE WHEN cb.type = 'thinking' THEN 1 ELSE 0 END) AS thinking_blocks,
    COUNT(DISTINCT m.id) AS assistant_messages,
    ROUND(
        100.0 * SUM(CASE WHEN cb.type = 'thinking' THEN 1 ELSE 0 END)
              / NULLIF(COUNT(DISTINCT m.id), 0),
        1
    ) AS pct
FROM messages m
LEFT JOIN content_blocks cb ON cb.message_id = m.id
WHERE m.role = 'assistant'
GROUP BY month
ORDER BY month DESC;
```

## The tool_calls view

The schema includes a convenience view called `tool_calls` that flattens every `tool_use` content block into a row with conversation and project context already joined:

```sql title="chatstrata/core/migrations/0001_initial.sql (view definition)"
CREATE VIEW tool_calls AS
SELECT
    cb.id AS call_id,
    cb.tool_use_id,
    cb.tool_name,
    cb.payload AS input,
    m.id AS message_id,
    m.conversation_id,
    m.created_at,
    c.project,
    c.source_id
FROM content_blocks cb
JOIN messages m ON m.id = cb.message_id
JOIN conversations c ON c.id = m.conversation_id
WHERE cb.type = 'tool_use';
```

This saves you from writing the three-way join every time. The `analyze tools` command uses it under the hood:

```bash
chatstrata query "SELECT tool_name, COUNT(*) AS calls FROM tool_calls GROUP BY tool_name ORDER BY calls DESC"
```

Since `input` is a JSON column containing the tool call's payload, you can drill into tool-specific fields. For Claude Code conversations, for example, `input->'input'->>'command'` gives you the shell command text for Bash calls, and `input->'input'->>'file_path'` gives you the file path for Read/Edit calls.

## Full-text search

chatstrata uses DuckDB's built-in FTS extension to provide BM25-ranked keyword search across all content blocks. The index uses Porter stemming and English stopword removal.

```bash
chatstrata search "auth module"
chatstrata search --source claude_code --since 2025-01-01 "refactor"
chatstrata search --limit 5 --json "deployment pipeline"
```

Results show the source, conversation title, role, timestamp, relevance score, and a text snippet with context around the match.

!!! tip
    The FTS index is not updated automatically during ingest. Run `chatstrata reindex` after ingesting new data to rebuild it.

The `--source`, `--since`, and `--until` flags filter results before ranking. Under the hood, BM25 scoring is performed via `fts_main_content_blocks.match_bm25()`, a function created by the FTS extension.

## Semantic and hybrid search

For queries where exact keywords fall short -- "that conversation about my grandma's video" or "refactoring the login flow" -- chatstrata supports embedding-based semantic search and a hybrid mode that combines both approaches.

### Generating embeddings

Before semantic search works, you need to populate the `message_embeddings` table:

```bash
chatstrata embed
chatstrata embed --source claude_code --since 2025-01-01
chatstrata embed --model all-MiniLM-L6-v2 --min-tokens 30 --batch-size 128
```

The `embed` command requires the optional `[embeddings]` extras:

```bash
uv pip install "chatstrata[embeddings]"
```

The default provider uses `sentence-transformers/all-MiniLM-L6-v2` (384 dimensions, ~23M parameters). The embedding architecture is pluggable -- any class implementing the `EmbeddingProvider` protocol (with `embed_texts` and `embed_query` methods) can serve as a provider.

Messages shorter than `--min-tokens` (default: 50) are skipped to avoid embedding trivial content. Messages already embedded with the same model are also skipped, making the command safe to re-run incrementally. Processing happens in configurable batches (default: 64).

### Semantic search

Semantic search computes cosine similarity between a query embedding and stored message embeddings using DuckDB's `list_cosine_similarity` function:

```bash
chatstrata search --semantic "grandma video"
chatstrata search --semantic --source claude_code "refactor login"
```

### Hybrid search

Hybrid mode runs both keyword and semantic search, then merges results using reciprocal rank fusion (RRF). Each result's final score is computed as:

    score = keyword_weight / (rank_keyword + k) + semantic_weight / (rank_semantic + k)

where `k = 60` is the RRF constant. Both weights default to 0.5. This tends to surface results that rank well in both systems while still including results that only one system finds.

```bash
chatstrata search --hybrid "deployment pipeline"
chatstrata search --hybrid --model all-MiniLM-L6-v2 "error handling patterns"
```

Both `--semantic` and `--hybrid` accept `--source`, `--since`, `--until`, and `--limit` for filtering. The `--model` flag selects which embedding model to query against (must match the model used during `chatstrata embed`).

## Bundled analysis commands

The `chatstrata analyze` group provides five prebuilt queries for the most common questions:

### activity

Messages over time, grouped by day, week, or month:

```bash
chatstrata analyze activity --by week
chatstrata analyze activity --by month --source claude_code --json
```

### tools

Tool usage frequency across all conversations:

```bash
chatstrata analyze tools
chatstrata analyze tools --source claude_code
```

### conversations

Conversation length statistics, sorted longest or shortest first:

```bash
chatstrata analyze conversations --longest 10
chatstrata analyze conversations --shortest 5
```

### models

Which models generated your assistant messages:

```bash
chatstrata analyze models
```

### projects

Per-project conversation counts for Claude Code sessions:

```bash
chatstrata analyze projects
```

All analysis commands support `--json` for machine-readable output and `--db` to target a specific database file. The underlying SQL lives in `chatstrata/analysis/queries/*.sql` -- you can read these files directly for inspiration or modify them for custom analysis.

## Key entry points

| File | Purpose |
|------|---------|
| `chatstrata/cli.py` | `query`, `search`, `reindex`, `stats` commands |
| `chatstrata/core/search.py` | BM25 full-text search implementation |
| `chatstrata/core/db.py` | FTS/VSS extension loading, index rebuild |
| `chatstrata/analysis/cli.py` | `analyze` subcommand group |
| `chatstrata/analysis/queries/*.sql` | Bundled analytical SQL |
| `chatstrata/embed/cli.py` | `embed` command |
| `chatstrata/embed/search.py` | `semantic_search` and `hybrid_search` |
| `chatstrata/embed/base.py` | `EmbeddingProvider` protocol |
| `chatstrata/embed/local_provider.py` | sentence-transformers provider |

## Related

- [Schema Design](schema.md) -- table definitions, the `content_blocks` model, and the `tool_calls` view DDL.
- [Ingestion Pipeline](ingestion.md) -- how conversations get into the database before you can query them.
- [Source Adapters](adapters.md) -- the sources that produce the data you query.

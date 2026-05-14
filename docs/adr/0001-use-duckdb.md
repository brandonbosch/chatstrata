# ADR 0001: Use DuckDB as the storage and query engine

**Status:** Accepted
**Date:** 2026-05

## Context

chatstrata needs a storage layer that:
- Lives on the user's machine (no network, no service to run)
- Handles tens of millions of rows comfortably
- Supports rich analytical queries (window functions, joins, aggregations)
- Can read JSON / JSONL directly for exploration
- Has Python bindings and works on Linux, macOS, Windows
- Is fast enough that "interactive analysis" is the default mode

## Decision

Use DuckDB.

## Alternatives considered

**SQLite** — would work, but is row-oriented and far slower for the analytical
queries we expect to dominate ("top n-grams in my prompts by year", "messages
per week", "tool call frequencies"). SQLite is great for transactional
workloads; chatstrata is not transactional.

**Postgres** — overkill for a personal archive, requires a running service,
adds friction to "git clone and go."

**Parquet files + DuckDB query layer** — the long-term direction is probably
this for archival, but managing a directory of Parquet files is more friction
than a single .duckdb file for v0.

**Plain JSONL with grep / jq** — viable for small users, painful at scale and
makes joins between sources nearly impossible.

## Consequences

- Single-file database. Easy to back up, easy to delete.
- DuckDB is single-writer; we don't support concurrent ingestion processes.
  Acceptable for a personal tool; we'd revisit if multi-user becomes a goal.
- We can `SELECT * FROM read_json('~/.claude/projects/**/*.jsonl')` during
  development without an ingest step. Big win for adapter authoring.
- The `vss` extension is available if/when we add embeddings.
- DuckDB's JSON support means we can store provider-specific extras as JSON
  columns and still query into them.

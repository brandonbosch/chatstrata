# Changelog

## Unreleased

- Add `chatstrata ingest --auto` to detect default local sources, ingest all available conversations, choose full vs incremental ingest per source, and generate missing embeddings.
- Add `--no-embed`, `--model`, `--min-tokens`, and `--batch-size` controls for auto ingest.
- Document auto ingest as the recommended first-run path.

## 0.1.0 - 2026-05-19

Initial public alpha.

- Ingest Claude Code, claude.ai exports, Codex CLI, and OpenCode conversations.
- Store normalized conversations, messages, content blocks, tool calls, and raw events in DuckDB.
- Query archives with SQL, keyword search, and bundled analysis commands.
- Support incremental re-ingestion for file-backed sources.
- Provide optional local redaction via Presidio and chatstrata-specific recognizers.
- Provide optional semantic and hybrid search through local embeddings.
- Provide an MCP server for read-only archive querying.

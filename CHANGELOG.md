# Changelog

## 0.3.1 - 2026-09-15

- Fix `chatstrata.__version__`: it was hardcoded (`0.2.1`) and stopped matching the package version at every release since; it is now read from installed package metadata, so `chatstrata --version` always agrees with the PyPI version.

## 0.3.0 - 2026-09-15

- Add Hermes Agent (`hermes_agent`) source adapter: ingests conversations from Hermes Agent's canonical SQLite session store (`~/.hermes/state.db`, or `$HERMES_HOME/state.db`). Maps assistant tool calls to `tool_use` blocks, tool rows to `tool_result`, reasoning columns to `thinking` blocks; skips superseded rows after rewinds, flags compaction summaries, and preserves raw session/message rows for re-parsing. The database is opened read-only. A `{"path": ...}` source config targets profile-specific databases.

## 0.2.3 - 2026-09-12

- Add Oh My Pi (`omp`) source adapter: ingests session transcripts from `~/.omp/agent/sessions/`, including tool calls/results, thinking blocks, extension (`custom_message`) turns, titles, and project paths from the session header.
- Fix `codex_cli` adapter: newer Codex rollouts encode tool-result `output` fields as content arrays instead of plain strings; these now parse correctly.

## 0.2.2 - 2026-07-29

- Cap the `mcp` dependency at `>=1.12.0,<2`. `mcp` 2.0.0 removed the `mcp.server.fastmcp` module that `chatstrata/mcp/server.py` imports, so the previously unbounded pin resolved 2.0.0 and crashed `chatstrata-mcp` on startup with `ModuleNotFoundError: No module named 'mcp.server.fastmcp'`.

## 0.2.1 - 2026-07-22

- Fix `claude_code` conversation `project` paths: derive the project from the lossless `cwd` recorded in each transcript instead of decoding the session folder name. The folder-name encoding collapses `/`, `_`, `-`, and `.` all into `-`, so decoding it produced wrong, non-existent paths (e.g. `bstaq_git/pepstaq` became `bstaq/git`). New ingests are now correct automatically; `codex_cli` and `opencode` were unaffected.

## 0.2.0 - 2026-06-16

- Add `chatstrata schedule` command group for automatic background sync.
- macOS: install a launchd agent that runs `chatstrata ingest --auto` on a configurable interval (default 15m).
- Subcommands: `schedule install`, `schedule uninstall`, `schedule status`.
- Supports `--interval`, `--no-embed`, and `--binary` options.
- Catches up automatically after sleep/wake via RunAtLoad.
- Logs to `~/Library/Logs/chatstrata/`.
- Linux (systemd) scheduling planned for a future release.

## 0.1.1 - 2026-06-01

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

# Changelog

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

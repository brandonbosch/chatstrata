# CLI reference

`chatstrata --help` lists commands; each command accepts `--help`.

| Command | Purpose |
|---|---|
| `ingest --auto` | Collect changed conversations from installed sources |
| `ingest SOURCE --path PATH` | Collect a particular source/location |
| `query SQL [--json]` | Read-only SQL against DuckDB |
| `search TERMS` | Keyword search across archived text |
| `stats` | Archive counts and source date ranges |
| `analyze activity / tools / models / projects / conversations` | Built-in analyses |
| `doctor` | Source-format, pending-collection, and archive checks |
| `paths` | Archive, log, and data-directory locations |
| `sources` | Built-in source adapters |
| `serve` | MCP server (stdio by default) |
| `mcp config CLIENT` | Print setup for codex, claude-code, or claude-desktop |
| `schedule install / uninstall / status` | Background collection service |
| `daemon` | Foreground collection and optional sync loop |
| `reindex` | Refresh the full-text index |
| `rebuild` | Recreate DuckDB from the observation log |
| `import-legacy [PATH]` | Import an old Python archive without Python |
| `pair --relay URL` | Configure sync and show a private pairing code |
| `join CODE` | Join another trusted device to the space |
| `sync` | Exchange encrypted observation-log segments |
| `devices` | Show device/sync state |
| `relay` | Run the optional segment relay |
| `version` | Installed build version |

Most archive commands accept `--db PATH`. `CHATSTRATA_GO_DB` changes the default
archive. `CHATSTRATA_DB` is used only to locate the old archive for legacy import.
There is no `init` step: ingestion creates the archive and log as needed.

Python-only `embed`, `redact`, and `label` commands are not available in v2.

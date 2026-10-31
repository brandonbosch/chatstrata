# Get started

## Install

```sh
curl -fsSL https://chatstrata.sprawlcommons.com/install.sh | sh
```

Linux x86-64/arm64 and Apple-silicon macOS are supported by downloadable
releases. Add `~/.local/bin` to PATH if prompted. The installer needs curl or
wget, tar, and sha256sum or shasum. No sudo, Python, or Go compiler is needed.

To pin a release, download the script first or pass the variable to `sh`:

```sh
curl -fsSL https://chatstrata.sprawlcommons.com/install.sh | CHATSTRATA_VERSION=v2.0.0 sh
```

Intel Macs and Windows build from source with Go and a C compiler:

```sh
go install github.com/brandonbosch/chatstrata/v2/cmd/chatstrata@latest
chatstrata reindex --install-fts
```

## Collect and connect

```sh
chatstrata ingest --auto
chatstrata stats
chatstrata search "migration"
chatstrata schedule install
chatstrata mcp config codex
```

Choose `claude-code` or `claude-desktop` instead of `codex` for those clients.
Follow the printed instructions, then reconnect/restart the client so it sees
the MCP tools. Ask the agent to call `get_schema` before writing queries.

## Upgrade

Re-run the installer, then `chatstrata schedule install` to restart the daemon
on the new binary. `chatstrata version` prints the installed version.

## Import an old Python archive

Before replacing the Python command, run `chatstrata schedule uninstall` to
remove its scheduled ingest. After installing Go:

```sh
chatstrata import-legacy
chatstrata ingest --auto
chatstrata schedule install
```

`import-legacy /path/to/old.duckdb` chooses another archive. The command reads
raw events using SQL and does not require Python. It leaves the old file intact.
Keep that file as a backup. Old labels and embeddings are not migrated; a
conversation without raw events cannot be recovered by legacy import.

## Check collection

`chatstrata doctor` reports source-format problems and local changes awaiting
collection. `chatstrata schedule status` shows the daemon and its log location.
Close direct DuckDB connections in idle analysis/notebook sessions: they block
collection even when opened read-only. Use MCP or `chatstrata query` instead.

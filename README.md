# chatstrata

<p align="center">
  <img src="docs/images/chatstrata.png" alt="chatstrata" width="360">
</p>

**Your AI conversations, archived locally and queryable by your agents.**

chatstrata collects conversations from Claude Code, Codex CLI, OpenCode,
Oh My Pi, Hermes Agent, and claude.ai exports into a local DuckDB archive.
Search across tools, run SQL, or let an MCP-connected agent analyze your
history. It is a single Go binary; no Python runtime or API key is required.

## Install

Linux (x86-64 and arm64) and macOS (Apple silicon):

```sh
curl -fsSL https://chatstrata.sprawlcommons.com/install.sh | sh
```

The installer downloads a [GitHub release](https://github.com/brandonbosch/chatstrata/releases),
checks its SHA-256 checksum, and installs to `~/.local/bin` without sudo.
Add that directory to your PATH if the installer asks you to.
Set `CHATSTRATA_VERSION=v2.0.0` to pin a release, or
`CHATSTRATA_INSTALL_DIR` to choose another installation directory.
Re-run the installer to upgrade; then restart an installed daemon with
`chatstrata schedule install`.

Intel Macs and Windows currently build from source with Go and a C compiler:

```sh
go install github.com/brandonbosch/chatstrata/v2/cmd/chatstrata@latest
```

Source builds use substring search until you explicitly install the FTS extension
with `chatstrata reindex --install-fts`. Downloadable releases bundle it.

## Collect, search, connect an agent

```sh
chatstrata ingest --auto           # collect installed sources once
chatstrata schedule install        # collect in the background every 5 minutes
chatstrata search "auth refactor"
chatstrata stats
chatstrata doctor
```

To connect an agent, print the setup for your client:

```sh
chatstrata mcp config claude-code   # prints a command to run
chatstrata mcp config codex         # prints TOML to add to ~/.codex/config.toml
chatstrata mcp config claude-desktop # prints JSON to merge into the desktop config
```

`mcp config` prints instructions; it does not edit another application's config.
The MCP server runs as `chatstrata serve` and exposes `get_schema`, `query`,
and the `chatstrata://schema` resource. Agents can use ordinary DuckDB SQL:

```sql
SELECT model, count(*) AS messages
FROM messages
WHERE model IS NOT NULL
GROUP BY model
ORDER BY messages DESC;
```

The CLI supports the same queries:

```sh
chatstrata query "SELECT source_id, count(*) FROM conversations GROUP BY source_id" --json
chatstrata analyze models
chatstrata analyze tools
chatstrata analyze activity --by month
```

Query access is read-only and restricted to the archive. External file reads,
network access, extension installation, database writes, and changes to query
settings are disabled. Results are limited to 500 rows and 512 KB, with a
30-second query timeout. Query calls close their database connection after
responding so background collection can continue.

## Sources

| Source | Command / input |
|---|---|
| Claude Code | `chatstrata ingest claude_code` |
| Codex CLI | `chatstrata ingest codex_cli` |
| OpenCode 1.x and 2.x | `chatstrata ingest opencode` |
| Oh My Pi | `chatstrata ingest omp` |
| Hermes Agent | `chatstrata ingest hermes_agent` |
| claude.ai export | `chatstrata ingest claude_export --path conversations.json` |

`ingest --auto` discovers installed sources. For a different location, use
`ingest SOURCE --path PATH`. Repeated collection skips unchanged content.
Generic ChatGPT web exports are not supported yet.

## Your data

The archive stays on your machine. Normal collection, search, SQL, and MCP make
no network requests. Optional [device sync](docs/rewrite/sync.md) sends encrypted
observation-log segments to a relay you configure; no hosted account is needed.

Local transcripts, the observation log, and DuckDB are **plaintext**. Automatic
PII sanitization, classifier/Jev labeling, embeddings, and semantic search are
not included in v2. Choose which archive you expose to an agent, whose provider
may receive the query results. The HTTP MCP transports are intended for local
use; use the default stdio transport for agent setup.

```sh
chatstrata paths                   # archive and observation-log locations
chatstrata rebuild                 # recreate DuckDB from the observation log
chatstrata schedule status
```

The database remains named `chatstrata-go.duckdb` for upgrade compatibility;
`CHATSTRATA_GO_DB` or `--db` overrides it. Back up the adjacent
`chatstrata-go.log/` directory: it is the source of truth. Rebuilds preserve
stored conversations even after the original tool deletes its transcripts.

If collection stalls, run `doctor` and check daemon logs. An idle notebook or
Python scratchpad holding DuckDB open can block collection even with a read-only
connection. Use MCP or the CLI for live archive analysis, or close the direct
connection promptly.

## Coming from Python

1. Before replacing the old command, remove its scheduled ingest with
   `chatstrata schedule uninstall`.
2. Run the installer. It replaces a Python-installed symlink without writing
   through it or deleting the Python environment.
3. Run `chatstrata import-legacy` to import your old archive's raw events into
   the Go observation log. You can also pass the old database path explicitly.
   The old archive is left intact; keep it as a backup.
4. Run `chatstrata ingest --auto` and `chatstrata schedule install`.
5. Update MCP clients to run the Go binary using `mcp config CLIENT`.

Legacy import works without Python. Labels and embeddings are not migrated;
archives without raw events cannot recover those conversations through import.
The old application is preserved at the `python-final` Git tag. Existing PyPI
releases remain available, but new application development and releases are Go-only.

## Development

```sh
go build -o bin/chatstrata ./cmd/chatstrata
go test ./...
go vet ./...
go test ./internal/project -run '^$' -bench . -benchmem
```

For a release-style build with full-text search:

```sh
go run ./internal/tools/fetchfts
go build -tags fts_embed -o bin/chatstrata ./cmd/chatstrata
scripts/smoke-release.sh bin/chatstrata
```

See [schema](docs/schema.md), [adapter guide](docs/adapter-guide.md),
[release instructions](docs/rewrite/release.md), and [regression fixtures](spec/README.md).

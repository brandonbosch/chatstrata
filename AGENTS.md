# Working in this repository

chatstrata is a Go application that archives AI conversations in DuckDB and
exposes read-only SQL to agents through MCP. The Python application is retired;
`python-final` preserves its final source. Read `docs/rewrite/plan.md` for the
history and `docs/rewrite/observation-log.md` for the storage rules.

## Layout

| Path | Purpose |
|---|---|
| `cmd/chatstrata/` | Binary entry point |
| `internal/cli/` | CLI, MCP, scheduling |
| `internal/sources/` | Source adapters; shared helpers in `srcutil` |
| `internal/store/migrations/` | All SQL migrations, embedded in the binary |
| `internal/analysis/queries/` | Embedded analysis SQL |
| `internal/obslog/`, `internal/project/` | Observation log and DuckDB projection |
| `internal/devsync/`, `internal/relay/` | Optional encrypted device sync |
| `spec/` | Golden-output and projection regression fixtures |
| `docs/adr/` | Architecture decisions (append-only) |
| `docs-site/` | Published Go application documentation |

## Build and verify

```sh
go build -o bin/chatstrata ./cmd/chatstrata
go test ./...
go vet ./...
gofmt -l .
go test ./internal/project -run '^$' -bench . -benchmem
```

DuckDB uses CGo: install Go and a C compiler, and build releases natively for
each OS/architecture. Release binaries embed DuckDB's signed full-text search
extension so search never needs a download:

```sh
go run ./internal/tools/fetchfts
go test -tags fts_embed ./...
go build -tags fts_embed -o bin/chatstrata ./cmd/chatstrata
scripts/smoke-release.sh bin/chatstrata
```

See `docs/rewrite/release.md` for release packaging and installer checks.
Zensical is optional documentation tooling; the application and its tests do
not need Python.

## Rules

- Keep PRs to one milestone step or focused fix. Go code lands on `main` in
  small pieces; there is no long-lived rewrite branch.
- Standard library first, non-public code under `internal/`, contextual errors,
  tests next to code. Commit summaries are imperative; explain why in the body.
- Golden JSON is generated, never hand-edited. Run
  `go test ./internal/golden -run TestGolden -update` only for an intentional
  behaviour change, and explain the fixture diff. Ordinary changes must pass
  `go test ./...` without changing expected output.
- Keep SQL table and column names compatible with `docs/schema.md` and saved
  agent queries. Schema/storage changes need the next ADR and a migration.
  Never renumber existing migrations. Do not change projection expectations
  without an ADR or an explicit explanation in the PR.
- Never commit real transcripts. Fixtures must be synthetic or sanitized.
- No network calls by default. Only explicitly configured sync and the user's
  `reindex --install-fts` request may access the network at runtime. Fetching
  release dependencies at build/install time is separate.
- Never log keys, `space.key`, or pairing codes. Only `pair` intentionally
  displays its pairing code.
- Use MCP or `chatstrata query` for analysis. Never leave a direct DuckDB
  connection to the live archive open in a Python/notebook scratchpad: even
  a read-only connection prevents collection. Close direct connections
  promptly in a `finally` block if they are unavoidable.

## Storage and adapters

`chatstrata-go.duckdb` (or `$CHATSTRATA_GO_DB`) is a rebuildable projection.
The adjacent `chatstrata-go.log/` is the source of truth. Never write archive
content except through `internal/project`, or `rebuild` will lose the change.
Keep this existing filename and environment variable compatible for upgrades.

Sync follows ADR 0005: each device writes its own log, IDs are deterministic
and include source identity/position rather than content alone, and merge
ordering never depends on wall clocks. Sync moves encrypted immutable segments.
The daemon closes DuckDB before waiting for the relay. Details and security
limits: `docs/rewrite/sync.md`.

Adapters implement `model.Source` and register in `internal/cli`. Each needs a
golden case and legacy `raw_events` import coverage. Sources without one file
per conversation set `Handle.Content` in `Discover`; the collector logs those
bytes. `model.Checker` lets `doctor` detect source-format changes.

MCP and `query` share validation and response limits in `internal/cli/read.go`.
Read connections load the bundled search extension before disabling external
file/network access and locking DuckDB settings. Test this boundary through
real SQL, not just keyword validation.

The FTS index rebuilds in batches (`store.FTSNeedsRebuild`). `search` must cover
both indexed blocks and new blocks awaiting indexing. `analyze` uses the SQL
under `internal/analysis/queries/`.

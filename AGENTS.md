# Working in this repository

chatstrata is a personal, queryable archive of AI conversations (Claude Code,
Codex, OpenCode, claude.ai exports and others) stored in DuckDB, with a CLI and
a read-only MCP server.

**The project is moving from Python to Go.** Read
[docs/rewrite/plan.md](docs/rewrite/plan.md) before making structural changes.
Until the cutover (milestone M5), both implementations live in this repo.

## Layout

| Path | What it is | Status |
|---|---|---|
| `chatstrata/`, `tests/`, `pyproject.toml` | Python app (published on PyPI as `chatstrata`) | **Feature-frozen** |
| `cmd/`, `internal/`, `go.mod` | Go app (added in M1) | Active development |
| `spec/` | Behaviour contract shared by both implementations | Change deliberately |
| `scripts/golden.py`, `scripts/bench.py` | Golden-output and benchmark tooling (Python) | Active |
| `docs/adr/` | Architecture decision records | Append-only |
| `docs/rewrite/` | Rewrite plan and Python baseline numbers | Active |
| `docs-site/` | Published documentation site | Python-era |

## Rules

**Python is feature-frozen.** Only bug fixes and performance work go into
`chatstrata/`. New features, commands and adapters go into the Go app only. If a
request asks for a new feature in Python, say so and confirm before doing it.

**`spec/` is the contract between the two implementations.**
- `spec/golden/expected/*.json` is generated, never hand-edited. If a Python
  change alters ingest output on purpose, run `python scripts/golden.py generate`
  and explain the diff in the PR. If it changes unexpectedly, fix the code.
- The Go implementation must reproduce `spec/golden/expected/` for every case in
  `spec/golden/cases.json`, comparing JSON values rather than text.
- `spec/projection/scenarios.json` is a proposal. Don't change expected results
  there without an ADR or a note in the PR explaining the new rule.

**Schema compatibility.** Users and agents write SQL against the existing tables
(`docs/schema.md`), and `chatstrata/analysis/queries/*.sql` is reused unchanged
by Go. Keep table and column names compatible. Schema or storage changes need an
ADR (`docs/adr/`, next number) and a migration.

**Privacy.** The archive holds people's private conversations.
- Never commit real transcripts. Fixtures are synthetic or sanitized.
- No network calls by default (a promise in the README). The exceptions are
  explicitly opt-in: `chatstrata label`, the DuckDB `vss` install behind
  `CHATSTRATA_INSTALL_DUCKDB_VSS=1`, and (in Go) sync to a relay the user
  configured.
- Never print keys or space secrets in logs or error messages.

**Sync design** follows [ADR 0005](docs/adr/0005-sync-device-logs.md): the
observation log is the source of truth, DuckDB is a rebuildable projection, each
device writes only its own log, IDs are deterministic (never random, never from
content alone), and ordering never depends on wall clocks.

## Commands

Python:

```sh
uv venv && uv pip install -e ".[dev,mcp]"
.venv/bin/python -m pytest              # includes tests/test_golden.py
.venv/bin/ruff check .
python scripts/golden.py check          # Python still matches spec/golden/expected
python scripts/bench.py --synthetic 100 --turns 20
```

Only run `ruff format` on files you change; much of the existing code isn't
formatted and reformatting it buries real changes in the diff.

Go (from M1):

```sh
go build ./cmd/chatstrata
go test ./...
go vet ./...
```

DuckDB makes the Go build use CGo; build release binaries natively per OS and
architecture rather than cross-compiling.

## Conventions

- Keep PRs to one milestone step. Go code lands on `main` in small pieces; there
  is no long-lived rewrite branch.
- Go: standard library first, `internal/` for everything not meant as a public
  API, errors wrapped with context, tests next to the code. Golden tests read
  `spec/` directly.
- Commit messages: imperative summary line, body explaining why.

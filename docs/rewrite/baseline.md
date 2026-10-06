# Python baseline (M0)

Numbers the Go rewrite has to beat. Produced with `scripts/bench.py`, which
runs the `chatstrata` CLI as a fresh process per measurement, so interpreter
start-up and imports are included, as they are for users and agents.

## Synthetic corpus, cloud container

**Not representative of a laptop.** This is a 4-vCPU x86_64 Linux container. It
is useful as a reproducible reference point, not as the target. Run the
benchmark on the real machines (below) before setting targets.

- chatstrata 0.4.0, Python 3.11.15, DuckDB 1.5.6, 2026-10-05
- Corpus: `bench.py --synthetic 100 --turns 20`. 100 Claude Code sessions,
  8,000 events, 8.2 MB of JSONL, giving a 42.7 MB database.
- Installed footprint: the venv with `[dev,mcp]` extras is 150 MB. The
  `[embeddings]` extra adds torch and is far larger.

| measurement | first (s) | median (s) | runs | peak RSS (MiB) |
|---|---:|---:|---:|---:|
| startup (`--help`) | 0.27 | 0.27 | 5 | 75 |
| full ingest | 94.7 | — | 1 | 174 |
| incremental, nothing changed | 0.37 | 0.37 | 5 | 91 |
| incremental, 5 files appended | 0.93 | — | 1 | 169 |
| reindex (FTS) | 1.89 | — | 1 | 160 |
| search | 0.34 | 0.36 | 5 | 135 |
| stats | 0.33 | 0.33 | 5 | 96 |
| query (tool counts) | 0.35 | 0.33 | 5 | 97 |

## Go (M1), same corpus and container

`bench.py --synthetic 100 --turns 20 --binary bin/chatstrata`, the same
generated corpus (fixed seed) on the same container. Go 1.24, DuckDB 1.5.6,
binary built with `-ldflags "-s -w"` (60 MB, nearly all of it DuckDB).

| measurement | Python median (s) | Go median (s) | speed-up | Go peak RSS (MiB) |
|---|---:|---:|---:|---:|
| startup (`--help`) | 0.27 | 0.008 | 33x | 50 |
| full ingest | 94.7 | 3.48 | 27x | 200 |
| incremental, nothing changed | 0.37 | 0.088 | 4x | 51 |
| incremental, 5 files appended | 0.93 | 0.70 | 1.3x | 142 |
| reindex (FTS) | 1.89 | 1.48 | 1.3x | 124 |
| search | 0.36 | 0.13 | 2.8x | 89 |
| stats | 0.33 | 0.077 | 4x | 51 |
| query (tool counts) | 0.33 | 0.11 | 3x | 52 |

- Commands that read the archive are now dominated by opening the DuckDB file
  (about 70 ms here), not by the process.
- Full ingest parses files in parallel and bulk-loads rows with DuckDB
  appenders.
- Re-ingesting a changed conversation costs 50 to 110 ms, almost all of it
  commits: DuckDB checks foreign keys against committed data, so deleting a
  conversation's old rows takes one commit per table. M2 replaces this write
  path with the observation log and projection, so it isn't worth optimizing
  now.
- Go `search` uses the FTS index after `reindex --install-fts`; without the
  extension it falls back to substring search, like Python.

M2 (ingest goes through the observation log, then the projection) on the same
corpus: full ingest 4.8 s, incremental no-op 0.10 s, 5 appended files 0.86 s;
read commands unchanged. The log for the 8.2 MB corpus is 1.8 MB (zstd). The
synthetic text compresses unusually well, so real transcripts will compress
less.

## What this shows

**Every command pays about 0.3 s before doing any work.** `--help` alone takes
0.27 s, and `search`, `stats` and `query` take only slightly longer, so for
everyday commands the cost is almost all interpreter start-up and imports. That
cost is paid on every CLI call an agent makes. A Go binary should start in
milliseconds.

**Full ingest is slow because of per-row inserts, not parsing.** 8.2 MB took
95 s (about 12 ms per event). Profiling a smaller run (15 sessions) showed
about 70% of ingest time inside about 4,300 individual `duckdb.execute()`
calls, at roughly 4 ms each. Those calls triggered about 58,000 Python import
lookups (`importlib` `find_spec`), which looks like per-call overhead in
DuckDB's Python binding. JSON parsing barely shows up. This matches the earlier
hypothesis that bulk insertion is the main opportunity, and it means:

- Go should be much faster at ingest, but mostly because it would batch, not
  because of the language. A Python change to batched inserts would likely
  close most of the gap, and is worth doing anyway while Python remains the
  daily driver.
- Benchmark the Go ingester against batched Python as well, so the comparison
  is honest (as the original plan said).

**Incremental runs are cheap.** The no-op incremental ingest is start-up plus
mtime checks, so the scheduled ingest's steady-state cost is mostly start-up.

## Running it on the real machines

```sh
python scripts/bench.py --source claude_code --output bench-$(hostname)-claude_code.json
python scripts/bench.py --source codex_cli   --output bench-$(hostname)-codex_cli.json
python scripts/bench.py --synthetic 100 --turns 20 --output bench-$(hostname)-synthetic.json
```

The script copies the transcripts to a temporary directory and never modifies
the originals or your archive. A real `~/.claude/projects` may take a long time
to ingest fully at the current speed; `--repeat 3` shortens the repeated
measurements.

## Targets to set after the real runs

Proposed starting points, to confirm against the laptop numbers:

- CLI start-up and `stats`/`query`/`search` on a typical archive: under 50 ms
  of overhead beyond DuckDB's own query time.
- Full ingest: at least 10x faster than the current Python, and faster than a
  batched-insert Python.
- Incremental no-op: under 100 ms.
- Daemon idle: negligible CPU, under 50 MiB RSS.
- Install: one binary, no Python, under 100 MB including DuckDB.

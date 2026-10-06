# chatstrata behaviour spec

Language-neutral test data for the v2 rewrite (see
[docs/rewrite/plan.md](../docs/rewrite/plan.md), milestone M0). The Python app
generates and checks it today; the Go implementation is tested against the same
files.

## `golden/`: what ingestion must produce

- `cases.json`: each case names a source, an input directory and one or more
  ingest steps.
- `inputs/<source>/`: source data laid out the way the real tool writes it
  (`projects/<encoded-cwd>/<session>.jsonl` for Claude Code,
  `sessions/YYYY/MM/DD/rollout-*.jsonl` for Codex, and so on). SQLite-backed
  sources (OpenCode, Hermes) are stored as SQL dumps; build each `*.sql` into a
  `*.db` next to it before ingesting.
- `expected/<case>.json`: the resulting archive in canonical form.

Running a case:

1. Copy `inputs/<input>/` to a fresh directory (`$INPUT`) and build any SQL dumps.
2. For each step, in order: truncate any file listed in `truncate_lines` to that
   many lines (restore the full file in steps that don't list it) and bump its
   mtime when its content changes; then run
   `chatstrata ingest <source> --path $INPUT/<path> --db <db>`, adding
   `--incremental` when the step says so. All steps use the same database.
3. Dump the database in canonical form and compare with `expected/<case>.json`.

Three cases exist to pin behaviour rather than parsing:
`claude_code_reingest` (re-ingesting changes nothing) and
`claude_code_appended` (a transcript that grows between incremental ingests ends
up the same as one full ingest) both expect output identical to `claude_code`.

### Canonical form

- Conversations sorted by `(source_id, source_native_id)`; messages by
  `sequence_index`; blocks by `block_index`; raw events by `line_number`.
- Internal ids are omitted. A message's parent is given as
  `parent_source_native_id`.
- Omitted as implementation details: `ingested_at`, `content_hash`,
  `source_file_mtime`, the `sources` table, labels, embeddings.
- Timestamps are RFC 3339 in UTC with trailing fractional zeros trimmed (Go's
  `time.RFC3339Nano`).
- JSON columns (`metadata`, `payload`) are parsed values, not strings. Compare
  them as JSON values, not text: number formatting may differ between
  languages.
- The input directory is written as `$INPUT` and the home directory as `$HOME`
  wherever they appear in a string value.

### Commands

```sh
python scripts/golden.py check      # Python still matches expected/ (also run by tests/test_golden.py)
python scripts/golden.py generate   # after an intentional change; review the diff
python scripts/golden.py dump archive.duckdb -o out.json   # canonical dump of any archive
```

`dump` is the tool for comparing implementations on a real archive: ingest the
same transcripts with Python and Go into two databases, dump both and diff.

## `projection/`: how synced observations merge

`scenarios.json` describes the merge rules from ADR 0005 as cases: a set of
events, the observations different devices made of them, and the archive every
device must end up with. Nothing executes these yet; M2 (the observation log and
projector) implements a runner. Status is **proposed**; `divergent_snapshots`
records its decision in a `decided` field.

Conventions:

- `lines` lists event ids; the runner serializes those events as JSONL to build
  the observation's bytes. `offset_lines` is the line offset of an `append`;
  the runner converts it to a byte offset.
- `check_all_delivery_orders: true` means every permutation of the
  observations must produce the same result.
- `expected.conversations[].messages` lists message native ids in projected
  order. `hidden` lists conversations that exist only as tombstones.
- `expected.divergences` lists conversations flagged as diverged: the last
  shared message (`fork_after`) and, for each branch's first message, the
  devices that observed it.

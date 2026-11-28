# ADR 0007: Classifier label durability, incremental runs, and Go packs

**Status:** Accepted
**Date:** 2026-11-25
**Extends:** [ADR 0004](0004-classifier-labels.md), [ADR 0005](0005-sync-device-logs.md), [ADR 0006](0006-go-only-cutover.md), [ADR 0008](0008-local-redaction-boundary.md)

## Context

`chatstrata` v2 separates durable truth (`chatstrata-go.log/`, append-only observation
segments) from the queryable DuckDB projection (`chatstrata-go.duckdb`), which
`chatstrata rebuild` deletes and recreates from the log at any time.

Schema v4 (`internal/store/migrations/0004_labels.sql`) already provides `labels`
and `label_runs` in DuckDB, and agents already query the archive through read-only
MCP SQL (`query` + `chatstrata://schema`). Bringing TypeSafe Jev labeling back in
Go needs to satisfy four operational constraints:

1. **Queryability:** Agents and `chatstrata query` must filter and join labels
   directly in DuckDB alongside `conversations`, `messages`, and `content_blocks`
   with zero schema break to v4 tables.
2. **Durability across `rebuild`:** Deleting `chatstrata-go.duckdb` and running
   `chatstrata rebuild` must not lose paid classifier outputs.
3. **Incremental growth without full regeneration:** The archive grows daily.
   Labeling must run only on newly arrived or materially changed targets, and be
   invocable on demand or periodically without re-labeling historical targets.
4. **Privacy / PII gate:** Labeling is the only feature that sends transcript
   excerpts off the machine (to TypeSafe's System One API). Because even personal
   archives contain secrets, credentials, and sensitive paths/work, outbound state
   must pass through a local redaction / exclusion boundary before transmission
   (implemented prior to enabling live labeling).

### Options considered for label durability

- **DuckDB-only (`labels` written only to `chatstrata-go.duckdb`):** Simplest,
  but violates ADR 0005's core invariant ("DuckDB is disposable"). Any parser
  upgrade or `chatstrata rebuild` wipes all labels and forces re-paying API costs.
- **Mix labels into the raw transcript observation log:** Couples derived,
  model-versioned ML outputs to raw source-byte observations and sync replay
  semantics. Re-running a pack or changing a prompt shouldn't bloat or complicate
  the transcript log.
- **Append-only local label log projected into DuckDB (chosen):** Keep an
  append-only sidecar log next to the observation log (`chatstrata-go.labels/`,
  one immutable `.jsonl.zst` file per `label_run`), and project it into DuckDB's
  existing `labels` and `label_runs` tables on write and during `chatstrata rebuild`.
  DuckDB remains the fast query layer; the sidecar is the durable source of truth
  for labels. Multi-device sync of label runs can optionally layer on the exact
  same segment-upload transport later without changing the format.

## Decision

### 1. Durable storage and DuckDB projection

- Keep `labels` and `label_runs` in DuckDB (`0004_labels.sql`) as the query
  surface for CLI, `analyze`, and MCP `query`. No breaking changes to existing
  columns or primary key `(pack, target_id, question)`.
- Persist every completed batch of label answers in an append-only sidecar
  directory adjacent to the archive:

  ```text
  chatstrata-go.duckdb           queryable projection (includes labels + label_runs)
  chatstrata-go.log/             source of truth for raw transcript observations
  chatstrata-go.labels/          source of truth for classifier runs
    runs/<run-id>.jsonl.zst      immutable run header + answer records
  ```

- Each run file starts with one `run` metadata record (matching `label_runs`,
  plus `pack_name`, `pack_version`, `model`, `redaction_version`) followed by one
  `answer` record per labeled target (carrying `target_kind`, `target_id`,
  `target_fingerprint`, and all question answers from that Jev call).
- Writing a label batch writes to the run file first (atomic temp-file + sync +
  rename on run close, or crash-safe appendable staging file promoted on finish),
  then upserts into DuckDB.
- `chatstrata rebuild` replays `chatstrata-go.log/` first, then replays
  `chatstrata-go.labels/runs/*.jsonl.zst` in run timestamp/ID order, keeping only
  rows whose `target_id` still exists in the rebuilt projection. Latest
  `(pack, target_id, question)` wins.
- **Target stability:** Because Go v2 IDs are deterministic hashes of
  `(space_id, source, scope, locator)` and `(conversation_id, source_native_id)`
  (ADR 0005), `target_id` remains stable across rebuilds.

### 2. Incremental labeling and freshness

Labeling never regenerates already-current targets unless `--force` is passed:

- **Immutable targets (`message`, `tool_call`):** Skip any `target_id` that
  already has answers for `(pack, pack_version)`.
- **Growing targets (`conversation`):** Conversations can receive new messages
  while active. Track a target freshness fingerprint — specifically
  `conversations.content_hash` (or `(message_count, ended_at)`) — alongside the
  target record, plus a configurable settle window (default `--min-age 30m` on
  `ended_at`) so live sessions are not labeled mid-flight. A conversation is
  skipped if it already has `(pack, pack_version)` at the same
  `target_fingerprint`, and automatically re-labeled on the next incremental run
  only if new turns were appended after it was last labeled.
- **Schema evolution (migration v7):** Add nullable
  `target_fingerprint VARCHAR` to `labels` (and `redaction_version VARCHAR` to
  `label_runs`) in a forward-only `0007_label_freshness.sql` migration
  (migration v6 was taken by ADR 0008's `0006_redactions.sql`). Existing
  SQL queries against `labels` remain 100% compatible.
- **Periodic execution:** Support an explicit one-shot `chatstrata label <pack>`
  (and `--all` / `--since` / `--project` / `--exclude-project` / `--limit`) that
  runs incrementally. Optional periodic scheduling (e.g. an opt-in daemon flag or
  systemd/launchd timer separate from collection) runs only explicitly enabled
  packs with a hard per-run token/cost budget (`--max-cost-usd`), never enabled
  by default.

### 3. Native Go Jev client and declarative TOML packs

- Implement `internal/label/` in pure Go using `net/http` directly against
  TypeSafe's `POST /v1/systemone` endpoint (no third-party SDK dependency).
- **Model pinning:** Record the exact versioned model ID returned by the API
  (e.g. `jev-1.14.0`). Default to an explicitly pinned model version in config
  (overridable via `--model`) so calibrated probability thresholds do not drift
  silently across `jev-latest` releases.
- **Declarative TOML packs:** Embed built-in packs under
  `internal/label/packs/*.toml` and discover user packs from
  `$CHATSTRATA_PACKS_DIR` (default `<config>/chatstrata/packs`). Each pack
  declares:
  - `name`, `description`, `target_kind` (`conversation | message | tool_call`),
    `state_version`, optional `depends_on` (for Noul-screened stage-2 packs)
  - `target_sql` returning at least `target_id`, `source_id`, `project`,
    `created_at`, `target_fingerprint`, plus columns referenced by `[state]`
  - `[state]` column transforms (`clip`, `json`, `unwrap`, `default`)
  - `[questions.<id>]` using TypeSafe's `noul`, `choice`, and `score` primitives
- `pack_version` is the SHA-256 prefix of `(questions, state_spec, state_version)`
  so editing criteria or state shaping automatically marks prior rows stale for
  that pack only.
- **Stage-2 screening (`depends_on`):** Packs like `benchmark-candidacy` can
  reference earlier pack labels in their `target_sql` (joining `labels` where
  e.g. `session-triage.is_real_work_session > 0.65`) so expensive/richer states
  only run on screened candidates (TypeSafe's "Screen with a Noul first" pattern).

### 4. Privacy and PII redaction prerequisite

- No transcript text is sent to TypeSafe without passing through a local
  pre-flight sanitization pipeline (prerequisite tracked separately):
  1. Path / project allowlist or denylist (`--project`, `--exclude-project`, and
     persistent `exclude_projects` config).
  2. Deterministic secret & PII scrubber (API keys, tokens, private keys,
     emails, IP/hostnames, local username/home-path normalization) applied to
     rendered `state` values immediately before serialization.
  3. Dry-run cost & target preview (`--dry-run` default or interactive confirm
     unless `--yes` / scheduled opt-in) showing exact target count, estimated
     input tokens, estimated USD cost, and sample scrubbed state.

### 5. Initial bundled pack set

Four packs cover benchmark curation (`workeval`), archive analysis, and future
open-weight finetuning dataset curation using one shared label vocabulary:

1. `session-triage` (`conversation`): filters out non-work sessions (reviewer
   copies, subagent duplicates, aborted chats), classifies `primary_activity`
   (including `cli_work` and `planning`) and `primary_domain`, and scores
   completion, complexity, verifiability, and multi-task structure.
2. `user-turn-boundaries` (`message`): separates real human prompts from
   injected harness wrappers (`is_human_authored`), classifies turn intent,
   scores prompt self-containment (`spec_self_contained`), and measures
   user corrections/frustration (both for benchmark extraction and DPO pairs).
3. `benchmark-candidacy` (`conversation`, screened by `session-triage` +
   `user-turn-boundaries`): evaluates replayability against `workeval` rules
   (task archetype, prompt leakage, external dependency risk, clean-start
   likelihood, difficulty).
4. `tool-failures` (`tool_call`): ports v1's tool-outcome taxonomy
   (`failure_mode`, `agent_caused`, `stale_view`) with recovery tracking.

## Consequences

- **Agents keep one query surface:** MCP `query` and `chatstrata query` join
  `labels` directly with `conversations`, `messages`, `content_blocks`, and
  `tool_calls` at DuckDB speed.
- **`rebuild` is lossless for labels:** Parser fixes or schema rebuilds replay
  both raw logs and label logs without re-calling TypeSafe.
- **Predictable incremental cost:** Running `chatstrata label` weekly or after
  work sessions labels only new/grown conversations and new turns; historical
  rows are untouched unless a pack definition changes.
- **No sync coupling in v2.1:** Labels start local-only in `chatstrata-go.labels/`.
  Because each run is already an immutable `.jsonl.zst` file keyed by deterministic
  `target_id`, syncing label runs across devices over the existing age-encrypted
  relay can be added later as an orthogonal step.
- **Gated on PII redaction:** Implementation of the network client and live runs
  waits until the local PII/secret scrubber is in place and verified on real
  state samples.

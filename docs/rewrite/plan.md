# chatstrata v2: Go rewrite with multi-device sync

**Status:** Draft
**Date:** 2026-10
**Supersedes:** the earlier "language, sync, and rewrite decision plan" draft

## Why

chatstrata has been in daily use for months. The things that hurt now are:

1. **Installation.** Colleagues who don't have Python, or don't know how to set
   up a venv and extras, don't install it.
2. **Speed.** CLI cold start (Python plus duckdb, pydantic and click imports) is
   paid on every command and every agent call. Ingestion inserts rows one at a
   time.
3. **One archive per machine.** I use four computers. Each has its own archive.

This rewrite fixes all three by moving to a single Go binary and adding sync
between my own machines. Nothing else is in scope.

## Goals

- Feature parity with the Python app for daily use: ingest, search, query,
  stats, analyze, MCP server, scheduling. See [Scope](#scope).
- One static-ish binary per platform (linux/amd64, darwin/arm64), no Python.
- Sync across my personal machines over my tailnet. Each machine collects
  offline and catches up later, without other machines needing to be online.
- Relay sees only ciphertext.
- The existing SQL schema stays compatible, so saved queries, `analysis/queries/*.sql`
  and agents' MCP habits keep working.

## Non-goals (for v2.0)

- Embeddings and semantic search.
- PII redaction (Presidio/spaCy).
- Classifier labels (TypeSafe/Jev).
- Python entry-point adapters from third parties.
- Team / shared spaces. The design keeps the *space* concept so this stays
  possible later, but nothing is built for it.
- Work machine sync. Pending a check on whether work transcripts may leave
  that machine. If it ever joins, it joins as a separate space.
- Full local encryption of the archive.
- Public relay endpoint. Tailnet only for now.

The Python app stays installed and remains the daily driver until v2 is clearly
better. Nothing in this plan requires turning it off early.

## Scope

| Feature (Python today) | v2.0 | Notes |
|---|---|---|
| `ingest` (incl. `--incremental`, `--auto`) | Yes | Becomes a continuous collector in the daemon; one-shot command kept |
| Adapters: claude_code, codex_cli, opencode, omp, hermes_agent, claude_export | Yes | claude_code first; the rest in M4 |
| `query`, `search` (FTS), `stats`, `doctor`, `paths`, `sources` | Yes | |
| `analyze` | Yes | Reuses `chatstrata/analysis/queries/*.sql` unchanged |
| `mcp` server (`query`, `get_schema`) + `mcp config` | Yes | Go MCP SDK; same tool names and read-only SQL rules (`mcp/safety.py`) |
| `schedule` (launchd/systemd) | Yes | Installs the daemon instead of a periodic ingest |
| `init`, `migrate`, `reindex`, `compact` | Reworked | DuckDB becomes a rebuildable projection, see below |
| `embed`, semantic search | No | Possible later via an optional worker |
| `redact` | No | |
| `label` | No | |
| Third-party adapter entry points | No | Deprecated |
| — | **New** | `sync`, `pair`, `devices`, `rebuild`, `import-legacy`, `relay` |

## Architecture

```
           ┌─────────────────────────── each machine ────────────────────────────┐
           │                                                                      │
 transcripts ─► collector ─► observation log ─► projector ─► archive.duckdb ◄─ CLI / MCP
 (~/.claude,      (adapters)   (append-only,      (normalize,    (disposable,
  ~/.codex, …)                  local segments)    dedupe)        same schema)
           │                        ▲   │                                         │
           │                        │   ▼                                         │
           │                      sync client                                     │
           └────────────────────────│───│─────────────────────────────────────────┘
                                    │   │  HTTPS over tailnet (WireGuard)
                                    │   ▼
                       ┌──────── relay (tsnet node) ────────┐
                       │ spaces/<space>/devices/<dev>/*.seg │  ciphertext only
                       └────────────────────────────────────┘
```

Three ideas carry the design; [ADR 0005](../adr/0005-sync-device-logs.md) has
the reasoning.

1. **The observation log is the source of truth, not DuckDB.** The collector
   records what it saw in source files (original bytes, where they came from,
   which device saw them) as immutable observations. DuckDB is a projection
   that can be deleted and rebuilt from the log at any time.
2. **Each device appends only to its own log.** Sync is "upload my new
   segments, download everyone else's". The relay never merges or resolves
   anything, so there are no write conflicts at the transport level.
3. **Merging is a deterministic projection step.** Given the same set of
   observations, every device builds the same archive, regardless of arrival
   order.

### Local process ownership

DuckDB allows one read-write process, *or* many read-only processes, not both.
v2 runs a single long-lived daemon (`chatstrata daemon`) that owns the
read-write connection and does collection, sync and projection. The CLI and
MCP server talk to the daemon over a local Unix socket when it is running, and
open the database directly when it is not. Decide in M1 whether the MCP server
runs inside the daemon process (simplest) or as a thin stdio proxy to it.

## Data model

### Observations

An observation says "device D saw these bytes from this source location".

| field | meaning |
|---|---|
| `space_id` | which archive this belongs to |
| `device_id` | the device that observed it |
| `device_seq` | strictly increasing per device; used for ordering and catch-up, never wall clock |
| `source` | adapter name, e.g. `claude_code` |
| `scope` | account / install scope where the adapter knows it (e.g. claude.ai org), else empty |
| `locator` | source-native conversation identity (session id, export conversation id) |
| `kind` | `append` (byte range of an append-only file), `snapshot` (whole file), `tombstone` (archive deletion) |
| `offset`, `length` | for `append` |
| `content` | original source bytes, zstd-compressed |
| `observed_at` | device clock, informational only |
| `format_version` | observation format version |

Claude Code and Codex transcripts are append-only JSONL, so most observations
are small `append` records. If a file shrinks or its prefix changes (e.g. a
rewrite after compaction), the collector emits a `snapshot` instead.

### Segments

Observations are batched into segments: `zstd(observations) → age-encrypt →
<device_seq_start>.seg`. Segments are immutable and named by sequence so a
device can ask "give me everything from device X after seq N".

### Identity

Python today assigns random UUIDs (`core/ingest.py:_uuid`). That cannot work
across devices: two machines ingesting the same session would create two
conversations. v2 IDs are deterministic:

- conversation id = hash(`space_id`, `source`, `scope`, `locator`)
- message id = hash(conversation id, source-native message id) where one
  exists; otherwise an adapter-defined fallback that includes position, never
  content alone (identical repeated messages are legitimate)
- device provenance is stored separately (`observations` table in the
  projection), not mixed into ids

### Projection rules

These need fixtures before code. Minimum cases:

- The same session observed by two devices → one conversation; overlapping
  byte ranges deduplicated by (locator, offset).
- A stale or truncated snapshot arriving after a fuller one → the fuller
  history wins; nothing is deleted by a shorter observation.
- Divergent snapshots for the same locator (two devices saw different
  rewrites) → keep both; project the longest consistent history and record the
  divergence.
- Tombstone → hide from projection; later observations of the same locator
  stay hidden unless explicitly undeleted.
- Parser upgrade → `chatstrata rebuild` reprojects everything from the log.

## Sync

### Transport: a tiny Go relay on the tailnet

The relay is part of the same binary (`chatstrata relay`) and embeds a
Tailscale node with [`tsnet`](https://pkg.go.dev/tailscale.com/tsnet), so it
joins the tailnet as its own machine (e.g. `chatstrata-relay`) with a stable
MagicDNS name and a TLS certificate from Tailscale. No public domain, no open
ports, no NAT traversal on my side: every client makes outbound connections
over WireGuard.

**Tailscale Services** (stable service names/VIPs that hosts advertise) could
give the relay an address that doesn't depend on which machine hosts it. Worth
trying once the basic relay works; a tsnet node name gives most of the same
benefit with less setup. Verify current feature status and ACL syntax at
implementation time.

The relay should run on something that is usually on: the Linux desktop if it
stays up, otherwise a Raspberry Pi or small VPS on the tailnet. If the relay is
down, devices keep collecting and catch up later.

API (all paths scoped to one space):

```
PUT  /v1/spaces/{space}/devices/{device}/segments/{seq}   upload (idempotent; rejects changed content)
GET  /v1/spaces/{space}/devices                           list devices and their latest seq
GET  /v1/spaces/{space}/devices/{device}/segments?after=N list segment names
GET  /v1/spaces/{space}/devices/{device}/segments/{seq}   download
```

Storage is plain files on the relay's disk. Backups are a copy of that
directory.

Authorization: the relay uses tsnet's `WhoIs` to identify the calling tailnet
node and user, and checks it against an allowlist per space. A device may
write only to its own device folder. Tailscale ACLs restrict who can reach the
relay at all.

The client talks to a `Transport` interface (`Put`, `List`, `Get`). The relay is
the first implementation. An S3-compatible bucket or a public HTTPS relay can be
added later without changing the log format.

### Encryption

- **In transit:** WireGuard (tailnet) plus TLS.
- **At rest on the relay:** each segment is encrypted with
  [age](https://github.com/FiloSottile/age) using a per-space key before upload.
  The relay never has the key. This costs very little now and means the relay's
  disk can later move to a VPS or bucket without changing the threat model.
- **Locally:** plaintext, as today. Not changing in v2.0.

### Devices and keys

- `chatstrata init` on the first machine creates a space and its key, stored
  in the macOS Keychain or the Linux secret service, with a file fallback
  (mode 0600).
- `chatstrata pair` prints a one-time code containing the space id and key.
  `chatstrata join <code>` on another machine stores them and registers the
  device with the relay.
- `chatstrata devices` lists devices, their last seq and last sync time.
- Removing a device: remove it from the relay allowlist and rotate the space
  key (new segments use the new key). Data it already downloaded can't be
  recalled.

## Migrating the existing archive

Months of history exist only in today's DuckDB files: Claude Code deletes
transcripts after 30 days by default. `chatstrata import-legacy <path>` reads a
Python-era database and emits observations from its `raw_events` table, marked
`legacy`. Those are re-serialized JSON rather than original bytes (see ADR
0002), which is acceptable as long as it is marked. Run it once per machine.
Overlap with live transcripts is deduplicated by the projection rules.

Python-era message and conversation UUIDs are not preserved. Nothing in v2.0
references them (labels and embeddings are out of scope). Keep the old
database files as backups.

## Milestones

Each milestone ends with something I can use. A milestone that fails its
acceptance check gets fixed or redesigned before the next one starts.

**M0 · Contract (Python, ~1 week)**: done except real-machine timings. See
[`spec/`](../../spec/README.md) and [baseline.md](baseline.md).
- Golden fixtures: run the Python ingester over the existing adapter fixtures
  and dump the projection (conversations, messages, content_blocks, tool
  calls) as canonical JSON. Go must reproduce these.
- Baseline timings on one real archive: `--help` cold start, full ingest,
  incremental no-op ingest, a search, a `stats`.
- Write the projection-rule fixtures listed above as test cases.

**M1 · Local Go core**: done. Golden cases for claude_code pass, and the
numbers are in [baseline.md](baseline.md). Decisions made along the way:
the Go archive defaults to `chatstrata-go.duckdb` next to the Python one
(override with `CHATSTRATA_GO_DB`) so the two never mix during parallel use;
DuckDB's FTS extension is not bundled with the Go driver, so
`reindex --install-fts` downloads it once on request; ids are deterministic.
- Go module at the repo root (`cmd/chatstrata`, `internal/`). CLI with `ingest` (claude_code only),
  `query`, `search`, `stats`, `doctor`. DuckDB via `duckdb-go`, schema from
  the existing migrations.
- Acceptance: matches the M0 golden output for claude_code; faster than the
  M0 baseline on the same archive; builds on Linux and macOS.

**M2 · Observation log and projection**
- Collector writes observations and segments; projector builds DuckDB from
  them. `rebuild` and `import-legacy`.
- Acceptance: deleting `archive.duckdb` and running `rebuild` gives an
  identical result. Killing the process between log write and projection
  update loses nothing.

**M3 · Relay and sync**
- `chatstrata relay` (tsnet), `daemon`, `pair`, `join`, `devices`, `sync`.
- Acceptance on two real machines: both ingest different sessions offline,
  reconnect, and end up with the same `search` and `stats` output, no
  duplicates. Then: interrupt an upload mid-way, restart, resend; deliver a
  stale snapshot; restore a third empty machine from the relay with the other
  two offline.

**M4 · Parity**
- Remaining adapters, `analyze`, MCP server, `schedule` (launchd/systemd
  installing the daemon), `paths`, `sources`, `mcp config`.
- Acceptance: golden fixtures pass for every adapter; MCP works from Claude
  Code and Codex with the same queries agents use today.

**M5 · Cutover**
- Release builds per platform (CGo, so build on native runners per OS/arch),
  install instructions without Python.
- chatstrata is on PyPI, so `pip install chatstrata` users need a path over.
  Either publish a final Python release whose README and CLI point to the Go
  binary, or keep publishing to PyPI as platform wheels that contain the Go
  binary (the way ruff and uv ship), so `pip install` and `uvx chatstrata`
  keep working. Decide before M5.
- Two weeks of parallel use on all three personal machines; then make v2 the
  daily driver. Python stays installable as a fallback, and `import-legacy` is
  how anything it collected in the meantime comes back.

## Open questions

- Can work transcripts leave the work machine? (Decides whether a work space
  ever exists.)
- Where does the relay live long-term: Linux desktop, Pi, or VPS?
- `duckdb-go` does not bundle the FTS extension (M1 finding). For release
  builds, decide between shipping the signed extension file alongside the
  binary, building DuckDB with FTS linked in, or keeping the explicit
  `reindex --install-fts` download.
- MCP server inside the daemon or as a stdio proxy?
- Segment size and how often to flush (latency vs. number of files).
- When can old segments be compacted, given devices that stay offline for a
  long time?

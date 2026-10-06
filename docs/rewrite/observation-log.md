# Observation log and projection (M2)

How the Go implementation stores what it collects, as built in M2. The design
is [ADR 0005](../adr/0005-sync-device-logs.md); this page records the concrete
choices. Code: `internal/obslog`, `internal/project`.

## Files

```
chatstrata-go.duckdb           the projection (the archive everyone queries)
chatstrata-go.log/
  identity.json                this device's id and its space id (random, created once)
  devices/<device-id>/
    0000000000000001.seg       segments, named by their first sequence number
```

The log sits next to the archive and is named after it, so `--db PATH` picks
both. It is the source of truth: deleting the `.duckdb` file and running
`chatstrata rebuild` recreates it. Deleting the log loses data.

## Segments

A segment is a file of observations from one device, in sequence order:

```
"CHATSTRATA-SEG1\n"
repeat:
  uint32 header length | uint32 content length | uint32 CRC-32C | header JSON | zstd(content)
```

Integers are big-endian; the checksum covers the header and the compressed
content. A segment is written to a temporary file, synced, and renamed into
place, so a reader never sees a partial one. Segments are never modified.
Sequence numbers are per device and strictly increasing.

An observation header carries: format version, space, device, sequence
number, source, scope, locator (the source's conversation id), kind, offset,
length, SHA-256 of the content, the device's clock (informational only), the
file's path and project hint on that device, and whether it is a legacy import.

| kind | meaning |
|---|---|
| `append` | bytes added at `offset` of an append-only file |
| `snapshot` | the whole file |
| `tombstone` | the conversation was deleted from the archive |

## Collecting

For each discovered file the collector compares the file with what this device
last logged for that conversation (`collector_state`):

- unchanged: nothing is logged
- grew, and the logged bytes are still its prefix: `append` of the new bytes
- anything else (rewritten, shrunk, or a source that isn't append-only):
  `snapshot`

For append-only sources a trailing line still being written is held back
until it is complete.

Most sources keep one file per conversation. Two kinds don't, and their
adapters hand the collector each conversation's bytes directly:

- `claude_export`: one `conversations.json` holds every conversation. Each
  conversation is its array element, exactly as exported.
- `opencode` and `hermes_agent`: SQLite databases. OpenCode 2.x writes new
  sessions to `session_v2`/`session_message` and copies 1.x sessions there
  under the same id, so a session in both is read from the 2.x tables;
  sessions only in the 1.x tables (`session`/`message`/`part`) are read from
  those. Each session's rows
  (session, messages, parts) are serialized as one JSON document. The
  database is opened read-only. Its mtime isn't used to skip unchanged
  sessions, because SQLite can hold new rows in its write-ahead log without
  touching the main file, so these sessions are compared by content.

These sources, and `omp` (whose title slot is rewritten in place), are logged
as snapshots. The log is written before `collector_state`; if a run
stops in between, the next run logs the same bytes again, which the
projection ignores.

## Projecting

The projection of a conversation depends only on the set of its observations,
never on arrival order or wall clocks:

1. Any tombstone hides the conversation. For sources logged as snapshots, a
   device's latest snapshot replaces its earlier ones (an edited or rewound
   session drops rows), so only different devices' snapshots are compared
   as versions below. Sequence numbers are per-device logical clocks, not
   wall clocks.
2. Byte ranges from all devices are combined into versions of the file.
   Ranges that agree byte for byte merge, so overlapping appends, stale
   snapshots and duplicates collapse into one version. A range that
   contradicts every version starts a new one; a range beyond a gap waits
   until the gap is filled.
3. Each version is parsed by the source's adapter. With one version, that is
   the conversation, exactly as M1 would have stored it.
4. With several, messages are merged by their source-native ids. Shared
   history is stored once, and the differing continuations become branches
   after the last shared message, ordered by their first message's time. The
   `divergences` table records the fork point and which devices saw each
   branch.
5. Legacy imports (Python-era `raw_events`, re-serialized JSON) are merged at
   the message level only and are never reported as divergences. Live
   versions come first, so original bytes win over re-serialized ones.

On opening the archive for writing, any segment not yet indexed is indexed
and its conversations re-projected. This catch-up is how a crash between
writing the log and updating the projection is repaired, and how segments
from other devices will be applied once sync (M3) delivers them into their
device directories.

## Commands

- `chatstrata ingest SOURCE`: collect, log, project.
- `chatstrata rebuild`: build a fresh projection from the log and swap it in.
  The search index is rebuilt with it when the FTS extension is installed.
- `chatstrata import-legacy PYTHON_ARCHIVE`: log each conversation of a
  Python-era archive as a legacy snapshot, so history whose transcripts are
  gone survives. Re-running it skips what was already imported. Every Python
  adapter has a Go port, so all of it projects; OpenCode conversations
  imported this way have no title (Python kept the session row only in its
  `conversations` table), unless the live database supplies one. Sources the
  Go version doesn't know stay in the log only.

## Not yet

- Creating tombstones from the CLI (the projection handles them already).
- Account scopes: conversation ids ignore `scope`, because `conversations`
  is unique on `(source_id, source_native_id)` and the claude.ai export
  doesn't name its account in `conversations.json`. Supporting two accounts'
  exports needs a schema change and an ADR. The
  `same_locator_different_scope` scenario is skipped until then.
- Snapshot sources log the whole session on every change, so an active
  OpenCode or Hermes session grows the log by one compressed snapshot per
  collection that saw it change. Compacting old segments would reclaim this.
- Projection parses conversations one at a time. Full ingest is 4.8 s on the
  benchmark corpus against 3.5 s in M1; parallel parsing would recover it.
- Compacting old segments.

## M4a verification

Checked on `0337466` from echo-1, the device that runs no daemon.
`go test ./...`, `go vet ./...` and `gofmt -l .` are clean, and
`~/.local/bin/chatstrata` is a regular file, not a symlink to the Python
tool. No transcripts were copied into anything below; it is counts, ids and
md5 fingerprints only.

`chatstrata sources` lists all six adapters. On echo-1 `~/.codex/sessions`
(66 files) and `~/.omp/agent/sessions` (76 files) exist;
`~/.local/share/opencode/opencode.db` exists but holds no sessions;
`~/.hermes` has no `state.db`, and there is no claude.ai export. Only
`codex_cli` and `omp` have real data here.

### Parity with Python

Go ingested into the live archive, Python (`uv pip install -e .`) into
scratch databases. Comparing conversation source ids, titles, projects and
message counts, and every message and block field (role, sequence index,
timestamps, block index/type/text and tool fields), the two agree:

- `codex_cli`: 65 conversations, 8990 messages, identical.
- `omp`: 70 conversations, identical except one session that was still being
  written during the run (`01a112d0…`, two extra messages in the later Python
  pass). Re-ingesting a frozen copy of it reproduced all 99 messages
  identically, so the difference is the live file, not the adapter.

Two fields differ by design and were excluded: the internal `conversations.id`
and `messages.id` (Python generates random UUIDv4, Go derives deterministic
ids), and `content_hash` (a per-implementation change detector). The
`content_blocks.payload` JSON is byte-different for every codex payload
(Python keeps insertion order, Go sorts keys) but value-identical when
compared as canonical JSON.

### Idempotency

`ingest codex_cli` again: `Ingested: 0  Skipped: 66`. `ingest omp`:
`Ingested: 1  Skipped: 75`, the one being the live session above; every other
session was unchanged. `ingest opencode` finds nothing.

### Sync

`sync` on echo-1 uploaded 14 segments and downloaded the other device's 8,
updating 34 conversations; a second `sync` is idle (`Uploaded: 0
Downloaded: 0`), and `divergences` is 0. The live archive holds 203
conversations / 30009 messages / 39241 content blocks / 13412 tool calls
(claude_code 34, codex_cli 79, omp 90; the extra codex and omp conversations
came from the other device). Fingerprints on echo-1:

```sql
select md5(string_agg(id, ',' order by id)) from conversations;  -- 725f22360bf9a517374553227e91df4f
select md5(string_agg(id, ',' order by id)) from messages;       -- 7ae2938348aa01b50aa3273881fe80a7
```

### Snapshot rewrite

OpenCode has no sessions on echo-1, so the revert was checked with `omp`,
which is also a snapshot source. On a scratch database a copied session
ingested as 166 messages; truncating its file and re-ingesting gave 138, with
`divergences` still 0, and `rebuild` from the log reproduced 138. The newest
snapshot replaces the older one, as intended.

### import-legacy

Importing the Python archive into a scratch database: `Imported: 128  Already
imported: 0  Kept in log only: 0  Failed: 0`. Per source it matches the Python
archive — claude_code 47, omp 13, opencode 25 — except codex_cli 43 vs 44.
That one conversation (`01a0a7b4…`, 343 messages, ingested 2026-10-05) has no
`raw_events` rows in the Python archive at all, so there is nothing to
re-serialize; its transcript still exists on disk and is in the Go archive.
Not an `import-legacy` defect.

### OpenCode format

The OpenCode installed on echo-1 (2.0.12) writes sessions to `session_v2` and
`session_message`. The adapter, like the Python one it was ported from, reads
`session`, `message` and `part`, so it finds no conversations here. Python
gives the same zero, so parity holds; it is a pre-existing source-format gap,
not a port defect.

### Not run from echo-1

The daemon pickup and the other device's half of the fingerprint comparison
need omarchy-macbook. echo-1 runs no daemon and has no SSH access to it.

### omarchy-macbook (relay and daemon node)

The same commit on the node that runs `chatstrata-relay.service` and
`chatstrata-daemon.service`. `go test ./...` is clean and
`~/.local/bin/chatstrata` was a regular ELF file, so `go build -o` could
replace it safely; the daemon was restarted afterwards.

`chatstrata sources` lists six. Locally present: `~/.codex/sessions` (14
files) and `~/.omp/agent/sessions` (46 files). Missing: no `opencode.db`, no
Hermes `state.db`, no claude.ai export and no Python `chatstrata.duckdb`, so
the revert and `import-legacy` checks stay on echo-1.

**Parity.** Ingesting the live sources into the real archive, then the same
sources from a frozen copy into scratch Go and scratch Python databases and
comparing with the canonical dump (`scripts/golden.py`): identical for
`codex_cli` and `omp` — 33 conversations, 3085 messages, 4322 content blocks
and 5992 raw_events, zero diff lines. The same two fields are excluded as on
echo-1 (random Python UUIDs against deterministic Go ids, and `content_hash`).
This run's own `omp` transcript is the live session and grows between passes,
which is why the frozen copy is used; on the live archive `ingest omp` reports
`Ingested: 1` for it and `Ingested: 0` for everything else and for
`codex_cli`.

**Sync and convergence.** `sync` uploaded 2 segments, then 0/0. The daemon's
next cycle downloaded echo-1's 14 segments and updated 135 conversations.
Both nodes now hold the same archive — 203 conversations, 30009 messages,
39241 content blocks and 13412 tool calls — with `divergences` 0 and
identical fingerprints:

```sql
select md5(string_agg(id, ',' order by id)) from conversations;  -- 725f22360bf9a517374553227e91df4f
select md5(string_agg(id, ',' order by id)) from messages;       -- 7ae2938348aa01b50aa3273881fe80a7
```

**Daemon pickup.** `codex exec "say hi"` created
`rollout-2026-10-06T14-08-40-01a112d5-….jsonl`. The next 5-minute cycle
logged one changed `codex_cli` and one changed `omp` conversation, updated
two, and uploaded two segments; the new conversation (`01a112d5…`, 3
messages) is in the archive and, after sync, on both devices.

Only the OpenCode revert and the legacy import were not run here, for the
missing data above; both were covered on echo-1.

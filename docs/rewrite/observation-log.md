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
- `opencode` and `hermes_agent`: SQLite databases. Each session's rows
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

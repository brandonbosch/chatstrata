# Syncing devices (M3)

How to sync the Go archive across your machines, and how it works. The design
is [ADR 0005](../adr/0005-sync-device-logs.md); the log format is in
[observation-log.md](observation-log.md).

## Setup

**1. Run the relay** on a machine that is usually on (the Linux desktop, a Pi,
a small VPS), and give it HTTPS on your tailnet:

```sh
chatstrata relay                              # listens on 127.0.0.1:8787
tailscale serve --bg http://127.0.0.1:8787    # https://<machine>.<tailnet>.ts.net
```

To keep the address stable if the relay moves to another machine, advertise
it as a Tailscale Service instead (`tailscale serve --service=svc:chatstrata
...`); check Tailscale's documentation for the current syntax and ACLs. To
keep it running, use a systemd unit or launchd agent; `chatstrata schedule`
will install one in M4.

**2. On your first device**, after ingesting as usual:

```sh
chatstrata pair --relay https://<machine>.<tailnet>.ts.net
chatstrata sync
```

`pair` prints a `chatstrata join ...` command. **The code in it contains the
space key**: anyone who has it can read the archive. Move it to your other
machines yourself (password manager, typing it, an encrypted note) rather
than through chat or email.

**3. On each other device:**

```sh
chatstrata join chatstrata-join:...
```

Joining downloads everything the relay has, so a brand-new machine gets the
whole archive immediately. A device that already ingested on its own keeps
its history: its segments are moved into the shared space and uploaded.

**4. Keep it going** on every device:

```sh
chatstrata daemon            # collect and sync every 5 minutes; --interval to change
chatstrata devices           # who is in the space, and what the relay holds
chatstrata sync              # sync now
```

The daemon opens the archive only during each run, so `query`, `search` and
the other commands work in between. If the relay is unreachable, devices
keep collecting and catch up on the next run.

## How it works

- Each device uploads its own segments and downloads everyone else's. Segments
  are immutable and named by sequence number, so a sync only moves what the
  other side lacks, and an interrupted sync leaves whole segments behind and
  resumes on the next run.
- Before upload, each segment is encrypted with [age](https://age-encryption.org)
  to the space key. The relay stores ciphertext only and can't read, merge or
  change anything.
- Downloaded segments are decrypted, checked record by record (checksum,
  device, space, sequence), stored in the log, and projected into the archive
  by the same catch-up that M2 uses for crash recovery. The merge rules are
  the ones in `spec/projection`.
- The relay requires a bearer token derived from the space key. The first
  upload to a space registers the token's hash; after that the space answers
  only to that token.

Files next to the archive, all mode 0600:

| file | contents |
|---|---|
| `chatstrata-go.log/identity.json` | this device's id and its space id |
| `chatstrata-go.log/space.key` | the space key (age identity) |
| `chatstrata-go.log/sync.json` | the relay URL |

## Why not tsnet

The plan had the relay join the tailnet itself through `tsnet`. M3 instead
makes it a plain HTTP server behind `tailscale serve`:

- `tailscale serve` already provides HTTPS, the MagicDNS name and Tailscale
  Services, and ACLs still decide who can reach the relay.
- Authorization comes from the space key, not tailnet identity, so the same
  relay also works behind any other HTTPS proxy, or publicly, later.
- chatstrata doesn't carry the Tailscale client (a large dependency) or keep
  its own tailnet login state.

Embedding `tsnet` remains possible if a single self-contained relay binary
turns out to matter.

## Security limits (v2.0)

- The space key is stored in a file (mode 0600), not the macOS Keychain or
  the Linux secret service yet.
- There is no key rotation or device removal yet. To cut a device off, pair a
  new space from a trusted device, join the others to it, and stop the old
  relay space. Data a device already downloaded can't be recalled.
- Members are trusted: any device with the key could write segments claiming
  another device's id. Per-device signing keys would close this and are a
  possible later step.
- Local archives stay plaintext, as planned.

## M3 verification notes

Verified 2026-10-06 on `claude/amazing-davinci-o1nlsv` (`3ff654e`),
linux/arm64, Go 1.27.1, against a real `chatstrata relay` process and three
separate archives on one host (local HTTP, **not** a tailnet yet). `go test
./...`, `go vet ./...` and `gofmt -l .` are clean. End-to-end with the built
binary:

- two devices ingest different sessions; `pair` + `sync`, then `join` on the
  second and `sync` again → same `stats`, identical conversation ids, no
  duplicate observations;
- a device holding a stale (shorter) copy of another's session converges to
  the fuller history; `divergences` stays empty;
- relay down mid-sync: `sync` exits 1 with no partial state; once the relay
  returns, the next `sync` uploads and the other devices catch up; an idle
  `sync` reports `Uploaded: 0  Downloaded: 0`;
- a third empty device restores the whole archive from the relay alone;
- the relay stores `age` ciphertext only (plus `token.sha256`).

### Known gaps found

1. **`search` is stale until `reindex`.** The projector (and plain `ingest`)
   do not update the FTS index, and the substring fallback in
   `internal/cli/read.go` only runs when the FTS query *errors*, not when it
   returns zero rows. A device that already has the fts extension therefore
   returns **no** results for content that arrived through a sync until
   `chatstrata reindex` runs. The acceptance case "same `search` output" only
   holds once every device has reindexed after its last projection.
   Pre-existing M1/M2 behaviour, not sync-specific. Candidate fix: rebuild FTS
   at the end of projection when `LOAD fts` succeeds.
   **Fixed:** catch-up now rebuilds the FTS index whenever it stored or removed
   conversations and the extension loads, so ingest, sync and the daemon keep
   search current. Covered by `TestSearchFindsSyncedContentWithoutReindex`.
   Cost: one index rebuild per run that changed something (about 1.5 s on the
   8 MB benchmark corpus).
2. **`daemon` loop is untested.** Only the one-shot `sync` path has coverage;
   the periodic collect+sync loop has none.
   **Fixed:** `TestDaemonRunsCollectAndSync` runs real daemon cycles on two
   devices (collecting from `~/.claude/projects` and syncing through a relay)
   and checks they converge; `TestDaemonStopsOnInterrupt` checks the loop
   exits cleanly.

### Running the cross-tailscale test

On the relay node: `chatstrata relay --data <dir>` behind
`tailscale serve --bg http://127.0.0.1:8787`. On device A:
`chatstrata pair --relay https://<host>.<tailnet>.ts.net` then `sync`. Carry
the printed join code to B out of band, `chatstrata join <code>` there. Ingest
on both, `sync` both, then diff `search`/`stats`. Since the gap 1 fix no
manual `reindex` is needed, once each device has the FTS extension
(`chatstrata reindex --install-fts` once).

### Cross-tailscale run: echo-1 ↔ omarchy-macbook (2026-10-06)

First run against a **real tailnet** (the verification above was same-host,
local HTTP). Both ends on `claude/amazing-davinci-o1nlsv` (`45a8e2d`), the
commit with the stale-search fix.

Environment:

- echo-1 `100.84.116.80`, Linux **amd64**, Go `1.27.0`, gcc 16.2.1 — built
  natively (`go build -o ~/.local/bin/chatstrata ./cmd/chatstrata`, 80 MB ELF,
  `version` → `chatstrata dev`).
- omarchy-macbook `100.78.103.27`, the relay, `https://omarchy-macbook.tail59d8d.ts.net`.
- Reachability: `tailscale ping` succeeds (via DERP `den`, ~18 ms; *no direct
  connection*), `curl` → `http_code=404` (server present, MagicDNS + TLS
  valid). So the path is relayed, not peer-to-peer, for this pair.
- Build note: `~/.local/bin/chatstrata` was a symlink into the uv-managed
  Python tool; `go build -o` would follow it and overwrite the Python
  binary. Remove the symlink first.

Leg 1 — join and converge:

```text
ingest claude_code   → Ingested: 29  Skipped: 2  Failed: 0
join <code>          → Joined. This device (92b52956) …
                       Synced. Uploaded: 5 segments  Downloaded: 1 segments
                                Conversations updated: 3
sync                 → Uploaded: 0  Downloaded: 0   (idle)
devices              → 919cce88… 1/1   ·   92b52956… (this device) 5/5
stats                → 32 conversations, 3853 messages, 3859 blocks, 1225 tool_calls
search refactor      → 13 results
```

Leg 2 — incremental cross-machine path, using a **genuine** new transcript
(a headless `claude -p` turn, carried on the wire as the token
`SYNCPROBE-ECHO1-1791311659`) rather than an edited fixture:

```text
ingest claude_code   → Ingested: 1  Skipped: 31  Failed: 0
sync                 → Uploaded: 1 segments  Downloaded: 0
search <token>       → exact matches at ranks 1–2 (scores 8.47 / 8.35)
devices              → this device now 6/6 locally and on relay
stats                → 33 conversations, 3855 messages, 3861 blocks, 1225 tool_calls
search refactor      → 13 results (unchanged)
sync                 → Uploaded: 0  Downloaded: 0   (idle)
```

Confirms, on two real hosts across a tailnet: one-sided ingest → `sync`
uploads the missing segment; the peer's projection picks up exactly the delta
(+1 conversation, +2 messages, +6 content blocks; tool calls unchanged); and
**search is current with no `reindex`** — the gap 1 fix holds end-to-end here.
The relay's per-device segment count matched local after each leg.

Observations for the relay-side diff: a query's sub-tokens can match many
blocks, so `search` returns up to `LIMIT 20` ranked rows with only the true
matches on top — diffing "same `search` output" between nodes should compare
the top-ranked rows, not the row count. The probe token above is the diff key;
the peer surfaces it at ranks 1–2 (see the relay-side result below).

#### Relay-side result: omarchy-macbook

Same run, the other end, also at `45a8e2d`.

- The daemon (every 5m) picked echo-1 up with no manual step — journal at
  12:34:40: `sync: uploaded 0, downloaded 6 segments, 30 conversations updated`.
- Converged to the numbers echo-1 reported: `stats` → 33 conversations, 3855
  messages, 3861 content blocks, 1225 tool calls.
- `search refactor` → 13 results, same as echo-1. The probe token
  `SYNCPROBE-ECHO1-1791311659` is at ranks 1–2 with scores 8.4746 / 8.3539 —
  the same rows in the same order the peer saw.
- One projection, no duplicates: 33 conversations, 35 observations, zero
  duplicated `(source_id, source_native_id)` pairs, zero divergences.
- Relay store: 7 `*.seg.age` plus `token.sha256`; every segment is X25519
  `age` ciphertext and a grep for the probe token or source names finds
  nothing. No warnings from either unit in the journal.

Exact cross-node check (run on echo-1; must match these):

```sql
select md5(string_agg(id, ',' order by id)) from conversations;  -- ea5d2a456d9303f5db3ff0ea091951a1
select md5(string_agg(id, ',' order by id)) from messages;       -- b8e0addc5409ee5d85d6eaa30d54291d
```

The 20 rows are `LIMIT 20` over every positive score, not a constant floor. A
query matching nothing returns nothing (`search qqqwwweee123` → "No results"),
and `match_bm25` is NULL for a non-matching block. The probe token's sub-token
`echo1` matches 49 blocks (shell `echo` output in the corpus), so 20 ranked
rows come back with the two true matches sorted on top.

Minor UX smell for M4: a zero-match query in text mode still prints the "run
`chatstrata reindex`" hint, which reads as a stale index when the index is
fine and the query simply matched nothing.
**Fixed in this PR:** with a current index, a zero-match search prints just
"No results."; it only suggests `chatstrata reindex` (or `reindex
--install-fts` when the extension is missing) when search fell back to
substring matching. `search --json` with no matches now prints `[]` instead
of the text message. Covered by `TestSearchWithNoMatches`.

#### Verifying 78625b3 on the relay node

Rebuilt `~/.local/bin/chatstrata` at `78625b3` (a regular file here, not a
symlink), restarted the daemon; `go vet`/`gofmt` clean and the full suite
passes, including `TestSearchWithNoMatches`. On the live archive:

```text
search qqqwwweee123        → No results.        (one line, no reindex hint)
search qqqwwweee123 --json → []
search refactor            → 13 results
```

Daemon on the new binary, next cycle: `claude_code: logged 1 changed
conversations` then `sync: uploaded 1, downloaded 0 segments, 0 conversations
updated` — no warnings.

Reverse probe, for the peer to confirm the other direction: the relay node
ingested a synthetic session carrying `SYNCPROBE-OMARCHY-1791313021`. It is
searchable straight away (rank 1, score 8.50, no reindex) and uploaded as
segment 2 of `919cce88…`. After echo-1's next `sync` the same token should
rank 1 there. Post-probe totals on the relay node: 34 conversations / 3857
messages / 3863 blocks, and the fingerprints become:

```sql
select md5(string_agg(id, ',' order by id)) from conversations;  -- 2347c98d954f9897452a714a63c4f040
select md5(string_agg(id, ',' order by id)) from messages;       -- 0c3da56cc21a9f36cb39f27180f7dd22
```

(The earlier `ea5d2a45…` / `b8e0addc…` fingerprints are pre-probe and now
stale.)

#### Verifying 78625b3 on echo-1, and the reverse probe

echo-1 rebuilt `~/.local/bin/chatstrata` at `78625b3` (checked first: a
regular file, not a symlink to the Python tool — the earlier hazard is gone).
Same three checks on echo-1's live archive:

```text
search qqqwwweee123        → No results.        (one line, no reindex hint)
search qqqwwweee123 --json → []
search refactor            → 13 results
```

echo-1 is **not** the daemon host — no `chatstrata daemon` runs there (only a
stale Python-era `chatstrata-sync.service`, failed 2026-10-04, whose unit file
no longer exists; the Go `schedule` installer is M4). The relay node's daemon
restart above is the only one.

The reverse probe crossed back cleanly. On echo-1, right after `sync`
(`Downloaded: 1 segments, Conversations updated: 1`):

```text
search SYNCPROBE-OMARCHY-1791313021 → rank 1, score 8.50, no reindex
stats → 34 conversations / 3857 messages / 3863 content blocks
idle sync → Uploaded: 0  Downloaded: 0
```

Fingerprints on echo-1 match the relay node exactly:

```sql
select md5(string_agg(id, ',' order by id)) from conversations;  -- 2347c98d954f9897452a714a63c4f040
select md5(string_agg(id, ',' order by id)) from messages;       -- 0c3da56cc21a9f36cb39f27180f7dd22
```

So both directions of the cross-tailscale sync now converge to identical
conversation/message id sets, with search current on arrival and no reindex.

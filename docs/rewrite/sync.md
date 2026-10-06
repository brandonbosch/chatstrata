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
2. **`daemon` loop is untested.** Only the one-shot `sync` path has coverage;
   the periodic collect+sync loop has none.

### Running the cross-tailscale test

On the relay node: `chatstrata relay --data <dir>` behind
`tailscale serve --bg http://127.0.0.1:8787`. On device A:
`chatstrata pair --relay https://<host>.<tailnet>.ts.net` then `sync`. Carry
the printed join code to B out of band, `chatstrata join <code>` there. Ingest
on both, `sync` both, then run `chatstrata reindex` on **both** before diffing
`search`/`stats` (see gap 1).

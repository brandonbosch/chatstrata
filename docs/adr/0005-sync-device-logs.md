# ADR 0005: Sync via per-device encrypted observation logs

**Status:** Proposed
**Date:** 2026-10

## Context

I run chatstrata on several personal machines (a Linux desktop and two
MacBooks). Each has a separate archive. I want one archive across all of them,
with these properties:

- Each machine collects on its own and works offline.
- A machine can catch up later even when none of the others are online.
- Data is encrypted in transit and the relay stores only ciphertext.
- No service I can't run myself; nothing touches a third-party cloud by
  default (the same local-first idea Anytype follows).

The current storage model can't sync. IDs are random UUIDs assigned at ingest
(`core/ingest.py`), so two machines ingesting the same session produce two
different conversations. Incremental ingest also replaces a conversation's
rows when a transcript changes, which is fine locally but wrong as a merge
rule between devices: a stale copy from another machine would overwrite a
fuller one.

Options considered:

- **any-sync (Anytype's sync stack).** Mature, encrypted and local-first, and
  in Go. But it is built for Anytype's object and space model, and
  self-hosting it means running several services (coordinator, consensus,
  file and sync nodes, plus their databases). Too much to operate for three
  laptops, and the object model doesn't match append-only transcripts.
- **Peer-to-peer over the tailnet (direct connections).** Tailscale solves
  reachability and NAT, but peer-to-peer needs both machines online at once,
  which contradicts the catch-up requirement.
- **Syncing the DuckDB file** (Syncthing, iCloud, etc.). DuckDB files can't be
  merged and are corrupted by syncing while open.
- **A store-and-forward relay holding per-device append-only logs.** Simple
  enough to write and run myself; no conflicts at the transport level.

## Decision

1. **Observations are the source of truth.** Adapters' input (original source
   bytes, with source, locator, byte range and device provenance) is recorded
   as immutable observations. The DuckDB archive becomes a projection that can
   be rebuilt from observations at any time.
2. **Each device appends only to its own log**, as immutable, numbered
   segments: `zstd` compressed, then encrypted with `age` using a per-space
   key.
3. **A relay stores segments and does nothing else.** It is a small Go HTTP
   service embedded in the chatstrata binary (`chatstrata relay`) that joins
   the tailnet with `tsnet`. Clients upload their own segments and download
   other devices' segments after a sequence number. Authorization uses tailnet
   identity (`WhoIs`) and a per-space allowlist; a device can write only its
   own folder.
4. **Merging happens in the projection, deterministically.** IDs are derived
   from space, source, scope and source-native identity, never random and never
   from content alone. Ordering uses per-device sequence numbers, never wall
   clocks. A shorter or stale observation never removes history. Deletion is an
   explicit tombstone observation.
5. **Transport is an interface** (`Put`, `List`, `Get`) so an S3-compatible
   bucket or public HTTPS relay can replace the tailnet relay later without
   changing the log format.
6. **A space** is the unit of membership and encryption: one key, a set of
   devices. v2.0 has exactly one personal space, but the format carries
   `space_id` so other spaces (a separate work archive, a team archive) remain
   possible.

## Consequences

- No conflict resolution at the transport level; the relay can be a few
  hundred lines and backed up by copying a directory.
- Parser improvements apply to the whole history: rebuild the projection from
  the log. This strengthens ADR 0002 by keeping original bytes instead of
  re-serialized JSON.
- Restoring a new machine is download-and-replay.
- Storage roughly doubles locally (log plus projection). zstd on JSONL keeps
  the log small relative to the projection.
- Existing archives need a one-time `import-legacy`, and their UUIDs are not
  preserved. Acceptable because nothing in v2.0 (labels and embeddings are out
  of scope) references them.
- The relay must be reachable eventually for sync to happen; machines still
  work fully offline in the meantime.
- Tailnet membership is required for sync in v2.0. A machine that can't join
  the tailnet (e.g. a managed work laptop) can't sync until a public transport
  exists.
- Removing a device can't recall data it already downloaded; key rotation only
  protects future segments.
- Local data stays plaintext. Full local encryption remains a separate, later
  decision.

## Amendment (M3, 2026-10)

Point 3 changed in implementation: the relay is a plain HTTP service that the
user exposes with `tailscale serve` (or any HTTPS proxy), not a `tsnet` node.
Authorization uses a bearer token derived from the space key instead of
tailnet identity; tailnet ACLs still control who can reach it. Segments are
encrypted with age to an X25519 key shared by the space. Details and
reasons: `docs/rewrite/sync.md`.

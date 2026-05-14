# ADR 0002: Store raw source data alongside normalized data

**Status:** Accepted
**Date:** 2026-05

## Context

Adapters normalize source-specific data into chatstrata's canonical schema.
Normalization is lossy — we discard fields we don't understand, collapse
structure, and make best-effort guesses about how to map a source's events
to our types.

Six months from now, an adapter author will improve their parser. A user will
discover a source-specific field we never extracted that would now be useful.
Without preserving the raw input, the only recourse is to re-ingest from disk,
assuming the source files still exist (Claude Code's default retention is 30
days; many users will have already lost the originals).

## Decision

Every adapter populates `ParsedConversation.raw_events` with the source
records line-for-line. The ingester writes them into the `raw_events` table.

Raw events are stored as JSON, with the source id and conversation id linking
them back to the normalized data.

## Consequences

- ~2-3x storage overhead. For a personal archive, this is acceptable —
  even 100,000 messages with raw is well under 10 GB.
- We can re-derive normalized data from `raw_events` whenever a parser
  improves, without re-ingesting from disk.
- Adapter authors who skip `raw_events` are explicitly choosing to lose
  this option. The adapter guide flags this prominently.
- For sources with large tool outputs (Claude Code reading big files), we
  may want to add a configurable size threshold above which raw payloads
  are stored on disk rather than in the DB. Deferred for now.

# ADR 0009: Redact observations at collection time

**Status:** Accepted
**Date:** 2026-11-26
**Extends:** [0008](0008-local-redaction-boundary.md)

## Context

ADR 0008's overlay hides secrets from the projection, but the observation log
remains the plaintext source of truth. New conversations are collected as-is;
removing their secrets needs `redact apply` (hides the projection) or
`redact purge --hard` (rewrites the log). Both scan or rewrite the whole
archive, and the archive is queryable in plaintext until then.

The ADR left collection-time redaction as a later option: redact before the
observation is appended, so new plaintext never enters this device's log or
projection.

## Decision

Add an opt-in policy key:

```toml
[redaction]
collect = true
```

When set, the collector runs the configured policy over every new observation
and over its path and project hint before appending. Observations carry
`redacted: true`, which also makes later re-collection of the same
conversation redact again.

- The rewrite is length-preserving: observation append offsets are byte
  positions, so a marker is padded to the span and a span shorter than its
  marker becomes `*` repeated. The cache length and hash therefore stay
  comparable across collections.
- Payloads that are valid JSON, or JSONL whose non-empty lines are each valid
  JSON, are scanned by decoded string value, and the spans are mapped back to
  the serialized bytes. A marker never splits an escape sequence, so the
  source parser can still read the observation after a rebuild. Other payloads
  are scanned as text.
- Redaction is fail-closed: an engine error or an input over `max_input_bytes`
  fails the file's collection instead of logging the original bytes.
- The policy is per device and is not recorded in observations. Every device
  that writes to a shared space must run the same policy, exactly as with the
  overlay.

## Consequences

- New observations are already redacted when `query`, MCP and search read
  them; no rescan is needed for them.
- The local plaintext is permanently discarded. Original source files, other
  devices, the relay and backups still hold it; rotation is still required.
- Existing plaintext observations are unchanged. They need `redact apply`
  (hide) or `redact purge --hard` (rewrite).
- Changing the policy does not rewrite earlier observations. Later appends are
  redacted under the new policy, and the state hash falls back to a snapshot
  when the prefix changes.
- `doctor` cannot compare a redacted conversation with its source, so pending
  collection reporting skips its locator, as it already does after a purge.

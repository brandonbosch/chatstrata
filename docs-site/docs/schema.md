# Schema

**Schema version:** 5

chatstrata stores conversations in a normalized form designed to be source-agnostic.
The same tables hold a Claude Code session, a claude.ai export, and a ChatGPT export.

## Versioning

`meta.schema_version` records the current version. Schema changes require:
1. An ADR in `docs/adr/` explaining the change.
2. A migration script.
3. A version bump.

Adapters implement `model.Source`; migrations are embedded from `internal/store/migrations/`. Existing table and column names remain compatible with saved SQL.

## Tables

### `sources`
One row per ingested source.

| column          | type          | notes                                          |
|-----------------|---------------|------------------------------------------------|
| id              | VARCHAR PK    | stable adapter name (e.g. `claude_code`)       |
| name            | VARCHAR       | display name                                   |
| adapter_version | VARCHAR       | version of the adapter that ingested data      |
| first_seen      | TIMESTAMPTZ   |                                                |
| last_ingested   | TIMESTAMPTZ   |                                                |
| config          | JSON          | snapshot of adapter config used                |

### `conversations`
One row per conversation/session/thread.

| column            | type        | notes                                                       |
|-------------------|-------------|-------------------------------------------------------------|
| id                | VARCHAR PK  | chatstrata-internal UUID                                    |
| source_id         | VARCHAR FK  | -> sources.id                                               |
| source_native_id  | VARCHAR     | id from the source (filename, ChatGPT id, etc.)             |
| title             | VARCHAR     | summary or fallback to first user turn                      |
| project           | VARCHAR     | cwd / workspace / project, if applicable                    |
| started_at        | TIMESTAMPTZ |                                                             |
| ended_at          | TIMESTAMPTZ |                                                             |
| message_count     | INTEGER     |                                                             |
| content_hash      | VARCHAR     | sha256 of concatenated content, for dedup                   |
| raw_path          | VARCHAR     | where the source data lives on disk                         |
| metadata          | JSON        | source-specific extras                                      |
| ingested_at       | TIMESTAMPTZ |                                                             |

`UNIQUE (source_id, source_native_id)` enforces idempotency.

### `messages`
One row per turn.

| column                 | type        | notes                                                |
|------------------------|-------------|------------------------------------------------------|
| id                     | VARCHAR PK  | chatstrata-internal UUID                             |
| conversation_id        | VARCHAR FK  |                                                      |
| source_native_id       | VARCHAR     | source-assigned id (uuid for Claude Code)            |
| parent_message_id      | VARCHAR     | for tree-shaped histories (ChatGPT branches)         |
| role                   | VARCHAR     | user / assistant / system / tool                     |
| model                  | VARCHAR     | model name when present                              |
| created_at             | TIMESTAMPTZ |                                                      |
| sequence_index         | INTEGER     | order within the conversation                        |
| metadata               | JSON        |                                                      |

### `content_blocks`
One row per content unit inside a message. This is where the data lives.

| column         | type    | notes                                                                  |
|----------------|---------|------------------------------------------------------------------------|
| id             | VARCHAR |                                                                        |
| message_id     | VARCHAR |                                                                        |
| block_index    | INTEGER | order within message                                                   |
| type           | VARCHAR | text / tool_use / tool_result / thinking / image / attachment          |
| text           | TEXT    | populated for text and thinking                                        |
| tool_name      | VARCHAR | for tool_use                                                           |
| tool_use_id    | VARCHAR | ties tool_use to its tool_result                                       |
| payload        | JSON    | type-specific structured data (tool input, image source, etc.)         |

### `tool_calls` (view)
Convenience view exposing every `tool_use` block as a row with its conversation
and project context. Use this for "every bash command I ran via Claude Code" queries.

### `attachments`
File attachments referenced from messages.

### `raw_events`
The source data preserved line-for-line. Lets us re-parse without re-ingesting
when normalization logic changes. **This is critical** — never assume the
normalized data is the only copy.

### `labels`
Legacy classifier answer tables retained for SQL compatibility (schema v4).
The Go application does not populate or migrate labels. One row
per target × question; see [labeling.md](labeling.md) and
[ADR 0004](https://github.com/brandonbosch/chatstrata/blob/main/docs/adr/0004-classifier-labels.md).

| column         | type        | notes                                                    |
|----------------|-------------|----------------------------------------------------------|
| pack           | VARCHAR     | question pack name (e.g. `tool-failures`)                |
| target_kind    | VARCHAR     | `tool_call` / `message`                                  |
| target_id      | VARCHAR     | `content_blocks.id` for tool calls, `messages.id` for messages |
| question       | VARCHAR     | question id within the pack                              |
| answer_type    | VARCHAR     | `noul` / `choice` / `score`                              |
| value          | DOUBLE      | noul probability or score expectation                   |
| choice         | VARCHAR     | chosen option for choice answers                         |
| confidence     | DOUBLE      | choice/score confidence; NULL for noul                   |
| probabilities  | JSON        | full distribution for choice/score                       |
| pack_version   | VARCHAR     | hash of the pack's questions; stale labels are re-run    |
| model          | VARCHAR     | versioned model id that answered                        |
| run_id         | VARCHAR     | -> label_runs.id                                         |

Primary key `(pack, target_id, question)`.

### `label_runs`
Legacy labeling-run metadata: pack, pack version, backend, model,
start/finish time, items labelled, failures, and input tokens used.

### `message_embeddings`
Retained for compatibility. Go does not generate or migrate embeddings.

## Observation-log projection tables (v5)

- `observations`: source/scope/locator, device sequence, kind and byte range,
  content hash, path/project hints, legacy flag, and segment/position provenance.
- `indexed_segments`: segments already projected; rebuild derives this again.
- `collector_state`: this device's last logged length, hash, and mtime per
  source/scope/locator. A rebuild can reconstruct content without this cache.
- `divergences`: fork and branch message IDs plus device provenance when
  observations disagree.
- `tool_calls` is a view over `content_blocks` of type `tool_use`, joining
  message and conversation information for analysis.

The observation log is the source of truth. Do not edit the DuckDB projection
as a way to change archived content: `rebuild` will overwrite those edits.

## Design principles

1. **Source-agnostic.** Adding a source should not require schema changes.
2. **Preserve the raw.** Normalization is lossy; raw is durable.
3. **Idempotent ingest.** Re-running an ingest is safe.
4. **Timestamps are UTC.** Adapters normalize on parse.
5. **Versioned.** Schema changes are ADRs with migrations.

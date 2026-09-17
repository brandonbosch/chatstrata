# Hermes Agent source

Hermes Agent (Nous Research) is a terminal/desktop AI agent. It persists every
session — messages, tool calls, reasoning traces, token usage, costs — in a
canonical SQLite store:

    $HERMES_HOME/state.db                     the default profile
    $HERMES_HOME/profiles/<name>/state.db     every named profile

`HERMES_HOME` defaults to `~/.hermes` when unset. Ingestion reads all of them:
the default store first, then each named profile, and sessions from a named
profile are archived under a profile-qualified id (`work/<session-id>`) because
profiles can be cloned and then share session ids. Point the adapter at exactly
one store with `--path`, or at one named profile with a source config:

    chatstrata ingest hermes_agent --path /path/to/state.db

A store that is missing, unreadable, or has no `sessions` table is reported as
an error; only a readable store that holds no sessions reads as an empty
source.

## Format notes

- `sessions` rows carry the title, model, cwd, timestamps, and token/cost
  totals; `messages` rows carry the conversation ordered by (timestamp, id).
- Assistant tool calls are a JSON array in `messages.tool_calls`
  (`{id, type: "function", function: {name, arguments}}`); each call maps to a
  `tool_use` block, and the matching `role='tool'` row maps to a
  `tool_result` block linked by `tool_call_id`.
- `messages.reasoning` / `reasoning_content` (chain-of-thought) map to
  `thinking` blocks.
- Compaction: rows with `active = 0` and `compacted = 0` are superseded
  versions and are skipped; `_compressed_summary = 1` rows are kept with a
  flag in message metadata.
- The database is opened in SQLite read-only mode (`file:...?mode=ro`), so
  ingestion can never mutate the live Hermes store, even while Hermes runs.

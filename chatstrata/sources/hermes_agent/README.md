# Hermes Agent source

Hermes Agent (Nous Research) is a terminal/desktop AI agent. It persists every
session — messages, tool calls, reasoning traces, token usage, costs — in a
canonical SQLite store:

    ~/.hermes/state.db        (or $HERMES_HOME/state.db when a profile is set)

Supporting tables live in the same file (FTS indexes, session_model_usage,
system_prompts). Multiple Hermes profiles each keep their own state.db; point
this adapter at a specific one with a source config:

    chatstrata ingest hermes_agent --config '{"path": "/path/to/state.db"}'

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

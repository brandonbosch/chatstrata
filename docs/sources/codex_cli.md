# Codex CLI

## What it ingests

Session transcripts from the OpenAI Codex CLI, stored as JSONL rollout files.
Codex CLI is an open-source command-line coding assistant from OpenAI.

## Where sessions are stored

Codex CLI writes one JSONL file per session to:

    ~/.codex/sessions/YYYY/MM/DD/rollout-<timestamp>-<session-uuid>.jsonl

Each line is a JSON object with envelope `{timestamp, type, payload}`.
The top-level `history.jsonl` file (command history) is ignored.

## Event types

The adapter recognizes these event types from real Codex CLI 0.118.0 rollout files:

| Envelope type | Payload type | Maps to |
|---|---|---|
| `session_meta` | — | Session config (cwd, cli_version) |
| `turn_context` | — | Per-turn metadata (model, cwd) |
| `event_msg` | `user_message` | User text message |
| `event_msg` | `agent_message` | Skipped (intermediary commentary) |
| `event_msg` | `task_started` / `task_complete` | Skipped (lifecycle) |
| `event_msg` | `exec_command_end` | Skipped (tool execution detail) |
| `event_msg` | `token_count` | Skipped (telemetry) |
| `response_item` | `message` (role=assistant) | Assistant text |
| `response_item` | `message` (role=developer) | Skipped (system prompt) |
| `response_item` | `reasoning` | Thinking block |
| `response_item` | `function_call` | Tool use (exec_command, etc.) |
| `response_item` | `function_call_output` | Tool result |
| `response_item` | `web_search_call` | Tool use (web_search) |
| `response_item` | `custom_tool_call` | Tool use (apply_patch, etc.) |
| `response_item` | `custom_tool_call_output` | Tool result |

## Ingesting

    chatstrata ingest codex_cli

Or with a custom path:

    chatstrata ingest codex_cli --path ~/custom/codex/sessions

## What gets extracted

- Conversation title (from first user message, truncated to 200 chars)
- Project directory (from `cwd` in session_meta or turn_context)
- All user messages, assistant responses, and tool interactions
- Reasoning / thinking blocks (summary text when available; content is encrypted)
- Function calls with name, call_id, and arguments
- Function call outputs linked by call_id
- Custom tool calls (apply_patch) and their outputs
- Web search calls with queries
- Model name from turn_context metadata
- All raw events preserved in raw_events

## Limitations

- **Reasoning content is encrypted**: Codex CLI encrypts chain-of-thought
  content. Only the summary (when present) is available as plain text.
- **No explicit title**: Codex CLI sessions have no title field. The first
  user message text is used as the title.
- **Developer messages skipped**: System prompts and permission instructions
  (role=developer) are not included as conversation messages.
- **Event_msg telemetry skipped**: Lifecycle events (task_started, token_count,
  exec_command_end) are preserved in raw_events but not parsed as messages.

## Idempotency

Re-running `chatstrata ingest codex_cli` is safe. Sessions are matched by
their UUID (extracted from the rollout filename).

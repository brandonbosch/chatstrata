---
title: Built-in Source Adapters
description: Claude Code, claude.ai exports, Codex CLI, and OpenCode — what each adapter parses and how.
---

# Built-in Source Adapters

chatstrata ships with four adapters out of the box, each targeting a different AI conversation source. To see which adapters are installed, run:

```bash
chatstrata sources
```

This prints a table of registered adapters loaded via the `chatstrata.sources` entry point group:

```
  claude_code          Claude Code  (v0.1.0)
  claude_export        Claude Export  (v0.1.0)
  codex_cli            Codex CLI  (v0.1.0)
  opencode             OpenCode  (v0.1.0)
```

Each adapter implements two methods: `discover()` finds conversations on disk and yields handles, and `parse()` reads one conversation into the canonical `ParsedConversation` model. The sections below cover where each source stores its data, what the raw format looks like, and what chatstrata extracts from it.

For default local sources, `chatstrata ingest --auto` will detect and ingest any adapters whose files are present. Use the per-source commands below when you need a custom path or an export source that has no default location.

---

## Claude Code (`claude_code`)

The most mature adapter. It reads session transcripts that Claude Code writes locally as you work.

**Default path:** `~/.claude/projects/<sanitized-cwd>/<session-uuid>.jsonl`

The `<sanitized-cwd>` directory name encodes the project's absolute path by replacing `/` with `-`. For example, a project at `/Users/alice/code/myproj` becomes the directory `-Users-alice-code-myproj`. The adapter reverses this to recover the original project path.

**Format:** One JSON object per line (JSONL). Each line is an event with a `type` field:

```json title="~/.claude/projects/-Users-example-myproj/abc-123.jsonl"
{"type":"summary","summary":"Refactor the user auth module","leafUuid":"msg-005"}
{"type":"user","uuid":"msg-001","timestamp":"2026-03-14T10:00:00Z","message":{"role":"user","content":"Can you help me refactor the auth module?"}}
{"type":"assistant","uuid":"msg-002","timestamp":"2026-03-14T10:00:05Z","message":{"role":"assistant","model":"claude-opus-4-7","content":[{"type":"text","text":"Sure, let me start by reading the current auth module."},{"type":"tool_use","id":"tool-call-1","name":"view","input":{"path":"/Users/example/myproj/auth.py"}}]}}
```

**What gets extracted:**

- **Conversation title** -- from the first `summary` event, or falls back to the first line of the first user message (truncated to 200 characters).
- **Project context** -- decoded from the parent directory name.
- **Messages** with role (`user`, `assistant`, `system`), model name, and timestamps.
- **Content blocks** -- text, `tool_use` / `tool_result` pairs (linked by `tool_use_id`), `thinking` blocks, and images. Unknown block types are preserved in a `payload` dict for forensics.
- **Tree structure** -- `parentUuid` on each event preserves the message tree, mapped to `parent_source_native_id`.
- **Per-message metadata** -- `requestId` and per-event `cwd`.

**Ingest command:**

```bash
chatstrata ingest claude_code
```

Or point at a non-default location:

```bash
chatstrata ingest claude_code --path ~/alternate/.claude/projects
```

!!! note
    Malformed JSONL lines are silently skipped rather than aborting the entire session import.

---

## claude.ai Export (`claude_export`)

Handles the official data export from Anthropic's web interface (Settings --> Account --> Export data on claude.ai). Unlike the other two adapters, this one has **no default path** -- you must point it at the export.

**Input:** The unzipped export archive, which contains `conversations.json`, `users.json`, and `projects.json`. The adapter reads only `conversations.json`.

**Format:** A single JSON array where each element is a conversation object:

```json title="conversations.json (excerpt)"
{
  "uuid": "conv-001",
  "name": "Understanding Python decorators",
  "created_at": "2026-02-10T14:00:00.000000+00:00",
  "updated_at": "2026-02-10T14:05:30.000000+00:00",
  "chat_messages": [
    {
      "uuid": "msg-001",
      "text": "Can you explain how Python decorators work?",
      "content": [],
      "sender": "human",
      "created_at": "2026-02-10T14:00:00.000000+00:00",
      "attachments": []
    }
  ]
}
```

**What gets extracted:**

- **Conversation title** -- from the `name` field, falling back to the first human message.
- **Messages** with role (mapped from `sender`: `"human"` becomes `user`, everything else becomes `assistant`) and timestamps.
- **Content blocks** -- text, tool_use, tool_result, thinking, and images, following the same Anthropic content-block schema as Claude Code.
- **Attachments** -- file name, size, MIME type, and extracted text content are captured as `ATTACHMENT`-type blocks. The original file bytes are not included in the export.
- **Dual text fallback** -- messages can carry content in either the structured `content` array or the legacy `text` field. The adapter checks `content` first; if empty, it falls back to `text`.

**Ingest command:**

```bash
chatstrata ingest claude_export --path ~/Downloads/data-export
```

You can point at either the directory or directly at `conversations.json` -- the adapter resolves both.

**Limitations:** No model information (the export omits which Claude model generated each response). No project association. Attachments are metadata-only.

---

## Codex CLI (`codex_cli`)

Ingests session transcripts from OpenAI's open-source Codex CLI.

**Default path:** `~/.codex/sessions/YYYY/MM/DD/rollout-<timestamp>-<session-uuid>.jsonl`

The adapter discovers sessions by globbing `**/rollout-*.jsonl` under the sessions directory and extracts the UUID from the filename (the last five hyphen-separated segments).

**Format:** JSONL with an envelope wrapping every event:

```json title="~/.codex/sessions/2026/04/10/rollout-2026-04-10T14-00-00-abc-def-123.jsonl"
{"timestamp":"2026-04-10T14:00:00Z","type":"session_meta","payload":{"id":"abc-def-123","cwd":"/Users/example/myproject","cli_version":"0.118.0"}}
{"timestamp":"2026-04-10T14:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"Add error handling to the database connection module"}}
{"timestamp":"2026-04-10T14:00:03Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Let me read the current database module first."}]}}
{"timestamp":"2026-04-10T14:00:04Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"cat db.py\"}","call_id":"call-001"}}
```

Unlike Claude Code's flat event types, Codex CLI uses a two-level structure: the envelope `type` (`session_meta`, `event_msg`, `response_item`, `turn_context`, `compacted`) and an inner `payload.type` that specifies the actual content.

**What gets extracted:**

- **User messages** from `event_msg` events with `payload.type == "user_message"`.
- **Assistant responses** from `response_item` messages with `role == "assistant"`.
- **Tool interactions** -- `function_call` and `function_call_output`, `custom_tool_call` and `custom_tool_call_output` (e.g., `apply_patch`), and `web_search_call` events.
- **Reasoning/thinking blocks** from `response_item` of type `reasoning`. Only the `summary` text is available; the actual chain-of-thought content is encrypted by Codex CLI.
- **Project directory** from `session_meta.cwd` or `turn_context.cwd`.
- **Model name** from `turn_context.model`.
- **Conversation title** -- first user message text, truncated to 200 characters (Codex CLI has no title field).

**Ingest command:**

```bash
chatstrata ingest codex_cli
```

**Quirks:**

- Developer-role messages (system prompts, permission instructions) are intentionally skipped.
- Lifecycle telemetry (`task_started`, `task_complete`, `token_count`, `exec_command_end`) is preserved in `raw_events` but not parsed into messages.
- Timestamps can be ISO 8601 strings or numeric Unix timestamps -- the adapter handles both.

---

## OpenCode (`opencode`)

Ingests session transcripts from [OpenCode](https://opencode.ai), a terminal-based AI coding assistant. OpenCode stores all session data in a local SQLite database rather than flat files.

**Default path:** `~/.local/share/opencode/opencode.db`

The adapter opens the database in read-only mode and queries three tables: `session`, `message`, and `part`.

**Database schema:**

| Table | Purpose |
|-------|---------|
| `session` | One row per coding session — id, title, working directory, timestamps |
| `message` | One row per conversational turn, with a JSON `data` column containing role, model info, token usage, and timing |
| `part` | One row per content unit within a message, with a JSON `data` column. Part types: `text`, `reasoning`, `tool`, `patch`, `step-start`, `step-finish` |

All timestamps in the database are Unix milliseconds.

**Format:** Unlike the other adapters which read flat files (JSONL or JSON), this adapter queries SQLite directly. Each message's `data` column contains a JSON object with role, model info (nested under a `model` key with `modelID` and `providerID`), token counts, and timing. Each part's `data` column describes one content unit:

```json title="message.data (example)"
{
  "role": "assistant",
  "model": {"modelID": "claude-sonnet-4-5-20250514", "providerID": "anthropic"},
  "tokens": {"input": 1200, "output": 350},
  "time": {"created": 1715700000000, "completed": 1715700005000}
}
```

```json title="part.data — text type"
{"type": "text", "text": "Let me look at the auth module."}
```

```json title="part.data — tool type"
{"type": "tool", "tool": "bash", "callID": "call-001", "state": {"status": "completed", "input": {"command": "cat auth.py"}, "output": "..."}}
```

**What gets extracted:**

- **Conversation title** -- from the `session.title` column.
- **Project directory** -- from `session.directory`.
- **Messages** with role (`user` or `assistant`), model ID, provider ID, and timestamps.
- **Content blocks** -- `text` parts become text blocks; `reasoning` parts become thinking blocks (with optional start/end timing); `tool` parts are split into `TOOL_USE` and `TOOL_RESULT` message pairs; `patch` parts become tool results with file lists and hashes.
- **Per-message metadata** -- provider ID, agent mode, interaction mode, and token counts when present.
- **Lifecycle markers** -- `step-start` and `step-finish` parts are intentionally skipped (they are internal accounting, not conversation content).

**Ingest command:**

```bash
chatstrata ingest opencode
```

Or point at a non-default database:

```bash
chatstrata ingest opencode --path ~/alternate/opencode.db
```

**Quirks:**

- The adapter opens the database with `?mode=ro` (read-only) to avoid interfering with a running OpenCode instance.
- Tool parts with `status: "completed"` produce both a `TOOL_USE` and `TOOL_RESULT` message. In-progress tool calls produce only the `TOOL_USE`.
- Tool output is first checked in `state.output`, then falls back to `state.metadata.output`.
- Model information can appear either as a nested object (`message.data.model.modelID`) or at the top level (`message.data.modelID`) -- the adapter handles both.

---

## Related

- [Writing a New Adapter](adapters.md) -- the `SourceAdapter` protocol and how to register your own.
- [Schema Reference](schema.md) -- the DuckDB tables where ingested data lands.
- [Ingestion Pipeline](ingestion.md) -- what happens after `parse()` returns a `ParsedConversation`.

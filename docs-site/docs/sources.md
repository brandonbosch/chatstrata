# Supported sources

Run `chatstrata sources` to list the six built-in adapters. `ingest --auto`
collects those installed on this machine. A claude.ai export requires an
explicit path. ChatGPT web exports are not currently supported.

| Adapter | Default source |
|---|---|
| `claude_code` | `~/.claude/projects/` JSONL transcripts |
| `codex_cli` | `~/.codex/sessions/` JSONL transcripts |
| `omp` | `~/.omp/agent/sessions/` JSONL sessions |
| `opencode` | OpenCode local SQLite store, including 1.x and 2.x layouts |
| `hermes_agent` | Hermes local SQLite store (`HERMES_HOME` when configured) |
| `claude_export` | An exported `conversations.json` file supplied with `--path` |

Use a source-specific command with `--path` for another location:

```sh
chatstrata ingest claude_code --path /backup/claude/projects
chatstrata ingest claude_export --path /exports/conversations.json
```

Adapters normalize conversations, message order and parent links, models,
text, reasoning, tool calls/results, metadata, and raw events where available.
Source formats differ; the archive preserves the source payloads rather than
promising fields the source never recorded. Binary attachment contents are not
a general-purpose file backup.

Run `doctor` after updating a source tool. It can detect missing/new storage
tables and conversations that no longer parse. Regression fixtures are
synthetic or sanitized, never real private transcripts.

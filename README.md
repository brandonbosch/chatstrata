# chatstrata

A personal, queryable archive of your AI conversations across providers.

Every conversation you've had with Claude, ChatGPT, or any other LLM is a record of
how you think, what you're working on, and how that's changed over time. Most of
that record lives scattered across browser exports, hidden JSONL files, and SaaS
dashboards you don't fully control. chatstrata pulls it into one place, normalizes
it, and lets you actually query and analyze it.

The name is from "strata" — layers of conversation deposited over time, with the
deeper layers telling you who you were.

## Why this exists

LLM providers collect rich data about how you interact with their models and use
it (in aggregate) to improve the experience for everyone. chatstrata is the same
idea, but for an audience of one: **you**. Your conversations, on your machine,
queryable on your terms.

Concretely, with chatstrata you can:

- Find every conversation where you discussed a topic, across providers.
- See how your prompting has changed over months or years.
- Audit every bash command you ran through Claude Code, grouped by project.
- Build a corpus that helps you brief a new model on who you are and what you care about.
- Identify abandoned projects, dropped threads, recurring patterns.

## Status

**Early alpha.** v0 includes adapters for Claude Code, claude.ai exports, Codex
CLI, and OpenCode. The architecture is built so that adding more sources
(ChatGPT exports, Cursor, etc.) is the work of one adapter — see
[docs/adapter-guide.md](docs/adapter-guide.md).

## Quickstart

Requires Python 3.10+ and [uv](https://github.com/astral-sh/uv).

```bash
git clone https://github.com/brandonbosch/chatstrata.git
cd chatstrata
uv venv
uv pip install -e ".[dev]"

# Ingest your Claude Code transcripts
chatstrata ingest claude_code

# See what's there
chatstrata stats

# Run a query
chatstrata query "SELECT model, COUNT(*) FROM messages GROUP BY model"
```

The default database lives at a platform-appropriate user data directory
(e.g. `~/.local/share/chatstrata/chatstrata.duckdb` on Linux). Override with
`CHATSTRATA_DB` or `--db`.

## Data model

chatstrata normalizes every conversation into the same shape regardless of source:

- **conversations** — one per session/thread
- **messages** — one per turn (user, assistant, system)
- **content_blocks** — one per content unit within a message (text, tool_use, tool_result, thinking, attachment)
- **tool_calls** — denormalized view of tool_use blocks for easy querying
- **raw_events** — the source data, line-for-line, for re-parsing without re-ingestion

See [docs/schema.md](docs/schema.md) for the full schema.

## Adding a source

Each source (Claude Code, ChatGPT export, etc.) is an adapter that implements a
small protocol: `discover()` finds available conversations, `parse()` turns them
into the canonical record types. See [docs/adapter-guide.md](docs/adapter-guide.md)
for the worked example using Claude Code.

Adapters can be contributed as PRs to this repo or as standalone pip packages
that register via entry points.

## Privacy

Your data stays on your machine. chatstrata makes no network calls during
ingestion or querying.

If you want to share queries or notebooks publicly, an optional redaction layer
(`uv pip install "chatstrata[redact]"`) wraps Microsoft Presidio with
chatstrata-specific recognizers for API keys, file paths, and other things that
commonly appear in LLM transcripts. See [docs/redaction.md](docs/redaction.md).

## Contributing

Contributions welcome. Especially valuable: new source adapters. See
[CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache 2.0. See [LICENSE](LICENSE).

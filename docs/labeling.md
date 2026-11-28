# Labeling with TypeSafe Jev

`chatstrata label` classifies conversations, user turns, and tool calls using
TypeSafe's System One API (`POST /v1/systemone`) behind the local redaction
egress gate ([ADR 0007](adr/0007-go-classifier-labels.md),
[ADR 0008](adr/0008-local-redaction-boundary.md)).

## Privacy boundary

Labeling is the only feature that sends transcript excerpts off the machine.
Before anything is serialized to HTTP:

1. Project allowlists (`--project`) and denylists (`--exclude-project`) exclude
   denied projects.
2. Every string field in the rendered state passes through `redact.Gate` using
   your local redaction policy (`$CHATSTRATA_REDACTION_CONFIG` or
   `<config>/chatstrata/redaction.toml`). Any redaction error or size-cap
   violation aborts fail-closed.
3. `--dry-run --show-state` prints target counts, redaction findings, estimated
   input tokens/cost, and the first redacted state without requiring an API key
   or touching the network.

## Durable storage and incremental runs

Completed runs are written first to immutable compressed JSONL files under
`chatstrata-go.labels/runs/<run-id>.jsonl.zst` and then upserted into DuckDB's
`labels` and `label_runs` tables. `chatstrata rebuild` replays both
`chatstrata-go.log/` and `chatstrata-go.labels/`, so rebuilding DuckDB never
loses paid labels.

Re-runs are incremental:

- `message` and `tool_call` targets are skipped once labeled for `(pack, pack_version)`.
- `conversation` targets track `conversations.content_hash` as
  `target_fingerprint` and apply a settle window (`--min-age 30m` by default),
  re-labeling a conversation only if new turns arrived after it was last labeled.

## Built-in packs

List packs and versions:

```sh
chatstrata label packs
```

| Pack | Target | Depends on | Purpose |
|---|---|---|---|
| `session-triage` | `conversation` | — | Screen real work sessions, activity, domain, completion, complexity, verifiability |
| `user-turn-boundaries` | `message` | `session-triage` | Separate human prompts from harness wrappers, turn role, self-containment, corrections |
| `benchmark-candidacy` | `conversation` | `session-triage`, `user-turn-boundaries` | Score replayable `workeval` benchmark candidates and task archetypes |
| `tool-failures` | `tool_call` | `session-triage` | Classify coding-harness tool failure modes (`sandbox_blocked` vs `user_rejected`, exit codes), agent-caused errors, stale views, and recovery |

Custom TOML packs placed in `$CHATSTRATA_PACKS_DIR` (default
`<config>/chatstrata/packs`) are discovered automatically and may add new packs
or override built-ins.

## Usage

```sh
# Preview without calling the API
chatstrata label run session-triage --dry-run --show-state --limit 10

# Run with an explicit key and cost cap
export TYPESAFE_API_KEY=...
chatstrata label run session-triage --max-cost-usd 2.00 --yes

# Calibrate tool-failures cheaply on Claude Code before a full run
chatstrata label run tool-failures --source claude_code --limit 500 --max-cost-usd 0.10 --yes

# Run all packs in dependency order
chatstrata label run --all --max-cost-usd 5.00 --yes

# Inspect summaries and runs
chatstrata label summary session-triage --by project
chatstrata label runs

# Remove a pack's runs from both sidecar log and DuckDB
chatstrata label clear session-triage --yes
```

Query labels directly through `chatstrata query` or MCP SQL by joining
`labels.target_id` to `conversations.id`, `messages.id`, or `content_blocks.id`.

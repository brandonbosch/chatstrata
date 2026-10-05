---
title: Labelling with Jev
description: Classify tool calls and your own messages with TypeSafe's Jev model.
---

# Labelling with TypeSafe Jev

`chatstrata label` runs a fast classifier over your archive and stores typed
answers next to your conversations. It's built for questions that SQL can't
answer alone: *why* a tool call failed, or *whether* a message was you
correcting the agent.

The backend is [TypeSafe](https://docs.typesafe.ai/introduction)'s Jev, a
"System One" model. It returns calibrated answers rather than text:

| Question type | Answer |
|---|---|
| `noul` | probability that a yes/no statement is true |
| `choice` | one option from a set you define, with a probability for every option and a confidence |
| `score` | an expected level on an ordered rubric, with per-level probabilities and a confidence |

## Privacy

This is the only chatstrata command that sends transcript content off your
machine. Each item's *state* (for example a tool call's arguments and output)
goes to `api.typesafe.ai`. chatstrata always prints the item count and an
estimated cost first, and asks before sending anything unless you pass
`--yes`. Use `--dry-run --show-state` to see exactly what would be sent.
TypeSafe says Jev is not trained on customer requests; see their
[legal page](https://docs.typesafe.ai/legal) for retention terms.

## Setup

```bash
uv tool install "chatstrata[jev]"        # or add jev to your existing extras
export TYPESAFE_API_KEY=...               # https://console.typesafe.ai/keys
```

## Built-in packs

```bash
chatstrata label packs
```

**`tool-failures`**: one item per tool call, paired with its result.
- `failure_mode` (choice): succeeded, match_failed, file_not_found,
  invalid_arguments, command_failed, blocked, timeout, other_error
- `agent_caused` (noul): it failed because of how the agent called it
- `stale_view` (noul): it failed because the agent assumed file content
  that wasn't there

**`user-turns`**: one item per message you wrote, with the assistant message
before it for context.
- `intent` (choice): new_task, continue, correction, answer, approval,
  question, other
- `frustration` (score): four levels from neutral to exasperated
- `states_done_criteria` (noul): the message says what "done" looks like

**`cyber`** (a TOML pack; see below): one item per tool call, for flagging
security work so you can filter it out of exports and anything fed back to a
model.
- `security_related` (noul): the call concerns security / hacking work
- `posture` (choice): offensive, defensive, general_security, not_security
- `sensitive_capability` (noul): contains usable attack material, not just
  discussion

```sql
-- hide security-related tool calls from a dump
SELECT cb.* FROM content_blocks cb
WHERE cb.type = 'tool_use'
  AND cb.id NOT IN (
    SELECT target_id FROM labels
    WHERE pack = 'cyber' AND question = 'security_related' AND value > 0.5
  );
```

## Running

```bash
# Preview: what gets sent and what it costs
chatstrata label run tool-failures --source omp --dry-run --show-state

# Start small, check the answers, then widen
chatstrata label run tool-failures --source omp --tool edit --limit 200
chatstrata label run tool-failures --since 2026-07-01
chatstrata label run user-turns --since 2026-01-01
```

Re-running a pack skips items it already labelled. If you change a pack's
questions, its version hash changes, and the next run re-labels everything
under the new wording. Use `--relabel` to force a refresh.

## Reading results

```bash
chatstrata label summary tool-failures --by tool
chatstrata label summary tool-failures --by source --min-confidence 0.6
chatstrata label summary user-turns --by quarter
```

Or query the `labels` table directly:

```sql
-- Edit-style failures per harness and tool
SELECT c.source_id, cb.tool_name, l.choice, COUNT(*) AS n
FROM labels l
JOIN content_blocks cb ON cb.id = l.target_id
JOIN messages m ON m.id = cb.message_id
JOIN conversations c ON c.id = m.conversation_id
WHERE l.pack = 'tool-failures' AND l.question = 'failure_mode'
  AND l.confidence >= 0.6
GROUP BY ALL ORDER BY n DESC;

-- How often I corrected the agent, by quarter and model
SELECT date_trunc('quarter', m.created_at) AS q, l.choice = 'correction' AS corrected, COUNT(*)
FROM labels l JOIN messages m ON m.id = l.target_id
WHERE l.pack = 'user-turns' AND l.question = 'intent'
GROUP BY ALL ORDER BY q;
```

`labels` columns: `pack`, `target_kind`, `target_id`, `question`,
`answer_type`, `value` (noul probability or score mean), `choice`,
`confidence`, `probabilities` (JSON), `pack_version`, `model`, `run_id`.
`label_runs` records each invocation's model and token usage.

## Throwing away an experiment

Labels are derived data: deleting them never touches your archive, only the
`labels` and `label_runs` tables. Use `label clear` to undo a run you don't
want to keep.

```bash
chatstrata label clear tool-failures --dry-run        # show what would go
chatstrata label clear tool-failures                  # whole pack (asks first)
chatstrata label clear tool-failures --version <hash> # just one stale version
chatstrata label clear user-turns --run <run-id> --yes
```

Editing a pack's questions or bumping its `state_version` changes its
`pack_version`, so a re-run writes a fresh set of labels and leaves the old
ones addressable by their version if you want to clear just those.

## Hiding labelled content from the MCP server

If parts of your archive should never reach a model that reads it through
`chatstrata serve` (say, security work whose text trips a content
classifier), label it and give the server a filter:

```bash
chatstrata label run cyber
chatstrata label run cyber-messages
chatstrata serve --filter 'cyber:security_related>0.3' \
                 --filter 'cyber-messages:security_related>0.3'
```

Or set `CHATSTRATA_MCP_FILTER` (comma-separated rules) in your MCP client's
config for the server. A rule is `pack:question>threshold` for noul and score
questions, or `pack:question=option` for choice questions
(`cyber:posture=offensive`).

With a filter on, the server shadows the archive tables with filtered views,
so queries run unchanged and counts stay correct, but:

- The text of a hidden block becomes `[hidden by chatstrata filter: <reason>]`
  and its `payload` is NULL. Hiding a tool call also hides its result, and
  the other way round.
- Titles and metadata of conversations with hidden content are hidden, as are
  metadata and attachment details of affected messages. `raw_events.payload`
  is always NULL.
- **It fails closed.** Anything a rule's pack covers but has no current label
  for (new data, a failed request, a pack you edited) is hidden as
  `unlabelled:<pack>` until you run the pack again. A filter that cannot be
  applied (unknown pack or question, no labels table) refuses every query
  rather than serving unfiltered data.
- File access is switched off for the connection, and queries that name the
  archive catalog directly (`<db>.main.content_blocks`) or read the full-text
  index's internal tables are rejected.

Content no rule's pack covers stays visible: `cyber` only sees tool calls, so
pair it with `cyber-messages` for message text and thinking. The labels are a
classifier's probabilities, so a lower threshold hides more. The filter
prevents accidental exposure through the server; it is not access control
against someone with the database file.

## Writing your own packs (TOML)

A pack is a `.toml` file. Drop one in your packs directory and `chatstrata
label` picks it up — no code, no reinstall. `chatstrata label packs` prints
the directory (default `<config>/chatstrata/packs`, override with
`$CHATSTRATA_PACKS_DIR`). The bundled `cyber` pack is a full worked example;
copy it as a starting point.

```toml
name = "my-pack"
description = "What this pack is for."
target_kind = "tool_call"        # tool_call | message | conversation
state_version = 1                 # bump by hand only if you change [state]

# Must return target_id, source_id, project, created_at (the runner filters
# and de-dupes on them) plus any columns [state] reads.
target_sql = """
SELECT cb.id AS target_id, c.source_id, c.project, m.created_at,
       cb.tool_name, cb.payload AS arguments
FROM content_blocks cb
JOIN messages m ON m.id = cb.message_id
JOIN conversations c ON c.id = m.conversation_id
WHERE cb.type = 'tool_use'
"""

# Each key becomes a field of the state sent to the model.
[state]
harness   = { column = "source_id" }
tool_name = { column = "tool_name" }
arguments = { column = "arguments", unwrap = true, json = true, clip = 2000 }

[questions.example]
type = "noul"                     # noul | choice | score
instructions = "Ask one thing about this item."
[questions.example.criteria]      # choice/noul: a table; score: a list
true = "..."
false = "..."
```

`[state]` field options: `column` (required), `clip` (trim a long string,
keeping head and tail), `json` (render as JSON text, then clip), `unwrap`
(strip the `{"input": ...}` / `{"arguments": ...}` envelope before `json`),
and `default` (used when the column is null or empty). Editing `[state]`
changes the pack's version automatically, so old labels won't silently mix
with new ones — no need to touch `state_version` for that. Use it only to
force a re-label when you've changed `target_sql` without changing `[state]`.

Validate a file by listing packs: a broken file fails with its path and the
problem.

## Writing good questions

The guidance in TypeSafe's
[Jev 1.13 jaggedness](https://docs.typesafe.ai/model-jaggedness/jev-1.13)
notes shaped the built-in packs:

- **Literal reading.** Jev answers the question as written. Put boundary
  cases in the criteria ("searches that return no matches count as
  succeeded").
- **No counting or arithmetic.** Ask one question per item, then count and
  compute rates in SQL.
- **Small, relevant state.** Long tool outputs are clipped, keeping the head
  and the tail, because errors usually show up at the end.
- **Use confidence.** Filter low-confidence answers with `--min-confidence`
  or `WHERE confidence >= ...`. For nouls, use `abs(2*value - 1)` as the
  confidence-style measure.

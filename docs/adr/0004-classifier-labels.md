# ADR 0004: Store classifier labels as typed answers keyed by pack

**Status:** Accepted
**Date:** 2026-10

## Context

SQL answers questions about structure ("how many Edit calls failed?") but not
about meaning ("why did they fail?", "was I correcting the agent?"). Fast,
cheap classifiers such as TypeSafe's Jev return calibrated, typed answers
(yes/no probabilities, a choice with a distribution, a score on a rubric) for
fractions of a cent per thousand items, which makes labelling a whole archive
practical.

Labels are derived data. They depend on the question wording, on how the
item was rendered into model input, and on the model version.

## Decision

- Add `labels` (one row per target × question) and `label_runs` (one row per
  invocation, with token usage) in migration v4.
- Group questions into **packs**. A pack declares its target SQL, a state
  builder, and its questions. `pack_version` is a hash of the questions plus
  a manual `state_version`, so editing a pack makes its old labels stale
  instead of mixing two meanings under one question id.
- Keep the primary key at `(pack, target_id, question)`: a re-run replaces
  the previous answer. History of past versions is not kept; `label_runs`
  records which model and pack version produced the current rows.
- Store the full probability distribution and confidence alongside the
  chosen value so analysis can apply its own thresholds later.
- Labelling is the only chatstrata feature that sends transcript content off
  the machine. It lives behind the optional `jev` extra, requires an explicit
  command, prints an estimate first, and asks for confirmation unless `--yes`.

## Consequences

- Labels can be joined to any table by `target_id` and queried through the
  existing read-only MCP `query` tool.
- Re-running a pack only pays for new or changed targets.
- Packs are either built-in (Python) or user-defined (TOML files discovered
  from a bundled directory and `$CHATSTRATA_PACKS_DIR`). A TOML pack declares
  its target SQL, a declarative state mapping, and its questions; the state
  mapping is fingerprinted into `pack_version` so editing it invalidates old
  labels, matching the manual `state_version` bump Python packs use. This
  needed no schema change.

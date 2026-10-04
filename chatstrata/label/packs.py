"""Question packs: what to label, how to describe it, and what to ask.

A pack bundles three things:

- ``target_sql`` selects the units to label. It must return ``target_id``,
  ``source_id``, ``project`` and ``created_at`` so the runner can filter and
  skip already-labelled targets, plus whatever columns ``build_state`` needs.
- ``build_state`` turns one selected row into the ``state`` sent to the model.
  Keep it small and relevant: Jev answers best on focused state, and you pay
  per input token.
- ``questions`` are TypeSafe question objects in their JSON wire format
  (``noul``, ``choice`` or ``score``). All of them are asked in one request
  per target.

Questions are read literally, so criteria spell out boundary cases. Anything
numeric (counting, rates, comparisons) happens later in SQL, not in a question.
"""

from __future__ import annotations

import hashlib
import json
from collections.abc import Callable
from dataclasses import dataclass
from typing import Any


@dataclass(frozen=True)
class Pack:
    name: str
    description: str
    target_kind: str
    target_sql: str
    build_state: Callable[[dict[str, Any]], dict[str, Any]]
    questions: dict[str, dict[str, Any]]
    # Bump when build_state changes shape; questions are hashed automatically.
    state_version: int = 1

    @property
    def version(self) -> str:
        """Stable hash of everything that changes the meaning of an answer."""
        blob = json.dumps(
            {"questions": self.questions, "state_version": self.state_version},
            sort_keys=True,
        )
        return hashlib.sha256(blob.encode()).hexdigest()[:12]


def clip(text: str | None, limit: int) -> str | None:
    """Trim text to ``limit`` chars, keeping head and tail.

    Tool errors usually sit at the end of the output and the command echo at
    the start, so a middle cut loses the least signal.
    """
    if text is None or len(text) <= limit:
        return text
    head = int(limit * 0.6)
    tail = limit - head
    omitted = len(text) - head - tail
    return f"{text[:head]}\n[... {omitted} chars omitted ...]\n{text[-tail:]}"


def _json_text(value: Any, limit: int) -> str | None:
    if value is None:
        return None
    if isinstance(value, str):
        try:
            value = json.loads(value)
        except json.JSONDecodeError:
            return clip(value, limit)
    return clip(json.dumps(value, ensure_ascii=False, default=str), limit)


# --------------------------------------------------------------- tool-failures

_TOOL_CALL_SQL = """
WITH results AS (
    SELECT
        m.conversation_id,
        cb.tool_use_id,
        cb.text AS result_text,
        COALESCE(
            TRY_CAST(cb.payload->>'is_error' AS BOOLEAN),
            TRY_CAST(cb.payload->>'isError' AS BOOLEAN),
            CASE WHEN cb.payload->>'status' = 'error' THEN TRUE END
        ) AS reported_error
    FROM content_blocks cb
    JOIN messages m ON m.id = cb.message_id
    WHERE cb.type = 'tool_result' AND cb.tool_use_id IS NOT NULL
    QUALIFY ROW_NUMBER() OVER (
        PARTITION BY m.conversation_id, cb.tool_use_id ORDER BY m.sequence_index
    ) = 1
)
SELECT
    cb.id AS target_id,
    c.source_id,
    c.project,
    m.created_at,
    m.model,
    cb.tool_name,
    cb.payload AS arguments,
    r.result_text,
    r.reported_error
FROM content_blocks cb
JOIN messages m ON m.id = cb.message_id
JOIN conversations c ON c.id = m.conversation_id
JOIN results r ON r.conversation_id = m.conversation_id AND r.tool_use_id = cb.tool_use_id
WHERE cb.type = 'tool_use'
"""


def _tool_arguments(payload: Any) -> Any:
    """Unwrap the adapter's {"input": ...} / {"arguments": ...} envelope."""
    if isinstance(payload, str):
        try:
            payload = json.loads(payload)
        except json.JSONDecodeError:
            return payload
    if isinstance(payload, dict) and len(payload) == 1:
        (key, inner), = payload.items()
        if key in ("input", "arguments"):
            return inner
    return payload


def _tool_call_state(row: dict[str, Any]) -> dict[str, Any]:
    state: dict[str, Any] = {
        "harness": row["source_id"],
        "tool_name": row["tool_name"],
        "arguments": _json_text(_tool_arguments(row["arguments"]), 2000),
        "result": clip(row["result_text"], 3000) or "(no output)",
    }
    if row.get("reported_error") is not None:
        state["harness_flagged_error"] = bool(row["reported_error"])
    return state


TOOL_FAILURES = Pack(
    name="tool-failures",
    description=(
        "Classify every tool call by outcome and failure mode, and whether a "
        "failure was the agent's fault. Compare tools and harnesses (e.g. omp "
        "edit vs Claude Code Edit vs Codex apply_patch)."
    ),
    target_kind="tool_call",
    target_sql=_TOOL_CALL_SQL,
    build_state=_tool_call_state,
    questions={
        "failure_mode": {
            "type": "choice",
            "instructions": (
                "What happened when the agent ran this tool call? Judge from `result`; "
                "`arguments` shows what the agent asked the tool to do."
            ),
            "criteria": {
                "succeeded": (
                    "The tool did what was asked. Includes commands that ran and printed "
                    "normal output, and searches that correctly returned no matches."
                ),
                "match_failed": (
                    "An edit, replace or patch could not be applied because the text, "
                    "anchor or hunk it had to find was missing, did not match exactly, "
                    "or matched more than one place."
                ),
                "file_not_found": "A path did not exist or a file or directory could not be found.",
                "invalid_arguments": (
                    "The tool rejected the call itself: missing or malformed parameters, "
                    "schema validation errors, or an unknown option."
                ),
                "command_failed": (
                    "A shell command, build, test or script ran but exited with an error, "
                    "reported failing tests, or printed a compile or runtime error."
                ),
                "blocked": (
                    "The call was not allowed to run: permission denied, sandbox refusal, "
                    "or the user rejected or interrupted it."
                ),
                "timeout": "The call timed out or was killed for running too long.",
                "other_error": "The call failed in a way none of the other options describe.",
            },
        },
        "agent_caused": {
            "type": "noul",
            "instructions": (
                "Did this tool call fail because of how the agent called it, such as a "
                "wrong path, guessed or outdated file content, malformed arguments, or "
                "using the wrong tool for the job?"
            ),
            "criteria": {
                "true": "The call failed and a better-formed call would have succeeded.",
                "false": (
                    "The call succeeded, or it failed because of the environment, the "
                    "code under test, or the user rather than the call itself."
                ),
            },
        },
        "stale_view": {
            "type": "noul",
            "instructions": (
                "Did the call fail because the agent assumed file contents or line "
                "positions that were not actually in the file?"
            ),
            "criteria": {
                "true": (
                    "The error shows the expected text, lines or hunk were not present "
                    "as the agent wrote them."
                ),
                "false": "The call succeeded, or failed for any other reason.",
            },
        },
    },
)


# ------------------------------------------------------------------ user-turns

_USER_TURN_SQL = """
WITH msg_text AS (
    SELECT
        m.id,
        m.conversation_id,
        m.role,
        m.sequence_index,
        m.created_at,
        c.source_id,
        c.project,
        string_agg(cb.text, '\n' ORDER BY cb.block_index) AS text
    FROM messages m
    JOIN conversations c ON c.id = m.conversation_id
    JOIN content_blocks cb ON cb.message_id = m.id AND cb.type = 'text'
    WHERE m.role IN ('user', 'assistant') AND cb.text IS NOT NULL AND cb.text <> ''
    GROUP BY ALL
),
ordered AS (
    SELECT
        *,
        last_value(CASE WHEN role = 'assistant' THEN text END IGNORE NULLS) OVER (
            PARTITION BY conversation_id ORDER BY sequence_index
            ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING
        ) AS previous_assistant
    FROM msg_text
)
SELECT
    id AS target_id,
    source_id,
    project,
    created_at,
    text AS user_text,
    previous_assistant
FROM ordered
WHERE role = 'user'
"""


def _user_turn_state(row: dict[str, Any]) -> dict[str, Any]:
    return {
        "previous_assistant_message": clip(row["previous_assistant"], 1500) or "(none: first turn)",
        "user_message": clip(row["user_text"], 3000),
    }


USER_TURNS = Pack(
    name="user-turns",
    description=(
        "Classify your own messages: intent, corrections, frustration and whether "
        "you stated what done looks like. Track how your prompting changes over time."
    ),
    target_kind="message",
    target_sql=_USER_TURN_SQL,
    build_state=_user_turn_state,
    questions={
        "intent": {
            "type": "choice",
            "instructions": "What is `user_message` doing in this conversation?",
            "criteria": {
                "new_task": "Starts a new, separate piece of work.",
                "continue": "Asks for the next step or an extension of work already under way.",
                "correction": (
                    "Says the assistant's previous answer or action was wrong, or redirects "
                    "it to do something different from what it did."
                ),
                "answer": "Answers a question the assistant asked, or supplies information it requested.",
                "approval": "Approves or accepts what the assistant proposed, e.g. 'looks good, go ahead'.",
                "question": "Asks for an explanation or information without asking for any change.",
                "other": "None of the above, e.g. pasted logs with no request, or chit-chat.",
            },
        },
        "frustration": {
            "type": "score",
            "instructions": "How frustrated with the assistant is the writer of `user_message`?",
            "criteria": [
                "Neutral or positive",
                "Mildly impatient or terse",
                "Clearly frustrated or annoyed",
                "Angry or exasperated",
            ],
        },
        "states_done_criteria": {
            "type": "noul",
            "instructions": (
                "Does `user_message` say how to tell when the task is done, such as "
                "expected behaviour, a test that should pass, or an exact output?"
            ),
        },
    },
)


BUILTIN_PACKS: dict[str, Pack] = {p.name: p for p in (TOOL_FAILURES, USER_TURNS)}


def get_pack(name: str) -> Pack:
    try:
        return BUILTIN_PACKS[name]
    except KeyError:
        known = ", ".join(sorted(BUILTIN_PACKS))
        raise KeyError(f"Unknown pack {name!r}. Available: {known}") from None

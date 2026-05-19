"""Codex CLI adapter.

OpenAI Codex CLI writes one JSONL rollout file per session to:
    ~/.codex/sessions/YYYY/MM/DD/rollout-<timestamp>-<session-uuid>.jsonl

Each line is a JSON object with an envelope: {timestamp, type, payload}.
Top-level event types observed in real rollout files:

    session_meta        — session config: id, cwd, model_provider, cli_version
    turn_context        — per-turn metadata: model, cwd, timezone, sandbox policy
    response_item       — model output items (the conversation content):
        message         — role=user|assistant|developer, content=[{type, text}]
        reasoning       — chain-of-thought (summary list, often empty; encrypted_content)
        function_call   — name, arguments (JSON string), call_id
        function_call_output  — call_id, output (string)
        web_search_call       — search action with query
        custom_tool_call      — apply_patch and other built-in tools
        custom_tool_call_output — output for custom_tool_call
    event_msg           — lifecycle and telemetry events:
        user_message    — the actual user prompt text
        agent_message   — intermediary commentary from the assistant
        task_started    — turn begins
        task_complete   — turn ends, includes last_agent_message
        exec_command_end — shell command execution result
        token_count     — usage stats
        patch_apply_end — file edit result
        web_search_end  — search completion
        turn_aborted    — interrupted turn
    compacted           — context compaction event (replacement_history)

Event shapes are based on inspection of real Codex CLI 0.118.0 rollout files.
"""

from __future__ import annotations

import json
from collections.abc import Iterable
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from chatstrata.core.models import (
    BlockType,
    ContentBlock,
    ConversationHandle,
    ParsedConversation,
    ParsedMessage,
    Role,
)

DEFAULT_CODEX_DIR = Path("~/.codex/sessions").expanduser()


def _parse_timestamp(s: Any) -> datetime | None:
    if not s or not isinstance(s, str):
        if isinstance(s, (int, float)):
            return datetime.fromtimestamp(s, tz=timezone.utc)
        return None
    try:
        if s.endswith("Z"):
            s = s[:-1] + "+00:00"
        return datetime.fromisoformat(s)
    except (ValueError, TypeError):
        return None


def _extract_session_id(filename: str) -> str:
    """Extract session UUID from rollout filename.

    Codex filenames look like: rollout-2026-04-07T10-21-42-019d68bf-f055-7183-ac5a-7ddae094e0aa.jsonl
    The UUID is the last 5 hyphen-separated groups (standard UUIDv7).
    """
    stem = Path(filename).stem
    if not stem.startswith("rollout-"):
        return stem
    # Strip "rollout-" prefix, then extract the trailing UUID (8-4-4-4-12 pattern)
    rest = stem[len("rollout-"):]
    parts = rest.split("-")
    # UUID is the last 5 segments (e.g., 019d68bf-f055-7183-ac5a-7ddae094e0aa)
    if len(parts) >= 5:
        return "-".join(parts[-5:])
    return rest


def _text_from_content(content: list[dict[str, Any]]) -> str | None:
    """Join text from a content array (input_text or output_text items)."""
    parts = []
    for item in content:
        if isinstance(item, dict):
            text = item.get("text")
            if text:
                parts.append(text)
    return "\n".join(parts) if parts else None


def _blocks_from_response_item(
    payload: dict[str, Any],
) -> tuple[Role | None, list[ContentBlock]]:
    """Map a response_item payload to a role and content blocks."""
    ptype = payload.get("type")

    if ptype == "message":
        role_str = payload.get("role", "")
        content = payload.get("content", [])
        text = _text_from_content(content) if isinstance(content, list) else None
        if not text:
            return None, []
        if role_str == "assistant":
            return Role.ASSISTANT, [ContentBlock(type=BlockType.TEXT, text=text)]
        if role_str == "user":
            return Role.USER, [ContentBlock(type=BlockType.TEXT, text=text)]
        # developer messages are system prompts — skip from conversation
        return None, []

    if ptype == "reasoning":
        summary = payload.get("summary", [])
        text = _text_from_content(summary) if isinstance(summary, list) else None
        # Reasoning items with empty summary still represent thinking (content is encrypted)
        return Role.ASSISTANT, [
            ContentBlock(
                type=BlockType.THINKING,
                text=text,
                payload={"has_encrypted_content": bool(payload.get("encrypted_content"))},
            )
        ]

    if ptype == "function_call":
        return Role.ASSISTANT, [
            ContentBlock(
                type=BlockType.TOOL_USE,
                tool_name=payload.get("name"),
                tool_use_id=payload.get("call_id"),
                payload={"arguments": payload.get("arguments", "")},
            )
        ]

    if ptype == "function_call_output":
        return Role.TOOL, [
            ContentBlock(
                type=BlockType.TOOL_RESULT,
                tool_use_id=payload.get("call_id"),
                text=payload.get("output"),
            )
        ]

    if ptype == "web_search_call":
        action = payload.get("action", {})
        return Role.ASSISTANT, [
            ContentBlock(
                type=BlockType.TOOL_USE,
                tool_name="web_search",
                tool_use_id=payload.get("id"),
                payload={
                    "status": payload.get("status"),
                    "query": action.get("query") if isinstance(action, dict) else None,
                },
            )
        ]

    if ptype == "custom_tool_call":
        return Role.ASSISTANT, [
            ContentBlock(
                type=BlockType.TOOL_USE,
                tool_name=payload.get("name"),
                tool_use_id=payload.get("call_id"),
                payload={"input": payload.get("input", "")},
            )
        ]

    if ptype == "custom_tool_call_output":
        return Role.TOOL, [
            ContentBlock(
                type=BlockType.TOOL_RESULT,
                tool_use_id=payload.get("call_id"),
                text=payload.get("output"),
            )
        ]

    return None, []


def _update_time_range(
    ts: datetime | None,
    started_at: datetime | None,
    ended_at: datetime | None,
) -> tuple[datetime | None, datetime | None]:
    if ts is None:
        return started_at, ended_at
    if started_at is None or ts < started_at:
        started_at = ts
    if ended_at is None or ts > ended_at:
        ended_at = ts
    return started_at, ended_at


class CodexCliAdapter:
    """Adapter for OpenAI Codex CLI session transcripts."""

    name = "codex_cli"
    display_name = "Codex CLI"
    version = "0.1.0"
    schema_version = 1

    def discover(self, config: dict | None = None) -> Iterable[ConversationHandle]:
        """Walk ~/.codex/sessions/**/rollout-*.jsonl and yield a handle per file."""
        root = Path((config or {}).get("path") or DEFAULT_CODEX_DIR).expanduser()
        if not root.exists():
            return
        for jsonl in sorted(root.glob("**/rollout-*.jsonl")):
            session_id = _extract_session_id(jsonl.name)
            yield ConversationHandle(
                source_native_id=session_id,
                path=jsonl,
                metadata={},
            )

    def parse(self, handle: ConversationHandle) -> ParsedConversation:
        if handle.path is None:
            raise ValueError("CodexCliAdapter requires a path on the handle")
        path = handle.path

        events: list[dict[str, Any]] = []
        with path.open("r", encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    events.append(json.loads(line))
                except json.JSONDecodeError:
                    continue

        messages: list[ParsedMessage] = []
        title: str | None = None
        project: str | None = None
        model: str | None = None
        started_at: datetime | None = None
        ended_at: datetime | None = None

        for ev in events:
            etype = ev.get("type")
            ts = _parse_timestamp(ev.get("timestamp"))
            started_at, ended_at = _update_time_range(ts, started_at, ended_at)

            payload = ev.get("payload") or {}

            # session_meta: extract cwd (project) and session id
            if etype == "session_meta":
                if project is None and payload.get("cwd"):
                    project = payload["cwd"]
                continue

            # turn_context: extract model and cwd
            if etype == "turn_context":
                if model is None and payload.get("model"):
                    model = payload["model"]
                if project is None and payload.get("cwd"):
                    project = payload["cwd"]
                continue

            # event_msg: user_message is the actual user prompt
            if etype == "event_msg":
                msg_type = payload.get("type")
                if msg_type == "user_message":
                    text = payload.get("message", "")
                    if text:
                        messages.append(
                            ParsedMessage(
                                source_native_id=None,
                                role=Role.USER,
                                created_at=ts,
                                blocks=[ContentBlock(type=BlockType.TEXT, text=text)],
                                metadata={},
                            )
                        )
                continue

            # response_item: the core conversation content
            if etype == "response_item":
                role, blocks = _blocks_from_response_item(payload)
                if role is not None and blocks:
                    messages.append(
                        ParsedMessage(
                            source_native_id=payload.get("call_id") or payload.get("id"),
                            role=role,
                            model=model if role == Role.ASSISTANT else None,
                            created_at=ts,
                            blocks=blocks,
                            metadata={},
                        )
                    )
                continue

        # Title: first user TEXT block, truncated
        if title is None:
            for m in messages:
                if m.role == Role.USER:
                    for b in m.blocks:
                        if b.type == BlockType.TEXT and b.text:
                            title = b.text.strip().splitlines()[0][:200]
                            break
                    if title:
                        break

        return ParsedConversation(
            source_native_id=handle.source_native_id,
            title=title,
            project=project,
            started_at=started_at,
            ended_at=ended_at,
            messages=messages,
            raw_path=str(path),
            metadata={"event_count": len(events)},
            raw_events=events,
        )

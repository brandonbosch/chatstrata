"""Claude Code adapter.

Claude Code writes one JSONL file per session to:
    ~/.claude/projects/<sanitized-cwd>/<session-uuid>.jsonl

Each line is a JSON object representing one event. Common shapes:
    - {"type": "user", "message": {...}, "uuid": ..., "timestamp": ...}
    - {"type": "assistant", "message": {...}, "uuid": ..., "timestamp": ...}
    - {"type": "summary", "summary": "...", "leafUuid": ...}
    - {"type": "system", "content": "...", "uuid": ...}

The `message` field follows the Anthropic Messages API shape and may contain
a list of content blocks (text, tool_use, tool_result, thinking).

This adapter is the reference implementation other adapters can copy.
"""

from __future__ import annotations

import json
from collections.abc import Iterable
from datetime import datetime
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

DEFAULT_CLAUDE_DIR = Path("~/.claude/projects").expanduser()


def _parse_timestamp(s: Any) -> datetime | None:
    """Claude Code timestamps are ISO 8601 strings, sometimes with 'Z' suffix."""
    if not s or not isinstance(s, str):
        return None
    try:
        # Replace trailing Z with +00:00 for fromisoformat
        if s.endswith("Z"):
            s = s[:-1] + "+00:00"
        return datetime.fromisoformat(s)
    except (ValueError, TypeError):
        return None


def _decode_project_dir(dir_name: str) -> str:
    """Decode Claude Code's encoded project-folder name back into a path.

    e.g. '-Users-alice-code-myproj' -> '/Users/alice/code/myproj'

    This decode is *lossy and irreversible*: Claude Code collapses '/', '_',
    '-', and '.' in the cwd all into '-' when naming the folder, so a '-' in
    the folder name could have been any of those characters. As a result many
    distinct cwds map to the same decoded path (e.g. both '.../my_proj' and
    '.../my/proj' decode identically). Prefer the lossless cwd recorded inside
    the transcript (see `_project_from_events`); this decode is only a
    last-resort fallback for transcripts that never recorded a cwd.
    """
    if dir_name.startswith("-"):
        return "/" + dir_name[1:].replace("-", "/")
    return dir_name


def _project_from_events(events: list[dict[str, Any]]) -> str | None:
    """Return the real working directory recorded in the transcript.

    Every Claude Code event carries the session cwd, e.g.
    ``"cwd": "/Users/alice/code/my_project"``. This is the lossless source of
    truth for the project path -- unlike the encoded folder name, which cannot
    be reversed (see `_decode_project_dir`). Use the first event that records a
    non-empty cwd; the cwd is constant across a session.
    """
    for ev in events:
        cwd = ev.get("cwd")
        if isinstance(cwd, str) and cwd:
            return cwd
    return None


def _role_from_event(event: dict[str, Any]) -> Role | None:
    t = event.get("type")
    if t == "user":
        return Role.USER
    if t == "assistant":
        return Role.ASSISTANT
    if t == "system":
        return Role.SYSTEM
    return None


def _content_blocks_from_message(message: dict[str, Any]) -> list[ContentBlock]:
    """Convert Anthropic-shaped message content into ContentBlocks.

    `message["content"]` may be either a plain string (legacy / simple user
    turns) or a list of content blocks (typical Anthropic API shape).
    """
    content = message.get("content")
    if content is None:
        return []
    if isinstance(content, str):
        return [ContentBlock(type=BlockType.TEXT, text=content)]
    if not isinstance(content, list):
        return []

    blocks: list[ContentBlock] = []
    for raw in content:
        if not isinstance(raw, dict):
            continue
        btype = raw.get("type")
        if btype == "text":
            blocks.append(ContentBlock(type=BlockType.TEXT, text=raw.get("text")))
        elif btype == "thinking":
            blocks.append(ContentBlock(type=BlockType.THINKING, text=raw.get("thinking")))
        elif btype == "tool_use":
            blocks.append(
                ContentBlock(
                    type=BlockType.TOOL_USE,
                    tool_name=raw.get("name"),
                    tool_use_id=raw.get("id"),
                    payload={"input": raw.get("input", {})},
                )
            )
        elif btype == "tool_result":
            result_content = raw.get("content")
            text_form: str | None = None
            if isinstance(result_content, str):
                text_form = result_content
            elif isinstance(result_content, list):
                # Tool results may themselves be a list of content blocks.
                parts = []
                for item in result_content:
                    if isinstance(item, dict) and item.get("type") == "text":
                        parts.append(item.get("text", ""))
                text_form = "\n".join(parts) if parts else None
            blocks.append(
                ContentBlock(
                    type=BlockType.TOOL_RESULT,
                    tool_use_id=raw.get("tool_use_id"),
                    text=text_form,
                    payload={
                        "is_error": raw.get("is_error", False),
                        "raw_content": result_content,
                    },
                )
            )
        elif btype == "image":
            blocks.append(
                ContentBlock(
                    type=BlockType.IMAGE,
                    payload={"source": raw.get("source", {})},
                )
            )
        else:
            # Unknown block type: preserve in payload for forensics.
            blocks.append(ContentBlock(type=BlockType.TEXT, payload={"unknown_block": raw}))
    return blocks


class ClaudeCodeAdapter:
    """Adapter for Claude Code session transcripts."""

    name = "claude_code"
    display_name = "Claude Code"
    version = "0.1.0"
    schema_version = 1

    def discover(self, config: dict | None = None) -> Iterable[ConversationHandle]:
        """Walk ~/.claude/projects/**/*.jsonl and yield a handle per file."""
        root = Path((config or {}).get("path") or DEFAULT_CLAUDE_DIR).expanduser()
        if not root.exists():
            return
        for jsonl in sorted(root.glob("*/*.jsonl")):
            session_id = jsonl.stem
            # Lossy fallback only: parse() prefers the lossless cwd from the
            # transcript and uses this decoded folder name just as a backstop.
            project = _decode_project_dir(jsonl.parent.name)
            yield ConversationHandle(
                source_native_id=session_id,
                path=jsonl,
                metadata={"project": project},
            )

    def parse(self, handle: ConversationHandle) -> ParsedConversation:
        if handle.path is None:
            raise ValueError("ClaudeCodeAdapter requires a path on the handle")
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
                    # Skip malformed lines but record nothing; we don't want
                    # one bad line to kill an entire session import.
                    continue

        messages: list[ParsedMessage] = []
        title: str | None = None
        started_at: datetime | None = None
        ended_at: datetime | None = None

        for ev in events:
            etype = ev.get("type")

            if etype == "summary":
                # Use the first summary we see as the conversation title.
                if title is None:
                    summary = ev.get("summary")
                    if isinstance(summary, str):
                        title = summary.strip()
                continue

            role = _role_from_event(ev)
            if role is None:
                continue

            message = ev.get("message") or {}
            blocks = _content_blocks_from_message(message)

            # Skip messages with no extractable content. This is common for
            # synthetic events Claude Code emits for bookkeeping.
            if not blocks:
                continue

            ts = _parse_timestamp(ev.get("timestamp"))
            if ts:
                if started_at is None or ts < started_at:
                    started_at = ts
                if ended_at is None or ts > ended_at:
                    ended_at = ts

            messages.append(
                ParsedMessage(
                    source_native_id=ev.get("uuid"),
                    parent_source_native_id=ev.get("parentUuid"),
                    role=role,
                    model=message.get("model"),
                    created_at=ts,
                    blocks=blocks,
                    metadata={
                        "request_id": ev.get("requestId"),
                        "cwd": ev.get("cwd"),
                    },
                )
            )

        # Prefer the lossless cwd recorded in the transcript over the lossy
        # folder-name decode carried on the handle. Fall back to the decoded
        # folder name only for transcripts that never recorded a cwd.
        project = _project_from_events(events)
        if project is None and handle.metadata:
            project = handle.metadata.get("project")
        # If no summary, fall back to the first user text block as a title.
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

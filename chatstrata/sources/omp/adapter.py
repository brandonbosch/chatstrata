"""Oh My Pi (omp) adapter.

Oh My Pi writes one JSONL file per session to:
    ~/.omp/agent/sessions/<encoded-cwd>/<timestamp>_<session-id>.jsonl

Each file physically begins with a fixed-width 256-byte title slot
({"type": "title", ...} padded with spaces), followed by a session header
({"type": "session", ...}) and then append-only SessionEntry values.

Conversation content lives in "message" entries whose persisted roles are
camelCase: "user", "assistant", "toolResult" (plus rarer coding-agent roles
like "bashExecution" we skip). Tool calls are content blocks of type
"toolCall" inside assistant messages; results are separate "toolResult"
messages carrying toolCallId/toolName. Extensions may add "custom_message"
entries that participate in the transcript.

Entries form a tree via id/parentId (branching, compaction); this adapter
flattens in file order, preserving ids so lineage stays queryable.
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

DEFAULT_OMP_DIR = Path("~/.omp/agent/sessions").expanduser()

_ROLE_MAP: dict[str, Role] = {
    "user": Role.USER,
    "assistant": Role.ASSISTANT,
    "toolResult": Role.TOOL,
}

# Roles persisted by omp that carry no conversational value for the archive
# (tool-feedback bookkeeping, standalone shell runs, legacy hook payloads).
_SKIPPED_ROLES = {"developer", "hookMessage", "bashExecution", "pythonExecution", "fileMention"}


def _parse_timestamp(s: Any) -> datetime | None:
    """omp timestamps are ISO 8601 strings, usually with 'Z' suffix."""
    if not isinstance(s, str):
        return None
    try:
        return datetime.fromisoformat(s.replace("Z", "+00:00"))
    except ValueError:
        return None


def _text_from_content(content: Any) -> str | None:
    """Extract concatenated text from a content string or block list."""
    if isinstance(content, str):
        return content
    if not isinstance(content, list):
        return None
    parts = [
        block.get("text", "")
        for block in content
        if isinstance(block, dict) and block.get("type") == "text" and isinstance(block.get("text"), str)
    ]
    return "\n".join(parts) if parts else None


def _blocks_from_content(content: Any) -> list[ContentBlock]:
    """Convert an omp message content value into ContentBlocks.

    Content may be a plain string or a list of typed blocks: "text",
    "thinking", "toolCall" (assistant-side), and "image".
    """
    if content is None:
        return []
    if isinstance(content, str):
        return [ContentBlock(type=BlockType.TEXT, text=content)] if content else []

    blocks: list[ContentBlock] = []
    for item in content:
        if not isinstance(item, dict):
            continue
        ctype = item.get("type")
        if ctype == "text":
            text = item.get("text")
            if isinstance(text, str) and text:
                blocks.append(ContentBlock(type=BlockType.TEXT, text=text))
        elif ctype == "thinking":
            text = item.get("thinking")
            if isinstance(text, str) and text:
                blocks.append(ContentBlock(type=BlockType.THINKING, text=text))
        elif ctype == "toolCall":
            arguments = item.get("arguments")
            if isinstance(arguments, str):
                try:
                    arguments = json.loads(arguments)
                except json.JSONDecodeError:
                    arguments = {"raw": arguments}
            blocks.append(
                ContentBlock(
                    type=BlockType.TOOL_USE,
                    tool_name=item.get("name"),
                    tool_use_id=item.get("id"),
                    payload={"arguments": arguments},
                )
            )
        elif ctype == "image":
            payload = {k: v for k, v in item.items() if k != "type"}
            blocks.append(ContentBlock(type=BlockType.IMAGE, payload=payload))
    return blocks


def _blocks_from_tool_result(message: dict[str, Any]) -> list[ContentBlock]:
    """Convert a toolResult message into a single TOOL_RESULT block."""
    text = _text_from_content(message.get("content"))
    details = message.get("details")
    payload: dict[str, Any] = {}
    if isinstance(details, dict):
        payload["details"] = details
    if message.get("isError"):
        payload["isError"] = True
    return [
        ContentBlock(
            type=BlockType.TOOL_RESULT,
            tool_name=message.get("toolName"),
            tool_use_id=message.get("toolCallId"),
            text=text,
            payload=payload,
        )
    ]


def _decode_project_dir(dir_name: str) -> str:
    """Best-effort decode of omp's encoded cwd bucket name back into a path.

    Buckets under home are spelled "-<relative>" (e.g. "-git-chatstrata");
    absolute fallback buckets are "--<encoded>--". parse() prefers the
    lossless cwd from the session header; this is only a backstop.
    """
    if dir_name.startswith("--") and dir_name.endswith("--"):
        return "/" + dir_name[2:-2].replace("-", "/")
    if dir_name.startswith("-"):
        return str(Path.home() / dir_name[1:].replace("-", "/"))
    return dir_name


class OmpAdapter:
    """Adapter for Oh My Pi session transcripts."""

    name = "omp"
    display_name = "Oh My Pi"
    version = "0.1.0"
    schema_version = 1

    def discover(self, config: dict | None = None) -> Iterable[ConversationHandle]:
        """Walk ~/.omp/agent/sessions/**/*.jsonl and yield a handle per file."""
        root = Path((config or {}).get("path") or DEFAULT_OMP_DIR).expanduser()
        if not root.exists():
            return
        for jsonl in sorted(root.glob("*/*.jsonl")):
            stem = jsonl.stem
            # Files are <timestamp>_<session-id>.jsonl; the id is the suffix
            # after the first underscore in the timestamp portion.
            session_id = stem.split("_", 1)[1] if "_" in stem else stem
            project = _decode_project_dir(jsonl.parent.name)
            yield ConversationHandle(
                source_native_id=session_id,
                path=jsonl,
                metadata={"project": project},
            )

    def parse(self, handle: ConversationHandle) -> ParsedConversation:
        if handle.path is None:
            raise ValueError("OmpAdapter requires a path on the handle")
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
                    # Skip malformed lines; one bad line should not kill an
                    # entire session import.
                    continue

        messages: list[ParsedMessage] = []
        title: str | None = None
        project: str | None = None
        started_at: datetime | None = None
        ended_at: datetime | None = None

        for ev in events:
            etype = ev.get("type")

            if etype == "title":
                # Fixed-width title slot; folded into the header below when
                # the header lacks a title.
                if title is None:
                    slot_title = ev.get("title")
                    if isinstance(slot_title, str) and slot_title.strip():
                        title = slot_title.strip()
                continue

            if etype == "session":
                header_title = ev.get("title")
                if isinstance(header_title, str) and header_title.strip():
                    title = header_title.strip()
                cwd = ev.get("cwd")
                if isinstance(cwd, str):
                    project = cwd
                ts = _parse_timestamp(ev.get("timestamp"))
                if ts:
                    started_at = ts
                continue

            if etype == "message":
                message = ev.get("message") or {}
                role_name = message.get("role")
                if not isinstance(role_name, str) or role_name in _SKIPPED_ROLES:
                    continue

                if role_name == "toolResult":
                    blocks = _blocks_from_tool_result(message)
                else:
                    blocks = _blocks_from_content(message.get("content"))

                if not blocks:
                    continue

                role = _ROLE_MAP[role_name]
                ts = _parse_timestamp(ev.get("timestamp"))
                if ts:
                    if started_at is None or ts < started_at:
                        started_at = ts
                    if ended_at is None or ts > ended_at:
                        ended_at = ts

                metadata: dict[str, Any] = {}
                attribution = message.get("attribution")
                if isinstance(attribution, str):
                    metadata["attribution"] = attribution

                messages.append(
                    ParsedMessage(
                        source_native_id=ev.get("id"),
                        parent_source_native_id=ev.get("parentId"),
                        role=role,
                        model=message.get("model"),
                        created_at=ts,
                        blocks=blocks,
                        metadata=metadata,
                    )
                )
                continue

            if etype == "custom_message":
                # Extension messages that participate in the transcript.
                attribution = ev.get("attribution")
                role = Role.USER if attribution == "user" else Role.ASSISTANT
                text = _text_from_content(ev.get("content"))
                if not text:
                    continue
                ts = _parse_timestamp(ev.get("timestamp"))
                if ts:
                    if started_at is None or ts < started_at:
                        started_at = ts
                    if ended_at is None or ts > ended_at:
                        ended_at = ts
                messages.append(
                    ParsedMessage(
                        source_native_id=ev.get("id"),
                        parent_source_native_id=ev.get("parentId"),
                        role=role,
                        created_at=ts,
                        blocks=[ContentBlock(type=BlockType.TEXT, text=text)],
                        metadata=(
                            {"attribution": attribution}
                            if isinstance(attribution, str)
                            else {}
                        ),
                    )
                )
                continue

            # Other entry types (compaction, model_change, custom, ...) affect
            # replay state, not transcript content; skip them.

        if project is None and handle.metadata:
            project = handle.metadata.get("project")

        # Fall back to the first user text block as a title.
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

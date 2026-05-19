"""Claude Export adapter.

Ingests conversations from the official Anthropic data export
(Settings → Account → Export data on claude.ai).

The export ZIP contains:
    - conversations.json — JSON array of all conversations
    - users.json — user info
    - projects.json — project info

Each conversation has uuid, name, created_at, updated_at, and a
chat_messages array. Each message has uuid, text, content (array of
blocks), sender (human/assistant), created_at, and attachments.
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


def _parse_timestamp(s: Any) -> datetime | None:
    if not s or not isinstance(s, str):
        return None
    try:
        if s.endswith("Z"):
            s = s[:-1] + "+00:00"
        return datetime.fromisoformat(s)
    except (ValueError, TypeError):
        return None


def _role_from_sender(sender: str) -> Role:
    if sender == "human":
        return Role.USER
    return Role.ASSISTANT


def _resolve_conversations_path(config: dict | None) -> Path | None:
    raw = (config or {}).get("path")
    if not raw:
        return None
    p = Path(raw).expanduser()
    if p.is_file():
        return p
    if p.is_dir():
        candidate = p / "conversations.json"
        if candidate.exists():
            return candidate
    return None


def _content_blocks_from_message(msg: dict[str, Any]) -> list[ContentBlock]:
    blocks: list[ContentBlock] = []

    content = msg.get("content")
    if isinstance(content, list) and content:
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
                blocks.append(ContentBlock(type=BlockType.TEXT, payload={"unknown_block": raw}))
    else:
        text = msg.get("text")
        if text:
            blocks.append(ContentBlock(type=BlockType.TEXT, text=text))

    for att in msg.get("attachments") or []:
        blocks.append(
            ContentBlock(
                type=BlockType.ATTACHMENT,
                text=att.get("file_name"),
                payload={
                    "file_name": att.get("file_name"),
                    "file_size": att.get("file_size"),
                    "file_type": att.get("file_type"),
                    "extracted_content": att.get("extracted_content"),
                },
            )
        )

    return blocks


class ClaudeExportAdapter:
    """Adapter for conversations from the official Anthropic data export."""

    name = "claude_export"
    display_name = "Claude Export"
    version = "0.1.0"
    schema_version = 1

    def discover(self, config: dict | None = None) -> Iterable[ConversationHandle]:
        conversations_path = _resolve_conversations_path(config)
        if conversations_path is None or not conversations_path.exists():
            return

        with conversations_path.open("r", encoding="utf-8") as f:
            try:
                conversations = json.load(f)
            except (json.JSONDecodeError, ValueError):
                return

        if not isinstance(conversations, list):
            return

        for conv in conversations:
            if not isinstance(conv, dict):
                continue
            uuid = conv.get("uuid")
            if not uuid:
                continue
            yield ConversationHandle(
                source_native_id=uuid,
                path=conversations_path,
                metadata={"_conversation": conv},
            )

    def parse(self, handle: ConversationHandle) -> ParsedConversation:
        conv = (handle.metadata or {}).get("_conversation")

        if conv is None and handle.path is not None:
            with handle.path.open("r", encoding="utf-8") as f:
                conversations = json.load(f)
            for c in conversations:
                if isinstance(c, dict) and c.get("uuid") == handle.source_native_id:
                    conv = c
                    break
            if conv is None:
                raise ValueError(
                    f"Conversation {handle.source_native_id} not found in {handle.path}"
                )

        title = conv.get("name")
        started_at = _parse_timestamp(conv.get("created_at"))
        ended_at = _parse_timestamp(conv.get("updated_at"))

        messages: list[ParsedMessage] = []
        for msg in conv.get("chat_messages") or []:
            blocks = _content_blocks_from_message(msg)
            if not blocks:
                continue

            ts = _parse_timestamp(msg.get("created_at"))
            if ts:
                if started_at is None or ts < started_at:
                    started_at = ts
                if ended_at is None or ts > ended_at:
                    ended_at = ts

            messages.append(
                ParsedMessage(
                    source_native_id=msg.get("uuid"),
                    role=_role_from_sender(msg.get("sender", "human")),
                    created_at=ts,
                    blocks=blocks,
                    metadata={
                        k: v
                        for k, v in {"sender": msg.get("sender")}.items()
                        if v is not None
                    },
                )
            )

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
            started_at=started_at,
            ended_at=ended_at,
            messages=messages,
            raw_path=str(handle.path) if handle.path else None,
            metadata={"message_count": len(conv.get("chat_messages") or [])},
            raw_events=[conv],
        )

"""Canonical record types that all source adapters produce.

Adapters consume their source-specific format and produce instances of these
types. The ingester knows only about these types, not about any specific source.

If you're adding a new source: your `parse()` should return a `ParsedConversation`.
The ingester handles persistence.
"""

from __future__ import annotations

from datetime import datetime
from enum import Enum
from pathlib import Path
from typing import Any

from pydantic import BaseModel, Field


class Role(str, Enum):
    USER = "user"
    ASSISTANT = "assistant"
    SYSTEM = "system"
    TOOL = "tool"


class BlockType(str, Enum):
    TEXT = "text"
    TOOL_USE = "tool_use"
    TOOL_RESULT = "tool_result"
    THINKING = "thinking"
    IMAGE = "image"
    ATTACHMENT = "attachment"


class ContentBlock(BaseModel):
    """A single content unit within a message."""

    type: BlockType
    text: str | None = None
    tool_name: str | None = None
    tool_use_id: str | None = None
    payload: dict[str, Any] = Field(default_factory=dict)


class ParsedMessage(BaseModel):
    """A normalized message ready for ingestion."""

    source_native_id: str | None = None
    parent_source_native_id: str | None = None  # for tree-shaped histories
    role: Role
    model: str | None = None
    created_at: datetime | None = None
    blocks: list[ContentBlock] = Field(default_factory=list)
    metadata: dict[str, Any] = Field(default_factory=dict)


class ParsedConversation(BaseModel):
    """A normalized conversation, ready to hand to the ingester."""

    source_native_id: str
    title: str | None = None
    project: str | None = None
    started_at: datetime | None = None
    ended_at: datetime | None = None
    messages: list[ParsedMessage] = Field(default_factory=list)
    raw_path: str | None = None
    metadata: dict[str, Any] = Field(default_factory=dict)

    # If the adapter wants to preserve the raw source lines (recommended),
    # populate this list. Each entry is one raw record (e.g. a JSONL line).
    raw_events: list[dict[str, Any]] = Field(default_factory=list)


class ConversationHandle(BaseModel):
    """A lightweight reference to a conversation a source can later parse.

    `discover()` yields these; `parse()` consumes one and returns the full
    `ParsedConversation`. The split exists so we can list available
    conversations without loading every byte of every file.
    """

    source_native_id: str
    path: Path | None = None
    metadata: dict[str, Any] = Field(default_factory=dict)

    model_config = {"arbitrary_types_allowed": True}

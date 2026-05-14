"""Source-agnostic ingester.

Takes a `ParsedConversation` from any adapter and persists it to DuckDB.
Idempotent: re-ingesting the same conversation (matched by
source_id + source_native_id) replaces existing rows for that conversation.
"""

from __future__ import annotations

import hashlib
import json
import uuid
from datetime import datetime, timezone
from typing import TYPE_CHECKING

from chatstrata.core.models import ParsedConversation

if TYPE_CHECKING:
    import duckdb


def _uuid() -> str:
    return str(uuid.uuid4())


def _hash_content(conv: ParsedConversation) -> str:
    """Stable hash of conversation content, for dedup detection."""
    h = hashlib.sha256()
    for m in conv.messages:
        h.update(m.role.value.encode())
        for b in m.blocks:
            h.update(b.type.value.encode())
            if b.text:
                h.update(b.text.encode("utf-8", errors="replace"))
            if b.payload:
                h.update(
                    json.dumps(b.payload, sort_keys=True, default=str).encode("utf-8")
                )
    return h.hexdigest()


def _as_utc(dt: datetime | None) -> datetime | None:
    if dt is None:
        return None
    if dt.tzinfo is None:
        return dt.replace(tzinfo=timezone.utc)
    return dt.astimezone(timezone.utc)


def _json(value) -> str | None:
    if value is None:
        return None
    if isinstance(value, str):
        return value
    return json.dumps(value, default=str)


def ensure_source(
    conn: "duckdb.DuckDBPyConnection",
    source_id: str,
    name: str,
    adapter_version: str | None = None,
    config: dict | None = None,
) -> None:
    """Register a source if it doesn't exist; touch last_ingested either way."""
    conn.execute(
        """
        INSERT INTO sources (id, name, adapter_version, config, last_ingested)
        VALUES (?, ?, ?, ?, now())
        ON CONFLICT (id) DO UPDATE SET
            adapter_version = excluded.adapter_version,
            config = excluded.config,
            last_ingested = now()
        """,
        [source_id, name, adapter_version, _json(config)],
    )


def ingest_conversation(
    conn: "duckdb.DuckDBPyConnection",
    source_id: str,
    conv: ParsedConversation,
) -> str:
    """Persist a single ParsedConversation. Returns the chatstrata conversation id.

    Idempotent: if a conversation with the same (source_id, source_native_id)
    already exists, its messages and content_blocks are replaced.
    """
    content_hash = _hash_content(conv)

    # Look up existing conversation
    existing = conn.execute(
        "SELECT id FROM conversations WHERE source_id = ? AND source_native_id = ?",
        [source_id, conv.source_native_id],
    ).fetchone()

    if existing:
        conv_id = existing[0]
        # Clear out previous content for clean replacement.
        # Order matters: content_blocks -> messages, due to FK.
        conn.execute(
            """
            DELETE FROM content_blocks
            WHERE message_id IN (SELECT id FROM messages WHERE conversation_id = ?)
            """,
            [conv_id],
        )
        conn.execute("DELETE FROM attachments WHERE message_id IN (SELECT id FROM messages WHERE conversation_id = ?)", [conv_id])
        conn.execute("DELETE FROM messages WHERE conversation_id = ?", [conv_id])
        conn.execute("DELETE FROM raw_events WHERE source_id = ? AND source_native_conversation_id = ?",
                     [source_id, conv.source_native_id])
        conn.execute(
            """
            UPDATE conversations SET
                title = ?,
                project = ?,
                started_at = ?,
                ended_at = ?,
                message_count = ?,
                content_hash = ?,
                raw_path = ?,
                metadata = ?
            WHERE id = ?
            """,
            [
                conv.title,
                conv.project,
                _as_utc(conv.started_at),
                _as_utc(conv.ended_at),
                len(conv.messages),
                content_hash,
                conv.raw_path,
                _json(conv.metadata),
                conv_id,
            ],
        )
    else:
        conv_id = _uuid()
        conn.execute(
            """
            INSERT INTO conversations (
                id, source_id, source_native_id, title, project,
                started_at, ended_at, message_count, content_hash,
                raw_path, metadata
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            [
                conv_id,
                source_id,
                conv.source_native_id,
                conv.title,
                conv.project,
                _as_utc(conv.started_at),
                _as_utc(conv.ended_at),
                len(conv.messages),
                content_hash,
                conv.raw_path,
                _json(conv.metadata),
            ],
        )

    # Insert messages and content blocks
    for seq, msg in enumerate(conv.messages):
        msg_id = _uuid()
        conn.execute(
            """
            INSERT INTO messages (
                id, conversation_id, source_native_id, parent_message_id,
                role, model, created_at, sequence_index, metadata
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            [
                msg_id,
                conv_id,
                msg.source_native_id,
                None,  # parent resolved in a second pass below
                msg.role.value,
                msg.model,
                _as_utc(msg.created_at),
                seq,
                _json(msg.metadata),
            ],
        )
        for bidx, block in enumerate(msg.blocks):
            conn.execute(
                """
                INSERT INTO content_blocks (
                    id, message_id, block_index, type, text,
                    tool_name, tool_use_id, payload
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
                """,
                [
                    _uuid(),
                    msg_id,
                    bidx,
                    block.type.value,
                    block.text,
                    block.tool_name,
                    block.tool_use_id,
                    _json(block.payload) if block.payload else None,
                ],
            )

    # Second pass: resolve parent_message_id references using source_native_id
    if any(m.parent_source_native_id for m in conv.messages):
        conn.execute(
            """
            UPDATE messages AS child
            SET parent_message_id = parent.id
            FROM messages AS parent
            WHERE child.conversation_id = ?
              AND parent.conversation_id = ?
              AND child.source_native_id IS NOT NULL
              AND parent.source_native_id IS NOT NULL
              AND child.source_native_id IN (
                SELECT source_native_id FROM messages
                WHERE conversation_id = ?
              )
              AND parent.source_native_id = (
                SELECT m2.source_native_id FROM messages m2
                WHERE m2.id = child.id
              )
            """,
            [conv_id, conv_id, conv_id],
        )
        # The above is a placeholder; precise parent resolution is adapter-dependent
        # and may be cleaner to do in the adapter. We resolve only the rows we have.
        # Adapters that need tree structure (ChatGPT) should populate parent IDs
        # directly when implementing parse().

    # Insert raw events if provided
    if conv.raw_events:
        for line_no, event in enumerate(conv.raw_events):
            conn.execute(
                """
                INSERT INTO raw_events (
                    id, source_id, source_native_conversation_id,
                    raw_path, line_number, payload
                ) VALUES (?, ?, ?, ?, ?, ?)
                """,
                [
                    _uuid(),
                    source_id,
                    conv.source_native_id,
                    conv.raw_path,
                    line_no,
                    json.dumps(event, default=str),
                ],
            )

    return conv_id

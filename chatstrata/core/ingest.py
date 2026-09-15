"""Source-agnostic ingester.

Takes a `ParsedConversation` from any adapter and persists it to DuckDB.
Idempotent: re-ingesting the same conversation preserves existing rows, appends
stable suffixes, and replaces rows only when previously stored content changed.
"""

from __future__ import annotations

import hashlib
import json
import uuid
from dataclasses import dataclass
from datetime import datetime, timezone
from enum import Enum
from typing import TYPE_CHECKING

from chatstrata.core.models import ParsedConversation, ParsedMessage

if TYPE_CHECKING:
    import duckdb


class IngestAction(str, Enum):
    """How an ingest changed the stored conversation."""

    INSERTED = "inserted"
    APPENDED = "appended"
    REPLACED = "replaced"
    UNCHANGED = "unchanged"


@dataclass(frozen=True)
class IngestOutcome:
    conversation_id: str
    action: IngestAction


def _uuid() -> str:
    return str(uuid.uuid4())


def _decode_json(value):
    """Normalize DuckDB JSON values and in-memory objects for comparisons."""
    if not isinstance(value, str):
        return value
    try:
        return json.loads(value)
    except (json.JSONDecodeError, TypeError):
        return value


def _canonical_json(value) -> str:
    return json.dumps(
        _decode_json(value),
        sort_keys=True,
        separators=(",", ":"),
        ensure_ascii=False,
        default=str,
    )


def _timestamp_key(dt: datetime | None) -> str | None:
    normalized = _as_utc(dt)
    return normalized.isoformat() if normalized is not None else None


def _message_payload(message: ParsedMessage) -> dict:
    return {
        "source_native_id": message.source_native_id,
        "parent_source_native_id": message.parent_source_native_id,
        "role": message.role.value,
        "model": message.model,
        "created_at": _timestamp_key(message.created_at),
        "metadata": message.metadata,
        "blocks": [
            {
                "type": block.type.value,
                "text": block.text,
                "tool_name": block.tool_name,
                "tool_use_id": block.tool_use_id,
                "payload": block.payload or None,
            }
            for block in message.blocks
        ],
    }


def _message_fingerprint(message: ParsedMessage) -> str:
    return _canonical_json(_message_payload(message))


def _raw_event_fingerprint(event: dict) -> str:
    return _canonical_json(event)


def _hash_content(conv: ParsedConversation) -> str:
    """Stable hash of every persisted message, block, and raw event field."""
    h = hashlib.sha256()
    for m in conv.messages:
        h.update(b"message\0")
        h.update(_message_fingerprint(m).encode("utf-8", errors="replace"))
    for event in conv.raw_events:
        h.update(b"raw_event\0")
        h.update(_raw_event_fingerprint(event).encode("utf-8", errors="replace"))
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
    conn: duckdb.DuckDBPyConnection,
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


def get_stored_mtime(
    conn: duckdb.DuckDBPyConnection,
    source_id: str,
    source_native_id: str,
) -> float | None:
    """Return the stored source_file_mtime for a conversation, or None if not found."""
    row = conn.execute(
        "SELECT source_file_mtime FROM conversations WHERE source_id = ? AND source_native_id = ?",
        [source_id, source_native_id],
    ).fetchone()
    if row is None:
        return None
    return row[0]


def _update_conversation(
    conn: duckdb.DuckDBPyConnection,
    conv_id: str,
    conv: ParsedConversation,
    content_hash: str,
    source_file_mtime: float | None,
    *,
    include_indexed_fields: bool = False,
) -> None:
    if include_indexed_fields:
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
                metadata = ?,
                source_file_mtime = ?
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
                source_file_mtime,
                conv_id,
            ],
        )
        return

    # DuckDB rejects updates to indexed columns on a row that is referenced by
    # a foreign key, even when the primary key itself is unchanged. Project and
    # started_at have secondary indexes, so append/no-op paths update only the
    # unindexed fields while messages still reference this conversation.
    conn.execute(
        """
        UPDATE conversations SET
            title = ?,
            ended_at = ?,
            message_count = ?,
            content_hash = ?,
            raw_path = ?,
            metadata = ?,
            source_file_mtime = ?
        WHERE id = ?
        """,
        [
            conv.title,
            _as_utc(conv.ended_at),
            len(conv.messages),
            content_hash,
            conv.raw_path,
            _json(conv.metadata),
            source_file_mtime,
            conv_id,
        ],
    )


def _insert_conversation(
    conn: duckdb.DuckDBPyConnection,
    source_id: str,
    conv: ParsedConversation,
    content_hash: str,
    source_file_mtime: float | None,
) -> str:
    conv_id = _uuid()
    conn.execute(
        """
        INSERT INTO conversations (
            id, source_id, source_native_id, title, project,
            started_at, ended_at, message_count, content_hash,
            raw_path, metadata, source_file_mtime
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
            source_file_mtime,
        ],
    )
    return conv_id


def _insert_messages(
    conn: duckdb.DuckDBPyConnection,
    conv_id: str,
    messages: list[ParsedMessage],
    *,
    start_index: int = 0,
) -> None:
    parent_refs: list[tuple[str, str]] = []
    for offset, msg in enumerate(messages):
        seq = start_index + offset
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
                None,
                msg.role.value,
                msg.model,
                _as_utc(msg.created_at),
                seq,
                _json(msg.metadata),
            ],
        )
        if msg.parent_source_native_id:
            parent_refs.append((msg_id, msg.parent_source_native_id))
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

    if parent_refs:
        parent_ids = dict(
            conn.execute(
                """
                SELECT source_native_id, id
                FROM messages
                WHERE conversation_id = ? AND source_native_id IS NOT NULL
                """,
                [conv_id],
            ).fetchall()
        )
        updates = [
            [parent_ids[parent_source_id], child_id]
            for child_id, parent_source_id in parent_refs
            if parent_source_id in parent_ids
        ]
        if updates:
            conn.executemany(
                "UPDATE messages SET parent_message_id = ? WHERE id = ?",
                updates,
            )


def _insert_raw_events(
    conn: duckdb.DuckDBPyConnection,
    source_id: str,
    conv: ParsedConversation,
    events: list[dict],
    *,
    start_line: int = 0,
) -> None:
    for offset, event in enumerate(events):
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
                start_line + offset,
                json.dumps(event, default=str),
            ],
        )


def _stored_message_fingerprints(
    conn: duckdb.DuckDBPyConnection,
    conv_id: str,
) -> list[str]:
    rows = conn.execute(
        """
        SELECT
            m.sequence_index,
            m.source_native_id,
            parent.source_native_id AS parent_source_native_id,
            m.role,
            m.model,
            m.created_at,
            m.metadata,
            cb.block_index,
            cb.type,
            cb.text,
            cb.tool_name,
            cb.tool_use_id,
            cb.payload
        FROM messages m
        LEFT JOIN messages parent ON parent.id = m.parent_message_id
        LEFT JOIN content_blocks cb ON cb.message_id = m.id
        WHERE m.conversation_id = ?
        ORDER BY m.sequence_index, cb.block_index
        """,
        [conv_id],
    ).fetchall()

    messages: list[dict] = []
    current_seq: int | None = None
    current: dict | None = None
    for row in rows:
        seq = row[0]
        if seq != current_seq:
            current = {
                "source_native_id": row[1],
                "parent_source_native_id": row[2],
                "role": row[3],
                "model": row[4],
                "created_at": _timestamp_key(row[5]),
                "metadata": _decode_json(row[6]),
                "blocks": [],
            }
            messages.append(current)
            current_seq = seq
        if row[7] is not None and current is not None:
            current["blocks"].append(
                {
                    "type": row[8],
                    "text": row[9],
                    "tool_name": row[10],
                    "tool_use_id": row[11],
                    "payload": _decode_json(row[12]),
                }
            )

    return [_canonical_json(message) for message in messages]


def _stored_raw_event_fingerprints(
    conn: duckdb.DuckDBPyConnection,
    source_id: str,
    source_native_id: str,
) -> list[str]:
    rows = conn.execute(
        """
        SELECT payload
        FROM raw_events
        WHERE source_id = ? AND source_native_conversation_id = ?
        ORDER BY line_number
        """,
        [source_id, source_native_id],
    ).fetchall()
    return [_canonical_json(payload) for (payload,) in rows]


def _is_prefix(stored: list[str], incoming: list[str]) -> bool:
    return len(stored) <= len(incoming) and stored == incoming[: len(stored)]


def _clear_conversation_content(
    conn: duckdb.DuckDBPyConnection,
    source_id: str,
    source_native_id: str,
    conv_id: str,
) -> None:
    conn.execute(
        """
        DELETE FROM content_blocks
        WHERE message_id IN (SELECT id FROM messages WHERE conversation_id = ?)
        """,
        [conv_id],
    )
    conn.execute(
        """
        DELETE FROM attachments
        WHERE message_id IN (SELECT id FROM messages WHERE conversation_id = ?)
        """,
        [conv_id],
    )
    conn.execute(
        """
        DELETE FROM message_embeddings
        WHERE message_id IN (SELECT id FROM messages WHERE conversation_id = ?)
        """,
        [conv_id],
    )
    conn.execute("DELETE FROM messages WHERE conversation_id = ?", [conv_id])
    conn.execute(
        """
        DELETE FROM raw_events
        WHERE source_id = ? AND source_native_conversation_id = ?
        """,
        [source_id, source_native_id],
    )


def ingest_conversation_with_status(
    conn: duckdb.DuckDBPyConnection,
    source_id: str,
    conv: ParsedConversation,
    *,
    source_file_mtime: float | None = None,
) -> IngestOutcome:
    """Persist a conversation while avoiding destructive rewrites when possible."""
    content_hash = _hash_content(conv)
    existing = conn.execute(
        """
        SELECT id, content_hash, project, started_at
        FROM conversations
        WHERE source_id = ? AND source_native_id = ?
        """,
        [source_id, conv.source_native_id],
    ).fetchone()

    if existing is None:
        conv_id = _insert_conversation(
            conn,
            source_id,
            conv,
            content_hash,
            source_file_mtime,
        )
        _insert_messages(conn, conv_id, conv.messages)
        _insert_raw_events(conn, source_id, conv, conv.raw_events)
        return IngestOutcome(conv_id, IngestAction.INSERTED)

    conv_id, stored_hash, stored_project, stored_started_at = existing
    indexed_fields_changed = (
        stored_project != conv.project
        or _timestamp_key(stored_started_at) != _timestamp_key(conv.started_at)
    )
    if stored_hash == content_hash and not indexed_fields_changed:
        _update_conversation(
            conn,
            conv_id,
            conv,
            content_hash,
            source_file_mtime,
        )
        return IngestOutcome(conv_id, IngestAction.UNCHANGED)

    stored_messages = _stored_message_fingerprints(conn, conv_id)
    incoming_messages = [_message_fingerprint(message) for message in conv.messages]
    stored_events = _stored_raw_event_fingerprints(
        conn,
        source_id,
        conv.source_native_id,
    )
    incoming_events = [_raw_event_fingerprint(event) for event in conv.raw_events]

    if (
        not indexed_fields_changed
        and _is_prefix(stored_messages, incoming_messages)
        and _is_prefix(stored_events, incoming_events)
    ):
        _update_conversation(
            conn,
            conv_id,
            conv,
            content_hash,
            source_file_mtime,
        )
        _insert_messages(
            conn,
            conv_id,
            conv.messages[len(stored_messages) :],
            start_index=len(stored_messages),
        )
        _insert_raw_events(
            conn,
            source_id,
            conv,
            conv.raw_events[len(stored_events) :],
            start_line=len(stored_events),
        )
        action = (
            IngestAction.APPENDED
            if len(stored_messages) < len(incoming_messages)
            or len(stored_events) < len(incoming_events)
            else IngestAction.UNCHANGED
        )
        return IngestOutcome(conv_id, action)

    _clear_conversation_content(
        conn,
        source_id,
        conv.source_native_id,
        conv_id,
    )
    _update_conversation(
        conn,
        conv_id,
        conv,
        content_hash,
        source_file_mtime,
        include_indexed_fields=True,
    )
    _insert_messages(conn, conv_id, conv.messages)
    _insert_raw_events(conn, source_id, conv, conv.raw_events)
    return IngestOutcome(conv_id, IngestAction.REPLACED)


def ingest_conversation(
    conn: duckdb.DuckDBPyConnection,
    source_id: str,
    conv: ParsedConversation,
    *,
    source_file_mtime: float | None = None,
) -> str:
    """Persist a conversation and return its stable chatstrata id."""
    outcome = ingest_conversation_with_status(
        conn,
        source_id,
        conv,
        source_file_mtime=source_file_mtime,
    )
    return outcome.conversation_id

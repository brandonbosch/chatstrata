"""OpenCode adapter.

OpenCode stores session data in a SQLite database at:
    ~/.local/share/opencode/opencode.db

The database has three main tables:

    session  — one row per coding session (id, title, directory, timestamps)
    message  — one row per conversational turn, with a JSON `data` column
               containing role, model info, token usage, and timing
    part     — one row per content unit within a message, with a JSON `data`
               column. Part types:
        text        — plain text content
        reasoning   — chain-of-thought / thinking trace
        tool        — tool invocation with input, output, and status
        patch       — file diff snapshot (hash + file list)
        step-start  — internal lifecycle marker (skipped)
        step-finish — token accounting marker (skipped)

All timestamps are Unix milliseconds.
"""

from __future__ import annotations

import json
import sqlite3
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

DEFAULT_DB_PATH = Path("~/.local/share/opencode/opencode.db").expanduser()


def _ms_to_datetime(ms: Any) -> datetime | None:
    if not ms:
        return None
    try:
        return datetime.fromtimestamp(int(ms) / 1000, tz=timezone.utc)
    except (ValueError, TypeError, OSError):
        return None


def _parse_json(text: str) -> dict[str, Any]:
    try:
        return json.loads(text)
    except (json.JSONDecodeError, TypeError):
        return {}


def _open_readonly(db_path: Path) -> sqlite3.Connection:
    uri = f"file:{db_path}?mode=ro"
    conn = sqlite3.connect(uri, uri=True)
    conn.row_factory = sqlite3.Row
    return conn


def _blocks_from_part(part_data: dict[str, Any]) -> list[ContentBlock]:
    ptype = part_data.get("type")

    if ptype == "text":
        text = part_data.get("text")
        if text:
            return [ContentBlock(type=BlockType.TEXT, text=text)]
        return []

    if ptype == "reasoning":
        text = part_data.get("text")
        time_info = part_data.get("time", {})
        return [
            ContentBlock(
                type=BlockType.THINKING,
                text=text or None,
                payload={
                    k: v
                    for k, v in {
                        "start_ms": time_info.get("start"),
                        "end_ms": time_info.get("end"),
                    }.items()
                    if v is not None
                },
            )
        ]

    if ptype == "patch":
        return [
            ContentBlock(
                type=BlockType.TOOL_RESULT,
                tool_name="patch",
                text=None,
                payload={
                    "hash": part_data.get("hash"),
                    "files": part_data.get("files", []),
                },
            )
        ]

    return []


def _tool_messages(
    part_data: dict[str, Any],
    msg_model: str | None,
    ts: datetime | None,
) -> list[ParsedMessage]:
    """Convert a tool part into TOOL_USE and optionally TOOL_RESULT messages."""
    state = part_data.get("state", {})
    call_id = part_data.get("callID")
    tool_name = part_data.get("tool")
    status = state.get("status")

    tool_use = ParsedMessage(
        source_native_id=call_id,
        role=Role.ASSISTANT,
        model=msg_model,
        created_at=ts,
        blocks=[
            ContentBlock(
                type=BlockType.TOOL_USE,
                tool_name=tool_name,
                tool_use_id=call_id,
                payload={"input": state.get("input", {}), "status": status},
            )
        ],
        metadata={},
    )

    messages = [tool_use]

    if status == "completed":
        output = state.get("output")
        if output is None:
            meta = state.get("metadata", {})
            output = meta.get("output")
        result_text = output if isinstance(output, str) else None

        messages.append(
            ParsedMessage(
                source_native_id=call_id,
                role=Role.TOOL,
                created_at=ts,
                blocks=[
                    ContentBlock(
                        type=BlockType.TOOL_RESULT,
                        tool_use_id=call_id,
                        text=result_text,
                        payload={"status": status},
                    )
                ],
                metadata={},
            )
        )

    return messages


class OpenCodeAdapter:
    """Adapter for OpenCode session transcripts stored in SQLite."""

    name = "opencode"
    display_name = "OpenCode"
    version = "0.1.0"
    schema_version = 1

    def discover(self, config: dict | None = None) -> Iterable[ConversationHandle]:
        db_path = Path((config or {}).get("path") or DEFAULT_DB_PATH).expanduser()
        if not db_path.exists():
            return

        conn = _open_readonly(db_path)
        try:
            rows = conn.execute(
                "SELECT id, title, directory, time_created, time_updated "
                "FROM session ORDER BY time_created"
            ).fetchall()
            for row in rows:
                yield ConversationHandle(
                    source_native_id=row["id"],
                    path=db_path,
                    metadata={
                        "title": row["title"],
                        "directory": row["directory"],
                    },
                )
        finally:
            conn.close()

    def parse(self, handle: ConversationHandle) -> ParsedConversation:
        if handle.path is None:
            raise ValueError("OpenCodeAdapter requires a path on the handle")

        conn = _open_readonly(handle.path)
        try:
            return self._parse_session(conn, handle)
        finally:
            conn.close()

    def _parse_session(
        self, conn: sqlite3.Connection, handle: ConversationHandle
    ) -> ParsedConversation:
        session_id = handle.source_native_id

        session_row = conn.execute(
            "SELECT id, title, directory, time_created, time_updated "
            "FROM session WHERE id = ?",
            (session_id,),
        ).fetchone()

        if session_row is None:
            raise ValueError(f"Session {session_id!r} not found in database")

        msg_rows = conn.execute(
            "SELECT id, data, time_created FROM message "
            "WHERE session_id = ? ORDER BY time_created",
            (session_id,),
        ).fetchall()

        part_rows = conn.execute(
            "SELECT id, message_id, data, time_created FROM part "
            "WHERE session_id = ? ORDER BY time_created",
            (session_id,),
        ).fetchall()

        parts_by_msg: dict[str, list[dict[str, Any]]] = {}
        for pr in part_rows:
            mid = pr["message_id"]
            parts_by_msg.setdefault(mid, []).append(
                {"id": pr["id"], "data": _parse_json(pr["data"]), "time_created": pr["time_created"]}
            )

        messages: list[ParsedMessage] = []
        raw_events: list[dict[str, Any]] = []
        started_at: datetime | None = None
        ended_at: datetime | None = None

        for msg_row in msg_rows:
            msg_data = _parse_json(msg_row["data"])
            msg_id = msg_row["id"]
            msg_parts = parts_by_msg.get(msg_id, [])

            raw_event: dict[str, Any] = {
                "id": msg_id,
                "data": msg_data,
                "time_created": msg_row["time_created"],
                "parts": [{"id": p["id"], "data": p["data"]} for p in msg_parts],
            }
            raw_events.append(raw_event)

            role_str = msg_data.get("role", "")
            time_info = msg_data.get("time", {})
            ts = _ms_to_datetime(time_info.get("created") or msg_row["time_created"])

            completed_ts = _ms_to_datetime(time_info.get("completed"))

            if ts:
                if started_at is None or ts < started_at:
                    started_at = ts
                if ended_at is None or ts > ended_at:
                    ended_at = ts
            if completed_ts:
                if ended_at is None or completed_ts > ended_at:
                    ended_at = completed_ts

            model_info = msg_data.get("model")
            if isinstance(model_info, dict):
                model_id = model_info.get("modelID") or msg_data.get("modelID")
                provider_id = model_info.get("providerID") or msg_data.get("providerID")
            else:
                model_id = msg_data.get("modelID")
                provider_id = msg_data.get("providerID")

            role = Role.USER if role_str == "user" else Role.ASSISTANT
            msg_metadata: dict[str, Any] = {}
            if provider_id:
                msg_metadata["provider_id"] = provider_id
            agent = msg_data.get("agent")
            if agent:
                msg_metadata["agent"] = agent
            mode = msg_data.get("mode")
            if mode:
                msg_metadata["mode"] = mode
            tokens = msg_data.get("tokens")
            if tokens:
                msg_metadata["tokens"] = tokens

            non_tool_blocks: list[ContentBlock] = []
            for part in msg_parts:
                pdata = part["data"]
                ptype = pdata.get("type")

                if ptype in ("step-start", "step-finish"):
                    continue

                if ptype == "tool":
                    if non_tool_blocks:
                        messages.append(
                            ParsedMessage(
                                source_native_id=msg_id,
                                role=role,
                                model=model_id if role == Role.ASSISTANT else None,
                                created_at=ts,
                                blocks=non_tool_blocks,
                                metadata=msg_metadata,
                            )
                        )
                        non_tool_blocks = []
                    messages.extend(
                        _tool_messages(pdata, model_id if role == Role.ASSISTANT else None, ts)
                    )
                    continue

                non_tool_blocks.extend(_blocks_from_part(pdata))

            if non_tool_blocks:
                messages.append(
                    ParsedMessage(
                        source_native_id=msg_id,
                        role=role,
                        model=model_id if role == Role.ASSISTANT else None,
                        created_at=ts,
                        blocks=non_tool_blocks,
                        metadata=msg_metadata,
                    )
                )

        title = handle.metadata.get("title") or (session_row["title"] if session_row else None)
        project = handle.metadata.get("directory") or (
            session_row["directory"] if session_row else None
        )

        return ParsedConversation(
            source_native_id=session_id,
            title=title,
            project=project,
            started_at=started_at,
            ended_at=ended_at,
            messages=messages,
            raw_path=str(handle.path),
            metadata={},
            raw_events=raw_events,
        )

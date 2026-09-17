"""Hermes Agent adapter.

Hermes Agent (Nous Research) persists its canonical session store in SQLite
databases under its Hermes home. Sessions live in the `sessions` table and
their messages in the `messages` table, ordered by (timestamp, id).

Where the databases are found (see `_candidate_databases`):

    $HERMES_HOME/state.db                     the default profile's store
    $HERMES_HOME/profiles/<name>/state.db     every named profile's store

`HERMES_HOME` defaults to `~/.hermes` when unset. When it already points at a
named profile (`<root>/profiles/<name>`) the containing root is searched, so a
process running as one profile still sees the default store and its siblings.
A source config narrows the search: `{"path": "/path/to/state.db"}` ingests
exactly that store, `{"profile": "work"}` exactly that named profile.

Discovery is loud about a store it cannot read: nothing found, an unopenable
file, and a database without a `sessions` table each raise, so the CLI can name
the source and the reason instead of reporting an empty archive. "Found the
store, it holds no sessions" is the only silent outcome.

Message shapes observed in real databases:

    user      — content holds the prompt text
    assistant — content is the visible reply (may be empty when the message
                is only tool calls); `reasoning`/`reasoning_content` hold the
                chain-of-thought; `tool_calls` is a JSON array of
                {id, type: "function", function: {name, arguments}}
    tool      — one tool result per row: content is the output,
                tool_call_id links back to the assistant's call,
                tool_name is the invoked tool
    system    — system prompts and summaries

Compaction columns: rows with active = 0 and compacted = 0 are historical
versions that are no longer part of the conversation and are skipped. Rows
with _compressed_summary = 1 are compaction summaries; they are kept as
system-role messages with a `compressed_summary` flag in metadata.

The adapter opens the database in read-only mode so ingestion can never
mutate the live Hermes session store. Because the source is a database, not
files, `raw_events` carry one entry per raw message row (and the session
row) so users can re-parse later if normalization improves.
"""

from __future__ import annotations

import json
import os
import re
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

DEFAULT_HERMES_HOME = "~/.hermes"

# Mirrors hermes_cli.profiles._PROFILE_ID_RE: the only directory names Hermes
# recognises as a named profile under <root>/profiles/.
PROFILE_ID_RE = re.compile(r"^[a-z0-9][a-z0-9_-]{0,63}$")

_SESSION_COLUMNS = (
    "id, title, model, source, started_at, ended_at, message_count, cwd, display_name"
)

_ROLE_MAP: dict[str, Role] = {
    "user": Role.USER,
    "assistant": Role.ASSISTANT,
    "system": Role.SYSTEM,
    "tool": Role.TOOL,
}


class HermesStateDatabaseError(RuntimeError):
    """A store exists at the expected path but is not a usable Hermes state DB."""


def _hermes_root() -> Path:
    """The Hermes home owning the profile tree: ``$HERMES_HOME`` or ``~/.hermes``.

    A ``HERMES_HOME`` that already points at a named profile
    (``<root>/profiles/<name>``, how Hermes binds a non-launch profile) resolves
    to ``<root>`` so the default store and the sibling profiles stay visible.
    """
    env = os.environ.get("HERMES_HOME", "").strip()
    if not env:
        return Path(DEFAULT_HERMES_HOME).expanduser()
    home = Path(env).expanduser()
    if home.parent.name == "profiles" and PROFILE_ID_RE.match(home.name):
        return home.parent.parent
    return home


def _candidate_databases(config: dict | None) -> list[tuple[str, Path]]:
    """Every store to read, as (profile_name, path) pairs, in ingest order.

    An explicit ``path`` is authoritative — it is exactly the store to ingest.
    ``profile`` selects one named profile; otherwise the default store plus every
    named profile under the Hermes root are candidates.
    """
    cfg = config or {}
    if cfg.get("path"):
        return [(cfg.get("profile") or "default", Path(cfg["path"]).expanduser())]

    root = _hermes_root()
    profile = cfg.get("profile")
    if profile:
        if profile == "default":
            return [("default", root / "state.db")]
        return [(profile, root / "profiles" / profile / "state.db")]

    candidates = [("default", root / "state.db")]
    profiles_dir = root / "profiles"
    if profiles_dir.is_dir():
        for entry in sorted(profiles_dir.iterdir()):
            if entry.is_dir() and entry.name != "default" and PROFILE_ID_RE.match(entry.name):
                candidates.append((entry.name, entry / "state.db"))
    return candidates


def _db_path(config: dict | None = None) -> Path:
    """The store this adapter reads when a handle carries no path of its own."""
    return _candidate_databases(config)[0][1]


def _native_id(profile: str, session_id: str) -> str:
    """Archive-wide session id; qualified by profile for every non-default store.

    A profile can be cloned from another (``hermes -p work --clone default``),
    which copies session ids verbatim, so the raw id alone does not identify a
    session within this source.
    """
    return session_id if profile == "default" else f"{profile}/{session_id}"


def _connect_readonly(db: Path) -> sqlite3.Connection:
    if not db.exists():
        raise FileNotFoundError(f"Hermes state database not found: {db}")
    # mode=ro guarantees the archive build never mutates the live store.
    conn = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
    conn.row_factory = sqlite3.Row
    return conn


def _to_datetime(ts: Any) -> datetime | None:
    if ts is None:
        return None
    try:
        return datetime.fromtimestamp(float(ts), tz=timezone.utc)
    except (TypeError, ValueError, OSError):
        return None


def _row_to_dict(row: sqlite3.Row) -> dict[str, Any]:
    d = dict(row)
    # BLOBs (display_identity) aren't JSON-serializable; drop them.
    return {k: v for k, v in d.items() if not isinstance(v, bytes)}


def _parse_tool_calls(raw: Any) -> list[dict[str, Any]]:
    """Parse the tool_calls JSON column; tolerate malformed/legacy values."""
    if isinstance(raw, list):
        return [c for c in raw if isinstance(c, dict)]
    if isinstance(raw, str) and raw.strip():
        try:
            parsed = json.loads(raw)
        except json.JSONDecodeError:
            return []
        if isinstance(parsed, list):
            return [c for c in parsed if isinstance(c, dict)]
    return []


def _message_blocks(row: sqlite3.Row) -> tuple[Role | None, list[ContentBlock]]:
    role = _ROLE_MAP.get(row["role"])
    if role is None:
        return None, []

    blocks: list[ContentBlock] = []

    # Assistant tool calls: one TOOL_USE block per call in the JSON array.
    if role == Role.ASSISTANT:
        for call in _parse_tool_calls(row["tool_calls"]):
            fn = call.get("function") or {}
            if not isinstance(fn, dict):
                continue
            blocks.append(
                ContentBlock(
                    type=BlockType.TOOL_USE,
                    tool_name=fn.get("name"),
                    tool_use_id=call.get("id") or call.get("call_id"),
                    payload={"arguments": fn.get("arguments", "")},
                )
            )

    text = row["content"]
    if text:
        blocks.append(ContentBlock(type=BlockType.TEXT, text=text))

    # Assistant chain-of-thought.
    if role == Role.ASSISTANT:
        reasoning = row["reasoning"] or row["reasoning_content"]
        if reasoning:
            blocks.append(ContentBlock(type=BlockType.THINKING, text=reasoning))

    if role == Role.TOOL:
        blocks.append(
            ContentBlock(
                type=BlockType.TOOL_RESULT,
                tool_use_id=row["tool_call_id"],
                tool_name=row["tool_name"],
                text=text or None,
            )
        )
        if text:
            blocks.pop(0)  # text already folded into the TOOL_RESULT block

    return role, blocks


class HermesAgentAdapter:
    """Adapter for Hermes Agent's canonical SQLite session store."""

    name = "hermes_agent"
    display_name = "Hermes Agent"
    version = "0.1.0"
    schema_version = 1

    def discover(self, config: dict | None = None) -> Iterable[ConversationHandle]:
        """List sessions across every store this adapter can read.

        Raises FileNotFoundError when no store exists at any candidate path, and
        :class:`HermesStateDatabaseError` when one exists but is unreadable or is
        not a Hermes session store — an empty result means the store itself is
        fine and holds no sessions.
        """
        candidates = _candidate_databases(config)
        present = [(profile, db) for profile, db in candidates if db.exists()]
        if not present:
            searched = ", ".join(str(db) for _profile, db in candidates)
            raise FileNotFoundError(
                f"Hermes state database not found: {searched} "
                "(set HERMES_HOME, or pass --path to pick one store)"
            )

        handles: list[ConversationHandle] = []
        for profile, db in present:
            try:
                conn = _connect_readonly(db)
            except sqlite3.Error as exc:
                raise HermesStateDatabaseError(
                    f"Hermes state database is not readable: {db} ({exc})"
                ) from exc
            try:
                rows = conn.execute(
                    f"SELECT {_SESSION_COLUMNS} FROM sessions ORDER BY started_at"
                ).fetchall()
            except sqlite3.Error as exc:
                raise HermesStateDatabaseError(
                    f"Hermes state database has no usable 'sessions' table: {db} ({exc})"
                ) from exc
            finally:
                conn.close()

            for row in rows:
                handles.append(
                    ConversationHandle(
                        source_native_id=_native_id(profile, row["id"]),
                        path=db,
                        metadata={
                            "profile": profile,
                            "session_id": row["id"],
                            "source": row["source"],
                            "model": row["model"],
                            "message_count": row["message_count"],
                        },
                    )
                )

        return handles

    def parse(self, handle: ConversationHandle) -> ParsedConversation:
        db = handle.path or _db_path()
        # Handles from a named-profile store carry a profile-qualified id; the
        # raw session id is kept alongside it in metadata.
        session_id = handle.metadata.get("session_id") or handle.source_native_id
        conn = _connect_readonly(Path(db))
        try:
            session = conn.execute(
                "SELECT * FROM sessions WHERE id = ?", (session_id,)
            ).fetchone()
            if session is None:
                raise ValueError(f"Session not found: {session_id}")

            rows = conn.execute(
                """
                SELECT * FROM messages
                WHERE session_id = ?
                ORDER BY timestamp, id
                """,
                (session_id,),
            ).fetchall()
        finally:
            conn.close()

        messages: list[ParsedMessage] = []
        raw_events: list[dict[str, Any]] = [_row_to_dict(session)]

        for row in rows:
            raw_events.append(_row_to_dict(row))

            # Inactive rows that were never compacted are superseded versions
            # (e.g. after a rewind) — they are not part of the conversation.
            if not row["active"] and not row["compacted"]:
                continue

            role, blocks = _message_blocks(row)
            if role is None or not blocks:
                continue

            metadata: dict[str, Any] = {
                "hermes_message_id": row["id"],
                "finish_reason": row["finish_reason"],
                "source": session["source"],
                "profile_name": session["profile_name"],
            }
            if row["compacted"]:
                metadata["compacted"] = True
            if row["_compressed_summary"]:
                metadata["compressed_summary"] = True

            messages.append(
                ParsedMessage(
                    source_native_id=str(row["id"]),
                    role=role,
                    model=session["model"] if role == Role.ASSISTANT else None,
                    created_at=_to_datetime(row["timestamp"]),
                    blocks=blocks,
                    metadata=metadata,
                )
            )

        started_at = _to_datetime(session["started_at"])
        ended_at = _to_datetime(session["ended_at"])
        if started_at is None or ended_at is None:
            for m in messages:
                if m.created_at is None:
                    continue
                if started_at is None or m.created_at < started_at:
                    started_at = m.created_at
                if ended_at is None or m.created_at > ended_at:
                    ended_at = m.created_at

        title = session["title"] or session["display_name"]
        if not title:
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
            project=session["cwd"],
            started_at=started_at,
            ended_at=ended_at,
            messages=messages,
            raw_path=str(db),
            metadata={
                "hermes_source": session["source"],
                "model": session["model"],
                "session_message_count": session["message_count"],
                "tool_call_count": session["tool_call_count"],
            },
            raw_events=raw_events,
        )

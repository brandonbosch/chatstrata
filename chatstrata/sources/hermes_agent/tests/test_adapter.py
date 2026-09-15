"""Tests for HermesAgentAdapter against a synthetic fixture database."""

from __future__ import annotations

import json
import sqlite3
from pathlib import Path

import pytest

from chatstrata.core.models import BlockType, ConversationHandle, Role
from chatstrata.sources.hermes_agent.adapter import (
    HermesAgentAdapter,
    _parse_tool_calls,
)

# Sanitized schema subset mirroring the shape of ~/.hermes/state.db.
SESSION_SCHEMA = """
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    source TEXT NOT NULL,
    display_name TEXT,
    model TEXT,
    system_prompt TEXT,
    started_at REAL NOT NULL,
    ended_at REAL,
    message_count INTEGER DEFAULT 0,
    tool_call_count INTEGER DEFAULT 0,
    cwd TEXT,
    profile_name TEXT,
    title TEXT
);
CREATE TABLE messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    role TEXT NOT NULL,
    content TEXT,
    tool_call_id TEXT,
    tool_calls TEXT,
    tool_name TEXT,
    timestamp REAL NOT NULL,
    finish_reason TEXT,
    reasoning TEXT,
    reasoning_content TEXT,
    active INTEGER NOT NULL DEFAULT 1,
    compacted INTEGER NOT NULL DEFAULT 0,
    _compressed_summary INTEGER NOT NULL DEFAULT 0,
    display_identity BLOB
);
"""


def build_fixture_db(path: Path) -> None:
    conn = sqlite3.connect(path)
    conn.executescript(SESSION_SCHEMA)
    conn.execute(
        "INSERT INTO sessions (id, source, model, title, started_at, ended_at,"
        " message_count, tool_call_count, cwd, profile_name)"
        " VALUES (?, 'cli', 'test-model', 'Fix the login bug',"
        " 1000.0, 1005.0, 6, 2, '/home/example/project', 'default')",
        ("sess_1",),
    )
    rows = [
        # (role, content, tool_call_id, tool_calls, tool_name, ts, finish,
        #  reasoning, active, compacted, compressed)
        ("system", "You are a helpful agent.", None, None, None, 1000.1, None, None, 1, 0, 0),
        ("user", "The login endpoint returns 500. Please fix it.", None, None, None,
         1000.2, None, None, 1, 0, 0),
        ("assistant", "Let me look at the auth code.", None, json.dumps([
            {"id": "call_1", "type": "function",
             "function": {"name": "search_files", "arguments": "{\"pattern\": \"auth\"}"}},
        ]), None, 1001.0, "tool_calls", "The user reports a 500 on login.", 1, 0, 0),
        ("tool", "/src/auth.py", "call_1", None, "search_files", 1001.5, None,
         None, 1, 0, 0),
        # Superseded version after a rewind: skipped.
        ("assistant", "outdated draft reply", None, None, None, 1002.0, "stop",
         None, 0, 0, 0),
        # Compaction summary: kept, flagged.
        ("assistant", "Summary of earlier turns: the login bug was diagnosed.", None,
         None, None, 1002.5, "stop", None, 1, 0, 1),
        ("assistant", "Fixed the bug in auth.py.", None, None, None, 1003.0,
         "stop", "The fix is a one-liner.", 1, 0, 0),
        ("user", "", None, None, None, 1004.0, None, None, 1, 0, 0),  # empty: skipped
        # Compacted row remains part of the conversation.
        ("user", "earlier context (compacted)", None, None, None, 999.0, None,
         None, 1, 1, 0),
    ]
    for r in rows:
        conn.execute(
            "INSERT INTO messages (session_id, role, content, tool_call_id,"
            " tool_calls, tool_name, timestamp, finish_reason, reasoning,"
            " active, compacted, _compressed_summary)"
            " VALUES ('sess_1', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", r
        )
    # Second session, for discover() coverage.
    conn.execute(
        "INSERT INTO sessions (id, source, model, title, started_at, ended_at,"
        " message_count, tool_call_count, cwd, profile_name)"
        " VALUES (?, 'telegram', 'test-model', 'Plan sprint', 2000.0, NULL,"
        " 1, 0, NULL, 'default')",
        ("sess_2",),
    )
    conn.execute(
        "INSERT INTO messages (session_id, role, content, timestamp)"
        " VALUES ('sess_2', 'user', 'hello', 2000.5)"
    )
    conn.commit()
    conn.close()


@pytest.fixture
def adapter() -> HermesAgentAdapter:
    return HermesAgentAdapter()


@pytest.fixture
def db_path(tmp_path: Path) -> Path:
    db = tmp_path / "state.db"
    build_fixture_db(db)
    return db


def test_discover_lists_sessions(adapter, db_path):
    handles = list(adapter.discover({"path": str(db_path)}))
    assert [h.source_native_id for h in handles] == ["sess_1", "sess_2"]
    assert all(h.path == db_path for h in handles)
    assert handles[0].metadata["source"] == "cli"


def test_discover_missing_db_yields_nothing(adapter, tmp_path):
    assert list(adapter.discover({"path": str(tmp_path / "nope.db")})) == []


def test_parse_conversation_fields(adapter, db_path):
    conv = adapter.parse(
        ConversationHandle(source_native_id="sess_1", path=db_path, metadata={})
    )
    assert conv.title == "Fix the login bug"
    assert conv.project == "/home/example/project"
    assert conv.started_at is not None and conv.ended_at is not None
    assert conv.started_at.tzinfo is not None
    assert conv.metadata["hermes_source"] == "cli"
    assert conv.metadata["model"] == "test-model"
    assert conv.raw_path == str(db_path)


def test_parse_roles_and_blocks(adapter, db_path):
    conv = adapter.parse(
        ConversationHandle(source_native_id="sess_1", path=db_path, metadata={})
    )
    roles = [m.role for m in conv.messages]
    # Rows sort by timestamp: compacted user (999) is first.
    assert roles == [
        Role.USER,       # compacted earlier context
        Role.SYSTEM,
        Role.USER,
        Role.ASSISTANT,
        Role.TOOL,
        Role.ASSISTANT,  # compaction summary
        Role.ASSISTANT,
    ]

    assistant = conv.messages[3]
    assert [b.type for b in assistant.blocks] == [
        BlockType.TOOL_USE, BlockType.TEXT, BlockType.THINKING,
    ]
    assert assistant.blocks[0].tool_name == "search_files"
    assert assistant.blocks[0].tool_use_id == "call_1"
    assert json.loads(assistant.blocks[0].payload["arguments"]) == {"pattern": "auth"}
    assert assistant.blocks[2].text == "The user reports a 500 on login."
    assert assistant.model == "test-model"
    assert assistant.created_at.tzinfo is not None

    tool = conv.messages[4]
    assert tool.blocks[0].type == BlockType.TOOL_RESULT
    assert tool.blocks[0].tool_use_id == "call_1"
    assert tool.blocks[0].tool_name == "search_files"
    assert tool.blocks[0].text == "/src/auth.py"


def test_parse_skips_superseded_and_empty_rows(adapter, db_path):
    conv = adapter.parse(
        ConversationHandle(source_native_id="sess_1", path=db_path, metadata={})
    )
    texts = [b.text for m in conv.messages for b in m.blocks if b.type == BlockType.TEXT]
    assert "outdated draft reply" not in texts
    assert "" not in texts
    assert "earlier context (compacted)" in texts
    # Compaction summary is flagged in metadata.
    assert conv.messages[5].metadata.get("compressed_summary") is True


def test_parse_preserves_raw_events(adapter, db_path):
    conv = adapter.parse(
        ConversationHandle(source_native_id="sess_1", path=db_path, metadata={})
    )
    # 1 session row + 9 message rows; BLOB columns dropped, JSON-serializable.
    assert len(conv.raw_events) == 10
    json.dumps(conv.raw_events)


def test_parse_missing_session_raises(adapter, db_path):
    with pytest.raises(ValueError, match="not found"):
        adapter.parse(
            ConversationHandle(source_native_id="missing", path=db_path, metadata={})
        )


def test_parse_tool_calls_tolerates_bad_input():
    assert _parse_tool_calls(None) == []
    assert _parse_tool_calls("") == []
    assert _parse_tool_calls("not json") == []
    assert _parse_tool_calls('{"not": "a list"}') == []
    calls = _parse_tool_calls('[{"id": "c1", "function": {"name": "t", "arguments": "{}"}}]')
    assert calls[0]["id"] == "c1"

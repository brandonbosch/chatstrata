"""Tests for HermesAgentAdapter against a synthetic fixture database."""

from __future__ import annotations

import json
import sqlite3
from pathlib import Path

import pytest

from chatstrata.core.models import BlockType, ConversationHandle, Role
from chatstrata.sources.hermes_agent.adapter import (
    HermesAgentAdapter,
    HermesStateDatabaseError,
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


@pytest.fixture
def hermes_home(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    """An isolated Hermes root: the adapter reads HERMES_HOME, never ~/.hermes."""
    home = tmp_path / "hermes"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    return home


def _profile_store(hermes_home: Path, name: str) -> Path:
    profile_dir = hermes_home / "profiles" / name
    profile_dir.mkdir(parents=True)
    build_fixture_db(profile_dir / "state.db")
    return profile_dir / "state.db"


def test_discover_lists_sessions(adapter, db_path):
    handles = list(adapter.discover({"path": str(db_path)}))
    assert [h.source_native_id for h in handles] == ["sess_1", "sess_2"]
    assert all(h.path == db_path for h in handles)
    assert handles[0].metadata["source"] == "cli"
    assert handles[0].metadata["profile"] == "default"


def test_discover_missing_db_raises_with_the_path(adapter, tmp_path):
    missing = tmp_path / "nope.db"
    with pytest.raises(FileNotFoundError) as excinfo:
        adapter.discover({"path": str(missing)})
    assert "Hermes state database not found" in str(excinfo.value)
    assert str(missing) in str(excinfo.value)


def test_discover_honours_hermes_home(adapter, hermes_home):
    build_fixture_db(hermes_home / "state.db")
    handles = list(adapter.discover())
    assert [h.source_native_id for h in handles] == ["sess_1", "sess_2"]
    assert all(h.path == hermes_home / "state.db" for h in handles)


def test_discover_without_a_store_names_what_it_searched(adapter, hermes_home):
    # A profile dir that exists but was never used is not an error in itself,
    # but with no store anywhere the failure must name the paths, not read empty.
    (hermes_home / "profiles" / "work").mkdir(parents=True)
    with pytest.raises(FileNotFoundError) as excinfo:
        adapter.discover()
    assert "Hermes state database not found" in str(excinfo.value)
    assert str(hermes_home / "state.db") in str(excinfo.value)


def test_discover_reads_the_default_store_and_every_profile(adapter, hermes_home):
    build_fixture_db(hermes_home / "state.db")
    _profile_store(hermes_home, "work")
    _profile_store(hermes_home, "ops")
    # A name Hermes would not accept as a profile id is not a profile store.
    stray = hermes_home / "profiles" / "Not A Profile"
    stray.mkdir(parents=True)
    build_fixture_db(stray / "state.db")

    handles = list(adapter.discover())

    # Default store first (ids unqualified), then named profiles in name order.
    assert [h.source_native_id for h in handles] == [
        "sess_1",
        "sess_2",
        "ops/sess_1",
        "ops/sess_2",
        "work/sess_1",
        "work/sess_2",
    ]
    assert {h.metadata["profile"] for h in handles} == {"default", "ops", "work"}


def test_discover_profile_config_selects_one_profile(adapter, hermes_home):
    build_fixture_db(hermes_home / "state.db")
    work_db = _profile_store(hermes_home, "work")

    handles = list(adapter.discover({"profile": "work"}))

    assert [h.source_native_id for h in handles] == ["work/sess_1", "work/sess_2"]
    assert all(h.path == work_db for h in handles)


def test_discover_from_a_profile_home_still_sees_the_root(adapter, hermes_home, monkeypatch):
    build_fixture_db(hermes_home / "state.db")
    _profile_store(hermes_home, "work")
    # Hermes binds a non-launch profile by pointing HERMES_HOME at its dir.
    monkeypatch.setenv("HERMES_HOME", str(hermes_home / "profiles" / "work"))

    handles = list(adapter.discover())

    assert [h.source_native_id for h in handles] == [
        "sess_1",
        "sess_2",
        "work/sess_1",
        "work/sess_2",
    ]


def test_discover_reports_a_store_without_a_sessions_table(adapter, hermes_home):
    db = hermes_home / "state.db"
    conn = sqlite3.connect(db)
    conn.execute("CREATE TABLE unrelated (x)")
    conn.commit()
    conn.close()

    with pytest.raises(HermesStateDatabaseError) as excinfo:
        adapter.discover()

    assert str(db) in str(excinfo.value)
    assert "sessions" in str(excinfo.value)


def test_discover_reports_an_unreadable_store(adapter, hermes_home):
    (hermes_home / "state.db").write_bytes(b"definitely not a sqlite database")

    with pytest.raises(HermesStateDatabaseError) as excinfo:
        adapter.discover()

    assert str(hermes_home / "state.db") in str(excinfo.value)


def test_discover_empty_store_is_not_an_error(adapter, hermes_home):
    """A readable store that holds no sessions is the one silent outcome."""
    conn = sqlite3.connect(hermes_home / "state.db")
    conn.executescript(SESSION_SCHEMA)
    conn.commit()
    conn.close()

    assert list(adapter.discover()) == []


def test_parse_resolves_a_profile_qualified_handle(adapter, hermes_home):
    build_fixture_db(hermes_home / "state.db")
    work_db = _profile_store(hermes_home, "work")

    handle = next(
        h for h in adapter.discover() if h.source_native_id.startswith("work/")
    )
    conv = adapter.parse(handle)

    assert conv.source_native_id == "work/sess_1"
    assert conv.title == "Fix the login bug"
    assert conv.raw_path == str(work_db)


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

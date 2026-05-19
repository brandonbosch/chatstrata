"""Tests for OpenCodeAdapter against fixture data."""

import json
import sqlite3
from pathlib import Path

import pytest

from chatstrata.core.models import BlockType, ConversationHandle, Role
from chatstrata.sources.opencode.adapter import OpenCodeAdapter


@pytest.fixture
def adapter() -> OpenCodeAdapter:
    return OpenCodeAdapter()


@pytest.fixture
def sample_db(tmp_path) -> Path:
    db_path = tmp_path / "opencode.db"
    conn = sqlite3.connect(db_path)
    conn.executescript(
        """
        CREATE TABLE project (
            id TEXT PRIMARY KEY,
            name TEXT NOT NULL
        );

        CREATE TABLE session (
            id TEXT PRIMARY KEY,
            project_id TEXT NOT NULL,
            parent_id TEXT,
            slug TEXT NOT NULL,
            directory TEXT NOT NULL,
            title TEXT NOT NULL,
            version TEXT NOT NULL,
            share_url TEXT,
            summary_additions INTEGER,
            summary_deletions INTEGER,
            summary_files INTEGER,
            summary_diffs TEXT,
            revert TEXT,
            permission TEXT,
            time_created INTEGER NOT NULL,
            time_updated INTEGER NOT NULL,
            time_compacting INTEGER,
            time_archived INTEGER,
            workspace_id TEXT
        );

        CREATE TABLE message (
            id TEXT PRIMARY KEY,
            session_id TEXT NOT NULL,
            time_created INTEGER NOT NULL,
            time_updated INTEGER NOT NULL,
            data TEXT NOT NULL
        );

        CREATE TABLE part (
            id TEXT PRIMARY KEY,
            message_id TEXT NOT NULL,
            session_id TEXT NOT NULL,
            time_created INTEGER NOT NULL,
            time_updated INTEGER NOT NULL,
            data TEXT NOT NULL
        );
        """
    )
    conn.execute("INSERT INTO project (id, name) VALUES (?, ?)", ["proj_001", "myapp"])
    conn.execute(
        """
        INSERT INTO session (
            id, project_id, slug, directory, title, version,
            time_created, time_updated
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
        """,
        [
            "ses_test001",
            "proj_001",
            "add-error-handling",
            "/home/user/projects/myapp",
            "Add error handling to database module",
            "1.0.0",
            1700000000000,
            1700001000000,
        ],
    )

    messages = [
        (
            "msg_user001",
            1700000000000,
            1700000000000,
            {
                "role": "user",
                "time": {"created": 1700000000000},
                "summary": {"diffs": []},
                "agent": "build",
                "model": {"providerID": "llamacpp", "modelID": "qwen-distill"},
            },
        ),
        (
            "msg_asst001",
            1700000010000,
            1700000060000,
            {
                "role": "assistant",
                "time": {"created": 1700000010000, "completed": 1700000060000},
                "parentID": "msg_user001",
                "modelID": "qwen-distill",
                "providerID": "llamacpp",
                "mode": "build",
                "agent": "build",
                "path": {"cwd": "/home/user/projects/myapp", "root": "/"},
                "cost": 0,
                "tokens": {
                    "total": 5000,
                    "input": 4500,
                    "output": 500,
                    "reasoning": 0,
                    "cache": {"read": 0, "write": 0},
                },
                "finish": "tool-calls",
            },
        ),
    ]
    for message_id, created, updated, data in messages:
        conn.execute(
            "INSERT INTO message (id, session_id, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?)",
            [message_id, "ses_test001", created, updated, json.dumps(data)],
        )

    parts = [
        (
            "prt_user001",
            "msg_user001",
            1700000000000,
            1700000000000,
            {"type": "text", "text": "Add error handling to the database connection module"},
        ),
        (
            "prt_step001",
            "msg_asst001",
            1700000010000,
            1700000010000,
            {"type": "step-start"},
        ),
        (
            "prt_reason001",
            "msg_asst001",
            1700000011000,
            1700000015000,
            {
                "type": "reasoning",
                "text": "I need to add try-except blocks around the database connection calls.",
                "time": {"start": 1700000011000, "end": 1700000015000},
            },
        ),
        (
            "prt_text001",
            "msg_asst001",
            1700000015000,
            1700000015000,
            {
                "type": "text",
                "text": "I'll add error handling to the database connection module.",
                "time": {"start": 1700000015000, "end": 1700000015000},
            },
        ),
        (
            "prt_tool001",
            "msg_asst001",
            1700000020000,
            1700000025000,
            {
                "type": "tool",
                "callID": "call_abc123",
                "tool": "bash",
                "state": {
                    "status": "completed",
                    "input": {"command": "cat db.py", "description": "Read database module"},
                    "output": 'import sqlite3\n\ndef connect():\n    return sqlite3.connect("app.db")',
                    "title": "Read database module",
                    "metadata": {
                        "output": 'import sqlite3\n\ndef connect():\n    return sqlite3.connect("app.db")',
                        "exit": 0,
                        "description": "Read database module",
                        "truncated": False,
                    },
                    "time": {"start": 1700000020000, "end": 1700000025000},
                },
            },
        ),
        (
            "prt_text002",
            "msg_asst001",
            1700000030000,
            1700000030000,
            {
                "type": "text",
                "text": "I've reviewed the module. Adding try-except blocks now.",
                "time": {"start": 1700000030000, "end": 1700000030000},
            },
        ),
        (
            "prt_stepfin001",
            "msg_asst001",
            1700000035000,
            1700000035000,
            {
                "type": "step-finish",
                "reason": "tool-calls",
                "snapshot": "abc123",
                "cost": 0,
                "tokens": {
                    "total": 5000,
                    "input": 4500,
                    "output": 500,
                    "reasoning": 0,
                    "cache": {"read": 0, "write": 0},
                },
            },
        ),
        (
            "prt_patch001",
            "msg_asst001",
            1700000040000,
            1700000040000,
            {
                "type": "patch",
                "hash": "deadbeef1234567890",
                "files": ["/home/user/projects/myapp/db.py"],
            },
        ),
    ]
    for part_id, message_id, created, updated, data in parts:
        conn.execute(
            "INSERT INTO part (id, message_id, session_id, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?, ?)",
            [part_id, message_id, "ses_test001", created, updated, json.dumps(data)],
        )

    conn.commit()
    conn.close()
    return db_path


@pytest.fixture
def sample_handle(sample_db) -> ConversationHandle:
    return ConversationHandle(
        source_native_id="ses_test001",
        path=sample_db,
        metadata={
            "title": "Add error handling to database module",
            "directory": "/home/user/projects/myapp",
        },
    )


def test_parse_returns_conversation(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    assert conv.source_native_id == "ses_test001"
    assert conv.title == "Add error handling to database module"
    assert conv.project == "/home/user/projects/myapp"


def test_parse_extracts_messages(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    # user text (1) + assistant reasoning+text (1) + tool_use (1) + tool_result (1)
    # + assistant text+patch after tool (1) = 5
    assert len(conv.messages) == 5


def test_parse_roles_correct(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    roles = [m.role for m in conv.messages]
    assert roles == [
        Role.USER,       # user text
        Role.ASSISTANT,  # reasoning + text before tool
        Role.ASSISTANT,  # tool_use (bash)
        Role.TOOL,       # tool_result
        Role.ASSISTANT,  # text after tool + patch
    ]


def test_parse_user_text(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    user_msg = conv.messages[0]
    assert user_msg.role == Role.USER
    assert len(user_msg.blocks) == 1
    assert user_msg.blocks[0].type == BlockType.TEXT
    assert "error handling" in user_msg.blocks[0].text


def test_parse_reasoning_becomes_thinking(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    asst_msg = conv.messages[1]
    assert asst_msg.role == Role.ASSISTANT
    thinking_blocks = [b for b in asst_msg.blocks if b.type == BlockType.THINKING]
    assert len(thinking_blocks) == 1
    assert "try-except" in thinking_blocks[0].text
    assert thinking_blocks[0].payload["start_ms"] == 1700000011000


def test_parse_tool_becomes_tool_use_and_result(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    tool_use_msg = conv.messages[2]
    assert tool_use_msg.role == Role.ASSISTANT
    assert tool_use_msg.blocks[0].type == BlockType.TOOL_USE
    assert tool_use_msg.blocks[0].tool_name == "bash"
    assert tool_use_msg.blocks[0].tool_use_id == "call_abc123"
    assert tool_use_msg.blocks[0].payload["status"] == "completed"

    tool_result_msg = conv.messages[3]
    assert tool_result_msg.role == Role.TOOL
    assert tool_result_msg.blocks[0].type == BlockType.TOOL_RESULT
    assert tool_result_msg.blocks[0].tool_use_id == "call_abc123"
    assert "sqlite3" in tool_result_msg.blocks[0].text


def test_parse_patch_becomes_tool_result(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    last_msg = conv.messages[4]
    assert last_msg.role == Role.ASSISTANT
    patch_blocks = [b for b in last_msg.blocks if b.type == BlockType.TOOL_RESULT]
    assert len(patch_blocks) == 1
    assert patch_blocks[0].payload["hash"] == "deadbeef1234567890"
    assert len(patch_blocks[0].payload["files"]) == 1


def test_parse_skips_lifecycle_parts(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    for msg in conv.messages:
        for block in msg.blocks:
            assert block.type != BlockType.TEXT or "step-start" not in (block.text or "")
            assert block.type != BlockType.TEXT or "step-finish" not in (block.text or "")


def test_parse_captures_model(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    assistant_msgs = [m for m in conv.messages if m.role == Role.ASSISTANT]
    assert len(assistant_msgs) >= 1
    for m in assistant_msgs:
        assert m.model == "qwen-distill", f"Expected 'qwen-distill', got {m.model!r}"


def test_parse_captures_timestamps(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    assert conv.started_at is not None
    assert conv.ended_at is not None
    assert conv.started_at <= conv.ended_at
    assert conv.started_at.tzinfo is not None
    assert conv.ended_at.tzinfo is not None


def test_parse_preserves_raw_events(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    assert len(conv.raw_events) == 2
    assert conv.raw_events[0]["id"] == "msg_user001"
    assert conv.raw_events[1]["id"] == "msg_asst001"
    assert len(conv.raw_events[1]["parts"]) == 7


def test_parse_captures_provider_in_metadata(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    asst_msg = conv.messages[1]
    assert asst_msg.metadata.get("provider_id") == "llamacpp"


def test_parse_captures_agent_mode(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    asst_msg = conv.messages[1]
    assert asst_msg.metadata.get("agent") == "build"
    assert asst_msg.metadata.get("mode") == "build"


def test_discover_returns_handles(adapter, sample_db):
    handles = list(adapter.discover({"path": str(sample_db)}))
    assert len(handles) == 1
    assert handles[0].source_native_id == "ses_test001"
    assert handles[0].metadata["title"] == "Add error handling to database module"
    assert handles[0].metadata["directory"] == "/home/user/projects/myapp"


def test_discover_handles_missing_db(adapter):
    handles = list(adapter.discover({"path": "/nonexistent/opencode.db"}))
    assert handles == []


def test_discover_handles_no_config(adapter, monkeypatch):
    monkeypatch.setattr(
        "chatstrata.sources.opencode.adapter.DEFAULT_DB_PATH",
        Path("/nonexistent/opencode.db"),
    )
    handles = list(adapter.discover(None))
    assert handles == []

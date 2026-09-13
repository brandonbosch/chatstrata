"""Tests for OmpAdapter against fixture data."""

import json
from pathlib import Path

import pytest

from chatstrata.core.models import BlockType, ConversationHandle, Role
from chatstrata.sources.omp.adapter import OmpAdapter

FIXTURES = Path(__file__).parent / "fixtures"


@pytest.fixture
def adapter() -> OmpAdapter:
    return OmpAdapter()


@pytest.fixture
def sample_handle() -> ConversationHandle:
    return ConversationHandle(
        source_native_id="01a0990e-6269-7183-b8bd-19502cf6afcd",
        path=FIXTURES / "sample_session.jsonl",
    )


def test_parse_returns_a_conversation(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    assert conv.source_native_id == "01a0990e-6269-7183-b8bd-19502cf6afcd"
    assert conv.title == "Add omp source adapter"
    assert conv.project == "/home/example/chatstrata"


def test_parse_extracts_all_messages(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    # user, assistant (thinking+toolCall+text), toolResult, assistant,
    # custom_message, user string = 6; compaction/session_init/custom are not
    # transcript messages and the malformed line is skipped.
    assert len(conv.messages) == 6


def test_parse_roles_are_correct(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    assert [m.role for m in conv.messages] == [
        Role.USER,
        Role.ASSISTANT,
        Role.TOOL,
        Role.ASSISTANT,
        Role.ASSISTANT,  # custom_message with agent attribution
        Role.USER,
    ]


def test_parse_assistant_blocks(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    msg = conv.messages[1]
    types = [b.type for b in msg.blocks]
    assert types == [
        BlockType.THINKING,
        BlockType.TOOL_USE,
        BlockType.TEXT,
    ]
    assert msg.blocks[0].text == "Need to study the session format first."
    assert msg.blocks[2].text.startswith("I studied the session format")


def test_parse_tool_call_becomes_tool_use(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    tool_blocks = [b for b in conv.messages[1].blocks if b.type == BlockType.TOOL_USE]
    assert len(tool_blocks) == 1
    assert tool_blocks[0].tool_name == "read"
    assert tool_blocks[0].tool_use_id == "call-001"
    assert tool_blocks[0].payload["arguments"]["path"] == "/home/brandon/.omp/agent/sessions"


def test_parse_tool_result_becomes_tool_result(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    result = conv.messages[2]
    assert result.role == Role.TOOL
    assert result.blocks[0].type == BlockType.TOOL_RESULT
    assert result.blocks[0].tool_name == "read"
    assert result.blocks[0].tool_use_id == "call-001"
    assert "14 session files" in result.blocks[0].text
    assert result.blocks[0].payload["details"]["matchCount"] == 14


def test_parse_captures_model(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    assistant_msgs = [m for m in conv.messages if m.role == Role.ASSISTANT]
    assert assistant_msgs[0].model == "claude-sonnet-4-5"

def test_parse_captures_timestamps(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    assert conv.started_at is not None
    assert conv.ended_at is not None
    assert conv.started_at.isoformat().startswith("2026-09-13T04:37:31")
    assert conv.ended_at.isoformat().startswith("2026-09-13T04:40:00")
    assert conv.started_at <= conv.ended_at


def test_parse_preserves_message_tree(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    assistant = conv.messages[1]
    assert assistant.source_native_id == "8fce7202"
    assert assistant.parent_source_native_id == "6023a196"


def test_parse_custom_message_becomes_agent_message(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    msg = conv.messages[4]
    assert msg.role == Role.ASSISTANT
    assert msg.blocks[0].text == "Injected context from extension"
    assert msg.metadata["attribution"] == "agent"


def test_parse_accepts_plain_string_content(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    last = conv.messages[5]
    assert last.role == Role.USER
    assert last.blocks[0].type == BlockType.TEXT
    assert last.blocks[0].text == "plain string user turn"


def test_parse_skips_non_transcript_events(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    # compaction summary must not appear as a message
    texts = [b.text for m in conv.messages for b in m.blocks]
    assert "Work so far" not in texts


def test_parse_survives_malformed_line(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    assert len(conv.raw_events) == 12  # 12 valid JSON lines of 13 total
    assert all(e.get("id") != "broken" for e in conv.raw_events)


def test_parse_preserves_raw_events(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    assert conv.metadata["event_count"] == 12


def test_parse_title_falls_back_to_first_user_text(tmp_path, adapter):
    # No title slot, no header title -> derive from first user text.
    p = tmp_path / "session.jsonl"
    lines = [
        json.dumps(
            {
                "type": "session",
                "version": 3,
                "id": "abc",
                "timestamp": "2026-09-13T04:37:31.881Z",
                "cwd": "/tmp/proj",
            }
        ),
        json.dumps(
            {
                "type": "message",
                "id": "m1",
                "parentId": None,
                "timestamp": "2026-09-13T04:37:40.000Z",
                "message": {"role": "user", "content": [{"type": "text", "text": "first line\nsecond"}]},
            }
        ),
    ]
    p.write_text("\n".join(lines) + "\n")
    conv = adapter.parse(ConversationHandle(source_native_id="abc", path=p))
    assert conv.title == "first line"
    assert conv.project == "/tmp/proj"


def test_discover_walks_encoded_directory_tree(adapter, tmp_path):
    bucket = tmp_path / "-git-chatstrata"
    bucket.mkdir()
    f = bucket / "2026-09-13T04-37-31-881Z_01a0990e-6269-7183-b8bd-19502cf6afcd.jsonl"
    f.write_text("{}\n")
    (bucket / "notes.txt").write_text("ignore me\n")

    handles = list(adapter.discover({"path": tmp_path}))
    assert len(handles) == 1
    assert handles[0].source_native_id == "01a0990e-6269-7183-b8bd-19502cf6afcd"
    assert handles[0].metadata["project"].endswith("git/chatstrata")


def test_discover_ignores_non_jsonl_files(adapter, tmp_path):
    bucket = tmp_path / "-tmp-x"
    bucket.mkdir()
    (bucket / "notes.txt").write_text("nope\n")
    assert list(adapter.discover({"path": tmp_path})) == []


def test_discover_handles_missing_directory(adapter):
    handles = list(adapter.discover({"path": "/nonexistent/path"}))
    assert handles == []


def test_discover_handles_no_config(adapter, tmp_path, monkeypatch):
    monkeypatch.setattr(
        "chatstrata.sources.omp.adapter.DEFAULT_OMP_DIR",
        Path("/nonexistent/omp/sessions"),
    )
    assert list(adapter.discover(None)) == []


def test_parse_requires_path(adapter):
    with pytest.raises(ValueError):
        adapter.parse(ConversationHandle(source_native_id="x", path=None))

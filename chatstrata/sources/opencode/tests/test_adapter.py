"""Tests for OpenCodeAdapter against fixture data."""

from pathlib import Path

import pytest

from chatstrata.core.models import BlockType, ConversationHandle, Role
from chatstrata.sources.opencode.adapter import OpenCodeAdapter

FIXTURES = Path(__file__).parent / "fixtures"


@pytest.fixture
def adapter() -> OpenCodeAdapter:
    return OpenCodeAdapter()


@pytest.fixture
def sample_handle() -> ConversationHandle:
    return ConversationHandle(
        source_native_id="ses_test001",
        path=FIXTURES / "opencode.db",
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


def test_discover_returns_handles(adapter):
    handles = list(adapter.discover({"path": str(FIXTURES / "opencode.db")}))
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

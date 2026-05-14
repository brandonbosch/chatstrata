"""Tests for CodexCliAdapter against fixture data."""

from pathlib import Path

import pytest

from chatstrata.core.models import BlockType, ConversationHandle, Role
from chatstrata.sources.codex_cli.adapter import CodexCliAdapter, _extract_session_id

FIXTURES = Path(__file__).parent / "fixtures"


@pytest.fixture
def adapter() -> CodexCliAdapter:
    return CodexCliAdapter()


@pytest.fixture
def sample_handle() -> ConversationHandle:
    return ConversationHandle(
        source_native_id="abc-def-123",
        path=FIXTURES / "sample_session.jsonl",
        metadata={},
    )


def test_parse_returns_a_conversation(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    assert conv.source_native_id == "abc-def-123"
    assert conv.project == "/Users/example/myproject"
    assert conv.title == "Add error handling to the database connection module"


def test_parse_extracts_all_messages(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    # 2 user_message, 2 reasoning, 4 assistant message, 1 function_call,
    # 1 function_call_output, 1 custom_tool_call, 1 custom_tool_call_output,
    # 1 web_search_call = 13 messages
    assert len(conv.messages) == 13


def test_parse_roles_are_correct(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    roles = [m.role for m in conv.messages]
    assert roles == [
        Role.USER,          # user_message "Add error handling..."
        Role.ASSISTANT,     # reasoning (empty summary)
        Role.ASSISTANT,     # assistant message "Let me read..."
        Role.ASSISTANT,     # function_call exec_command
        Role.TOOL,          # function_call_output
        Role.ASSISTANT,     # assistant message "The module has no..."
        Role.ASSISTANT,     # custom_tool_call apply_patch
        Role.TOOL,          # custom_tool_call_output
        Role.ASSISTANT,     # web_search_call
        Role.ASSISTANT,     # assistant message "Done..."
        Role.USER,          # user_message "Looks good..."
        Role.ASSISTANT,     # reasoning (with summary)
        Role.ASSISTANT,     # assistant message "You're welcome..."
    ]


def test_parse_user_messages(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    first = conv.messages[0]
    assert first.role == Role.USER
    assert len(first.blocks) == 1
    assert first.blocks[0].type == BlockType.TEXT
    assert "error handling" in first.blocks[0].text


def test_parse_reasoning_becomes_thinking(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    # First reasoning has empty summary (encrypted content only)
    reasoning_empty = conv.messages[1]
    assert reasoning_empty.role == Role.ASSISTANT
    assert reasoning_empty.blocks[0].type == BlockType.THINKING
    assert reasoning_empty.blocks[0].text is None
    assert reasoning_empty.blocks[0].payload["has_encrypted_content"] is True

    # Second reasoning (last message) has a summary
    reasoning_with_summary = conv.messages[11]
    assert reasoning_with_summary.blocks[0].type == BlockType.THINKING
    assert "no action needed" in reasoning_with_summary.blocks[0].text


def test_parse_function_call_becomes_tool_use(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    fc = conv.messages[3]  # function_call exec_command
    assert fc.role == Role.ASSISTANT
    tool_blocks = [b for b in fc.blocks if b.type == BlockType.TOOL_USE]
    assert len(tool_blocks) == 1
    assert tool_blocks[0].tool_name == "exec_command"
    assert tool_blocks[0].tool_use_id == "call-001"
    assert "cmd" in tool_blocks[0].payload["arguments"]


def test_parse_function_call_output_becomes_tool_result(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    fco = conv.messages[4]  # function_call_output
    assert fco.role == Role.TOOL
    result_blocks = [b for b in fco.blocks if b.type == BlockType.TOOL_RESULT]
    assert len(result_blocks) == 1
    assert result_blocks[0].tool_use_id == "call-001"
    assert "sqlite3" in result_blocks[0].text


def test_parse_custom_tool_call(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    ctc = conv.messages[6]  # custom_tool_call apply_patch
    assert ctc.role == Role.ASSISTANT
    assert ctc.blocks[0].type == BlockType.TOOL_USE
    assert ctc.blocks[0].tool_name == "apply_patch"
    assert ctc.blocks[0].tool_use_id == "call-002"


def test_parse_custom_tool_call_output(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    ctco = conv.messages[7]  # custom_tool_call_output
    assert ctco.role == Role.TOOL
    assert ctco.blocks[0].type == BlockType.TOOL_RESULT
    assert ctco.blocks[0].tool_use_id == "call-002"


def test_parse_web_search(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    ws = conv.messages[8]  # web_search_call
    assert ws.role == Role.ASSISTANT
    assert ws.blocks[0].type == BlockType.TOOL_USE
    assert ws.blocks[0].tool_name == "web_search"
    assert ws.blocks[0].payload["query"] == "sqlite3 retry best practices python"


def test_parse_assistant_text(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    # Third message is the first assistant text
    msg = conv.messages[2]
    assert msg.role == Role.ASSISTANT
    assert msg.blocks[0].type == BlockType.TEXT
    assert "read the current database module" in msg.blocks[0].text


def test_parse_captures_model(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    assistant_msgs = [m for m in conv.messages if m.role == Role.ASSISTANT]
    assert all(m.model == "o4-mini" for m in assistant_msgs)


def test_parse_captures_timestamps(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    assert conv.started_at is not None
    assert conv.ended_at is not None
    assert conv.started_at <= conv.ended_at


def test_parse_preserves_raw_events(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    assert len(conv.raw_events) == 25


def test_parse_extracts_project_from_session_meta(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    assert conv.project == "/Users/example/myproject"


def test_parse_skips_developer_messages(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    # Developer role messages (system prompts) should not appear
    for m in conv.messages:
        # All user messages come from event_msg->user_message, not response_item->message
        assert m.role in (Role.USER, Role.ASSISTANT, Role.TOOL)


def test_discover_walks_date_directory_tree(adapter, tmp_path):
    # Create YYYY/MM/DD layout
    session_dir = tmp_path / "2026" / "04" / "10"
    session_dir.mkdir(parents=True)
    rollout = session_dir / "rollout-2026-04-10T14-00-00-abc-def-123-456-789abcdef012.jsonl"
    rollout.write_text((FIXTURES / "sample_session.jsonl").read_text())

    handles = list(adapter.discover({"path": str(tmp_path)}))
    assert len(handles) == 1
    assert handles[0].source_native_id == "abc-def-123-456-789abcdef012"


def test_discover_ignores_non_rollout_files(adapter, tmp_path):
    # history.jsonl and other files should not be discovered
    (tmp_path / "history.jsonl").write_text("{}\n")
    (tmp_path / "other.jsonl").write_text("{}\n")
    sub = tmp_path / "2026" / "04" / "10"
    sub.mkdir(parents=True)
    rollout = sub / "rollout-2026-04-10T00-00-00-019d68bf-f055-7183-ac5a-7ddae094e0aa.jsonl"
    rollout.write_text("{}\n")

    handles = list(adapter.discover({"path": str(tmp_path)}))
    assert len(handles) == 1


def test_discover_handles_missing_directory(adapter):
    handles = list(adapter.discover({"path": "/nonexistent/path"}))
    assert handles == []


def test_discover_handles_no_config(adapter, tmp_path, monkeypatch):
    # With no config and default path missing, should return empty
    monkeypatch.setattr(
        "chatstrata.sources.codex_cli.adapter.DEFAULT_CODEX_DIR",
        tmp_path / "nonexistent",
    )
    handles = list(adapter.discover(None))
    assert handles == []


def test_extract_session_id_real_filename():
    name = "rollout-2026-04-07T10-21-42-019d68bf-f055-7183-ac5a-7ddae094e0aa.jsonl"
    assert _extract_session_id(name) == "019d68bf-f055-7183-ac5a-7ddae094e0aa"


def test_extract_session_id_simple():
    assert _extract_session_id("rollout-abc-def-123-456-789.jsonl") == "abc-def-123-456-789"


def test_extract_session_id_non_rollout():
    assert _extract_session_id("something_else.jsonl") == "something_else"

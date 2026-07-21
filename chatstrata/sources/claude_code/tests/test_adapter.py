"""Tests for ClaudeCodeAdapter against fixture data."""

from pathlib import Path

import pytest

from chatstrata.core.models import BlockType, ConversationHandle, Role
from chatstrata.sources.claude_code.adapter import (
    ClaudeCodeAdapter,
    _project_from_events,
)

FIXTURES = Path(__file__).parent / "fixtures"


@pytest.fixture
def adapter() -> ClaudeCodeAdapter:
    return ClaudeCodeAdapter()


@pytest.fixture
def sample_handle() -> ConversationHandle:
    return ConversationHandle(
        source_native_id="sample_session",
        path=FIXTURES / "sample_session.jsonl",
        metadata={"project": "/Users/example/myproj"},
    )


def test_parse_returns_a_conversation(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    assert conv.source_native_id == "sample_session"
    assert conv.project == "/Users/example/myproj"
    assert conv.title == "Refactor the user auth module"


def test_parse_extracts_all_real_messages(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    # Fixture has 5 message events + 1 summary. Summary is metadata not a message.
    assert len(conv.messages) == 5


def test_parse_roles_are_correct(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    roles = [m.role for m in conv.messages]
    assert roles == [Role.USER, Role.ASSISTANT, Role.USER, Role.ASSISTANT, Role.USER]


def test_parse_handles_string_and_list_content(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    # First user message had string content
    first = conv.messages[0]
    assert len(first.blocks) == 1
    assert first.blocks[0].type == BlockType.TEXT
    assert "refactor the auth module" in first.blocks[0].text


def test_parse_extracts_tool_use(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    second = conv.messages[1]  # first assistant message
    tool_blocks = [b for b in second.blocks if b.type == BlockType.TOOL_USE]
    assert len(tool_blocks) == 1
    assert tool_blocks[0].tool_name == "view"
    assert tool_blocks[0].tool_use_id == "tool-call-1"


def test_parse_extracts_tool_result(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    third = conv.messages[2]  # user message containing tool_result
    result_blocks = [b for b in third.blocks if b.type == BlockType.TOOL_RESULT]
    assert len(result_blocks) == 1
    assert result_blocks[0].tool_use_id == "tool-call-1"
    assert "def login" in result_blocks[0].text


def test_parse_extracts_thinking(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    fourth = conv.messages[3]  # second assistant message
    thinking_blocks = [b for b in fourth.blocks if b.type == BlockType.THINKING]
    assert len(thinking_blocks) == 1
    assert "too simple to refactor" in thinking_blocks[0].text


def test_parse_captures_model(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    assistant_messages = [m for m in conv.messages if m.role == Role.ASSISTANT]
    assert all(m.model == "claude-opus-4-7" for m in assistant_messages)


def test_parse_captures_timestamps(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    assert conv.started_at is not None
    assert conv.ended_at is not None
    assert conv.started_at <= conv.ended_at


def test_parse_preserves_raw_events(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    # 6 lines total in the fixture (1 summary + 5 messages)
    assert len(conv.raw_events) == 6


def test_project_uses_transcript_cwd_not_lossy_folder_decode(adapter):
    """project must come from the lossless transcript cwd, not the folder decode.

    Claude Code's folder-name encoding collapses '/', '_', '-', and '.' all
    into '-', so decoding it is lossy. Here the handle carries a *corrupted*
    decoded path (as the on-disk folder name would produce) that disagrees with
    the real cwd recorded in the transcript; the transcript cwd must win.
    """
    handle = ConversationHandle(
        source_native_id="sample_session",
        path=FIXTURES / "sample_session.jsonl",
        # What the lossy folder-name decode would yield (wrong):
        metadata={"project": "/Users/example/my/proj"},
    )
    conv = adapter.parse(handle)
    # The real cwd from the transcript (correct):
    assert conv.project == "/Users/example/myproj"


def test_project_falls_back_to_folder_decode_when_no_cwd(adapter):
    """When no event records a cwd, fall back to the decoded folder name."""
    handle = ConversationHandle(
        source_native_id="no_cwd_session",
        path=FIXTURES / "no_cwd_session.jsonl",
        metadata={"project": "/Users/example/fallback"},
    )
    conv = adapter.parse(handle)
    assert conv.project == "/Users/example/fallback"


def test_project_from_events_returns_first_non_empty_cwd():
    events = [
        {"type": "summary"},
        {"type": "user", "cwd": ""},
        {"type": "user", "cwd": "/Users/example/real"},
        {"type": "assistant", "cwd": "/Users/example/later"},
    ]
    assert _project_from_events(events) == "/Users/example/real"


def test_project_from_events_returns_none_when_absent():
    assert _project_from_events([{"type": "user"}, {"type": "summary"}]) is None


def test_discover_walks_a_directory(adapter, tmp_path):
    # Create a fake ~/.claude/projects layout
    project_dir = tmp_path / "-Users-example-myproj"
    project_dir.mkdir()
    session = project_dir / "abc-123.jsonl"
    session.write_text((FIXTURES / "sample_session.jsonl").read_text())

    handles = list(adapter.discover({"path": str(tmp_path)}))
    assert len(handles) == 1
    assert handles[0].source_native_id == "abc-123"
    assert handles[0].metadata["project"] == "/Users/example/myproj"

"""Tests for ClaudeExportAdapter against fixture data."""

from pathlib import Path

import pytest

from chatstrata.core.models import BlockType, ConversationHandle, Role
from chatstrata.sources.claude_export.adapter import ClaudeExportAdapter

FIXTURES = Path(__file__).parent / "fixtures"


@pytest.fixture
def adapter() -> ClaudeExportAdapter:
    return ClaudeExportAdapter()


@pytest.fixture
def handles(adapter):
    return list(adapter.discover({"path": str(FIXTURES)}))


def _parse_conv(adapter, handles, source_id):
    handle = next(h for h in handles if h.source_native_id == source_id)
    return adapter.parse(handle)


def test_discover_yields_all_conversations(handles):
    assert len(handles) == 3


def test_discover_accepts_file_path(adapter):
    handles = list(adapter.discover({"path": str(FIXTURES / "conversations.json")}))
    assert len(handles) == 3


def test_discover_handles_missing_path(adapter):
    handles = list(adapter.discover({"path": "/nonexistent/path"}))
    assert handles == []


def test_discover_handles_no_config(adapter):
    handles = list(adapter.discover(None))
    assert handles == []


def test_discover_source_native_ids(handles):
    ids = {h.source_native_id for h in handles}
    assert ids == {"conv-001", "conv-002", "conv-003"}


def test_parse_basic_text_conversation(adapter, handles):
    conv = _parse_conv(adapter, handles, "conv-001")
    assert conv.source_native_id == "conv-001"
    assert conv.title == "Understanding Python decorators"
    assert len(conv.messages) == 2


def test_parse_roles_are_correct(adapter, handles):
    conv = _parse_conv(adapter, handles, "conv-001")
    roles = [m.role for m in conv.messages]
    assert roles == [Role.USER, Role.ASSISTANT]


def test_parse_text_content(adapter, handles):
    conv = _parse_conv(adapter, handles, "conv-001")
    first = conv.messages[0]
    assert len(first.blocks) == 1
    assert first.blocks[0].type == BlockType.TEXT
    assert "decorators" in first.blocks[0].text


def test_parse_timestamps(adapter, handles):
    conv = _parse_conv(adapter, handles, "conv-001")
    assert conv.started_at is not None
    assert conv.ended_at is not None
    assert conv.started_at <= conv.ended_at


def test_parse_structured_content_blocks(adapter, handles):
    conv = _parse_conv(adapter, handles, "conv-002")
    assistant_msg = conv.messages[1]
    text_blocks = [b for b in assistant_msg.blocks if b.type == BlockType.TEXT]
    assert len(text_blocks) == 2
    assert "Pydantic" in text_blocks[0].text
    assert "Optional" in text_blocks[1].text


def test_parse_text_fallback_when_content_empty(adapter, handles):
    conv = _parse_conv(adapter, handles, "conv-002")
    last_msg = conv.messages[2]
    assert len(last_msg.blocks) == 1
    assert last_msg.blocks[0].type == BlockType.TEXT
    assert last_msg.blocks[0].text == "That fixed it, thanks!"


def test_parse_attachments(adapter, handles):
    conv = _parse_conv(adapter, handles, "conv-003")
    first_msg = conv.messages[0]
    att_blocks = [b for b in first_msg.blocks if b.type == BlockType.ATTACHMENT]
    assert len(att_blocks) == 1
    att = att_blocks[0]
    assert att.text == "config.yaml"
    assert att.payload["file_name"] == "config.yaml"
    assert att.payload["file_size"] == 2048
    assert att.payload["file_type"] == "application/x-yaml"
    assert "port: 8080" in att.payload["extracted_content"]


def test_parse_null_title_fallback(adapter, handles):
    conv = _parse_conv(adapter, handles, "conv-003")
    assert conv.title == "Can you review this configuration file I uploaded?"


def test_parse_preserves_raw_events(adapter, handles):
    conv = _parse_conv(adapter, handles, "conv-001")
    assert len(conv.raw_events) == 1
    assert conv.raw_events[0]["uuid"] == "conv-001"


def test_parse_message_source_native_ids(adapter, handles):
    conv = _parse_conv(adapter, handles, "conv-001")
    ids = [m.source_native_id for m in conv.messages]
    assert ids == ["msg-001", "msg-002"]


def test_parse_sender_mapping(adapter, handles):
    conv = _parse_conv(adapter, handles, "conv-001")
    assert conv.messages[0].role == Role.USER
    assert conv.messages[1].role == Role.ASSISTANT


def test_idempotent_discover(adapter):
    ids_1 = [h.source_native_id for h in adapter.discover({"path": str(FIXTURES)})]
    ids_2 = [h.source_native_id for h in adapter.discover({"path": str(FIXTURES)})]
    assert ids_1 == ids_2


def test_parse_via_file_fallback(adapter):
    handle = ConversationHandle(
        source_native_id="conv-002",
        path=FIXTURES / "conversations.json",
        metadata={},
    )
    conv = adapter.parse(handle)
    assert conv.source_native_id == "conv-002"
    assert conv.title == "Debugging a REST API"
    assert len(conv.messages) == 3

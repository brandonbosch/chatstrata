"""End-to-end test: parse with adapter, ingest into DuckDB, query results."""

from pathlib import Path

import pytest

from chatstrata.core.db import connect
from chatstrata.core.ingest import ensure_source, ingest_conversation
from chatstrata.core.models import ConversationHandle
from chatstrata.sources.claude_code.adapter import ClaudeCodeAdapter

FIXTURES = (
    Path(__file__).parent.parent
    / "chatstrata"
    / "sources"
    / "claude_code"
    / "tests"
    / "fixtures"
)


@pytest.fixture
def db(tmp_path):
    conn = connect(tmp_path / "test.duckdb")
    yield conn
    conn.close()


def test_ingest_end_to_end(db):
    adapter = ClaudeCodeAdapter()
    handle = ConversationHandle(
        source_native_id="sample_session",
        path=FIXTURES / "sample_session.jsonl",
        metadata={"project": "/Users/example/myproj"},
    )

    ensure_source(db, adapter.name, adapter.display_name, adapter.version)
    conv = adapter.parse(handle)
    conv_id = ingest_conversation(db, adapter.name, conv)

    assert conv_id

    # Conversation row exists
    row = db.execute(
        "SELECT title, project, message_count FROM conversations WHERE id = ?",
        [conv_id],
    ).fetchone()
    assert row[0] == "Refactor the user auth module"
    assert row[1] == "/Users/example/myproj"
    assert row[2] == 5

    # Messages persisted
    n_messages = db.execute(
        "SELECT COUNT(*) FROM messages WHERE conversation_id = ?", [conv_id]
    ).fetchone()[0]
    assert n_messages == 5

    # Tool use block found
    n_tool_use = db.execute(
        """
        SELECT COUNT(*) FROM content_blocks cb
        JOIN messages m ON m.id = cb.message_id
        WHERE m.conversation_id = ? AND cb.type = 'tool_use'
        """,
        [conv_id],
    ).fetchone()[0]
    assert n_tool_use == 1

    # tool_calls view works
    tool_call = db.execute(
        "SELECT tool_name FROM tool_calls WHERE conversation_id = ?", [conv_id]
    ).fetchone()
    assert tool_call[0] == "view"

    # raw_events preserved
    n_raw = db.execute(
        "SELECT COUNT(*) FROM raw_events WHERE source_native_conversation_id = ?",
        ["sample_session"],
    ).fetchone()[0]
    assert n_raw == 6


def test_ingest_is_idempotent(db):
    adapter = ClaudeCodeAdapter()
    handle = ConversationHandle(
        source_native_id="sample_session",
        path=FIXTURES / "sample_session.jsonl",
    )

    ensure_source(db, adapter.name, adapter.display_name, adapter.version)
    conv = adapter.parse(handle)

    id1 = ingest_conversation(db, adapter.name, conv)
    id2 = ingest_conversation(db, adapter.name, conv)
    assert id1 == id2

    # Still only one conversation row
    n_conv = db.execute("SELECT COUNT(*) FROM conversations").fetchone()[0]
    assert n_conv == 1

    # Messages were replaced, not duplicated
    n_messages = db.execute("SELECT COUNT(*) FROM messages").fetchone()[0]
    assert n_messages == 5

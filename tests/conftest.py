"""Shared fixtures."""

from __future__ import annotations

from pathlib import Path

import pytest

from chatstrata.core.db import connect
from chatstrata.core.ingest import ensure_source, ingest_conversation
from chatstrata.core.models import ConversationHandle
from chatstrata.sources.claude_code.adapter import ClaudeCodeAdapter
from chatstrata.sources.omp.adapter import OmpAdapter

SOURCES = Path(__file__).parent.parent / "chatstrata" / "sources"


@pytest.fixture
def db_path(tmp_path):
    path = tmp_path / "test.duckdb"
    conn = connect(path)
    for adapter, fixture in (
        (ClaudeCodeAdapter(), SOURCES / "claude_code/tests/fixtures/sample_session.jsonl"),
        (OmpAdapter(), SOURCES / "omp/tests/fixtures/sample_session.jsonl"),
    ):
        handle = ConversationHandle(
            source_native_id=fixture.stem, path=fixture, metadata={"project": "/p"}
        )
        ensure_source(conn, adapter.name, adapter.display_name, adapter.version)
        ingest_conversation(conn, adapter.name, adapter.parse(handle))
    conn.close()
    return path

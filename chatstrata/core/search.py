"""Full-text search across archived conversations."""

from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    import duckdb


@dataclass(frozen=True)
class SearchResult:
    score: float
    conversation_id: str
    conversation_title: str | None
    source_id: str
    project: str | None
    message_role: str
    message_created_at: datetime | None
    text: str
    content_block_id: str


def search_messages(
    conn: duckdb.DuckDBPyConnection,
    query: str,
    *,
    limit: int = 50,
    source: str | None = None,
    since: datetime | None = None,
    until: datetime | None = None,
) -> list[SearchResult]:
    """Search content blocks by full-text query, returning BM25-ranked results."""
    conditions = ["score IS NOT NULL"]
    params: list = []

    if source:
        conditions.append("c.source_id = ?")
        params.append(source)
    if since:
        conditions.append("m.created_at >= ?")
        params.append(since)
    if until:
        conditions.append("m.created_at < ?")
        params.append(until)

    where_clause = " AND ".join(conditions)
    params.append(limit)

    sql = f"""
        SELECT
            fts_main_content_blocks.match_bm25(cb.id, ?) AS score,
            c.id AS conversation_id,
            c.title AS conversation_title,
            c.source_id,
            c.project,
            m.role AS message_role,
            m.created_at AS message_created_at,
            cb.text,
            cb.id AS content_block_id
        FROM content_blocks cb
        JOIN messages m ON m.id = cb.message_id
        JOIN conversations c ON c.id = m.conversation_id
        WHERE {where_clause}
        ORDER BY score DESC
        LIMIT ?
    """

    try:
        rows = conn.execute(sql, [query] + params).fetchall()
    except Exception:  # noqa: BLE001
        return []

    return [
        SearchResult(
            score=row[0],
            conversation_id=row[1],
            conversation_title=row[2],
            source_id=row[3],
            project=row[4],
            message_role=row[5],
            message_created_at=row[6],
            text=row[7],
            content_block_id=row[8],
        )
        for row in rows
    ]


def snippet(text: str | None, query: str, context_chars: int = 120) -> str:
    """Extract a snippet of text around the first occurrence of a query term."""
    if not text:
        return ""
    first_term = query.split()[0] if query.split() else query
    lower_text = text.lower()
    idx = lower_text.find(first_term.lower())
    if idx == -1:
        return text[: context_chars * 2] + ("..." if len(text) > context_chars * 2 else "")
    start = max(0, idx - context_chars)
    end = min(len(text), idx + len(first_term) + context_chars)
    prefix = "..." if start > 0 else ""
    suffix = "..." if end < len(text) else ""
    return prefix + text[start:end] + suffix

"""Semantic similarity search using message embeddings."""

from __future__ import annotations

from datetime import datetime
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    import duckdb

from chatstrata.core.search import SearchResult


def semantic_search(
    conn: duckdb.DuckDBPyConnection,
    query_vector: list[float],
    model: str,
    *,
    limit: int = 20,
    source: str | None = None,
    since: datetime | None = None,
    until: datetime | None = None,
) -> list[SearchResult]:
    """Search messages by cosine similarity to a query vector."""
    conditions = ["me.model = ?"]
    where_params: list = [model]

    if source:
        conditions.append("c.source_id = ?")
        where_params.append(source)
    if since:
        conditions.append("m.created_at >= ?")
        where_params.append(since)
    if until:
        conditions.append("m.created_at < ?")
        where_params.append(until)

    where_clause = " AND ".join(conditions)

    sql = f"""
        SELECT
            list_cosine_similarity(me.vector, ?::FLOAT[]) AS score,
            c.id AS conversation_id,
            c.title AS conversation_title,
            c.source_id,
            c.project,
            m.role AS message_role,
            m.created_at AS message_created_at,
            COALESCE(
                (SELECT STRING_AGG(cb.text, ' ' ORDER BY cb.block_index)
                 FROM content_blocks cb
                 WHERE cb.message_id = m.id AND cb.text IS NOT NULL),
                ''
            ) AS text,
            m.id AS message_ref_id
        FROM message_embeddings me
        JOIN messages m ON m.id = me.message_id
        JOIN conversations c ON c.id = m.conversation_id
        WHERE {where_clause}
        ORDER BY score DESC
        LIMIT ?
    """

    all_params = [query_vector] + where_params + [limit]

    try:
        rows = conn.execute(sql, all_params).fetchall()
    except Exception:
        return []

    return [
        SearchResult(
            score=row[0] if row[0] is not None else 0.0,
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


def hybrid_search(
    conn: duckdb.DuckDBPyConnection,
    query: str,
    query_vector: list[float],
    model: str,
    *,
    limit: int = 20,
    source: str | None = None,
    since: datetime | None = None,
    until: datetime | None = None,
    keyword_weight: float = 0.5,
    semantic_weight: float = 0.5,
) -> list[SearchResult]:
    """Combined keyword + semantic search using reciprocal rank fusion."""
    from chatstrata.core.search import search_messages

    keyword_results = search_messages(
        conn, query, limit=limit * 3, source=source, since=since, until=until,
    )
    sem_results = semantic_search(
        conn, query_vector, model, limit=limit * 3, source=source, since=since, until=until,
    )

    rrf_k = 60
    scores: dict[str, float] = {}
    result_map: dict[str, SearchResult] = {}

    for rank, r in enumerate(keyword_results):
        key = r.content_block_id
        scores[key] = scores.get(key, 0.0) + keyword_weight / (rank + rrf_k)
        result_map[key] = r

    for rank, r in enumerate(sem_results):
        key = r.content_block_id
        scores[key] = scores.get(key, 0.0) + semantic_weight / (rank + rrf_k)
        if key not in result_map:
            result_map[key] = r

    ranked = sorted(scores.items(), key=lambda x: x[1], reverse=True)[:limit]

    return [
        SearchResult(
            score=score,
            conversation_id=result_map[key].conversation_id,
            conversation_title=result_map[key].conversation_title,
            source_id=result_map[key].source_id,
            project=result_map[key].project,
            message_role=result_map[key].message_role,
            message_created_at=result_map[key].message_created_at,
            text=result_map[key].text,
            content_block_id=result_map[key].content_block_id,
        )
        for key, score in ranked
        if key in result_map
    ]

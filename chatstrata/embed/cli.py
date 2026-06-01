"""Embed CLI command for chatstrata."""

from __future__ import annotations

import click


class EmbedStats:
    """Summary of an embedding generation run."""

    def __init__(self, candidates: int, above_threshold: int, embedded: int) -> None:
        self.candidates = candidates
        self.above_threshold = above_threshold
        self.embedded = embedded


def _estimate_tokens(text: str) -> int:
    return len(text.split())


def _require_embeddings():
    try:
        from sentence_transformers import SentenceTransformer  # noqa: F401
    except ImportError:
        raise click.UsageError(
            "The [embeddings] extras are required for this command.\n"
            'Install with: uv pip install "chatstrata[embeddings]"'
        )


@click.command()
@click.option("--source", default=None, help="Filter to a specific source (e.g. claude_code).")
@click.option("--since", default=None, help="Only embed messages after this date (YYYY-MM-DD).")
@click.option("--model", "model_name", default="all-MiniLM-L6-v2", help="Embedding model name.")
@click.option("--min-tokens", type=int, default=50, help="Skip messages shorter than this (default: 50).")
@click.option("--batch-size", type=int, default=64, help="Batch size for embedding (default: 64).")
@click.option("--db", default=None, help="Override the database path.")
def embed(
    source: str | None,
    since: str | None,
    model_name: str,
    min_tokens: int,
    batch_size: int,
    db: str | None,
) -> None:
    """Generate embeddings for messages.

    Populates the message_embeddings table for non-trivial messages.
    Skips messages already embedded with the same model.

    \b
    Example:
        chatstrata embed --source claude_code
        chatstrata embed --model all-MiniLM-L6-v2 --min-tokens 30
    """
    _require_embeddings()

    from chatstrata.core.db import connect, resolve_db_path
    from chatstrata.embed import get_provider

    provider = get_provider(model_name)
    click.echo(f"Model: {provider.name} ({provider.dimension}d)")

    conn = connect(resolve_db_path(db))
    try:
        stats = generate_embeddings(
            conn,
            provider,
            source=source,
            since=since,
            min_tokens=min_tokens,
            batch_size=batch_size,
            show_progress=True,
        )
        if not stats.candidates:
            click.echo("No new messages to embed.")
            return

        click.echo(
            f"Found {stats.candidates} un-embedded messages, "
            f"{stats.above_threshold} above {min_tokens}-token threshold."
        )
        if not stats.above_threshold:
            click.echo("Nothing to embed.")
            return

        click.echo(f"\nDone. Embedded {stats.embedded} messages with {provider.name}.")
    finally:
        conn.close()


def generate_embeddings(
    conn,
    provider,
    *,
    source: str | None = None,
    since: str | None = None,
    min_tokens: int = 50,
    batch_size: int = 64,
    show_progress: bool = False,
) -> EmbedStats:
    """Generate missing embeddings for messages and return run counts."""
    candidates = _get_candidates(conn, provider.name, source=source, since=since)
    above_threshold = [
        (mid, text) for mid, text in candidates if _estimate_tokens(text) >= min_tokens
    ]
    if not above_threshold:
        return EmbedStats(
            candidates=len(candidates),
            above_threshold=len(above_threshold),
            embedded=0,
        )

    embedded = 0
    progress = click.progressbar(length=len(above_threshold), label="Embedding")
    bar_context = progress if show_progress else NullProgressBar()
    with bar_context as bar:
        for i in range(0, len(above_threshold), batch_size):
            batch = above_threshold[i : i + batch_size]
            ids = [mid for mid, _ in batch]
            texts = [text for _, text in batch]

            vectors = provider.embed_texts(texts)

            for msg_id, vector in zip(ids, vectors):
                conn.execute(
                    "INSERT INTO message_embeddings (message_id, model, vector) "
                    "VALUES (?, ?, ?)",
                    [msg_id, provider.name, vector],
                )
            embedded += len(batch)
            if show_progress:
                bar.update(len(batch))

    return EmbedStats(
        candidates=len(candidates),
        above_threshold=len(above_threshold),
        embedded=embedded,
    )


class NullProgressBar:
    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, tb):
        return False

    def update(self, n: int) -> None:
        return None


def _get_candidates(
    conn,
    model_name: str,
    *,
    source: str | None = None,
    since: str | None = None,
) -> list[tuple[str, str]]:
    """Return (message_id, aggregated_text) for messages not yet embedded."""
    conditions = ["me.message_id IS NULL", "cb.text IS NOT NULL"]
    params: list = [model_name]

    if source:
        conditions.append("c.source_id = ?")
        params.append(source)
    if since:
        from datetime import datetime, timezone

        since_dt = datetime.strptime(since, "%Y-%m-%d").replace(tzinfo=timezone.utc)
        conditions.append("m.created_at >= ?")
        params.append(since_dt)

    where_clause = " AND ".join(conditions)

    sql = f"""
        SELECT m.id,
               STRING_AGG(cb.text, '\n' ORDER BY cb.block_index) AS full_text
        FROM messages m
        JOIN content_blocks cb ON cb.message_id = m.id
        JOIN conversations c ON c.id = m.conversation_id
        LEFT JOIN message_embeddings me
            ON me.message_id = m.id AND me.model = ?
        WHERE {where_clause}
        GROUP BY m.id
    """

    rows = conn.execute(sql, params).fetchall()
    return [(row[0], row[1]) for row in rows if row[1]]

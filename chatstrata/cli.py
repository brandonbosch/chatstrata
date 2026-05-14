"""chatstrata CLI."""

from __future__ import annotations

import json
import sys

import click

from chatstrata import __version__
from chatstrata.analysis.cli import analyze
from chatstrata.core.db import (
    apply_migrations,
    connect,
    get_schema_version,
    rebuild_fts_index,
    resolve_db_path,
)
from chatstrata.core.ingest import ensure_source, ingest_conversation
from chatstrata.core.migrations import LATEST_VERSION
from chatstrata.core.search import search_messages, snippet
from chatstrata.sources import load_adapters


@click.group()
@click.version_option(__version__, prog_name="chatstrata")
def cli() -> None:
    """chatstrata: a personal, queryable archive of your AI conversations."""


@cli.command("sources")
def list_sources() -> None:
    """List installed source adapters."""
    adapters = load_adapters()
    if not adapters:
        click.echo("No source adapters installed.")
        return
    for name, adapter in sorted(adapters.items()):
        click.echo(f"  {name:20} {adapter.display_name}  (v{adapter.version})")


@cli.command()
@click.argument("source_name", required=True)
@click.option("--path", "path", default=None, help="Override the default path for this source.")
@click.option("--db", "db", default=None, help="Override the database path.")
@click.option("--limit", type=int, default=None, help="Ingest at most N conversations.")
@click.option("--dry-run", is_flag=True, help="Discover only; do not write to the database.")
def ingest(source_name: str, path: str | None, db: str | None, limit: int | None, dry_run: bool) -> None:
    """Ingest conversations from a source.

    Example: chatstrata ingest claude_code
    """
    adapters = load_adapters()
    if source_name not in adapters:
        click.echo(f"Unknown source: {source_name}", err=True)
        click.echo("Run `chatstrata sources` to see available adapters.", err=True)
        sys.exit(1)
    adapter = adapters[source_name]

    config = {"path": path} if path else None
    handles = list(adapter.discover(config))
    if not handles:
        click.echo(f"No conversations found for source '{source_name}'.")
        return

    if limit:
        handles = handles[:limit]

    if dry_run:
        click.echo(f"Would ingest {len(handles)} conversations from {source_name}:")
        for h in handles[:20]:
            loc = h.path or h.source_native_id
            click.echo(f"  {h.source_native_id}  ({loc})")
        if len(handles) > 20:
            click.echo(f"  ... and {len(handles) - 20} more")
        return

    db_path = resolve_db_path(db)
    conn = connect(db_path)
    try:
        ensure_source(
            conn,
            source_id=adapter.name,
            name=adapter.display_name,
            adapter_version=adapter.version,
        )

        successes = 0
        failures = 0
        with click.progressbar(handles, label=f"Ingesting from {source_name}") as bar:
            for handle in bar:
                try:
                    conv = adapter.parse(handle)
                    if not conv.messages:
                        continue
                    ingest_conversation(conn, adapter.name, conv)
                    successes += 1
                except Exception as e:  # noqa: BLE001
                    failures += 1
                    click.echo(f"\n  ! failed to parse {handle.source_native_id}: {e}", err=True)

        click.echo(f"\nDone. Ingested: {successes}  Failed: {failures}")
        click.echo(f"Database: {db_path}")
    finally:
        conn.close()


@cli.command()
@click.argument("sql", required=True)
@click.option("--db", "db", default=None, help="Override the database path.")
@click.option("--json", "as_json", is_flag=True, help="Output rows as JSON.")
def query(sql: str, db: str | None, as_json: bool) -> None:
    """Run a SQL query against the chatstrata database.

    Example: chatstrata query "SELECT role, COUNT(*) FROM messages GROUP BY role"
    """
    conn = connect(resolve_db_path(db))
    try:
        result = conn.execute(sql)
        cols = [d[0] for d in result.description] if result.description else []
        rows = result.fetchall()
        if as_json:
            out = [dict(zip(cols, row, strict=False)) for row in rows]
            click.echo(json.dumps(out, default=str, indent=2))
        else:
            if cols:
                click.echo("\t".join(cols))
                click.echo("\t".join("-" * max(3, len(c)) for c in cols))
            for row in rows:
                click.echo("\t".join(str(v) if v is not None else "" for v in row))
    finally:
        conn.close()


@cli.command()
@click.argument("query", required=True)
@click.option("--db", "db", default=None, help="Override the database path.")
@click.option("--source", "source", default=None, help="Filter to a specific source (e.g. claude_code).")
@click.option("--since", "since", default=None, help="Only include messages after this date (YYYY-MM-DD).")
@click.option("--until", "until_", default=None, help="Only include messages before this date (YYYY-MM-DD).")
@click.option("--limit", type=int, default=20, help="Maximum number of results (default: 20).")
@click.option("--json", "as_json", is_flag=True, help="Output results as JSON.")
def search(
    query: str,
    db: str | None,
    source: str | None,
    since: str | None,
    until_: str | None,
    limit: int,
    as_json: bool,
) -> None:
    """Search conversations by keyword.

    Example: chatstrata search "auth module"
    """
    from datetime import datetime, timezone

    since_dt = None
    until_dt = None
    if since:
        since_dt = datetime.strptime(since, "%Y-%m-%d").replace(tzinfo=timezone.utc)
    if until_:
        until_dt = datetime.strptime(until_, "%Y-%m-%d").replace(tzinfo=timezone.utc)

    conn = connect(resolve_db_path(db))
    try:
        results = search_messages(
            conn, query, limit=limit, source=source, since=since_dt, until=until_dt,
        )
        if not results:
            click.echo("No results. If you recently ingested data, run `chatstrata reindex` first.")
            return

        if as_json:
            out = [
                {
                    "score": r.score,
                    "conversation_id": r.conversation_id,
                    "title": r.conversation_title,
                    "source": r.source_id,
                    "project": r.project,
                    "role": r.message_role,
                    "created_at": str(r.message_created_at) if r.message_created_at else None,
                    "snippet": snippet(r.text, query),
                }
                for r in results
            ]
            click.echo(json.dumps(out, default=str, indent=2))
            return

        for i, r in enumerate(results):
            if i > 0:
                click.echo()
            title = r.conversation_title or "(untitled)"
            ts = str(r.message_created_at)[:19] if r.message_created_at else "?"
            click.echo(f"  [{r.source_id}] {title}")
            click.echo(f"  {r.message_role} @ {ts}  (score: {r.score:.2f})")
            click.echo(f"  {snippet(r.text, query)}")
    finally:
        conn.close()


@cli.command()
@click.option("--db", "db", default=None, help="Override the database path.")
def reindex(db: str | None) -> None:
    """Rebuild the full-text search index.

    Run this after ingesting new data to update search results.
    """
    conn = connect(resolve_db_path(db))
    try:
        version = get_schema_version(conn)
        if version < 2:
            click.echo("FTS not available. Run `chatstrata migrate` first.", err=True)
            sys.exit(1)

        n_blocks = conn.execute(
            "SELECT COUNT(*) FROM content_blocks WHERE text IS NOT NULL"
        ).fetchone()[0]
        click.echo(f"Rebuilding search index over {n_blocks} content blocks...")
        rebuild_fts_index(conn)
        click.echo("Done. Search index is up to date.")
    finally:
        conn.close()


@cli.command()
@click.option("--db", "db", default=None)
def stats(db: str | None) -> None:
    """Show a summary of what's in the database."""
    conn = connect(resolve_db_path(db))
    try:
        click.echo(f"Database: {resolve_db_path(db)}")
        click.echo()

        sources = conn.execute(
            """
            SELECT s.id, s.name, COUNT(c.id) AS conversations,
                   MIN(c.started_at) AS earliest,
                   MAX(c.ended_at) AS latest
            FROM sources s
            LEFT JOIN conversations c ON c.source_id = s.id
            GROUP BY s.id, s.name
            ORDER BY s.id
            """
        ).fetchall()
        if not sources:
            click.echo("No data ingested yet. Try `chatstrata ingest claude_code`.")
            return

        click.echo("Sources:")
        for sid, name, n_conv, earliest, latest in sources:
            click.echo(f"  {sid:20} {name:25} {n_conv:>6} conversations  "
                       f"{earliest or '-'}  to  {latest or '-'}")

        totals = conn.execute(
            """
            SELECT
                (SELECT COUNT(*) FROM conversations),
                (SELECT COUNT(*) FROM messages),
                (SELECT COUNT(*) FROM content_blocks),
                (SELECT COUNT(*) FROM content_blocks WHERE type = 'tool_use')
            """
        ).fetchone()
        click.echo()
        click.echo(f"Totals: conversations={totals[0]}, messages={totals[1]}, "
                   f"content_blocks={totals[2]}, tool_calls={totals[3]}")
    finally:
        conn.close()


@cli.command()
@click.option("--db", "db", default=None)
def doctor(db: str | None) -> None:
    """Run basic sanity checks on the database."""
    conn = connect(resolve_db_path(db))
    issues = 0
    try:
        # Empty sources
        empty_sources = conn.execute(
            """
            SELECT s.id FROM sources s
            LEFT JOIN conversations c ON c.source_id = s.id
            GROUP BY s.id HAVING COUNT(c.id) = 0
            """
        ).fetchall()
        for (sid,) in empty_sources:
            click.echo(f"  ⚠ source '{sid}' has no conversations")
            issues += 1

        # Conversations with no messages
        empty_convs = conn.execute(
            """
            SELECT COUNT(*) FROM conversations c
            WHERE NOT EXISTS (SELECT 1 FROM messages m WHERE m.conversation_id = c.id)
            """
        ).fetchone()[0]
        if empty_convs:
            click.echo(f"  ⚠ {empty_convs} conversations have no messages")
            issues += 1

        # Messages with no content
        empty_msgs = conn.execute(
            """
            SELECT COUNT(*) FROM messages m
            WHERE NOT EXISTS (SELECT 1 FROM content_blocks cb WHERE cb.message_id = m.id)
            """
        ).fetchone()[0]
        if empty_msgs:
            click.echo(f"  ⚠ {empty_msgs} messages have no content blocks")
            issues += 1

        if issues == 0:
            click.echo("✓ All checks passed.")
        else:
            click.echo(f"\n{issues} issue(s) found.")
    finally:
        conn.close()


@cli.command()
@click.option("--db", "db", default=None, help="Override the database path.")
@click.option("--status", "status_only", is_flag=True, help="Show migration status without applying.")
def migrate(db: str | None, status_only: bool) -> None:
    """Apply pending schema migrations (or show status with --status)."""
    db_path = resolve_db_path(db)
    conn = connect(db_path, auto_migrate=False)
    try:
        current = get_schema_version(conn)
        if status_only:
            pending = LATEST_VERSION - current
            if pending > 0:
                click.echo(f"Schema version: {current}, latest: {LATEST_VERSION}, "
                           f"{pending} migration(s) pending")
            else:
                click.echo(f"Schema is up to date (version {current}).")
            return

        applied = apply_migrations(conn)
        if not applied:
            click.echo(f"Already at latest version ({current}).")
        else:
            for m in applied:
                click.echo(f"Applied migration {m.version:04d}: {m.description}")
            click.echo(f"\nSchema upgraded to version {applied[-1].version}.")
    finally:
        conn.close()


cli.add_command(analyze)

if __name__ == "__main__":
    cli()

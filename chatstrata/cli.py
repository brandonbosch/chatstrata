"""chatstrata CLI."""

from __future__ import annotations

import json
import sys
from pathlib import Path

import click

from chatstrata import __version__
from chatstrata.core.db import connect, get_default_db_path
from chatstrata.core.ingest import ensure_source, ingest_conversation
from chatstrata.sources import load_adapters


def _resolve_db(db: str | None) -> Path:
    return Path(db).expanduser() if db else get_default_db_path()


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

    db_path = _resolve_db(db)
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
    conn = connect(_resolve_db(db))
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
@click.option("--db", "db", default=None)
def stats(db: str | None) -> None:
    """Show a summary of what's in the database."""
    conn = connect(_resolve_db(db))
    try:
        click.echo(f"Database: {_resolve_db(db)}")
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
    conn = connect(_resolve_db(db))
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


if __name__ == "__main__":
    cli()

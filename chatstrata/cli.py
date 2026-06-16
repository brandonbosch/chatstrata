"""chatstrata CLI."""

from __future__ import annotations

import json
import os
import shlex
import sys
from dataclasses import dataclass

import click

from chatstrata import __version__
from chatstrata.analysis.cli import analyze
from chatstrata.core.db import (
    apply_migrations,
    connect,
    get_default_config_path,
    get_default_data_dir,
    get_schema_version,
    rebuild_fts_index,
    resolve_db_path,
)
from chatstrata.core.ingest import ensure_source, get_stored_mtime, ingest_conversation
from chatstrata.core.migrations import LATEST_VERSION
from chatstrata.core.models import ConversationHandle
from chatstrata.core.search import search_messages, snippet
from chatstrata.embed.cli import embed
from chatstrata.mcp.safety import execute_safe
from chatstrata.redact.cli import redact
from chatstrata.schedule.cli import schedule
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


@cli.command("paths")
@click.option("--db", "db", default=None, help="Show paths for this database override.")
def paths(db: str | None) -> None:
    """Show where chatstrata stores local files."""
    db_path = resolve_db_path(db)
    click.echo(f"Database: {db_path}")
    click.echo(f"Data dir:  {get_default_data_dir()}")
    click.echo(f"Config:    {get_default_config_path()}  (optional)")
    click.echo()
    click.echo("Override the database with CHATSTRATA_DB or per-command --db.")


def _mcp_stdio_spec(runner: str) -> dict:
    if runner == "uvx":
        return {
            "type": "stdio",
            "command": "uvx",
            "args": ["--from", "chatstrata[mcp]", "chatstrata-mcp"],
        }
    return {
        "type": "stdio",
        "command": "chatstrata-mcp",
        "args": [],
    }


@cli.group("mcp")
def mcp_cli() -> None:
    """Generate MCP client setup snippets."""


@mcp_cli.command("config")
@click.argument("client", type=click.Choice(["claude-code", "claude-desktop"]))
@click.option(
    "--runner",
    type=click.Choice(["uvx", "installed"]),
    default="uvx",
    show_default=True,
    help="How the MCP server should be launched.",
)
@click.option("--db", "db", default=None, help="Set CHATSTRATA_DB for the MCP server.")
@click.option(
    "--scope",
    type=click.Choice(["local", "project", "user"]),
    default="user",
    show_default=True,
    help="Claude Code MCP scope.",
)
def mcp_config(client: str, runner: str, db: str | None, scope: str) -> None:
    """Print setup for an MCP client.

    Examples:
        chatstrata mcp config claude-code
        chatstrata mcp config claude-desktop --runner installed
    """
    spec = _mcp_stdio_spec(runner)
    if db:
        spec["env"] = {"CHATSTRATA_DB": str(resolve_db_path(db))}

    if client == "claude-desktop":
        click.echo(json.dumps({"mcpServers": {"chatstrata": spec}}, indent=2))
        return

    command = ["claude", "mcp", "add", "--transport", "stdio", "--scope", scope]
    if db:
        command.extend(["--env", f"CHATSTRATA_DB={resolve_db_path(db)}"])
    command.extend(["chatstrata", "--", spec["command"]])
    command.extend(spec["args"])
    click.echo(shlex.join(command))


@cli.command("init")
@click.option("--db", "db", default=None, help="Create or migrate this database path.")
@click.option("--no-discover", is_flag=True, help="Skip source discovery checks.")
def init(db: str | None, no_discover: bool) -> None:
    """Create the local database and show first-run next steps."""
    db_path = resolve_db_path(db)
    existed = db_path.exists()
    conn = connect(db_path)
    try:
        version = get_schema_version(conn)
    finally:
        conn.close()

    action = "Using existing database" if existed else "Created database"
    click.echo(f"{action}:")
    click.echo(f"  {db_path}")
    click.echo()
    click.echo(f"Schema: version {version}, latest {LATEST_VERSION}")

    if not no_discover:
        click.echo()
        click.echo("Detected sources:")
        adapters = load_adapters()
        if not adapters:
            click.echo("  none")
        for name, adapter in sorted(adapters.items()):
            try:
                count = len(list(adapter.discover()))
                status = f"{count} conversation{'s' if count != 1 else ''} found"
            except Exception as exc:  # noqa: BLE001
                status = f"not available ({exc})"
            click.echo(f"  {name:20} {status}")

    click.echo()
    click.echo("Next:")
    click.echo("  chatstrata sources")
    click.echo("  chatstrata ingest --auto")
    click.echo("  chatstrata ingest <source> --incremental")
    click.echo("  chatstrata stats")


def _get_file_mtime(handle: ConversationHandle) -> float | None:
    if handle.path is None:
        return None
    try:
        return os.path.getmtime(handle.path)
    except OSError:
        return None


@dataclass
class IngestResult:
    source_name: str
    discovered: int
    ingested: int = 0
    skipped: int = 0
    failed: int = 0
    warned_no_path: bool = False


def _source_has_ingested_conversations(conn, source_name: str) -> bool:
    row = conn.execute(
        "SELECT 1 FROM conversations WHERE source_id = ? LIMIT 1",
        [source_name],
    ).fetchone()
    return row is not None


def _ingest_source(
    conn,
    adapter,
    handles: list[ConversationHandle],
    *,
    incremental: bool,
    progress_label: str,
) -> IngestResult:
    ensure_source(
        conn,
        source_id=adapter.name,
        name=adapter.display_name,
        adapter_version=adapter.version,
    )

    result = IngestResult(source_name=adapter.name, discovered=len(handles))

    with click.progressbar(handles, label=progress_label) as bar:
        for handle in bar:
            try:
                file_mtime = _get_file_mtime(handle)

                if incremental:
                    if file_mtime is None and not result.warned_no_path:
                        click.echo(
                            f"\n  Warning: source '{adapter.name}' has handles without file paths; "
                            "incremental mode cannot skip these.",
                            err=True,
                        )
                        result.warned_no_path = True

                    if file_mtime is not None:
                        stored = get_stored_mtime(conn, adapter.name, handle.source_native_id)
                        if stored is not None and stored == file_mtime:
                            result.skipped += 1
                            continue

                conv = adapter.parse(handle)
                if not conv.messages:
                    continue
                ingest_conversation(
                    conn, adapter.name, conv,
                    source_file_mtime=file_mtime,
                )
                result.ingested += 1
            except Exception as e:  # noqa: BLE001
                result.failed += 1
                click.echo(f"\n  ! failed to parse {handle.source_native_id}: {e}", err=True)

    return result


@cli.command()
@click.argument("source_name", required=False)
@click.option("--path", "path", default=None, help="Override the default path for this source.")
@click.option("--db", "db", default=None, help="Override the database path.")
@click.option("--limit", type=int, default=None, help="Ingest at most N conversations.")
@click.option("--dry-run", is_flag=True, help="Discover only; do not write to the database.")
@click.option("--incremental", is_flag=True, help="Skip conversations whose source file has not changed since last ingest.")
@click.option("--auto", "auto_mode", is_flag=True, help="Discover available default sources, ingest them, then generate embeddings.")
@click.option("--no-embed", is_flag=True, help="With --auto, ingest sources but skip embedding generation.")
@click.option("--model", "embed_model", default="all-MiniLM-L6-v2", help="Embedding model for --auto.")
@click.option("--min-tokens", type=int, default=50, help="Minimum message size for --auto embeddings.")
@click.option("--batch-size", type=int, default=64, help="Embedding batch size for --auto.")
def ingest(
    source_name: str | None,
    path: str | None,
    db: str | None,
    limit: int | None,
    dry_run: bool,
    incremental: bool,
    auto_mode: bool,
    no_embed: bool,
    embed_model: str,
    min_tokens: int,
    batch_size: int,
) -> None:
    """Ingest conversations from a source.

    Example: chatstrata ingest claude_code
    """
    if auto_mode:
        if source_name:
            raise click.UsageError("Do not pass a source name with --auto.")
        if path:
            raise click.UsageError("--path is only supported when ingesting one source.")
        _auto_ingest(
            db=db,
            limit=limit,
            dry_run=dry_run,
            no_embed=no_embed,
            embed_model=embed_model,
            min_tokens=min_tokens,
            batch_size=batch_size,
        )
        return

    if not source_name:
        raise click.UsageError("Missing argument 'SOURCE_NAME'. Use `chatstrata ingest --auto` to auto-detect sources.")
    if no_embed:
        raise click.UsageError("--no-embed is only supported with --auto.")

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
        result = _ingest_source(
            conn,
            adapter,
            handles,
            incremental=incremental,
            progress_label=f"Ingesting from {source_name}",
        )
        click.echo(
            f"\nDone. Ingested: {result.ingested}  "
            f"Skipped: {result.skipped}  Failed: {result.failed}"
        )
        click.echo(f"Database: {db_path}")
    finally:
        conn.close()


def _auto_ingest(
    *,
    db: str | None,
    limit: int | None,
    dry_run: bool,
    no_embed: bool,
    embed_model: str,
    min_tokens: int,
    batch_size: int,
) -> None:
    adapters = load_adapters()
    if not adapters:
        click.echo("No source adapters installed.")
        return

    discovered: list[tuple[str, object, list[ConversationHandle]]] = []
    errors: list[tuple[str, Exception]] = []
    for name, adapter in sorted(adapters.items()):
        try:
            handles = list(adapter.discover())
        except Exception as exc:  # noqa: BLE001
            errors.append((name, exc))
            continue
        if limit:
            handles = handles[:limit]
        if handles:
            discovered.append((name, adapter, handles))

    if errors:
        for name, exc in errors:
            click.echo(f"  ! failed to discover {name}: {exc}", err=True)

    if not discovered:
        click.echo("No conversations found in default source locations.")
        return

    if dry_run:
        click.echo("Would auto-ingest detected sources:")
        for name, _adapter, handles in discovered:
            click.echo(f"  {name:20} {len(handles)} conversation{'s' if len(handles) != 1 else ''}")
        return

    db_path = resolve_db_path(db)
    conn = connect(db_path)
    try:
        results: list[tuple[IngestResult, bool]] = []
        for name, adapter, handles in discovered:
            use_incremental = _source_has_ingested_conversations(conn, name)
            mode = "incremental" if use_incremental else "full"
            click.echo(f"\n{name}: {len(handles)} conversation{'s' if len(handles) != 1 else ''} found ({mode})")
            result = _ingest_source(
                conn,
                adapter,
                handles,
                incremental=use_incremental,
                progress_label=f"Ingesting from {name}",
            )
            results.append((result, use_incremental))

        click.echo("\nIngest summary:")
        for result, use_incremental in results:
            mode = "incremental" if use_incremental else "full"
            click.echo(
                f"  {result.source_name:20} {mode:11} "
                f"ingested={result.ingested} skipped={result.skipped} failed={result.failed}"
            )

        if no_embed:
            click.echo(f"\nDatabase: {db_path}")
            return

        from chatstrata.embed import get_provider
        from chatstrata.embed.cli import _require_embeddings, generate_embeddings

        _require_embeddings()
        provider = get_provider(embed_model)
        click.echo(f"\nEmbedding model: {provider.name} ({provider.dimension}d)")
        stats = generate_embeddings(
            conn,
            provider,
            min_tokens=min_tokens,
            batch_size=batch_size,
            show_progress=True,
        )
        click.echo(
            f"\nEmbedding summary: candidates={stats.candidates} "
            f"above_threshold={stats.above_threshold} embedded={stats.embedded}"
        )
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
        try:
            cols, rows, truncated = execute_safe(conn, sql)
        except ValueError as exc:
            raise click.UsageError(str(exc)) from exc
        if as_json:
            out = [dict(zip(cols, row, strict=False)) for row in rows]
            click.echo(json.dumps(out, default=str, indent=2))
        else:
            if cols:
                click.echo("\t".join(cols))
                click.echo("\t".join("-" * max(3, len(c)) for c in cols))
            for row in rows:
                click.echo("\t".join(str(v) if v is not None else "" for v in row))
            if truncated:
                click.echo("\nResults truncated. Refine the query or add LIMIT.")
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
@click.option("--semantic", is_flag=True, help="Use semantic similarity search (requires embeddings).")
@click.option("--hybrid", is_flag=True, help="Combine keyword + semantic search via reciprocal rank fusion.")
@click.option("--model", "embed_model", default="all-MiniLM-L6-v2", help="Embedding model for --semantic/--hybrid.")
def search(
    query: str,
    db: str | None,
    source: str | None,
    since: str | None,
    until_: str | None,
    limit: int,
    as_json: bool,
    semantic: bool,
    hybrid: bool,
    embed_model: str,
) -> None:
    """Search conversations by keyword (default) or semantic similarity.

    \b
    Examples:
        chatstrata search "auth module"
        chatstrata search --semantic "grandma video"
        chatstrata search --hybrid "refactor login"
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
        if semantic or hybrid:
            from chatstrata.embed import get_provider
            from chatstrata.embed.search import (
                hybrid_search,
            )
            from chatstrata.embed.search import (
                semantic_search as sem_search,
            )

            provider = get_provider(embed_model)
            query_vector = provider.embed_query(query)

            if hybrid:
                results = hybrid_search(
                    conn, query, query_vector, provider.name,
                    limit=limit, source=source, since=since_dt, until=until_dt,
                )
            else:
                results = sem_search(
                    conn, query_vector, provider.name,
                    limit=limit, source=source, since=since_dt, until=until_dt,
                )
        else:
            results = search_messages(
                conn, query, limit=limit, source=source, since=since_dt, until=until_dt,
            )

        if not results:
            if semantic or hybrid:
                click.echo("No results. Run `chatstrata embed` first to generate embeddings.")
            else:
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

        mode_label = "hybrid" if hybrid else ("semantic" if semantic else "keyword")
        click.echo(f"  ({mode_label} search, {len(results)} results)\n")
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


@cli.command()
@click.option(
    "--transport",
    type=click.Choice(["stdio", "streamable-http", "sse"]),
    default="stdio",
    help="MCP transport protocol (default: stdio).",
)
@click.option("--host", default="127.0.0.1", help="Host for HTTP transports.")
@click.option("--port", type=int, default=8462, help="Port for HTTP transports.")
def serve(transport: str, host: str, port: int) -> None:
    """Start the ChatStrata MCP server.

    \b
    Examples:
        chatstrata serve                                    # stdio for Claude Code
        chatstrata serve --transport streamable-http        # HTTP for remote access
        chatstrata serve --transport streamable-http --host 0.0.0.0  # Tailscale
    """
    try:
        from chatstrata.mcp.server import mcp as mcp_server
    except ImportError:
        click.echo(
            "MCP dependencies not installed. Run: uv pip install chatstrata[mcp]",
            err=True,
        )
        sys.exit(1)

    if transport == "stdio":
        mcp_server.run(transport="stdio")
    elif transport == "streamable-http":
        mcp_server.run(transport="streamable-http", host=host, port=port)
    elif transport == "sse":
        mcp_server.run(transport="sse", host=host, port=port)


cli.add_command(analyze)
cli.add_command(embed)
cli.add_command(redact)
cli.add_command(schedule)

if __name__ == "__main__":
    cli()

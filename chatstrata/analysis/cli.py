"""Analyze subcommands for chatstrata."""

from __future__ import annotations

import json

import click

from chatstrata.analysis import build_source_filter, load_query
from chatstrata.core.db import connect, resolve_db_path


def _output(cols: list[str], rows: list[tuple], as_json: bool) -> None:
    """Output results as either a formatted table or JSON."""
    if as_json:
        out = [dict(zip(cols, row, strict=False)) for row in rows]
        click.echo(json.dumps(out, default=str, indent=2))
        return
    if not rows:
        click.echo("No data.")
        return
    widths = [
        max(len(c), *(len(str(v) if v is not None else "") for v in (r[i] for r in rows)))
        for i, c in enumerate(cols)
    ]
    click.echo("  ".join(c.ljust(w) for c, w in zip(cols, widths)))
    click.echo("  ".join("-" * w for w in widths))
    for row in rows:
        click.echo("  ".join(str(v if v is not None else "").ljust(w) for v, w in zip(row, widths)))


@click.group()
def analyze() -> None:
    """Analyze your conversation archive."""


@analyze.command()
@click.option(
    "--by", "granularity", type=click.Choice(["day", "week", "month"]),
    default="month", help="Time granularity (default: month).",
)
@click.option("--source", default=None, help="Filter to a specific source.")
@click.option("--db", default=None, help="Override the database path.")
@click.option("--json", "as_json", is_flag=True, help="Output as JSON.")
def activity(granularity: str, source: str | None, db: str | None, as_json: bool) -> None:
    """Messages over time, grouped by period."""
    sql_template = load_query("activity")
    source_filter, params = build_source_filter(source)
    sql = sql_template.format(granularity=granularity, source_filter=source_filter)

    conn = connect(resolve_db_path(db))
    try:
        result = conn.execute(sql, params)
        cols = [d[0] for d in result.description]
        rows = result.fetchall()
        _output(cols, rows, as_json)
    finally:
        conn.close()


@analyze.command()
@click.option("--source", default=None, help="Filter to a specific source (e.g. claude_code).")
@click.option("--db", default=None, help="Override the database path.")
@click.option("--json", "as_json", is_flag=True, help="Output as JSON.")
def tools(source: str | None, db: str | None, as_json: bool) -> None:
    """Tool usage frequency."""
    sql_template = load_query("tools")
    source_filter, params = build_source_filter(source, column="source_id")
    sql = sql_template.format(source_filter=source_filter)

    conn = connect(resolve_db_path(db))
    try:
        result = conn.execute(sql, params)
        cols = [d[0] for d in result.description]
        rows = result.fetchall()
        _output(cols, rows, as_json)
    finally:
        conn.close()


@analyze.command()
@click.option("--longest", "longest_n", type=int, default=None, help="Show N longest conversations.")
@click.option("--shortest", "shortest_n", type=int, default=None, help="Show N shortest conversations.")
@click.option("--db", default=None, help="Override the database path.")
@click.option("--json", "as_json", is_flag=True, help="Output as JSON.")
def conversations(
    longest_n: int | None, shortest_n: int | None, db: str | None, as_json: bool,
) -> None:
    """Conversation length statistics."""
    if longest_n and shortest_n:
        raise click.UsageError("Specify --longest or --shortest, not both.")

    order = "DESC"
    limit = 20
    if shortest_n:
        order = "ASC"
        limit = shortest_n
    elif longest_n:
        limit = longest_n

    sql_template = load_query("conversations")
    sql = sql_template.format(order=order, limit=limit)

    conn = connect(resolve_db_path(db))
    try:
        result = conn.execute(sql)
        cols = [d[0] for d in result.description]
        rows = result.fetchall()
        _output(cols, rows, as_json)
    finally:
        conn.close()


@analyze.command()
@click.option("--db", default=None, help="Override the database path.")
@click.option("--json", "as_json", is_flag=True, help="Output as JSON.")
def models(db: str | None, as_json: bool) -> None:
    """Model usage breakdown."""
    sql = load_query("models")

    conn = connect(resolve_db_path(db))
    try:
        result = conn.execute(sql)
        cols = [d[0] for d in result.description]
        rows = result.fetchall()
        _output(cols, rows, as_json)
    finally:
        conn.close()


@analyze.command()
@click.option("--db", default=None, help="Override the database path.")
@click.option("--json", "as_json", is_flag=True, help="Output as JSON.")
def projects(db: str | None, as_json: bool) -> None:
    """Per-project conversation counts (Claude Code)."""
    sql = load_query("projects")

    conn = connect(resolve_db_path(db))
    try:
        result = conn.execute(sql)
        cols = [d[0] for d in result.description]
        rows = result.fetchall()
        _output(cols, rows, as_json)
    finally:
        conn.close()

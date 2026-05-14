"""Redact subcommands for chatstrata."""

from __future__ import annotations

import json
import sys

import click

from chatstrata.redact.base import RedactionMode

_MODE_CHOICES = [m.value for m in RedactionMode]


def _require_presidio():
    """Verify the [redact] extras are installed."""
    try:
        from chatstrata.redact.presidio_engine import PresidioEngine  # noqa: F401
    except ImportError:
        raise click.UsageError(
            "The [redact] extras are required for this command.\n"
            'Install with: uv pip install "chatstrata[redact]"'
        )


def _get_engine(allow_entities: tuple[str, ...] = ()):
    from chatstrata.redact.presidio_engine import DEFAULT_DENY_ENTITY_TYPES, PresidioEngine

    deny = DEFAULT_DENY_ENTITY_TYPES - frozenset(allow_entities)
    return PresidioEngine(deny_entity_types=deny)


@click.group()
def redact() -> None:
    """Detect and redact PII from text or query results."""


@redact.command("text")
@click.argument("input_text")
@click.option(
    "--mode",
    type=click.Choice(_MODE_CHOICES),
    default="mask",
    help="Redaction mode (default: mask).",
)
@click.option("--json", "as_json", is_flag=True, help="Output as JSON.")
@click.option(
    "--allow-entity",
    "allow_entities",
    multiple=True,
    help="Entity types to detect that are off by default (e.g. DATE_TIME, ORGANIZATION, PERSON).",
)
def redact_text(input_text: str, mode: str, as_json: bool, allow_entities: tuple[str, ...]) -> None:
    """Redact PII from a text string.

    Example: chatstrata redact text "My API key is sk-ant-api03-abc123..."
    """
    _require_presidio()
    engine = _get_engine(allow_entities)
    result = engine.redact(input_text, RedactionMode(mode))

    if as_json:
        out = {
            "original": result.original_text,
            "redacted": result.redacted_text,
            "entities": [
                {
                    "type": e.type,
                    "start": e.start,
                    "end": e.end,
                    "text": e.text,
                    "confidence": e.confidence,
                    "recognizer": e.recognizer,
                }
                for e in result.entities
            ],
            "mapping": result.mapping,
        }
        click.echo(json.dumps(out, indent=2))
        return

    click.echo(result.redacted_text)
    if result.entities:
        click.echo()
        click.echo(f"  {len(result.entities)} entit{'y' if len(result.entities) == 1 else 'ies'} detected:")
        for e in result.entities:
            click.echo(f"    {e.type:30} {e.confidence:.2f}  {e.text[:40]}")


@redact.command("query")
@click.argument("sql")
@click.option(
    "--mode",
    type=click.Choice(_MODE_CHOICES),
    default="mask",
    help="Redaction mode (default: mask).",
)
@click.option("--db", default=None, help="Override the database path.")
@click.option("--json", "as_json", is_flag=True, help="Output as JSON.")
@click.option(
    "--allow-entity",
    "allow_entities",
    multiple=True,
    help="Entity types to detect that are off by default (e.g. DATE_TIME, ORGANIZATION, PERSON).",
)
def redact_query(sql: str, mode: str, db: str | None, as_json: bool, allow_entities: tuple[str, ...]) -> None:
    """Run a SQL query and redact PII from text columns in the results.

    Example: chatstrata redact query "SELECT text FROM content_blocks LIMIT 5"
    """
    _require_presidio()
    from chatstrata.core.db import connect, resolve_db_path

    engine = _get_engine(allow_entities)
    redaction_mode = RedactionMode(mode)

    conn = connect(resolve_db_path(db))
    try:
        result = conn.execute(sql)
        cols = [d[0] for d in result.description] if result.description else []
        rows = result.fetchall()
    finally:
        conn.close()

    if not rows:
        click.echo("No results.")
        return

    redacted_rows = []
    for row in rows:
        new_row = []
        for val in row:
            if isinstance(val, str) and val.strip():
                r = engine.redact(val, redaction_mode)
                new_row.append(r.redacted_text)
            else:
                new_row.append(val)
        redacted_rows.append(tuple(new_row))

    if as_json:
        out = [dict(zip(cols, row, strict=False)) for row in redacted_rows]
        click.echo(json.dumps(out, default=str, indent=2))
        return

    if cols:
        widths = [
            max(
                len(c),
                *(len(str(v) if v is not None else "") for v in (r[i] for r in redacted_rows)),
            )
            for i, c in enumerate(cols)
        ]
        click.echo("  ".join(c.ljust(w) for c, w in zip(cols, widths)))
        click.echo("  ".join("-" * w for w in widths))
        for row in redacted_rows:
            click.echo(
                "  ".join(
                    str(v if v is not None else "").ljust(w) for v, w in zip(row, widths)
                )
            )


@redact.command("interactive")
@click.option("--db", default=None, help="Override the database path.")
@click.option(
    "--mode",
    type=click.Choice(_MODE_CHOICES),
    default="mask",
    help="Redaction mode for confirmed entities (default: mask).",
)
@click.option(
    "--sql",
    default=None,
    help="SQL to select text to review (must return a 'text' column).",
)
@click.option(
    "--allow-entity",
    "allow_entities",
    multiple=True,
    help="Entity types to detect that are off by default (e.g. DATE_TIME, ORGANIZATION, PERSON).",
)
def redact_interactive(db: str | None, mode: str, sql: str | None, allow_entities: tuple[str, ...]) -> None:
    """Interactively review and redact PII from your archive.

    Walks through detected entities one at a time, letting you confirm or skip.
    """
    _require_presidio()
    from chatstrata.core.db import connect, resolve_db_path

    engine = _get_engine(allow_entities)
    redaction_mode = RedactionMode(mode)

    if sql is None:
        sql = "SELECT id, text FROM content_blocks WHERE text IS NOT NULL LIMIT 100"

    conn = connect(resolve_db_path(db))
    try:
        result = conn.execute(sql)
        cols = [d[0] for d in result.description] if result.description else []
        rows = result.fetchall()
    finally:
        conn.close()

    if not rows:
        click.echo("No results to review.")
        return

    text_col = None
    for i, c in enumerate(cols):
        if c.lower() == "text":
            text_col = i
            break
    if text_col is None:
        text_col = 0

    skip_types: set[str] = set()
    redact_types: set[str] = set()
    total_redacted = 0
    total_skipped = 0

    for row_idx, row in enumerate(rows):
        text_val = row[text_col]
        if not isinstance(text_val, str) or not text_val.strip():
            continue

        entities = engine.detect(text_val)
        if not entities:
            continue

        click.echo(f"\n--- Block {row_idx + 1}/{len(rows)} ({len(entities)} entities) ---")

        to_redact: list[int] = []
        quit_all = False

        for ent_idx, e in enumerate(entities):
            if e.type in skip_types:
                total_skipped += 1
                continue
            if e.type in redact_types:
                to_redact.append(ent_idx)
                total_redacted += 1
                continue

            _show_entity_context(text_val, e, ent_idx + 1, len(entities))

            choice = _prompt_choice(e.type)
            if choice == "r":
                to_redact.append(ent_idx)
                total_redacted += 1
            elif choice == "s":
                total_skipped += 1
            elif choice == "R":
                redact_types.add(e.type)
                to_redact.append(ent_idx)
                total_redacted += 1
            elif choice == "S":
                skip_types.add(e.type)
                total_skipped += 1
            elif choice == "q":
                quit_all = True
                break

        if quit_all:
            break

        if to_redact:
            kept = [entities[i] for i in to_redact]
            _redacted, _mapping = engine._apply(text_val, kept, redaction_mode)
            click.echo(click.style("\nRedacted:", fg="green"))
            click.echo(f"  {_redacted[:200]}")

    click.echo(f"\nDone. Redacted: {total_redacted}  Skipped: {total_skipped}")


def _show_entity_context(text: str, entity, idx: int, total: int) -> None:
    """Display a text snippet highlighting the entity."""
    ctx = 40
    start = max(0, entity.start - ctx)
    end = min(len(text), entity.end + ctx)

    prefix = text[start : entity.start]
    matched = text[entity.start : entity.end]
    suffix = text[entity.end : end]

    if start > 0:
        prefix = "..." + prefix
    if end < len(text):
        suffix = suffix + "..."

    click.echo(
        f"\n  [{idx}/{total}] {click.style(entity.type, bold=True)}"
        f" (confidence: {entity.confidence:.2f})"
    )
    click.echo(f"  {prefix}{click.style(matched, fg='red', bold=True)}{suffix}")


def _prompt_choice(entity_type: str) -> str:
    """Prompt user for redaction decision."""
    click.echo(
        f"  [r]edact / [s]kip / "
        f"[R]edact all {entity_type} / [S]kip all {entity_type} / [q]uit"
    )
    while True:
        c = click.getchar()
        if c in ("r", "s", "R", "S", "q"):
            return c
        click.echo("  Invalid choice. Press r/s/R/S/q.", err=True)

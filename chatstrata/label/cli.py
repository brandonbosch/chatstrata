"""`chatstrata label` subcommands."""

from __future__ import annotations

import json
from datetime import datetime, timezone

import click

from chatstrata.analysis.cli import _output
from chatstrata.core.db import connect, resolve_db_path
from chatstrata.label.backend import JEV_PRICE_PER_MTOK, BackendError, TypeSafeBackend
from chatstrata.label.packs import BUILTIN_PACKS, get_pack
from chatstrata.label.runner import (
    TargetFilters,
    clear_labels,
    count_labels,
    estimate_tokens,
    run_pack,
    select_targets,
    summarize,
)


def _date(value: str | None) -> datetime | None:
    if not value:
        return None
    return datetime.strptime(value, "%Y-%m-%d").replace(tzinfo=timezone.utc)


def _pack_arg(_ctx, _param, value: str):
    try:
        return get_pack(value)
    except KeyError as exc:
        raise click.BadParameter(str(exc.args[0])) from exc


@click.group()
def label() -> None:
    """Label your archive with a classifier (TypeSafe Jev).

    \b
    Labelling sends the selected text to the TypeSafe API. Nothing is sent
    without a cost estimate and confirmation (or --yes).
    """


@label.command("packs")
@click.option("--json", "as_json", is_flag=True, help="Output full pack definitions as JSON.")
def list_packs(as_json: bool) -> None:
    """List available question packs."""
    if as_json:
        out = {
            p.name: {
                "description": p.description,
                "target_kind": p.target_kind,
                "version": p.version,
                "questions": p.questions,
            }
            for p in BUILTIN_PACKS.values()
        }
        click.echo(json.dumps(out, indent=2))
        return
    for pack in BUILTIN_PACKS.values():
        click.echo(f"{pack.name}  ({pack.target_kind}, version {pack.version})")
        click.echo(f"  {pack.description}")
        for qid, q in pack.questions.items():
            click.echo(f"  - {qid} [{q['type']}]")
        click.echo("")


@label.command("run")
@click.argument("pack", callback=_pack_arg)
@click.option("--source", default=None, help="Only label data from this source (e.g. omp).")
@click.option("--project", default=None, help="Only label projects whose path contains this text.")
@click.option("--tool", default=None, help="Only label calls to this tool (tool_call packs).")
@click.option("--since", default=None, help="Only label items on or after this date (YYYY-MM-DD).")
@click.option("--until", "until_", default=None, help="Only label items before this date (YYYY-MM-DD).")
@click.option("--limit", type=int, default=None, help="Label at most N items.")
@click.option("--relabel", is_flag=True, help="Re-label items that already have current labels.")
@click.option("--model", default="jev-latest", show_default=True, help="TypeSafe model or alias.")
@click.option("--concurrency", type=int, default=8, show_default=True, help="Parallel requests.")
@click.option("--dry-run", is_flag=True, help="Show what would be sent and the estimated cost.")
@click.option("--show-state", is_flag=True, help="With --dry-run, print the first state.")
@click.option("--yes", "-y", is_flag=True, help="Skip the confirmation prompt.")
@click.option("--db", default=None, help="Override the database path.")
def run(
    pack, source, project, tool, since, until_, limit, relabel, model, concurrency,
    dry_run, show_state, yes, db,
) -> None:
    """Label items with PACK's questions and store the answers.

    \b
    Examples:
        chatstrata label run tool-failures --source omp --limit 50 --dry-run
        chatstrata label run tool-failures --tool edit --since 2026-07-01
        chatstrata label run user-turns --since 2026-01-01
    """
    filters = TargetFilters(
        source=source, project=project, tool=tool, since=_date(since), until=_date(until_),
    )
    conn = connect(resolve_db_path(db))
    try:
        try:
            targets = select_targets(conn, pack, filters, limit=limit, relabel=relabel)
        except ValueError as exc:
            raise click.UsageError(str(exc)) from exc

        if not targets:
            click.echo(f"Nothing to label for {pack.name} (already labelled or no matches).")
            return

        tokens = estimate_tokens(pack, targets)
        cost = tokens / 1_000_000 * JEV_PRICE_PER_MTOK
        cost_text = f"~${cost:.2f}" if cost >= 0.01 else "<$0.01"
        click.echo(
            f"{pack.name}: {len(targets)} items, ~{tokens:,} input tokens, "
            f"{cost_text} at ${JEV_PRICE_PER_MTOK}/Mtok input"
        )

        if dry_run:
            if show_state:
                click.echo(json.dumps(targets[0].state, indent=2, ensure_ascii=False))
            return

        if not yes:
            click.confirm(
                f"Send {len(targets)} items to the TypeSafe API ({model})?", abort=True,
            )

        try:
            backend = TypeSafeBackend(model=model)
        except BackendError as exc:
            raise click.ClickException(str(exc)) from exc

        with click.progressbar(length=len(targets), label="Labelling") as bar:
            try:
                stats = run_pack(
                    conn, pack, backend, targets,
                    concurrency=concurrency, on_progress=bar.update,
                )
            except BackendError as exc:
                raise click.ClickException(str(exc)) from exc

        click.echo(
            f"Labelled {stats.labelled} items with {stats.model or model} "
            f"({stats.input_tokens:,} input tokens, {stats.failures} failures)."
        )
        for err in stats.errors:
            click.echo(f"  ! {err}", err=True)
        click.echo(f"Summarize with: chatstrata label summary {pack.name}")
    finally:
        conn.close()


@label.command("clear")
@click.argument("pack")
@click.option("--version", "version", default=None, help="Only clear this pack_version (e.g. a stale experiment).")
@click.option("--run", "run_id", default=None, help="Only clear labels from this run id.")
@click.option("--dry-run", is_flag=True, help="Show what would be deleted without deleting.")
@click.option("--yes", "-y", is_flag=True, help="Skip the confirmation prompt.")
@click.option("--db", default=None, help="Override the database path.")
def clear(pack: str, version, run_id, dry_run: bool, yes: bool, db: str | None) -> None:
    """Delete labels for PACK, to throw away an experiment.

    Only the labels and label_runs tables are touched; your archive is never
    changed. Scope the delete with --version (a single pack version) or --run.

    \b
    Examples:
        chatstrata label clear tool-failures --dry-run
        chatstrata label clear tool-failures --version cd323b311b30
        chatstrata label clear user-turns --run 3f2a... --yes
    """
    conn = connect(resolve_db_path(db))
    try:
        scope = count_labels(conn, pack, version=version, run_id=run_id)
        if scope.labels == 0 and scope.runs == 0:
            click.echo(f"No labels to clear for {pack!r} (nothing matched).")
            return

        where = pack
        if version:
            where += f" version {version}"
        if run_id:
            where += f" run {run_id}"
        click.echo(f"{where}: {scope.labels} labels and {scope.runs} runs match.")

        if dry_run:
            return
        if not yes:
            click.confirm(f"Delete {scope.labels} labels and {scope.runs} runs?", abort=True)

        clear_labels(conn, pack, version=version, run_id=run_id)
        click.echo(f"Cleared {scope.labels} labels and {scope.runs} runs.")
    finally:
        conn.close()


@label.command("summary")
@click.argument("pack", callback=_pack_arg)
@click.option("--by", default=None, help="Group by a dimension (source, tool, model, month, ...).")
@click.option(
    "--min-confidence", type=float, default=0.0, show_default=True,
    help="Ignore choice/score answers below this confidence.",
)
@click.option("--json", "as_json", is_flag=True, help="Output as JSON.")
@click.option("--db", default=None, help="Override the database path.")
def summary(pack, by: str | None, min_confidence: float, as_json: bool, db: str | None) -> None:
    """Summarize PACK's labels.

    \b
    Examples:
        chatstrata label summary tool-failures --by tool
        chatstrata label summary user-turns --by quarter
    """
    conn = connect(resolve_db_path(db))
    try:
        try:
            cols, rows = summarize(conn, pack, by=by, min_confidence=min_confidence)
        except ValueError as exc:
            raise click.UsageError(str(exc)) from exc
        _output(cols, rows, as_json)
    finally:
        conn.close()

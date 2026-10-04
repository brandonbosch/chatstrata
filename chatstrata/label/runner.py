"""Select targets for a pack, send them to a backend, and store the answers."""

from __future__ import annotations

import json
import uuid
from collections.abc import Callable
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass, field
from datetime import datetime
from typing import Any

import duckdb

from chatstrata.label.backend import BackendError, BackendResult, LabelBackend
from chatstrata.label.packs import Pack


@dataclass
class TargetFilters:
    source: str | None = None
    project: str | None = None
    tool: str | None = None
    since: datetime | None = None
    until: datetime | None = None


@dataclass
class Target:
    target_id: str
    state: dict[str, Any]


@dataclass
class RunStats:
    run_id: str
    model: str | None = None
    labelled: int = 0
    failures: int = 0
    input_tokens: int = 0
    errors: list[str] = field(default_factory=list)


def select_targets(
    conn: duckdb.DuckDBPyConnection,
    pack: Pack,
    filters: TargetFilters | None = None,
    *,
    limit: int | None = None,
    relabel: bool = False,
) -> list[Target]:
    """Return targets for ``pack`` that match ``filters``.

    Targets already labelled with the current pack version are skipped unless
    ``relabel`` is set, so re-running a pack only pays for new data.
    """
    filters = filters or TargetFilters()
    clauses: list[str] = []
    params: list[Any] = []
    if filters.source:
        clauses.append("t.source_id = ?")
        params.append(filters.source)
    if filters.project:
        clauses.append("t.project LIKE ?")
        params.append(f"%{filters.project}%")
    if filters.tool:
        if pack.target_kind != "tool_call":
            raise ValueError(f"--tool only applies to tool_call packs, not {pack.name!r}")
        clauses.append("t.tool_name = ?")
        params.append(filters.tool)
    if filters.since:
        clauses.append("t.created_at >= ?")
        params.append(filters.since)
    if filters.until:
        clauses.append("t.created_at < ?")
        params.append(filters.until)
    if not relabel:
        clauses.append(
            "t.target_id NOT IN (SELECT target_id FROM labels WHERE pack = ? AND pack_version = ?)"
        )
        params.extend([pack.name, pack.version])

    where = " AND ".join(clauses) if clauses else "TRUE"
    sql = f"SELECT * FROM ({pack.target_sql}) t WHERE {where} ORDER BY t.created_at, t.target_id"
    if limit is not None:
        sql += f" LIMIT {int(limit)}"

    result = conn.execute(sql, params)
    cols = [d[0] for d in result.description]
    return [
        Target(target_id=row["target_id"], state=pack.build_state(row))
        for row in (dict(zip(cols, r)) for r in result.fetchall())
    ]


def estimate_tokens(pack: Pack, targets: list[Target]) -> int:
    """Rough input-token estimate (~4 chars/token) for state plus questions."""
    question_chars = len(json.dumps(pack.questions))
    state_chars = sum(len(json.dumps(t.state, default=str)) for t in targets)
    return (state_chars + question_chars * len(targets)) // 4


def _label_rows(
    pack: Pack, target_id: str, result: BackendResult, run_id: str
) -> list[tuple]:
    rows = []
    for question, answer in result.answers.items():
        kind = answer.get("type")
        value = choice = confidence = probabilities = None
        if kind == "noul":
            value = answer.get("noul")
        elif kind == "choice":
            choice = answer.get("choice")
            confidence = answer.get("confidence")
            probabilities = json.dumps(answer.get("probabilities"))
        elif kind == "score":
            value = answer.get("score")
            confidence = answer.get("confidence")
            probabilities = json.dumps(answer.get("probabilities"))
        else:
            continue
        rows.append(
            (
                pack.name, pack.target_kind, target_id, question, kind, value, choice,
                confidence, probabilities, pack.version, result.model, run_id,
            )
        )
    return rows


def run_pack(
    conn: duckdb.DuckDBPyConnection,
    pack: Pack,
    backend: LabelBackend,
    targets: list[Target],
    *,
    concurrency: int = 8,
    on_progress: Callable[[int], None] | None = None,
) -> RunStats:
    """Ask ``pack``'s questions about every target and upsert the labels.

    Requests run concurrently; all database writes happen on the calling
    thread. Per-target failures are counted and skipped. A ``BackendError``
    (bad key, missing SDK) stops the run.
    """
    stats = RunStats(run_id=str(uuid.uuid4()))
    conn.execute(
        "INSERT INTO label_runs (id, pack, pack_version, backend) VALUES (?, ?, ?, ?)",
        [stats.run_id, pack.name, pack.version, backend.name],
    )
    try:
        with ThreadPoolExecutor(max_workers=max(1, concurrency)) as pool:
            futures = {
                pool.submit(backend.ask, t.state, pack.questions): t.target_id
                for t in targets
            }
            for future in as_completed(futures):
                target_id = futures[future]
                try:
                    result = future.result()
                except BackendError:
                    for f in futures:
                        f.cancel()
                    raise
                except Exception as exc:  # one bad target should not kill the run
                    stats.failures += 1
                    if len(stats.errors) < 5:
                        stats.errors.append(f"{target_id}: {exc}")
                else:
                    rows = _label_rows(pack, target_id, result, stats.run_id)
                    if rows:
                        conn.executemany(
                            """
                            INSERT OR REPLACE INTO labels (
                                pack, target_kind, target_id, question, answer_type, value,
                                choice, confidence, probabilities, pack_version, model, run_id
                            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                            """,
                            rows,
                        )
                    stats.labelled += 1
                    stats.input_tokens += result.input_tokens
                    stats.model = stats.model or result.model
                if on_progress:
                    on_progress(1)
    finally:
        conn.execute(
            """
            UPDATE label_runs
            SET finished_at = current_timestamp, model = ?, targets = ?, failures = ?,
                input_tokens = ?
            WHERE id = ?
            """,
            [stats.model, stats.labelled, stats.failures, stats.input_tokens, stats.run_id],
        )
    return stats


# Dimensions available to `summarize`, per target kind.
_DIMENSIONS: dict[str, dict[str, str]] = {
    "tool_call": {
        "source": "c.source_id",
        "tool": "cb.tool_name",
        "model": "m.model",
        "project": "c.project",
        "month": "strftime(date_trunc('month', m.created_at), '%Y-%m')",
        "quarter": "strftime(m.created_at, '%Y') || '-Q' || quarter(m.created_at)",
    },
    "message": {
        "source": "c.source_id",
        "project": "c.project",
        "month": "strftime(date_trunc('month', m.created_at), '%Y-%m')",
        "quarter": "strftime(m.created_at, '%Y') || '-Q' || quarter(m.created_at)",
    },
}

_TARGET_JOINS: dict[str, str] = {
    "tool_call": """
        JOIN content_blocks cb ON cb.id = l.target_id
        JOIN messages m ON m.id = cb.message_id
        JOIN conversations c ON c.id = m.conversation_id
    """,
    "message": """
        JOIN messages m ON m.id = l.target_id
        JOIN conversations c ON c.id = m.conversation_id
    """,
}


def summarize(
    conn: duckdb.DuckDBPyConnection,
    pack: Pack,
    *,
    by: str | None = None,
    min_confidence: float = 0.0,
) -> tuple[list[str], list[tuple]]:
    """Aggregate a pack's current labels, optionally grouped by a dimension.

    Choice questions report each option's share; noul questions report the
    share above 0.5 and the mean probability; score questions report the mean
    level. The confidence floor applies to choice and score answers only.
    """
    group = "'all'"
    if by:
        dims = _DIMENSIONS.get(pack.target_kind, {})
        if by not in dims:
            raise ValueError(f"--by must be one of: {', '.join(dims)}")
        group = dims[by]

    sql = f"""
        WITH l AS (
            SELECT l.*, {group} AS grp
            FROM labels l
            {_TARGET_JOINS[pack.target_kind]}
            WHERE l.pack = ? AND l.pack_version = ?
              AND (l.confidence IS NULL OR l.confidence >= ?)
        )
        SELECT grp AS "group", question, choice AS answer, COUNT(*) AS n,
               ROUND(100.0 * COUNT(*) / SUM(COUNT(*)) OVER (PARTITION BY grp, question), 1)
                   AS share_pct,
               NULL::DOUBLE AS mean, ROUND(AVG(confidence), 2) AS avg_conf
        FROM l WHERE answer_type = 'choice'
        GROUP BY grp, question, choice
        UNION ALL
        SELECT grp, question, 'yes (p > 0.5)', COUNT(*),
               ROUND(100.0 * AVG(CASE WHEN value > 0.5 THEN 1 ELSE 0 END), 1),
               ROUND(AVG(value), 2), NULL
        FROM l WHERE answer_type = 'noul'
        GROUP BY grp, question
        UNION ALL
        SELECT grp, question, 'score', COUNT(*), NULL, ROUND(AVG(value), 2),
               ROUND(AVG(confidence), 2)
        FROM l WHERE answer_type = 'score'
        GROUP BY grp, question
        ORDER BY 1, 2, 4 DESC
    """
    result = conn.execute(sql, [pack.name, pack.version, min_confidence])
    return [d[0] for d in result.description], result.fetchall()

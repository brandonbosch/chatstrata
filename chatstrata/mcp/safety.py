"""Read-only SQL enforcement and query safety limits."""

from __future__ import annotations

import re
import threading
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    import duckdb

_MUTATING_KEYWORDS = frozenset({
    "INSERT",
    "UPDATE",
    "DELETE",
    "DROP",
    "ALTER",
    "CREATE",
    "TRUNCATE",
    "COPY",
    "EXPORT",
    "ATTACH",
    "DETACH",
    "LOAD",
    "INSTALL",
    "SET",
    "GRANT",
    "REVOKE",
    "BEGIN",
    "COMMIT",
    "ROLLBACK",
    "VACUUM",
    "CHECKPOINT",
})

_COMMENT_RE = re.compile(r"--[^\n]*|/\*.*?\*/", re.DOTALL)


def validate_read_only(sql: str) -> None:
    """Raise ValueError if *sql* contains write operations."""
    stripped = _COMMENT_RE.sub(" ", sql).strip()
    if not stripped:
        raise ValueError("Empty query.")

    if ";" in stripped.rstrip(";"):
        raise ValueError("Multi-statement queries are not allowed.")

    first_token = stripped.split()[0].upper()
    if first_token in _MUTATING_KEYWORDS:
        raise ValueError(f"{first_token} queries are not allowed. Only SELECT/WITH/DESCRIBE/SHOW/PRAGMA are permitted.")


def execute_safe(
    conn: duckdb.DuckDBPyConnection,
    sql: str,
    *,
    timeout_seconds: float = 30.0,
    max_rows: int = 500,
    max_result_bytes: int = 512_000,
) -> tuple[list[str], list[tuple], bool]:
    """Execute a read-only SQL query with safety limits.

    Returns ``(column_names, rows, truncated)``.
    """
    validate_read_only(sql)

    timer = threading.Timer(timeout_seconds, conn.interrupt)
    timer.start()
    try:
        result = conn.execute(sql)
    except Exception as exc:
        if "interrupt" in str(exc).lower():
            raise TimeoutError(f"Query exceeded {timeout_seconds}s timeout.") from exc
        raise
    finally:
        timer.cancel()

    cols = [d[0] for d in result.description] if result.description else []
    rows = result.fetchmany(max_rows + 1)
    truncated = len(rows) > max_rows
    if truncated:
        rows = rows[:max_rows]

    import json
    size = len(json.dumps(rows, default=str))
    if size > max_result_bytes:
        ratio = max_result_bytes / size
        keep = max(1, int(len(rows) * ratio * 0.9))
        rows = rows[:keep]
        truncated = True

    return cols, rows, truncated

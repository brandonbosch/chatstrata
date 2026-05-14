"""DuckDB connection management and schema migrations."""

from __future__ import annotations

import os
from pathlib import Path

import duckdb
from platformdirs import user_data_dir

from chatstrata.core.migrations import MIGRATIONS, Migration


def get_default_db_path() -> Path:
    """Return the default chatstrata database path.

    Override with the CHATSTRATA_DB environment variable.
    """
    env = os.environ.get("CHATSTRATA_DB")
    if env:
        return Path(env).expanduser().resolve()
    data_dir = Path(user_data_dir("chatstrata", appauthor=False))
    data_dir.mkdir(parents=True, exist_ok=True)
    return data_dir / "chatstrata.duckdb"


def connect(
    db_path: Path | str | None = None,
    *,
    auto_migrate: bool = True,
) -> duckdb.DuckDBPyConnection:
    """Open a DuckDB connection, optionally applying pending migrations."""
    path = Path(db_path) if db_path else get_default_db_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    conn = duckdb.connect(str(path))
    if auto_migrate:
        apply_migrations(conn)
    _load_fts_extension(conn)
    return conn


def _load_fts_extension(conn: duckdb.DuckDBPyConnection) -> None:
    """Load the FTS extension if it has been installed (schema >= v2)."""
    try:
        conn.execute("LOAD fts")
    except (duckdb.IOException, duckdb.CatalogException):
        pass


def rebuild_fts_index(conn: duckdb.DuckDBPyConnection) -> None:
    """Drop and recreate the FTS index on content_blocks.text."""
    conn.execute(
        "PRAGMA create_fts_index('content_blocks', 'id', 'text', "
        "stemmer='porter', stopwords='english', overwrite=1)"
    )


def get_schema_version(conn: duckdb.DuckDBPyConnection) -> int:
    """Return the current schema version, or 0 if the DB is uninitialized."""
    try:
        row = conn.execute(
            "SELECT value FROM meta WHERE key = 'schema_version'"
        ).fetchone()
        return int(row[0]) if row else 0
    except duckdb.CatalogException:
        return 0


def apply_migrations(conn: duckdb.DuckDBPyConnection) -> list[Migration]:
    """Apply all pending migrations and return those that were applied."""
    current = get_schema_version(conn)
    applied: list[Migration] = []
    for migration in MIGRATIONS:
        if migration.version <= current:
            continue
        conn.execute(migration.sql)
        conn.execute(
            "INSERT OR REPLACE INTO meta (key, value) VALUES ('schema_version', ?)",
            [str(migration.version)],
        )
        applied.append(migration)
    return applied

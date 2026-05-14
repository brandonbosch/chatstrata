"""DuckDB connection management and schema initialization."""

from __future__ import annotations

import os
from pathlib import Path

import duckdb
from platformdirs import user_data_dir

SCHEMA_FILE = Path(__file__).parent / "schema.sql"


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


def connect(db_path: Path | str | None = None) -> duckdb.DuckDBPyConnection:
    """Open a DuckDB connection, initializing schema if needed."""
    path = Path(db_path) if db_path else get_default_db_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    conn = duckdb.connect(str(path))
    init_db(conn)
    return conn


def init_db(conn: duckdb.DuckDBPyConnection) -> None:
    """Apply the schema. Idempotent."""
    schema = SCHEMA_FILE.read_text()
    conn.execute(schema)

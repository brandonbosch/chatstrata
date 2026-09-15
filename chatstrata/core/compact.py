"""Database compaction by copying live rows into a fresh DuckDB file."""

from __future__ import annotations

import os
import uuid
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path

import duckdb

CORE_TABLES = (
    "meta",
    "sources",
    "conversations",
    "messages",
    "content_blocks",
    "attachments",
    "raw_events",
    "message_embeddings",
)


class CompactionError(RuntimeError):
    """Raised when a compacted database cannot be safely produced or verified."""


@dataclass(frozen=True)
class CompactionResult:
    database_path: Path
    backup_path: Path | None
    original_size: int
    compacted_size: int
    table_counts: dict[str, int]

    @property
    def reclaimed_bytes(self) -> int:
        return max(0, self.original_size - self.compacted_size)


def _sql_string(value: Path | str) -> str:
    return "'" + str(value).replace("'", "''") + "'"


def _sql_identifier(value: str) -> str:
    return '"' + value.replace('"', '""') + '"'


def _qualified_table(
    database: str,
    table: str,
    *,
    schema: str = "main",
) -> str:
    return (
        f"{_sql_identifier(database)}."
        f"{_sql_identifier(schema)}."
        f"{_sql_identifier(table)}"
    )


def _table_counts(
    conn: duckdb.DuckDBPyConnection,
    database: str,
) -> dict[str, int]:
    return {
        table: conn.execute(
            f"SELECT COUNT(*) FROM {_qualified_table(database, table)}"
        ).fetchone()[0]
        for table in CORE_TABLES
    }


def _has_fts_index(
    conn: duckdb.DuckDBPyConnection,
    database: str,
) -> bool:
    return (
        conn.execute(
            """
            SELECT COUNT(*)
            FROM duckdb_schemas()
            WHERE database_name = ? AND schema_name = 'fts_main_content_blocks'
            """,
            [database],
        ).fetchone()[0]
        > 0
    )


def _copy_database(
    conn: duckdb.DuckDBPyConnection,
    source_database: str,
    target_database: str,
    *,
    skip_schemas: set[str] | None = None,
) -> None:
    """Copy schema, then table data in foreign-key-safe dependency order."""
    skip_schemas = skip_schemas or set()
    conn.execute(
        f"COPY FROM DATABASE {_sql_identifier(source_database)} "
        f"TO {_sql_identifier(target_database)} (SCHEMA)"
    )
    tables = conn.execute(
        """
        SELECT schema_name, table_name
        FROM duckdb_tables()
        WHERE database_name = ? AND NOT internal
        """,
        [source_database],
    ).fetchall()
    tables = [table for table in tables if table[0] not in skip_schemas]
    core_rank = {table: index for index, table in enumerate(CORE_TABLES)}
    tables.sort(
        key=lambda item: (
            0 if item[0] == "main" and item[1] in core_rank else 1,
            core_rank.get(item[1], len(core_rank)),
            item[0],
            item[1],
        )
    )
    pending = list(tables)
    last_error: Exception | None = None
    while pending:
        deferred: list[tuple[str, str]] = []
        for schema, table in pending:
            source = _qualified_table(source_database, table, schema=schema)
            target = _qualified_table(target_database, table, schema=schema)
            try:
                conn.execute(f"INSERT INTO {target} SELECT * FROM {source}")
            except duckdb.ConstraintException as exc:
                deferred.append((schema, table))
                last_error = exc
        if len(deferred) == len(pending):
            raise CompactionError(
                "Could not copy tables in a foreign-key-safe order."
            ) from last_error
        pending = deferred


def _rebuild_attached_fts(
    conn: duckdb.DuckDBPyConnection,
    database: str,
) -> None:
    try:
        conn.execute("LOAD fts")
        table = f"{database}.main.content_blocks".replace("'", "''")
        conn.execute(
            f"PRAGMA create_fts_index('{table}', 'id', 'text', "
            "stemmer='porter', stopwords='english', overwrite=1)"
        )
        indexed_docs = conn.execute(
            f"""
            SELECT COUNT(*)
            FROM {_qualified_table(database, "docs", schema="fts_main_content_blocks")}
            """
        ).fetchone()[0]
        expected_docs = conn.execute(
            f"""
            SELECT COUNT(*)
            FROM {_qualified_table(database, "content_blocks")}
            """
        ).fetchone()[0]
        if indexed_docs != expected_docs:
            raise CompactionError(
                "The compacted full-text index does not cover every text block."
            )
    except Exception as exc:
        raise CompactionError(
            "The compacted database was copied, but its full-text index "
            "could not be rebuilt."
        ) from exc


def _backup_path(database_path: Path) -> Path:
    timestamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    candidate = database_path.with_name(f"{database_path.name}.backup-{timestamp}")
    suffix = 1
    while candidate.exists():
        candidate = database_path.with_name(
            f"{database_path.name}.backup-{timestamp}-{suffix}"
        )
        suffix += 1
    return candidate


def compact_database(
    database_path: Path | str,
    *,
    keep_backup: bool = True,
) -> CompactionResult:
    """Rewrite a DuckDB database and atomically replace the original.

    The source remains exclusively open during copy and verification. The
    original file is first moved to a backup path so it can be restored if the
    final swap fails.
    """
    path = Path(database_path).expanduser().resolve()
    if not path.is_file():
        raise CompactionError(f"Database does not exist: {path}")

    original_stat = path.stat()
    original_size = original_stat.st_size
    original_mode = original_stat.st_mode & 0o777
    temp_path = path.with_name(f".{path.name}.compact-{uuid.uuid4().hex}.tmp")
    temp_wal_path = Path(f"{temp_path}.wal")
    backup_path = _backup_path(path)
    source_database = ""
    source_counts: dict[str, int] = {}

    try:
        conn = duckdb.connect(str(path))
        try:
            conn.execute("CHECKPOINT")
            source_database = conn.execute("SELECT current_database()").fetchone()[0]
            source_counts = _table_counts(conn, source_database)
            source_version = conn.execute(
                f"""
                SELECT value
                FROM {_qualified_table(source_database, "meta")}
                WHERE key = 'schema_version'
                """
            ).fetchone()
            source_has_fts = _has_fts_index(conn, source_database)

            conn.execute(f"ATTACH {_sql_string(temp_path)} AS compacted_db")
            _copy_database(
                conn,
                source_database,
                "compacted_db",
                skip_schemas={"fts_main_content_blocks"} if source_has_fts else None,
            )

            if source_has_fts:
                _rebuild_attached_fts(conn, "compacted_db")

            compacted_counts = _table_counts(conn, "compacted_db")
            compacted_version = conn.execute(
                """
                SELECT value
                FROM compacted_db.main.meta
                WHERE key = 'schema_version'
                """
            ).fetchone()
            if compacted_counts != source_counts:
                raise CompactionError(
                    "Compacted database row counts do not match the source."
                )
            if compacted_version != source_version:
                raise CompactionError(
                    "Compacted database schema version does not match the source."
                )

            conn.execute("CHECKPOINT compacted_db")
            conn.execute("DETACH compacted_db")
        finally:
            conn.close()

        if not temp_path.is_file():
            raise CompactionError("DuckDB did not create the compacted database file.")
        temp_path.chmod(original_mode)

        os.replace(path, backup_path)
        try:
            os.replace(temp_path, path)
        except Exception:
            os.replace(backup_path, path)
            raise

        compacted_size = path.stat().st_size
        retained_backup: Path | None = backup_path
        if not keep_backup:
            try:
                backup_path.unlink()
            except OSError:
                pass
            else:
                retained_backup = None

        return CompactionResult(
            database_path=path,
            backup_path=retained_backup,
            original_size=original_size,
            compacted_size=compacted_size,
            table_counts=source_counts,
        )
    except CompactionError:
        raise
    except Exception as exc:
        raise CompactionError(
            "Compaction failed. Ensure all other chatstrata processes are stopped "
            "and that enough free disk space is available."
        ) from exc
    finally:
        if temp_path.exists():
            temp_path.unlink()
        if temp_wal_path.exists():
            temp_wal_path.unlink()

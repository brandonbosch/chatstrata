---
title: Schema Migrations
description: How chatstrata's database schema evolves safely.
---

# Schema Migrations

A personal archive is only useful if it survives upgrades. When chatstrata adds a feature that requires new columns, tables, or indexes, a **migration** carries your existing database forward without data loss. The migration framework is deliberately simple: a numbered list of SQL scripts applied in order, tracked by a single version number. No ORM, no rollback, no downtime.

## How it works

The framework lives in `chatstrata/core/migrations/__init__.py` and `chatstrata/core/db.py`. Three pieces make it work:

### The Migration dataclass

Each migration is a frozen dataclass holding a version number, a human-readable description, and raw SQL:

```python title="chatstrata/core/migrations/__init__.py"
@dataclass(frozen=True)
class Migration:
    version: int
    description: str
    sql: str
```

### The MIGRATIONS list

All known migrations are registered in order. The SQL is loaded from `.sql` files at import time:

```python title="chatstrata/core/migrations/__init__.py"
MIGRATIONS: list[Migration] = [
    Migration(version=1, description="Initial schema", sql=_load("0001_initial.sql")),
    Migration(version=2, description="Full-text search index", sql=_load("0002_fts_index.sql")),
    Migration(version=3, description="Source file mtime for incremental ingest", sql=_load("0003_conversation_mtime.sql")),
]

LATEST_VERSION = MIGRATIONS[-1].version
```

`LATEST_VERSION` is always the version of the last entry in the list.

### The meta table and version tracking

The `meta` table (created by the very first migration) stores the current schema version as a key-value pair. `get_schema_version()` reads it, returning `0` for an uninitialized database:

```python title="chatstrata/core/db.py"
def get_schema_version(conn: duckdb.DuckDBPyConnection) -> int:
    try:
        row = conn.execute(
            "SELECT value FROM meta WHERE key = 'schema_version'"
        ).fetchone()
        return int(row[0]) if row else 0
    except duckdb.CatalogException:
        return 0
```

### Sequential application

`apply_migrations()` compares the current version to each migration's version and runs only those that haven't been applied yet. After each migration, it updates `meta` immediately:

```python title="chatstrata/core/db.py"
def apply_migrations(conn: duckdb.DuckDBPyConnection) -> list[Migration]:
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
```

The function returns the list of migrations it applied, which is empty when the database is already current.

## Existing migrations

The three migrations tell the story of chatstrata's evolution:

### 0001 -- Initial schema

Creates all core tables: `meta`, `sources`, `conversations`, `messages`, `content_blocks`, `attachments`, `raw_events`, `message_embeddings`, and the `tool_calls` convenience view. This is the foundation -- every table, index, and foreign key that makes the normalized data model work. See [Schema](schema.md) for detailed column documentation.

### 0002 -- Full-text search index

Installs and loads the DuckDB `fts` extension, then creates a Porter-stemmed, English-stopword full-text index on `content_blocks.text`:

```sql title="chatstrata/core/migrations/0002_fts_index.sql"
INSTALL fts;
LOAD fts;

PRAGMA create_fts_index(
    'content_blocks', 'id', 'text',
    stemmer = 'porter',
    stopwords = 'english',
    overwrite = 1
);
```

!!! note
    The FTS index is **not** automatically updated when new data is ingested. Run `chatstrata reindex` after ingesting to rebuild it.

### 0003 -- Conversation mtime for incremental ingest

A single `ALTER TABLE` that adds `source_file_mtime` to the `conversations` table:

```sql title="chatstrata/core/migrations/0003_conversation_mtime.sql"
ALTER TABLE conversations ADD COLUMN source_file_mtime DOUBLE;
```

This column powers the `--incremental` flag on `chatstrata ingest`, allowing the CLI to skip conversations whose source file has not changed since the last ingest.

## Auto-migration

By default, `connect()` applies all pending migrations the moment a database is opened:

```python title="chatstrata/core/db.py"
def connect(
    db_path: Path | str | None = None,
    *,
    auto_migrate: bool = True,
) -> duckdb.DuckDBPyConnection:
    path = Path(db_path) if db_path else get_default_db_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    conn = duckdb.connect(str(path))
    if auto_migrate:
        apply_migrations(conn)
    ...
```

This means users never need to think about migrations. Opening a database created with an older version of chatstrata will transparently upgrade it.

The `auto_migrate=False` option exists for the `migrate` command itself (to show status before applying) and for tests that need a raw, uninitialized connection.

## The migrate command

For explicit control, the CLI exposes `chatstrata migrate`:

```
chatstrata migrate            # apply all pending migrations
chatstrata migrate --status   # show current vs latest version without changing anything
```

When `--status` is used, the command reports the current version, the latest available version, and how many migrations are pending. When migrations are applied, each one is logged:

```
Applied migration 0002: Full-text search index
Applied migration 0003: Source file mtime for incremental ingest

Schema upgraded to version 3.
```

## Contributing a migration

Schema changes require three things, documented in [Schema](schema.md):

1. **An ADR** in `docs/adr/` explaining why the change is needed and what alternatives were considered.
2. **A migration SQL script** in `chatstrata/core/migrations/` following the naming convention `NNNN_short_description.sql` (e.g. `0004_add_token_counts.sql`).
3. **A version bump** -- append a new `Migration` entry to the `MIGRATIONS` list in `__init__.py`.

If the change affects what adapters produce, update the `schema_version` attribute on affected adapters. Every source adapter declares which schema version it targets via the `SourceAdapter` protocol:

```python title="chatstrata/sources/base.py"
class SourceAdapter(Protocol):
    schema_version: int
    """Which chatstrata schema version this adapter targets."""
```

Tests in `tests/test_migrations.py` verify that migrations apply cleanly from a fresh database, are idempotent on re-run, and that `LATEST_VERSION` stays in sync. Add a test for any new column or table your migration introduces.

## Key entry points

| Symbol | Location | Purpose |
|---|---|---|
| `Migration` | `chatstrata/core/migrations/__init__.py` | Dataclass holding version, description, SQL |
| `MIGRATIONS` | `chatstrata/core/migrations/__init__.py` | Ordered list of all migrations |
| `LATEST_VERSION` | `chatstrata/core/migrations/__init__.py` | Version number of the newest migration |
| `get_schema_version()` | `chatstrata/core/db.py` | Read current version from the `meta` table |
| `apply_migrations()` | `chatstrata/core/db.py` | Run pending migrations sequentially |
| `connect()` | `chatstrata/core/db.py` | Open a connection, auto-migrating by default |

## Related

- [Schema](schema.md) -- table and column reference, design principles, versioning rules.
- [Ingestion](ingestion.md) -- how adapters use `connect()` and how `--incremental` relies on migration 0003.
- [Adapters](adapters.md) -- the `SourceAdapter` protocol and its `schema_version` field.

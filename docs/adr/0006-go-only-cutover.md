# ADR 0006: Retire the Python application

**Status:** Accepted

**Date:** 2026-10-30

## Decision

Ship chatstrata 2 as a native Go binary. The owner confirmed that Jev/classifier
labeling, automatic PII sanitization, embeddings and semantic search are not
requirements for the initial Go release. Preserve the Python source at the
`python-final` tag and leave existing PyPI releases available.

Move the existing migrations and analysis SQL unchanged under `internal/`.
Keep migration numbers, archive filenames, the `CHATSTRATA_GO_DB` override,
SQL table/column names, observation-log format, and deterministic IDs intact.
No data migration is needed for these file moves. Keep `import-legacy` and
its synthetic Python-era archive tests; import requires SQL alone.

The Go golden updater now maintains every expected fixture. Retire the
Python application/test/publishing jobs and benchmark harness. Current
benchmarks run with Go; old baseline results remain historical evidence.
Zensical remains optional documentation tooling and is not an application,
installation, test, or runtime dependency.

Use `github.com/brandonbosch/chatstrata/v2` as the module path so Go can
resolve v2 release tags. Publish native Linux amd64/arm64 and macOS arm64
assets with checksums and the signed FTS extension embedded.

## Consequences

Users can archive, search, query, analyze and expose chats over MCP with no
Python installation. The release and first-run smoke checks must exercise
the same `ingest --auto` and MCP setup commands the documentation advertises.
Future labeling or sanitization work belongs in Go and must preserve the
observation log as the source of truth.

# Schema and legacy migration

All schema migrations live in `internal/store/migrations/`, are embedded in
the Go binary, and retain their original numbers. Opening an archive for
writing applies new migrations. Read-only MCP requests never migrate files.
SQL table/column names remain compatible with the Python-era archive.

```sh
chatstrata import-legacy /path/to/old/chatstrata.duckdb
```

Legacy import reads the old database's raw events and records observations,
then builds the Go projection. It does not need Python or alter the old file.
Conversation/message IDs become deterministic Go IDs; labels and embeddings
are not migrated. Missing raw events cannot be recovered. OpenCode titles
that existed only in a legacy session row may be unavailable.

Keep the old database as a backup. The Go archive remains named
`chatstrata-go.duckdb` and its source log `chatstrata-go.log/`, preserving
existing installations. Back up the log and use `chatstrata rebuild` to
recreate the projection. Do not rename the archive during an upgrade without
also moving its corresponding log and updating daemon/MCP configuration.

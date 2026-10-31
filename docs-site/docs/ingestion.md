# Collection and recovery

```sh
chatstrata ingest --auto
chatstrata ingest claude_code --incremental
chatstrata ingest codex_cli --path /path/to/sessions
chatstrata ingest claude_export --path /path/to/conversations.json
```

The collector stores source bytes in the append-only observation log before
updating DuckDB. Append-only transcripts record new bytes; databases and
multi-conversation exports provide snapshots. Re-ingesting unchanged content
is harmless. Incomplete JSONL tails wait for the next collection.

```sh
chatstrata schedule install
chatstrata schedule status
chatstrata schedule uninstall
```

The daemon collects every five minutes by default. Use `schedule install
--interval 10m` to change it. Linux uses a systemd user service; macOS uses
launchd. Linux user services normally run while logged in; lingering is a
separate system setting. The daemon closes DuckDB between collection phases
and before waiting for an optional relay.

`chatstrata paths` shows the projection and adjacent observation log.
Back up the log directory. `chatstrata rebuild` recreates DuckDB from it,
including conversations no longer present in their original source files.
Do not edit the projection directly: a rebuild would discard those edits.

Normal collection makes no network requests. Optional device sync requires
explicit relay configuration; see the repository's
[sync guide](https://github.com/brandonbosch/chatstrata/blob/main/docs/rewrite/sync.md).

# Writing a source adapter

This guide walks through adding support for a new source by example. We'll
treat `chatstrata/sources/claude_code/` as the reference and explain the
contract.

## The contract

A source adapter is a class that satisfies the `SourceAdapter` protocol from
`chatstrata.sources.base`:

```python
class SourceAdapter(Protocol):
    name: str                  # stable identifier
    display_name: str          # human-readable
    version: str               # adapter version
    schema_version: int        # which chatstrata schema version this targets

    def discover(self, config: dict | None) -> Iterable[ConversationHandle]: ...
    def parse(self, handle: ConversationHandle) -> ParsedConversation: ...
```

`discover()` enumerates available conversations cheaply, returning lightweight
handles. `parse()` does the heavy work of loading and normalizing a single
conversation. The split lets users see what's available before paying the cost
of full ingestion.

## Step by step

### 1. Create the package

```
chatstrata/sources/your_source/
├── __init__.py          # exports the adapter class
├── adapter.py           # the implementation
├── manifest.yaml        # metadata
└── tests/
    ├── __init__.py
    ├── fixtures/
    │   └── sample.<ext> # small sanitized sample of source data
    └── test_adapter.py
```

### 2. Implement `discover()`

For local-file sources, walk the relevant directory and yield one handle per
conversation file. For exports, walk the unzipped archive.

```python
def discover(self, config: dict | None = None) -> Iterable[ConversationHandle]:
    root = Path((config or {}).get("path") or DEFAULT_ROOT).expanduser()
    for f in sorted(root.glob("**/*.json")):
        yield ConversationHandle(
            source_native_id=f.stem,
            path=f,
            metadata={...},  # anything useful for parse()
        )
```

Keep this fast — don't open or parse files here, just list them.

### 3. Implement `parse()`

Read the file, walk its events, and produce a `ParsedConversation`:

```python
def parse(self, handle: ConversationHandle) -> ParsedConversation:
    raw_events = [...]  # load source data
    messages = [
        ParsedMessage(
            source_native_id=...,
            role=Role.USER,  # or ASSISTANT / SYSTEM / TOOL
            model=...,
            created_at=...,  # parse timestamp, ideally UTC
            blocks=[ContentBlock(type=BlockType.TEXT, text=...)],
        )
        for ... in ...
    ]
    return ParsedConversation(
        source_native_id=handle.source_native_id,
        title=...,
        project=...,
        started_at=...,
        ended_at=...,
        messages=messages,
        raw_path=str(handle.path),
        raw_events=raw_events,  # IMPORTANT: include these
    )
```

**Always populate `raw_events`** with the original source records. This is what
lets users re-parse later if normalization improves, without re-ingesting from
disk.

### 4. Map content to `BlockType`

The canonical block types are:

| type           | meaning                                     |
|----------------|---------------------------------------------|
| `text`         | plain text content                          |
| `thinking`     | model's chain-of-thought / reasoning trace  |
| `tool_use`     | a tool the assistant invoked                |
| `tool_result`  | output from a tool                          |
| `image`        | image content                               |
| `attachment`   | file attachment                             |

For unknown source-specific block types, preserve them in `payload` rather than
discarding them.

### 5. Write fixtures and tests

Create a tiny sanitized sample of your source's data in `tests/fixtures/`. Strip
all PII from it — these fixtures live in the repo.

Then write tests that assert your parser produces the expected normalized
events. Use the Claude Code tests as a template.

### 6. Register the entry point

In `pyproject.toml`:

```toml
[project.entry-points."chatstrata.sources"]
your_source = "chatstrata.sources.your_source.adapter:YourSourceAdapter"
```

If you're publishing a standalone package (not a PR to this repo), this same
mechanism works — chatstrata will auto-discover your adapter at runtime.

### 7. Document the source

Add a brief section to your adapter's `__init__.py` or a `README.md` next to
it describing:
- Where the source stores its data
- How users export from it (if applicable)
- Any quirks of the format that affected parsing

## Idempotency

The ingester handles idempotency — re-running `chatstrata ingest <source>` will
update existing conversations rather than duplicate them. Your adapter just
needs to produce stable `source_native_id` values across runs.

## Timestamps

Always return timezone-aware datetimes. The ingester normalizes to UTC, but it
needs the offset to do that correctly. If your source omits timezones, document
that assumption.

## Common gotchas

- **Tree-shaped histories** (ChatGPT): set `parent_source_native_id` on each
  message. The default is to store the whole tree; users can query for "leaf
  paths" with SQL.
- **Streaming/partial responses**: collapse to the final state on parse. Don't
  emit one message per chunk.
- **Empty messages**: skip them. They add noise to the database.
- **Resumed sessions**: if your source duplicates earlier messages on resume,
  deduplicate by `source_native_id` within the conversation.

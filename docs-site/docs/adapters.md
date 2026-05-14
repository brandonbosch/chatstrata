---
title: Writing a Source Adapter
description: How to add support for a new AI conversation source by implementing the SourceAdapter protocol.
---

# Writing a Source Adapter

Every AI tool stores conversations differently. Claude Code writes JSONL files to `~/.claude/projects/`. ChatGPT lets you export a ZIP archive. Cursor, Copilot, and countless others each have their own format. Source adapters are how chatstrata bridges that gap -- each one teaches chatstrata how to find and normalize conversations from a specific provider into the common schema.

Growing the list of supported sources is the project's primary growth vector, and the adapter system was designed from the start to make contributing one straightforward. If your preferred AI tool is not yet supported, you can write an adapter in an afternoon and either submit a PR or publish it as a standalone pip package.

## The SourceAdapter protocol

Every adapter implements the `SourceAdapter` protocol defined in `chatstrata/sources/base.py`:

```python title="chatstrata/sources/base.py"
@runtime_checkable
class SourceAdapter(Protocol):
    name: str            # stable identifier, e.g. "claude_code"
    display_name: str    # human-readable, e.g. "Claude Code"
    version: str         # adapter version; bump when parse semantics change
    schema_version: int  # which chatstrata schema version this targets

    def discover(self, config: dict | None = None) -> Iterable[ConversationHandle]: ...
    def parse(self, handle: ConversationHandle) -> ParsedConversation: ...
```

The protocol is decorated with `@runtime_checkable`, so `load_adapters()` validates every registered adapter at startup and raises a `TypeError` if the contract is not satisfied.

### The discover/parse split

The two methods serve deliberately different purposes:

- **`discover()`** enumerates conversations cheaply. Walk directories, list files, read an index -- but do not open or parse conversation content. Yield a `ConversationHandle` for each conversation found. The optional `config` dict passes source-specific knobs like custom paths.

- **`parse()`** does the heavy lifting. It receives one `ConversationHandle`, reads the underlying data, and returns a fully normalized `ParsedConversation`. The ingestion pipeline calls `parse()` only for conversations it decides to ingest.

This split means users can list what is available (`chatstrata sources`) without paying the cost of parsing every file.

### Canonical record types

All of these are Pydantic models defined in `chatstrata/core/models.py`:

| Type | Purpose |
|------|---------|
| `ConversationHandle` | Lightweight reference yielded by `discover()`. Carries `source_native_id`, an optional `path`, and a `metadata` dict. |
| `ParsedConversation` | The full normalized conversation returned by `parse()`. Includes `messages`, `title`, `project`, timestamps, and `raw_events`. |
| `ParsedMessage` | A single turn. Has a `role` (user/assistant/system/tool), optional `model`, `created_at` timestamp, and a list of `blocks`. |
| `ContentBlock` | One content unit within a message. Typed by `BlockType`. |

### BlockType mapping

Map your source's content into these canonical block types:

| BlockType | When to use |
|-----------|-------------|
| `text` | Plain text content |
| `thinking` | Model chain-of-thought or reasoning traces |
| `tool_use` | A tool invocation by the assistant. Set `tool_name` and `tool_use_id`. |
| `tool_result` | Output returned from a tool. Set `tool_use_id` to link it back. |
| `image` | Image content. Store source details in `payload`. |
| `attachment` | File attachments |

For source-specific block types that do not fit any of these, preserve the raw data in `ContentBlock.payload` rather than discarding it.

## Step-by-step walkthrough

The Claude Code adapter (`chatstrata/sources/claude_code/adapter.py`) is the reference implementation. Here is how it is structured, step by step.

### 1. Create the package layout

```
chatstrata/sources/your_source/
    __init__.py
    adapter.py
    tests/
        __init__.py
        fixtures/
            sample.<ext>       # small, sanitized sample of source data
        test_adapter.py
```

### 2. Implement discover()

The Claude Code adapter walks `~/.claude/projects/` for JSONL session files:

```python title="chatstrata/sources/claude_code/adapter.py"
def discover(self, config: dict | None = None) -> Iterable[ConversationHandle]:
    root = Path((config or {}).get("path") or DEFAULT_CLAUDE_DIR).expanduser()
    if not root.exists():
        return
    for jsonl in sorted(root.glob("*/*.jsonl")):
        session_id = jsonl.stem
        project = _decode_project_dir(jsonl.parent.name)
        yield ConversationHandle(
            source_native_id=session_id,
            path=jsonl,
            metadata={"project": project},
        )
```

Keep this fast. No file I/O beyond listing. Stash anything `parse()` will need later in `metadata`.

### 3. Implement parse()

Read the file, walk events, and produce a `ParsedConversation`. The Claude Code adapter reads each JSONL line, maps Anthropic API content blocks into `ContentBlock` instances, and tracks timestamps for `started_at`/`ended_at`:

```python title="chatstrata/sources/claude_code/adapter.py"
def parse(self, handle: ConversationHandle) -> ParsedConversation:
    # Load raw events
    events = []
    with handle.path.open("r", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if line:
                try:
                    events.append(json.loads(line))
                except json.JSONDecodeError:
                    continue

    messages = []
    for ev in events:
        role = _role_from_event(ev)
        if role is None:
            continue
        message = ev.get("message") or {}
        blocks = _content_blocks_from_message(message)
        if not blocks:
            continue
        messages.append(
            ParsedMessage(
                source_native_id=ev.get("uuid"),
                parent_source_native_id=ev.get("parentUuid"),
                role=role,
                model=message.get("model"),
                created_at=_parse_timestamp(ev.get("timestamp")),
                blocks=blocks,
            )
        )

    return ParsedConversation(
        source_native_id=handle.source_native_id,
        title=title,
        messages=messages,
        raw_path=str(handle.path),
        raw_events=events,  # preserve every raw line
    )
```

!!! tip "Always populate raw_events"
    The `raw_events` field stores the original source records verbatim. This is what lets chatstrata re-parse conversations later if normalization improves -- without going back to disk. For sources like Claude Code where session files are deleted after 30 days, the raw events may be the **only** surviving copy of your data. Do not skip this field.

### 4. Produce stable native IDs

The ingestion pipeline uses `source_native_id` for idempotency. Re-running `chatstrata ingest` updates existing conversations rather than duplicating them, so your IDs must be stable across runs. Session UUIDs, file stems, and export-provided IDs all work well.

## Testing

Every built-in adapter follows a fixture-driven test pattern. Create a small, sanitized sample of your source's data at `tests/fixtures/` -- strip all PII, since these live in the repo.

The Claude Code test suite demonstrates the pattern:

```python title="chatstrata/sources/claude_code/tests/test_adapter.py"
FIXTURES = Path(__file__).parent / "fixtures"

@pytest.fixture
def adapter() -> ClaudeCodeAdapter:
    return ClaudeCodeAdapter()

@pytest.fixture
def sample_handle() -> ConversationHandle:
    return ConversationHandle(
        source_native_id="sample_session",
        path=FIXTURES / "sample_session.jsonl",
        metadata={"project": "/Users/example/myproj"},
    )

def test_parse_returns_a_conversation(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    assert conv.source_native_id == "sample_session"
    assert conv.title == "Refactor the user auth module"

def test_parse_preserves_raw_events(adapter, sample_handle):
    conv = adapter.parse(sample_handle)
    assert len(conv.raw_events) == 6
```

Tests to include for any adapter:

- Correct `source_native_id` and `title` extraction
- Expected message count (skip empty/synthetic events)
- Correct role sequence
- Each `BlockType` your source produces (text, tool_use, tool_result, thinking, etc.)
- Timestamp extraction and ordering
- **`raw_events` preservation** -- verify the count matches the source data
- `discover()` against a temporary directory with the expected layout

## Registration

Adapters are registered via Python entry points in the `chatstrata.sources` group. This is a deliberate design choice (see [ADR-0003](https://github.com/brandonbosch/chatstrata/blob/main/docs/adr/0003-adapter-pattern-entry-points.md)) that enables two registration paths.

### In-repo adapters

Add an entry to `pyproject.toml` alongside the existing adapters:

```toml title="pyproject.toml"
[project.entry-points."chatstrata.sources"]
claude_code = "chatstrata.sources.claude_code.adapter:ClaudeCodeAdapter"
claude_export = "chatstrata.sources.claude_export.adapter:ClaudeExportAdapter"
codex_cli = "chatstrata.sources.codex_cli.adapter:CodexCliAdapter"
opencode = "chatstrata.sources.opencode.adapter:OpenCodeAdapter"
your_source = "chatstrata.sources.your_source.adapter:YourSourceAdapter"
```

### Standalone packages

Third parties can publish their own pip package (e.g. `chatstrata-cursor`) with the same entry-point group in their own `pyproject.toml`. Once installed, `load_adapters()` in `chatstrata/sources/base.py` picks it up automatically via `importlib.metadata.entry_points(group="chatstrata.sources")` -- no changes to chatstrata itself required.

This is the mechanism that makes the adapter ecosystem scalable: anyone can publish a new source adapter without coordinating a PR to this repository.

## Key files

| File | Purpose |
|------|---------|
| `chatstrata/sources/base.py` | `SourceAdapter` protocol and `load_adapters()` discovery |
| `chatstrata/core/models.py` | `ConversationHandle`, `ParsedConversation`, `ParsedMessage`, `ContentBlock`, `BlockType`, `Role` |
| `chatstrata/sources/claude_code/adapter.py` | Reference implementation (Claude Code) |
| `chatstrata/sources/claude_code/tests/test_adapter.py` | Reference test suite |
| `pyproject.toml` | Entry-point registration for all in-repo adapters |
| `docs/adr/0003-adapter-pattern-entry-points.md` | Design rationale for the entry-point approach |

## Related

- [Schema](schema.md) -- the DuckDB schema that stores ingested conversations
- [Ingestion pipeline](ingestion.md) -- how parsed conversations flow into the database
- [Built-in sources](sources.md) -- details on the adapters that ship with chatstrata
- [CLI reference](cli.md) -- `chatstrata ingest` and `chatstrata sources` commands

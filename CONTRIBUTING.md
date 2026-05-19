# Contributing to chatstrata

Thanks for your interest. The most valuable contributions right now are **new
source adapters** — anything that adds a new provider or tool to the supported
list.

## Adding a source adapter

The full walkthrough lives in [docs/adapter-guide.md](docs/adapter-guide.md).
Quick version:

1. Copy `chatstrata/sources/claude_code/` to `chatstrata/sources/your_source/`.
2. Implement `discover()` and `parse()` against the canonical record types in
   `chatstrata/core/models.py`.
3. Add a fixture file (small, sanitized sample of your source's data) under
   `tests/fixtures/`, and a test that asserts your parser produces the expected
   normalized events.
4. Register your adapter in `pyproject.toml` under
   `[project.entry-points."chatstrata.sources"]`.
5. Open a PR.

PRs are reviewed for:
- **Schema fidelity** — does the adapter produce well-formed canonical records?
- **Test coverage** — fixtures and tests demonstrate the parsing works.
- **Idempotency** — re-running ingest on the same source doesn't duplicate.

We do not review parsing logic line-by-line; your fixture tests are the proof
that it works.

## Development setup

```bash
git clone https://github.com/brandonbosch/chatstrata.git
cd chatstrata
uv venv
uv pip install -e ".[dev,redact]"
pytest
ruff check .
```

## Architecture decisions

We keep [Architecture Decision Records](docs/adr/) for significant design
choices. If you're proposing a change that affects the schema, the adapter
contract, or core data flow, please include an ADR in your PR explaining the
why.

## Scope

In scope:
- Source adapters
- Schema improvements and migrations
- Analysis primitives and example queries
- Redaction recognizers
- CLI improvements
- Documentation

Out of scope (for now):
- GUIs / web UIs
- Hosted services
- Real-time / streaming ingestion
- Multi-user features

This may change as the project evolves. If you want to build something
out-of-scope, that's great — please do, as a separate project that depends on
chatstrata as a library.

## Code of conduct

Be kind. Assume good faith. Critique ideas, not people.

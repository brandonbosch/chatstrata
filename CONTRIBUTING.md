# Contributing to chatstrata

chatstrata is a Go application. The Python application is retired and its
final source is preserved at `python-final`. New work belongs in Go.

## Development setup

Install Go at the version specified in `go.mod` and a C compiler. DuckDB uses CGo.

```sh
git clone https://github.com/brandonbosch/chatstrata.git
cd chatstrata
go build -o bin/chatstrata ./cmd/chatstrata
go test ./...
go vet ./...
gofmt -l .
```

Source builds use substring search unless you install or bundle the full-text
extension. See the [release procedure](docs/rewrite/release.md) for a build
with the signed extension embedded and its smoke checks.

## Add a source adapter

Follow the [adapter guide](docs/adapter-guide.md).

1. Implement `model.Source` under `internal/sources/<source>/`.
2. Register the adapter in `internal/cli/cli.go`.
3. Add synthetic or sanitized input under `spec/golden/inputs/` and a case in
   `spec/golden/cases.json`. Never commit private transcripts.
4. Generate the expected output with
   `go test ./internal/golden -run TestGolden/YOUR_CASE -update` and review it.
5. Run `go test ./...`. Include legacy-import coverage when the source existed
   in Python.

Reviewers check normalized records, message ordering and parent links,
idempotent collection, and recovery from the observation log. Parsers must
use the bytes passed to `Parse`, since the original path may not exist during
rebuild or on another device.

## Architecture decisions

Schema, adapter-contract, or storage changes need an
[architecture decision record](docs/adr/) explaining the change. Keep existing
migration numbers and SQL names compatible. The observation log is the source
of truth; DuckDB is a rebuildable projection.

## Documentation

Published guides live under `docs-site/docs/`; repository references live under
`docs/`. Keep both copies of the schema and adapter guide consistent. Build the
landing page and guides together:

```sh
sh scripts/build-site.sh
```

Zensical uses Python through `uv` only for documentation tooling. Application
builds, tests, and releases do not require Python. See the
[site guide](docs-site/README.md) for preview and deployment instructions.

## Scope

Contributions can add source adapters, improve collection and recovery, add
analysis queries, or improve the CLI, MCP, optional device sync, and docs.
Labeling, automatic redaction, embeddings, and semantic search are outside
the current v2 release. Discuss substantial additions before implementing them.

## Code of conduct

Be kind. Assume good faith. Critique ideas, not people.

# Releasing the Go app

Go releases are tags starting with `v2`: release candidates (`v2.0.0-rc.1`,
`-rc.2`, ...) during the M5 parallel-use period, then `v2.0.0` at M6. The
Python app's releases stay `v0.x` and go to PyPI through `publish.yml`;
`release.yml` handles only `v2` tags and never touches PyPI.

## Cutting a release

```sh
git tag v2.0.0-rc.1 && git push origin v2.0.0-rc.1
```

`.github/workflows/release.yml` then builds on one native runner per target
(DuckDB needs CGo, so nothing is cross-compiled):

| target | runner |
|---|---|
| linux/amd64 | `ubuntu-22.04` (glibc 2.35 floor) |
| linux/arm64 | `ubuntu-22.04-arm` |
| darwin/arm64 | `macos-latest` |

Each job fetches DuckDB's fts extension for its platform, runs
`go test -tags fts_embed ./...`, builds with the version stamped in, and runs
`scripts/smoke-release.sh` against the binary in an empty home directory
(ingest, `reindex`, `search` with the full-text index, and nothing written to
`~/.duckdb`). The release job uploads `chatstrata_<version>_<os>_<arch>.tar.gz`
for each target plus `checksums.txt`, as a pre-release when the tag has a
`-suffix`. Re-running for an existing tag (`workflow_dispatch` with `tag`)
replaces the assets.

Intel Macs and Windows have no release build yet; both build from source.

## Installing

```sh
curl -fsSL https://raw.githubusercontent.com/brandonbosch/chatstrata/main/scripts/install.sh | sh
```

It picks the newest `v2` release (or `CHATSTRATA_VERSION`), checks the
archive against `checksums.txt` and installs to `~/.local/bin`
(`CHATSTRATA_INSTALL_DIR`). If `~/.local/bin/chatstrata` is a symlink, which
is how `uv tool` and `pipx` install the Python app, it replaces the link and
leaves the Python install alone; it never writes through a symlink.

From source, with Go and a C toolchain:

```sh
go install github.com/brandonbosch/chatstrata/cmd/chatstrata@main
```

A source build has no embedded search extension: run
`chatstrata reindex --install-fts` once, or build as a release does:

```sh
go run ./internal/tools/fetchfts
go build -tags fts_embed -o bin/chatstrata ./cmd/chatstrata
```

## The embedded search extension

`duckdb-go` doesn't bundle DuckDB's fts extension. Release builds carry
DuckDB's own signed build of it instead of downloading it at run time:

- `internal/tools/fetchfts` reads the DuckDB version and platform this module
  links and downloads `extensions.duckdb.org/<version>/<platform>/fts.duckdb_extension.gz`
  into `internal/store/fts/` (git-ignored).
- With `-tags fts_embed`, `internal/store/fts_embed.go` embeds that file
  (3.6 MB; the stripped linux/amd64 binary is 72 MB, 29 MB compressed).
- The first process that needs search writes it, once, to
  `<data dir>/extensions/<hash>/fts.duckdb_extension` and `LOAD`s it from
  there. The directory is named after the content hash, so an upgrade never
  loads an older release's file. DuckDB verifies the extension's signature on
  load, as it does for `INSTALL`.

So a release binary keeps the README's promise: no network request unless
the user configured sync.

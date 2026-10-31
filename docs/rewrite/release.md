# Releasing chatstrata

Go releases use `v2.*` tags and module path
`github.com/brandonbosch/chatstrata/v2`. The Python application is preserved at
`python-final`; its publishing workflow has been retired. Existing PyPI
releases remain available, but the installer installs native Go binaries.

## Verify and tag

From the reviewed commit on `main`:

```sh
go test ./...
go vet ./...
test -z "$(gofmt -l .)"
go run ./internal/tools/fetchfts
go test -tags fts_embed ./...
go build -trimpath -tags fts_embed -o bin/chatstrata ./cmd/chatstrata
scripts/smoke-release.sh bin/chatstrata
git tag v2.0.0
git push origin v2.0.0
```

The release workflow builds natively because DuckDB uses CGo:

| Target | Runner | Notes |
|---|---|---|
| linux/amd64 | ubuntu-22.04 | glibc 2.35 or newer |
| linux/arm64 | ubuntu-22.04-arm | glibc 2.35 or newer |
| darwin/arm64 | macos-latest | Apple silicon |

Intel Macs and Windows have no downloadable release yet. They build from
source with Go and a C toolchain:

```sh
go install github.com/brandonbosch/chatstrata/v2/cmd/chatstrata@latest
```

Each native job fetches DuckDB's signed FTS extension, runs the tests with
`-tags fts_embed`, builds with the tag's version stamped in, and runs the
release smoke script in an empty home directory. Smoke checks exercise the
public `ingest --auto` workflow twice, `mcp config codex`, rebuild, full-text
search, and the actual stdio MCP binary through `internal/tools/smokemcp`.
MCP checks include schema/tool discovery, a count query, rejected database
writes, and a rejected external-file read. No runtime extension download or
write to `~/.duckdb` is expected.

The workflow uploads `chatstrata_<version>_<os>_<arch>.tar.gz` and
`checksums.txt`. Tags with a suffix are prereleases; stable tags become the
latest release. `workflow_dispatch` can rebuild an existing tag and replace
its assets. Inspect all three native job results before advertising a release.

## Verify installation from the published assets

```sh
curl -fsSL https://chatstrata.sprawlcommons.com/install.sh | sh
chatstrata version
chatstrata ingest --auto
chatstrata mcp config codex
```

For an isolated download check, set `CHATSTRATA_INSTALL_DIR` to a temporary
directory and `CHATSTRATA_VERSION` to the exact tag. Do not replace a real
installation merely to test a candidate.

The installer prefers stable v2 releases even if a newer candidate exists.
Before the first stable release it can select a candidate. The override pins
an explicit tag. It verifies SHA-256, runs the binary's `version` command to
check compatibility before replacement, then installs to `~/.local/bin` (or
`CHATSTRATA_INSTALL_DIR`). It replaces a Python entry-point symlink without
writing through it. Restart an installed daemon with `schedule install` after
an upgrade; reconnect MCP clients so their server processes use the new binary.

`go test ./scripts` runs the real installer against local fake downloads,
covering stable/candidate selection, checksum rejection, symlink replacement,
and preservation of a working install when a new binary cannot run.

The independent installer fallback is:

```sh
curl -fsSL https://raw.githubusercontent.com/brandonbosch/chatstrata/main/scripts/install.sh | sh
```

## Publish docs and the domain installer

The documentation workflow builds Go-focused guides with optional Zensical
and copies `scripts/install.sh` byte-for-byte to the static site's root.
GitHub Pages remains available at the repository's Pages URL.

The public domain uses the same static artifact with Cloudflare Workers
Static Assets. Follow `docs-site/README.md` to build, dry-run, and deploy it.
`docs-site/wrangler.jsonc` configures only `chatstrata.sprawlcommons.com`;
the parent Sprawl Commons site is independent. Verify HTTPS, the HTML home
page, and `/install.sh` (text/plain, byte-identical to the repository script),
then run an isolated install from that exact public URL.

No MCP server, private transcripts, archives, or credentials belong on the
public site. Automatic Cloudflare deployment requires its own scoped CI token
or repository build integration; do not copy a personal OAuth login into CI.

## Embedded search extension

`internal/tools/fetchfts` reads the linked DuckDB version/platform and fetches
its signed FTS extension into git-ignored `internal/store/fts/`. Release builds
embed it with `-tags fts_embed`. At runtime it is extracted to a content-hash
path under the data directory and DuckDB verifies its signature on load.

Source builds have no bundled extension and use substring search until the
user explicitly runs `chatstrata reindex --install-fts`. Normal runtime
collection/search/query/MCP performs no network requests; optional configured
sync is separate. Read connections load the trusted FTS extension before
external access and extension autoload/install are disabled and settings locked.

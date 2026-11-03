#!/bin/sh
# The one paved gate: everything a change must pass before it is handed off.
# CI runs this too, so a green local run means a green build.
#
#   scripts/check.sh         gofmt, vet, tests (which include internal/guard), build
#   scripts/check.sh --full  also go mod tidy -diff, golangci-lint, and the
#                            release build with the embedded fts extension
set -eu

repo=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo"

full=0
if [ $# -gt 1 ] || { [ $# -eq 1 ] && [ "$1" != "--full" ]; }; then
	echo "usage: scripts/check.sh [--full]" >&2
	exit 2
fi
if [ $# -eq 1 ]; then
	full=1
fi

step() { printf '==> %s\n' "$*"; }

step "gofmt"
unformatted=$(gofmt -l ./cmd ./internal ./scripts)
if [ -n "$unformatted" ]; then
	printf '%s\n' "$unformatted"
	echo "check: gofmt found unformatted files above" >&2
	exit 1
fi

step "go vet"
go vet ./...

# internal/guard turns the static gates into ordinary tests, so this is also
# the boundary, ratchet and escape-hatch run.
step "go test ./..."
go test ./...

step "go build"
go build -o bin/chatstrata ./cmd/chatstrata
./bin/chatstrata version

if [ "$full" -eq 0 ]; then
	exit 0
fi

step "go mod tidy -diff"
go mod tidy -diff

step "golangci-lint (pinned in go.mod's tool directive)"
go tool golangci-lint run ./...

step "embedded fts extension"
go run ./internal/tools/fetchfts
go test -tags fts_embed ./...
go build -tags fts_embed -o bin/chatstrata-release ./cmd/chatstrata
scripts/smoke-release.sh bin/chatstrata-release

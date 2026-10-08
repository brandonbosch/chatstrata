#!/bin/sh
# Smoke-test a release binary in an empty home directory: it must run,
# ingest the golden claude_code input, and search it with the embedded
# full-text extension (no `reindex --install-fts`, no download).
#
#   scripts/smoke-release.sh path/to/chatstrata
set -eu

bin=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
repo=$(cd "$(dirname "$0")/.." && pwd)
home=$(mktemp -d)
trap 'rm -rf "$home"' EXIT

run() {
	env -i PATH="$PATH" HOME="$home" XDG_DATA_HOME="$home/data" "$bin" "$@"
}

run version
run ingest claude_code --path "$repo/spec/golden/inputs/claude_code/projects"
run reindex
out=$(run search refactor)
echo "$out"
case $out in
*"substring"*) echo "smoke: search fell back to substring matching" >&2; exit 1 ;;
*"No results"*) echo "smoke: search found nothing" >&2; exit 1 ;;
esac
if [ -d "$home/.duckdb" ]; then
	echo "smoke: the fts extension was installed into ~/.duckdb instead of loaded from the binary" >&2
	exit 1
fi
echo "smoke: ok"

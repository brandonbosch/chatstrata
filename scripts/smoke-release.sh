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
mkdir -p "$home/.claude"
cp -R "$repo/spec/golden/inputs/claude_code/projects" "$home/.claude/projects"
run ingest --auto
run ingest --auto
run mcp config codex
counts="SELECT (SELECT count(*) FROM conversations) AS conversations, (SELECT count(*) FROM messages) AS messages, (SELECT count(*) FROM content_blocks) AS blocks"
before=$(run query "$counts" --json)
run rebuild
after=$(run query "$counts" --json)
[ "$before" = "$after" ] || { echo "smoke: rebuild changed archive counts" >&2; exit 1; }
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
(
	cd "$repo"
	go run ./internal/tools/smokemcp "$bin" "$home"
)
echo "smoke: ok"

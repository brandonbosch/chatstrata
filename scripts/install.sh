#!/bin/sh
# Install the chatstrata binary from GitHub Releases.
#
#   curl -fsSL https://raw.githubusercontent.com/brandonbosch/chatstrata/main/scripts/install.sh | sh
#
# Environment:
#   CHATSTRATA_VERSION      tag to install, e.g. v2.0.0 (default: newest stable v2
#                           release; a release candidate only if no stable exists)
#   CHATSTRATA_INSTALL_DIR  where to put the binary (default: ~/.local/bin)
#
# It downloads one archive and checks it against the release's checksums.txt.
set -eu

repo=brandonbosch/chatstrata
dir=${CHATSTRATA_INSTALL_DIR:-$HOME/.local/bin}

fail() { echo "install: $*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

if have curl; then
	fetch() { curl -fsSL "$1"; }
	fetch_to() { curl -fsSL -o "$2" "$1"; }
elif have wget; then
	fetch() { wget -qO- "$1"; }
	fetch_to() { wget -qO "$2" "$1"; }
else
	fail "needs curl or wget"
fi

case $(uname -s) in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) fail "no release build for $(uname -s); build from source: go install github.com/brandonbosch/chatstrata/v2/cmd/chatstrata@latest" ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) fail "no release build for $(uname -m)" ;;
esac
if [ "$os/$arch" = darwin/amd64 ]; then
	fail "no release build for Intel Macs yet; build from source: go install github.com/brandonbosch/chatstrata/v2/cmd/chatstrata@latest"
fi

tag=${CHATSTRATA_VERSION:-}
if [ -z "$tag" ]; then
	# Prefer stable releases even when a newer release candidate exists.
	# The retired Python releases share this repository and are skipped.
	tags=$(fetch "https://api.github.com/repos/$repo/releases?per_page=100" |
		tr ',' '\n' | sed -n 's/.*"tag_name": *"\(v2[^"]*\)".*/\1/p')
	tag=$(printf '%s\n' "$tags" | sed -n '/^v2\.[0-9][0-9]*\.[0-9][0-9]*$/p' | head -n 1)
	if [ -z "$tag" ]; then
		tag=$(printf '%s\n' "$tags" | head -n 1)
	fi
	[ -n "$tag" ] || fail "couldn't find a v2 release of $repo"
fi
case "$tag" in
v2.*) ;;
*) fail "expected a v2 release tag, got $tag" ;;
esac
case "$tag" in
*[!A-Za-z0-9.+-]*) fail "invalid release tag" ;;
esac
version=${tag#v}
name="chatstrata_${version}_${os}_${arch}"
base="https://github.com/$repo/releases/download/$tag"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
echo "Downloading chatstrata $tag ($os/$arch)..."
fetch_to "$base/$name.tar.gz" "$tmp/$name.tar.gz" || fail "download failed: $base/$name.tar.gz"
fetch_to "$base/checksums.txt" "$tmp/checksums.txt" || fail "download failed: $base/checksums.txt"

want=$(grep " $name.tar.gz\$" "$tmp/checksums.txt" | cut -d' ' -f1)
[ -n "$want" ] || fail "$name.tar.gz is not in checksums.txt"
if have sha256sum; then
	got=$(sha256sum "$tmp/$name.tar.gz" | cut -d' ' -f1)
else
	got=$(shasum -a 256 "$tmp/$name.tar.gz" | cut -d' ' -f1)
fi
[ "$got" = "$want" ] || fail "checksum mismatch for $name.tar.gz"

tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
# Check compatibility before replacing a working installation (e.g. an older
# Linux libc cannot run the release binary). This touches no user archive.
installed_version=$("$tmp/$name/chatstrata" version) || fail "this binary cannot run here; Linux releases require glibc 2.35 or newer"
mkdir -p "$dir"
target="$dir/chatstrata"
if [ -L "$target" ]; then
	# Usually the Python app's entry point (uv tool or pipx). Replace the
	# link itself; the Python install it pointed to is left alone.
	echo "Replacing the symlink $target -> $(readlink "$target")"
	echo "(the environment it pointed to stays installed)"
fi
# Write next to the target and rename, so a running daemon keeps its old
# binary and nothing ever writes through a symlink.
cp "$tmp/$name/chatstrata" "$dir/.chatstrata.new"
chmod 755 "$dir/.chatstrata.new"
mv -f "$dir/.chatstrata.new" "$target"

echo "Installed $installed_version to $target"
case ":$PATH:" in
*":$dir:"*) ;;
*) echo "Note: $dir is not on your PATH." ;;
esac
cat <<'NEXT'

Next steps:
  chatstrata ingest --auto         # collect installed sources once
  chatstrata schedule install      # keep the archive current in the background
  chatstrata mcp config codex       # print Codex setup (or claude-code / claude-desktop)
NEXT

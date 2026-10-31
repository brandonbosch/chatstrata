#!/bin/sh
# Build the guides, then overlay the standalone WebGL landing page.
set -eu
cd "$(dirname "$0")/../docs-site"
uv run --group dev zensical build --clean
cp -R ../site/. site/
cp ../scripts/install.sh site/install.sh
cmp ../scripts/install.sh site/install.sh

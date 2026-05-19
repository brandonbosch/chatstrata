# Release Checklist

This project publishes as a normal Python package. DuckDB is a Python
dependency; users do not need to install a separate DuckDB server or CLI.

## One-time PyPI setup

1. Create a PyPI account and enable two-factor authentication.
2. Create the `chatstrata` project by publishing the first release.
3. Prefer PyPI trusted publishing from GitHub Actions for future releases.
4. If using an API token instead, store it as a GitHub Actions secret and never
   commit it to the repository.

## Pre-release checks

```bash
uv run --extra dev python -m ruff check .
uv run --extra dev --extra redact python -m pytest
uv build
uvx twine check dist/*
```

Inspect package contents before uploading:

```bash
tar -tzf dist/chatstrata-0.1.0.tar.gz | less
unzip -l dist/chatstrata-0.1.0-py3-none-any.whl
```

The wheel should contain only the importable package and package metadata. The
source distribution may contain docs and tests, but should not contain local
databases, virtualenvs, caches, `.DS_Store`, or private tool config.

## Publish

For a first release, publish to TestPyPI first:

```bash
uvx twine upload --repository testpypi dist/*
```

Install from TestPyPI in a clean environment and smoke test:

```bash
uv tool install --index-url https://test.pypi.org/simple/ --extra-index-url https://pypi.org/simple/ chatstrata
chatstrata init --no-discover
chatstrata paths
chatstrata sources
```

If that works, publish to PyPI:

```bash
uvx twine upload dist/*
```

Tag the release after PyPI is confirmed:

```bash
git tag v0.1.0
git push origin v0.1.0
```

## After release

- Confirm `uv tool install chatstrata` works from PyPI.
- Confirm `chatstrata init`, `chatstrata paths`, and `chatstrata sources` work.
- Create a GitHub release from the tag using `CHANGELOG.md` notes.

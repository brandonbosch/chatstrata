"""chatstrata - a personal, queryable archive of your AI conversations."""

from importlib.metadata import PackageNotFoundError, version

from chatstrata.core.migrations import LATEST_VERSION as SCHEMA_VERSION

# Single source of truth is pyproject.toml; fall back for editable installs
# where package metadata may be missing.
try:
    __version__ = version("chatstrata")
except PackageNotFoundError:  # pragma: no cover
    __version__ = "0.0.0.dev0"

__all__ = ["SCHEMA_VERSION", "__version__"]

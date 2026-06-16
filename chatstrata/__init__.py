"""chatstrata - a personal, queryable archive of your AI conversations."""

from chatstrata.core.migrations import LATEST_VERSION as SCHEMA_VERSION

__version__ = "0.2.0"

__all__ = ["SCHEMA_VERSION", "__version__"]

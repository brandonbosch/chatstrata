"""Core: schema, canonical models, ingest, db connection.

Only models are re-exported here to avoid pulling DuckDB into the import graph
for code that only needs the canonical types (e.g. adapter authors writing
parsers in isolation). Use `from chatstrata.core.db import connect` directly.
"""

from chatstrata.core.models import (
    BlockType,
    ContentBlock,
    ConversationHandle,
    ParsedConversation,
    ParsedMessage,
    Role,
)

__all__ = [
    "BlockType",
    "ContentBlock",
    "ConversationHandle",
    "ParsedConversation",
    "ParsedMessage",
    "Role",
]

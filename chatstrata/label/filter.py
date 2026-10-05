"""Hide labelled (and not-yet-labelled) content from a read-only connection.

``apply_filter`` turns label rules such as ``cyber:security_related>0.3`` into
TEMP views that shadow the archive's tables for the rest of the connection.
Queries keep working unchanged -- counts, joins and ids are intact -- but the
text of hidden content is replaced with a marker and its JSON payloads are
NULL.

The filter fails closed. A target a rule's pack covers but has no current
label for (new data, a failed request, an edited pack) is hidden as
``unlabelled:<pack>`` until ``chatstrata label run <pack>`` clears it.

What gets hidden:

- Each rule's flagged and unlabelled targets: a tool call's block, every block
  of a message, or every block of a conversation, by the pack's target kind.
- The other half of any hidden tool call: its tool_use or tool_result.
- Titles and metadata of conversations with hidden blocks; metadata and
  attachment details of messages with hidden blocks.
- ``raw_events.payload``, always: raw records cannot be tied to blocks.

Content no rule's pack covers stays visible. The ``cyber`` pack covers tool
calls only; add ``cyber-messages`` to cover message text and thinking.

This guards against reading content by accident, not against a determined
reader: ``check_query`` rejects the obvious ways around the views (naming the
archive catalog, reading FTS internals) and the caller disables file access.
"""

from __future__ import annotations

import re
from dataclasses import dataclass

import duckdb

from chatstrata.label.packs import Pack, get_pack

HIDDEN = "[hidden by chatstrata filter]"

_RULE_RE = re.compile(r"^\s*([\w.-]+):(\w+)\s*(>|=)\s*(\S.*?)\s*$")


class FilterError(ValueError):
    """The filter could not be applied; the connection must not be queried."""


@dataclass(frozen=True)
class FilterRule:
    """Hide a pack's targets whose answer to ``question`` crosses a line.

    ``pack:question>0.3`` hides noul/score answers above 0.3;
    ``pack:question=offensive`` hides choice answers equal to that option.
    """

    pack: str
    question: str
    op: str
    value: float | str

    @classmethod
    def parse(cls, text: str) -> FilterRule:
        match = _RULE_RE.match(text)
        if not match:
            raise FilterError(
                f"Bad filter rule {text!r}; expected pack:question>0.5 or pack:question=choice."
            )
        pack, question, op, raw = match.groups()
        if op == ">":
            try:
                return cls(pack, question, op, float(raw))
            except ValueError:
                raise FilterError(f"Filter rule {text!r}: {raw!r} is not a number.") from None
        return cls(pack, question, op, raw)

    def __str__(self) -> str:
        return f"{self.pack}:{self.question}{self.op}{self.value}"


def parse_rules(text: str | None) -> list[FilterRule]:
    """Parse a comma-separated rule list, e.g. from ``$CHATSTRATA_MCP_FILTER``."""
    if not text:
        return []
    return [FilterRule.parse(part) for part in text.split(",") if part.strip()]


def _resolve(rule: FilterRule) -> Pack:
    try:
        pack = get_pack(rule.pack)
    except KeyError as exc:
        raise FilterError(str(exc.args[0])) from None
    question = pack.questions.get(rule.question)
    if question is None:
        raise FilterError(
            f"Filter rule {rule}: pack {pack.name!r} has no question {rule.question!r}."
        )
    wants = "=" if question["type"] == "choice" else ">"
    if rule.op != wants:
        raise FilterError(
            f"Filter rule {rule}: {question['type']} questions use '{wants}', not '{rule.op}'."
        )
    return pack


def _columns(conn: duckdb.DuckDBPyConnection, db: str, table: str) -> list[str]:
    rows = conn.execute(
        "SELECT column_name FROM information_schema.columns "
        "WHERE table_catalog = ? AND table_schema = 'main' AND table_name = ? "
        "ORDER BY ordinal_position",
        [db, table],
    ).fetchall()
    return [r[0] for r in rows]


def _shadow(
    conn: duckdb.DuckDBPyConnection,
    db: str,
    table: str,
    replacements: dict[str, str],
    join: str = "",
) -> None:
    """Create a TEMP view named ``table`` over the archive table, rewriting columns.

    Columns keep their names and order, so ``SELECT *`` and the stored
    ``tool_calls`` view see the same shape as before.
    """
    cols = _columns(conn, db, table)
    if not cols:
        return
    select = ", ".join(
        f"{replacements[c]} AS {c}" if c in replacements else f"t.{c}" for c in cols
    )
    conn.execute(
        f'CREATE OR REPLACE TEMP VIEW {table} AS SELECT {select} FROM "{db}".main.{table} t {join}'
    )


def apply_filter(conn: duckdb.DuckDBPyConnection, rules: list[FilterRule]) -> None:
    """Shadow the archive tables with filtered TEMP views for ``rules``.

    Must run before anything else creates TEMP objects on ``conn``. Raises
    :class:`FilterError` if a rule cannot be applied; the caller must then
    refuse to run queries rather than fall back to the unfiltered archive.
    """
    if not rules:
        return
    db = conn.execute("SELECT current_database()").fetchone()[0]
    if not _columns(conn, db, "labels"):
        raise FilterError(
            "The archive has no labels table yet; run `chatstrata migrate` and "
            "`chatstrata label run <pack>` before filtering."
        )

    conn.execute(
        "CREATE OR REPLACE TEMP TABLE _cs_hidden_targets "
        "(target_kind VARCHAR, target_id VARCHAR, reason VARCHAR)"
    )
    for rule in rules:
        pack = _resolve(rule)
        cond = "value > ?" if rule.op == ">" else "choice = ?"
        labelled = (
            f'SELECT target_id FROM "{db}".main.labels '
            "WHERE pack = ? AND question = ? AND pack_version = ?"
        )
        key = [pack.name, rule.question, pack.version]
        # Flagged by the classifier.
        conn.execute(
            f"INSERT INTO _cs_hidden_targets SELECT ?, target_id, ? FROM ({labelled} AND {cond})",
            [pack.target_kind, str(rule), *key, rule.value],
        )
        # Covered by the pack but not (currently) labelled: fail closed. The
        # pack SQL reads the archive tables; no shadow views exist yet.
        conn.execute(
            f"INSERT INTO _cs_hidden_targets SELECT ?, t.target_id, ? "
            f"FROM ({pack.target_sql}) t WHERE t.target_id NOT IN ({labelled})",
            [pack.target_kind, f"unlabelled:{pack.name}", *key],
        )

    a = f'"{db}".main'
    conn.execute(
        f"""
        CREATE OR REPLACE TEMP TABLE _cs_hidden_blocks AS
        WITH direct AS (
            SELECT cb.id, h.reason
            FROM _cs_hidden_targets h JOIN {a}.content_blocks cb ON cb.id = h.target_id
            WHERE h.target_kind = 'tool_call'
            UNION ALL
            SELECT cb.id, h.reason
            FROM _cs_hidden_targets h JOIN {a}.content_blocks cb ON cb.message_id = h.target_id
            WHERE h.target_kind = 'message'
            UNION ALL
            SELECT cb.id, h.reason
            FROM _cs_hidden_targets h
            JOIN {a}.messages m ON m.conversation_id = h.target_id
            JOIN {a}.content_blocks cb ON cb.message_id = m.id
            WHERE h.target_kind = 'conversation'
        ),
        paired AS (  -- the tool_use / tool_result partner of a hidden block
            SELECT other.id, d.reason
            FROM direct d
            JOIN {a}.content_blocks cb ON cb.id = d.id AND cb.tool_use_id IS NOT NULL
            JOIN {a}.messages m ON m.id = cb.message_id
            JOIN {a}.content_blocks other
                ON other.tool_use_id = cb.tool_use_id AND other.id <> cb.id
            JOIN {a}.messages om
                ON om.id = other.message_id AND om.conversation_id = m.conversation_id
        )
        SELECT id, min(reason) AS reason
        FROM (SELECT * FROM direct UNION ALL SELECT * FROM paired)
        GROUP BY id
        """
    )
    conn.execute(
        f"""
        CREATE OR REPLACE TEMP TABLE _cs_hidden_messages AS
        SELECT DISTINCT cb.message_id AS id
        FROM _cs_hidden_blocks h JOIN {a}.content_blocks cb ON cb.id = h.id
        """
    )
    conn.execute(
        f"""
        CREATE OR REPLACE TEMP TABLE _cs_hidden_conversations AS
        SELECT DISTINCT m.conversation_id AS id
        FROM _cs_hidden_messages h JOIN {a}.messages m ON m.id = h.id
        """
    )

    marker = "'[hidden by chatstrata filter: ' || hb.reason || ']'"
    _shadow(
        conn, db, "content_blocks",
        {
            "text": f"CASE WHEN hb.id IS NULL THEN t.text ELSE {marker} END",
            "payload": "CASE WHEN hb.id IS NULL THEN t.payload END",
        },
        "LEFT JOIN _cs_hidden_blocks hb ON hb.id = t.id",
    )
    _shadow(
        conn, db, "messages",
        {"metadata": "CASE WHEN hm.id IS NULL THEN t.metadata END"},
        "LEFT JOIN _cs_hidden_messages hm ON hm.id = t.id",
    )
    _shadow(
        conn, db, "conversations",
        {
            "title": f"CASE WHEN hc.id IS NULL THEN t.title ELSE '{HIDDEN}' END",
            "metadata": "CASE WHEN hc.id IS NULL THEN t.metadata END",
        },
        "LEFT JOIN _cs_hidden_conversations hc ON hc.id = t.id",
    )
    _shadow(
        conn, db, "attachments",
        {
            "filename": f"CASE WHEN hm.id IS NULL THEN t.filename ELSE '{HIDDEN}' END",
            "source_url": "CASE WHEN hm.id IS NULL THEN t.source_url END",
            "metadata": "CASE WHEN hm.id IS NULL THEN t.metadata END",
        },
        "LEFT JOIN _cs_hidden_messages hm ON hm.id = t.message_id",
    )
    _shadow(conn, db, "raw_events", {"payload": "NULL::JSON"})


_FTS_INTERNALS_RE = re.compile(
    r"\bfts_main_content_blocks\s*\.\s*(?!match_bm25\b)\w+", re.IGNORECASE
)


def check_query(sql: str, db: str) -> None:
    """Reject SQL that would read around the filter's shadow views.

    Naming the archive catalog (``<db>.main.content_blocks``) reaches the raw
    tables, and the FTS index tables hold the archive's vocabulary.
    """
    catalog = re.compile(rf'(?<![\w.]){re.escape(db)}"?\s*\.', re.IGNORECASE)
    if catalog.search(sql.replace(f'"{db}"', db)):
        raise ValueError(
            f"The content filter is on: refer to tables without the {db!r} catalog "
            f"prefix (e.g. content_blocks, not {db}.main.content_blocks)."
        )
    if _FTS_INTERNALS_RE.search(sql):
        raise ValueError(
            "The content filter is on: only fts_main_content_blocks.match_bm25() "
            "may be used from the full-text index."
        )

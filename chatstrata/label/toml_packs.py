"""Load question packs from TOML files, so a new idea needs no Python.

A pack file declares the same four things a built-in :class:`~chatstrata.label.packs.Pack`
does, but entirely as data:

```toml
name = "cyber"
description = "Flag security-related tool calls so they can be filtered out."
target_kind = "tool_call"          # tool_call | message | conversation
state_version = 1                   # bump by hand if you change [state]

target_sql = \"\"\"
SELECT cb.id AS target_id, c.source_id, c.project, m.created_at, ...
FROM content_blocks cb ...
\"\"\"

[state]
# Each key becomes a field of the state sent to the model. `column` names a
# column returned by target_sql; the rest are optional transforms.
harness   = { column = "source_id" }
arguments = { column = "arguments", unwrap = true, json = true, clip = 2000 }
result    = { column = "result_text", clip = 3000, default = "(no output)" }

[questions.security_related]
type = "noul"
instructions = "Is this tool call part of security or hacking work?"
```

``target_sql`` must return at least ``target_id``, ``source_id``, ``project``
and ``created_at`` (the runner filters and de-dupes on them), plus whatever
columns ``[state]`` reads.

Discovery looks in a bundled directory (packs that ship with chatstrata) and a
user directory (``$CHATSTRATA_PACKS_DIR`` or ``<config>/chatstrata/packs``).
Drop a ``.toml`` file there and ``chatstrata label`` picks it up.
"""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
from typing import Any

from platformdirs import user_config_dir

from chatstrata.label.packs import Pack, _json_text, _tool_arguments, clip

try:  # tomllib is stdlib on 3.11+; tomli backfills 3.10
    import tomllib
except ModuleNotFoundError:  # pragma: no cover - only on 3.10
    import tomli as tomllib  # type: ignore[no-redef]

VALID_TARGET_KINDS = {"tool_call", "message", "conversation"}
VALID_ANSWER_TYPES = {"noul", "choice", "score"}
_BUNDLED_DIR = Path(__file__).parent / "bundled_packs"


class PackFileError(ValueError):
    """A TOML pack file is missing a field or uses an invalid value."""


def _make_state_builder(spec: dict[str, Any]):
    """Turn a ``[state]`` table into a ``build_state`` callable.

    Each field reads one column and applies optional transforms in order:
    ``unwrap`` (strip a tool-arguments envelope), ``json`` (render as JSON
    text, clipped), ``clip`` (trim a plain string), ``default`` (used when the
    value is missing or empty). A field whose value ends up ``None`` with no
    default is omitted from the state.
    """
    fields = []
    for key, raw in spec.items():
        if not isinstance(raw, dict) or "column" not in raw:
            raise PackFileError(
                f"[state] field {key!r} must be a table with a 'column' key."
            )
        unknown = set(raw) - {"column", "clip", "json", "unwrap", "default"}
        if unknown:
            raise PackFileError(
                f"[state] field {key!r} has unknown options: {', '.join(sorted(unknown))}."
            )
        fields.append(
            (
                key,
                raw["column"],
                raw.get("clip"),
                bool(raw.get("json")),
                bool(raw.get("unwrap")),
                raw.get("default"),
            )
        )

    def build_state(row: dict[str, Any]) -> dict[str, Any]:
        out: dict[str, Any] = {}
        for key, column, climit, as_json, unwrap, default in fields:
            value = row.get(column)
            if unwrap:
                value = _tool_arguments(value)
            if as_json:
                value = _json_text(value, climit if climit is not None else 100_000)
            elif climit is not None and isinstance(value, str):
                value = clip(value, climit)
            if (value is None or value == "") and default is not None:
                value = default
            if value is not None:
                out[key] = value
        return out

    return build_state


def _validate_questions(questions: Any) -> dict[str, dict[str, Any]]:
    if not isinstance(questions, dict) or not questions:
        raise PackFileError("A pack needs a non-empty [questions] table.")
    for qid, q in questions.items():
        if not isinstance(q, dict) or q.get("type") not in VALID_ANSWER_TYPES:
            raise PackFileError(
                f"Question {qid!r} needs a 'type' of {', '.join(sorted(VALID_ANSWER_TYPES))}."
            )
    return questions


def load_pack_file(path: Path) -> Pack:
    """Parse and validate one TOML pack file into a :class:`Pack`."""
    try:
        with open(path, "rb") as fh:
            data = tomllib.load(fh)
    except tomllib.TOMLDecodeError as exc:
        raise PackFileError(f"{path}: invalid TOML ({exc}).") from exc

    for field in ("name", "target_kind", "target_sql"):
        if not data.get(field):
            raise PackFileError(f"{path}: missing required field {field!r}.")
    if data["target_kind"] not in VALID_TARGET_KINDS:
        raise PackFileError(
            f"{path}: target_kind must be one of {', '.join(sorted(VALID_TARGET_KINDS))}."
        )

    state_spec = data.get("state")
    if not isinstance(state_spec, dict) or not state_spec:
        raise PackFileError(f"{path}: a pack needs a non-empty [state] table.")
    questions = _validate_questions(data.get("questions"))

    fingerprint = hashlib.sha256(
        json.dumps(state_spec, sort_keys=True).encode()
    ).hexdigest()[:12]

    try:
        return Pack(
            name=data["name"],
            description=data.get("description", ""),
            target_kind=data["target_kind"],
            target_sql=data["target_sql"],
            build_state=_make_state_builder(state_spec),
            questions=questions,
            state_version=int(data.get("state_version", 1)),
            source=str(path),
            state_fingerprint=fingerprint,
        )
    except PackFileError as exc:
        raise PackFileError(f"{path}: {exc}") from exc


def user_packs_dir() -> Path:
    """Directory users drop their own pack files in."""
    env = os.environ.get("CHATSTRATA_PACKS_DIR")
    if env:
        return Path(env).expanduser()
    return Path(user_config_dir("chatstrata", appauthor=False)) / "packs"


def discover_toml_packs() -> dict[str, Pack]:
    """Load every ``*.toml`` pack from the bundled and user directories.

    User packs load last, so a user file can shadow a bundled one by name.
    An unreadable file raises :class:`PackFileError` naming the file.
    """
    packs: dict[str, Pack] = {}
    for directory in (_BUNDLED_DIR, user_packs_dir()):
        if not directory.is_dir():
            continue
        for path in sorted(directory.glob("*.toml")):
            pack = load_pack_file(path)
            packs[pack.name] = pack
    return packs

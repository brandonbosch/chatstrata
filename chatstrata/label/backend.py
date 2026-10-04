"""Classifier backends that answer a pack's questions about one state.

The runner only depends on the ``LabelBackend`` protocol so tests (and other
classifiers later) can stand in for the TypeSafe API.
"""

from __future__ import annotations

import os
from dataclasses import dataclass
from typing import Any, Protocol

# jev-1.13 list price per million input tokens (output tokens are free).
# Used only for the pre-run estimate; see https://docs.typesafe.ai/models.
JEV_PRICE_PER_MTOK = 0.042


class BackendError(RuntimeError):
    """A failure that should stop the whole run (bad key, missing SDK)."""


@dataclass
class BackendResult:
    model: str
    answers: dict[str, dict[str, Any]]
    input_tokens: int = 0


class LabelBackend(Protocol):
    name: str

    def ask(self, state: Any, questions: dict[str, dict[str, Any]]) -> BackendResult:
        ...


class TypeSafeBackend:
    """Calls TypeSafe's System One endpoint through the official SDK."""

    name = "typesafe"

    def __init__(self, model: str = "jev-latest", api_key: str | None = None) -> None:
        try:
            from typesafe_sdk import TypeSafeClient
        except ImportError as exc:
            raise BackendError(
                "The TypeSafe SDK is not installed. Install the extra: "
                'uv tool install "chatstrata[jev]"'
            ) from exc
        key = api_key or os.environ.get("TYPESAFE_API_KEY")
        if not key:
            raise BackendError(
                "TYPESAFE_API_KEY is not set. Create a key at https://console.typesafe.ai/keys."
            )
        self._client = TypeSafeClient(api_key=key, model=model)

    def ask(self, state: Any, questions: dict[str, dict[str, Any]]) -> BackendResult:
        from typesafe_sdk import TypeSafeAuthenticationError, TypeSafePermissionDeniedError

        try:
            response = self._client.system_one(state, questions)
        except (TypeSafeAuthenticationError, TypeSafePermissionDeniedError) as exc:
            raise BackendError(f"TypeSafe rejected the API key: {exc}") from exc
        return BackendResult(
            model=response.model,
            answers={k: a.model_dump(mode="json") for k, a in response.answers.items()},
            input_tokens=response.usage.input_tokens or 0,
        )

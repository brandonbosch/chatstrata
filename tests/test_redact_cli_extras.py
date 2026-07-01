"""Tests for redact CLI features: --allow-entity, _require_presidio, and new recognizer integration."""

from __future__ import annotations

import json
from importlib.util import find_spec
from pathlib import Path
from unittest.mock import patch

import pytest
from click.testing import CliRunner

from chatstrata.cli import cli

HAS_PRESIDIO = find_spec("presidio_analyzer") is not None

FIXTURES = (
    Path(__file__).parent.parent
    / "chatstrata"
    / "sources"
    / "claude_code"
    / "tests"
    / "fixtures"
)


@pytest.fixture
def runner():
    return CliRunner()


class TestRequirePresidio:
    def test_text_shows_install_hint_when_missing(self, runner):
        with patch.dict("sys.modules", {"presidio_analyzer": None}):
            from chatstrata.redact import cli as redact_cli

            orig = redact_cli._require_presidio

            def fake_require():
                import click

                raise click.UsageError(
                    "The [redact] extras are required for this command.\n"
                    'Install with: uv pip install "chatstrata[redact]"'
                )

            redact_cli._require_presidio = fake_require
            try:
                result = runner.invoke(cli, ["redact", "text", "hello"])
                assert result.exit_code != 0
                assert "redact" in result.output.lower()
            finally:
                redact_cli._require_presidio = orig


@pytest.mark.skipif(not HAS_PRESIDIO, reason="presidio not installed")
class TestAllowEntityOption:
    def test_allow_person_detects_names(self, runner):
        result = runner.invoke(
            cli,
            [
                "redact",
                "text",
                "Contact John Smith for details",
                "--json",
                "--allow-entity",
                "PERSON",
            ],
        )
        assert result.exit_code == 0
        data = json.loads(result.output)
        types = [e["type"] for e in data["entities"]]
        assert "PERSON" in types

    def test_default_does_not_detect_person(self, runner):
        result = runner.invoke(
            cli,
            [
                "redact",
                "text",
                "Contact John Smith for details",
                "--json",
            ],
        )
        assert result.exit_code == 0
        data = json.loads(result.output)
        types = [e["type"] for e in data["entities"]]
        assert "PERSON" not in types

    def test_allow_multiple_entities(self, runner):
        result = runner.invoke(
            cli,
            [
                "redact",
                "text",
                "John Smith at Microsoft on 2026-05-14",
                "--json",
                "--allow-entity",
                "PERSON",
                "--allow-entity",
                "ORGANIZATION",
                "--allow-entity",
                "DATE_TIME",
            ],
        )
        assert result.exit_code == 0
        data = json.loads(result.output)
        types = {e["type"] for e in data["entities"]}
        assert len(types & {"PERSON", "ORGANIZATION", "DATE_TIME"}) >= 1


@pytest.mark.skipif(not HAS_PRESIDIO, reason="presidio not installed")
class TestNewRecognizersCli:
    def test_private_key_detected_via_cli(self, runner):
        result = runner.invoke(
            cli,
            [
                "redact",
                "text",
                "found key: -----BEGIN RSA PRIVATE KEY----- MIIEowIBAAK...",
                "--json",
            ],
        )
        assert result.exit_code == 0
        data = json.loads(result.output)
        types = [e["type"] for e in data["entities"]]
        assert "PRIVATE_KEY" in types

    def test_internal_hostname_detected_via_cli(self, runner):
        result = runner.invoke(
            cli,
            [
                "redact",
                "text",
                "connect to db.staging.internal",
                "--json",
            ],
        )
        assert result.exit_code == 0
        data = json.loads(result.output)
        types = [e["type"] for e in data["entities"]]
        assert "INTERNAL_HOSTNAME" in types

    def test_private_ip_detected_via_cli(self, runner):
        result = runner.invoke(
            cli,
            [
                "redact",
                "text",
                "bastion at 10.0.1.42",
                "--json",
            ],
        )
        assert result.exit_code == 0
        data = json.loads(result.output)
        types = [e["type"] for e in data["entities"]]
        assert "PRIVATE_IP" in types

    def test_git_credential_url_detected_via_cli(self, runner):
        result = runner.invoke(
            cli,
            [
                "redact",
                "text",
                "clone https://user:secret@github.com/org/repo.git",
                "--json",
            ],
        )
        assert result.exit_code == 0
        data = json.loads(result.output)
        types = [e["type"] for e in data["entities"]]
        assert "GIT_CREDENTIAL_URL" in types

    def test_clean_text_no_entities(self, runner):
        result = runner.invoke(
            cli,
            ["redact", "text", "just normal text with nothing sensitive", "--json"],
        )
        assert result.exit_code == 0
        data = json.loads(result.output)
        sensitive_types = {
            "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GITHUB_TOKEN",
            "AWS_ACCESS_KEY", "PRIVATE_KEY", "GIT_CREDENTIAL_URL",
            "INTERNAL_HOSTNAME", "PRIVATE_IP", "CONNECTION_STRING",
        }
        found_types = {e["type"] for e in data["entities"]}
        assert len(found_types & sensitive_types) == 0

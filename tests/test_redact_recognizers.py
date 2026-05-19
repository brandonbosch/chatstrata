"""Tests for custom Presidio recognizers."""

from __future__ import annotations

from importlib.util import find_spec

import pytest

HAS_PRESIDIO = find_spec("presidio_analyzer") is not None

pytestmark = pytest.mark.skipif(not HAS_PRESIDIO, reason="presidio not installed")


@pytest.fixture(scope="module")
def engine():
    from chatstrata.redact.presidio_engine import PresidioEngine

    return PresidioEngine()


def _find(engine, text: str, expected_type: str):
    """Detect and return entities matching expected_type."""
    entities = engine.detect(text)
    return [e for e in entities if e.type == expected_type]


class TestApiKeyRecognizers:
    def test_anthropic_key(self, engine):
        text = "config: sk-ant-api03-" + "A" * 80
        matches = _find(engine, text, "ANTHROPIC_API_KEY")
        assert len(matches) == 1
        assert matches[0].text.startswith("sk-ant-api03-")
        assert matches[0].confidence >= 0.9

    def test_openai_key(self, engine):
        text = "export OPENAI_API_KEY=sk-" + "a1b2c3d4e5" * 5
        matches = _find(engine, text, "OPENAI_API_KEY")
        assert len(matches) == 1
        assert matches[0].text.startswith("sk-")
        assert matches[0].confidence >= 0.85

    def test_openai_does_not_match_anthropic(self, engine):
        text = "key: sk-ant-api03-" + "X" * 80
        matches = _find(engine, text, "OPENAI_API_KEY")
        assert len(matches) == 0

    def test_github_pat_classic(self, engine):
        text = "token: ghp_" + "A" * 36
        matches = _find(engine, text, "GITHUB_TOKEN")
        assert len(matches) == 1
        assert matches[0].text.startswith("ghp_")

    def test_github_oauth_token(self, engine):
        text = "auth: gho_" + "B" * 36
        matches = _find(engine, text, "GITHUB_TOKEN")
        assert len(matches) == 1
        assert matches[0].text.startswith("gho_")

    def test_github_fine_grained_pat(self, engine):
        text = "github_pat_" + "C" * 30
        matches = _find(engine, text, "GITHUB_TOKEN")
        assert len(matches) == 1
        assert matches[0].text.startswith("github_pat_")

    def test_aws_access_key(self, engine):
        text = "aws_access_key_id = AKIAIOSFODNN7EXAMPLE"
        matches = _find(engine, text, "AWS_ACCESS_KEY")
        assert len(matches) == 1
        assert matches[0].text == "AKIAIOSFODNN7EXAMPLE"

    def test_gcp_service_account(self, engine):
        text = '{"type": "service_account", "project_id": "my-project"}'
        matches = _find(engine, text, "GCP_SERVICE_ACCOUNT")
        assert len(matches) == 1

    def test_stripe_live_key(self, engine):
        text = "STRIPE_KEY=sk_live_" + "a" * 24
        matches = _find(engine, text, "STRIPE_API_KEY")
        assert len(matches) == 1
        assert matches[0].text.startswith("sk_live_")

    def test_stripe_test_key(self, engine):
        text = "sk_test_" + "b" * 24
        matches = _find(engine, text, "STRIPE_API_KEY")
        assert len(matches) == 1
        assert matches[0].text.startswith("sk_test_")

    def test_slack_bot_token(self, engine):
        text = "SLACK_TOKEN=xoxb-1234567890-1234567890-AbCdEfGhIjKlMnOpQrStUvWx"
        matches = _find(engine, text, "SLACK_TOKEN")
        assert len(matches) == 1
        assert matches[0].text.startswith("xoxb-")

    def test_slack_user_token(self, engine):
        text = "xoxp-1234567890-1234567890-1234567890-abcdef1234567890abcdef1234567890"
        matches = _find(engine, text, "SLACK_TOKEN")
        assert len(matches) == 1
        assert matches[0].text.startswith("xoxp-")


class TestPathRecognizers:
    def test_macos_home_path(self, engine):
        text = "editing /Users/brandon/projects/secret/main.py"
        matches = _find(engine, text, "FILE_PATH")
        assert len(matches) == 1
        assert "/Users/brandon" in matches[0].text

    def test_linux_home_path(self, engine):
        text = "file at /home/developer/app/config.yml"
        matches = _find(engine, text, "FILE_PATH")
        assert len(matches) == 1
        assert "/home/developer" in matches[0].text

    def test_windows_home_path(self, engine):
        text = r"path: C:\Users\jdoe\Documents\notes.txt"
        matches = _find(engine, text, "FILE_PATH")
        assert len(matches) == 1
        assert "Users" in matches[0].text

    def test_system_path_not_matched(self, engine):
        text = "binary at /usr/bin/python3"
        matches = _find(engine, text, "FILE_PATH")
        assert len(matches) == 0


class TestConnectionStringRecognizers:
    def test_postgres_url(self, engine):
        text = "DATABASE_URL=postgresql://admin:secret@db.example.com:5432/mydb"
        matches = _find(engine, text, "CONNECTION_STRING")
        assert len(matches) == 1
        assert "postgresql://" in matches[0].text

    def test_mysql_url(self, engine):
        text = "mysql://root:password@localhost:3306/app"
        matches = _find(engine, text, "CONNECTION_STRING")
        assert len(matches) == 1

    def test_mongodb_url(self, engine):
        text = "MONGO_URI=mongodb://user:pass@mongo.example.com/test"
        matches = _find(engine, text, "CONNECTION_STRING")
        assert len(matches) == 1

    def test_mongodb_srv_url(self, engine):
        text = "mongodb+srv://user:pass@cluster.example.net/db"
        matches = _find(engine, text, "CONNECTION_STRING")
        assert len(matches) == 1
        assert "mongodb+srv://" in matches[0].text


class TestTokenRecognizers:
    def test_jwt(self, engine):
        token = (
            "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9."
            "eyJzdWIiOiIxMjM0NTY3ODkwIn0."
            "dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"
        )
        text = f"Authorization: Bearer {token}"
        matches = _find(engine, text, "JWT_TOKEN")
        assert len(matches) == 1
        assert matches[0].text.startswith("eyJ")
        assert matches[0].confidence >= 0.9

    def test_bearer_token(self, engine):
        text = "Authorization: Bearer " + "a" * 40
        matches = _find(engine, text, "BEARER_TOKEN")
        assert len(matches) >= 1
        assert matches[0].text.startswith("Bearer")

    def test_short_string_not_matched_as_bearer(self, engine):
        text = "Bearer abc"
        matches = _find(engine, text, "BEARER_TOKEN")
        assert len(matches) == 0

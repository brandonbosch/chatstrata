"""Tests for recognizer false-positive resistance — things that look similar but shouldn't match."""

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
    entities = engine.detect(text)
    return [e for e in entities if e.type == expected_type]


class TestApiKeyFalsePositives:
    def test_short_sk_prefix_not_openai(self, engine):
        text = "the variable sk_mode = true"
        matches = _find(engine, text, "OPENAI_API_KEY")
        assert len(matches) == 0

    def test_random_ghp_prefix_too_short(self, engine):
        text = "ghp_short"
        matches = _find(engine, text, "GITHUB_TOKEN")
        assert len(matches) == 0

    def test_akia_wrong_length(self, engine):
        text = "AKIA12345"
        matches = _find(engine, text, "AWS_ACCESS_KEY")
        assert len(matches) == 0

    def test_stripe_prefix_too_short(self, engine):
        text = "sk_live_short"
        matches = _find(engine, text, "STRIPE_API_KEY")
        assert len(matches) == 0


class TestConnectionStringFalsePositives:
    def test_word_postgres_not_url(self, engine):
        text = "we use postgres for the database"
        matches = _find(engine, text, "CONNECTION_STRING")
        assert len(matches) == 0

    def test_word_mongodb_not_url(self, engine):
        text = "MongoDB is a document database"
        matches = _find(engine, text, "CONNECTION_STRING")
        assert len(matches) == 0


class TestTokenFalsePositives:
    def test_bearer_with_short_value(self, engine):
        text = "Bearer short"
        matches = _find(engine, text, "BEARER_TOKEN")
        assert len(matches) == 0

    def test_base64_not_jwt(self, engine):
        text = "eyJhbGciOiJIUzI1NiJ9"
        matches = _find(engine, text, "JWT_TOKEN")
        assert len(matches) == 0


class TestPathFalsePositives:
    def test_usr_bin_not_home(self, engine):
        text = "/usr/bin/python3"
        matches = _find(engine, text, "FILE_PATH")
        assert len(matches) == 0

    def test_etc_config_not_home(self, engine):
        text = "/etc/nginx/nginx.conf"
        matches = _find(engine, text, "FILE_PATH")
        assert len(matches) == 0

    def test_var_log_not_home(self, engine):
        text = "/var/log/syslog"
        matches = _find(engine, text, "FILE_PATH")
        assert len(matches) == 0


class TestHostnameFalsePositives:
    def test_public_com_domain(self, engine):
        text = "api.example.com"
        matches = _find(engine, text, "INTERNAL_HOSTNAME")
        assert len(matches) == 0

    def test_public_io_domain(self, engine):
        text = "docs.readthedocs.io"
        matches = _find(engine, text, "INTERNAL_HOSTNAME")
        assert len(matches) == 0

    def test_public_org_domain(self, engine):
        text = "en.wikipedia.org"
        matches = _find(engine, text, "INTERNAL_HOSTNAME")
        assert len(matches) == 0


class TestPrivateKeyFalsePositives:
    def test_begin_public_key(self, engine):
        text = "-----BEGIN PUBLIC KEY-----"
        matches = _find(engine, text, "PRIVATE_KEY")
        assert len(matches) == 0

    def test_begin_certificate(self, engine):
        text = "-----BEGIN CERTIFICATE-----"
        matches = _find(engine, text, "PRIVATE_KEY")
        assert len(matches) == 0

    def test_begin_certificate_request(self, engine):
        text = "-----BEGIN CERTIFICATE REQUEST-----"
        matches = _find(engine, text, "PRIVATE_KEY")
        assert len(matches) == 0


class TestPrivateIpFalsePositives:
    def test_public_ip(self, engine):
        text = "server at 8.8.8.8"
        matches = _find(engine, text, "PRIVATE_IP")
        assert len(matches) == 0

    def test_172_15_is_public(self, engine):
        text = "address 172.15.255.255"
        matches = _find(engine, text, "PRIVATE_IP")
        assert len(matches) == 0


class TestGitRemoteFalsePositives:
    def test_plain_https_no_auth(self, engine):
        text = "https://github.com/user/repo"
        matches = _find(engine, text, "GIT_CREDENTIAL_URL")
        assert len(matches) == 0

    def test_regular_url_not_git(self, engine):
        text = "visit https://example.com/page"
        matches = _find(engine, text, "GIT_CREDENTIAL_URL")
        assert len(matches) == 0

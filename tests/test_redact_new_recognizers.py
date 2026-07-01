"""Tests for new Presidio recognizers: git remotes, hostnames, private keys."""

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


class TestGitRemoteRecognizers:
    def test_https_url_with_credentials(self, engine):
        text = "git clone https://user:s3cret@github.com/org/repo.git"
        matches = _find(engine, text, "GIT_CREDENTIAL_URL")
        assert len(matches) == 1
        assert "user:s3cret@" in matches[0].text

    def test_https_url_with_token_password(self, engine):
        token = "ghp_" + "A" * 36
        text = f"remote: https://x-access-token:{token}@github.com/org/repo.git"
        matches = _find(engine, text, "GIT_CREDENTIAL_URL")
        assert len(matches) == 1
        assert token in matches[0].text

    def test_ssh_git_url(self, engine):
        text = "origin	git@github.com:myorg/myrepo.git (fetch)"
        matches = _find(engine, text, "GIT_CREDENTIAL_URL")
        assert len(matches) == 1
        assert matches[0].text.startswith("git@")

    def test_ssh_git_url_internal(self, engine):
        text = "git@gitlab.internal.corp:team/project.git"
        matches = _find(engine, text, "GIT_CREDENTIAL_URL")
        assert len(matches) == 1

    def test_plain_https_no_creds_not_matched(self, engine):
        text = "git clone https://github.com/public/repo.git"
        matches = _find(engine, text, "GIT_CREDENTIAL_URL")
        assert len(matches) == 0


class TestHostnameRecognizers:
    def test_internal_domain(self, engine):
        text = "connect to db.staging.internal:5432"
        matches = _find(engine, text, "INTERNAL_HOSTNAME")
        assert len(matches) == 1
        assert "db.staging.internal" in matches[0].text

    def test_corp_domain(self, engine):
        text = "api.prod.corp is the production endpoint"
        matches = _find(engine, text, "INTERNAL_HOSTNAME")
        assert len(matches) == 1
        assert "api.prod.corp" in matches[0].text

    def test_local_domain(self, engine):
        text = "check redis.cache.local for session data"
        matches = _find(engine, text, "INTERNAL_HOSTNAME")
        assert len(matches) == 1
        assert "redis.cache.local" in matches[0].text

    def test_lan_domain(self, engine):
        text = "printer.office.lan is not responding"
        matches = _find(engine, text, "INTERNAL_HOSTNAME")
        assert len(matches) == 1

    def test_intranet_domain(self, engine):
        text = "wiki.team.intranet has the docs"
        matches = _find(engine, text, "INTERNAL_HOSTNAME")
        assert len(matches) == 1

    def test_k8s_service_dns(self, engine):
        text = "curl http://my-service.default.svc.cluster.local:8080/health"
        matches = _find(engine, text, "INTERNAL_HOSTNAME")
        assert len(matches) == 1
        assert "svc.cluster.local" in matches[0].text

    def test_k8s_custom_namespace(self, engine):
        text = "grpc://auth-service.auth-ns.svc.cluster.local:9090"
        matches = _find(engine, text, "INTERNAL_HOSTNAME")
        assert len(matches) == 1
        assert "auth-service.auth-ns.svc.cluster.local" in matches[0].text

    def test_public_domain_not_matched(self, engine):
        text = "visit github.com or google.com"
        matches = _find(engine, text, "INTERNAL_HOSTNAME")
        assert len(matches) == 0


class TestPrivateIpRecognizers:
    def test_10_network(self, engine):
        text = "bastion host at 10.0.1.42"
        matches = _find(engine, text, "PRIVATE_IP")
        assert len(matches) == 1
        assert matches[0].text == "10.0.1.42"

    def test_172_16_network(self, engine):
        text = "docker bridge 172.17.0.1"
        matches = _find(engine, text, "PRIVATE_IP")
        assert len(matches) == 1
        assert matches[0].text == "172.17.0.1"

    def test_192_168_network(self, engine):
        text = "gateway is 192.168.1.1"
        matches = _find(engine, text, "PRIVATE_IP")
        assert len(matches) == 1
        assert matches[0].text == "192.168.1.1"

    def test_172_15_not_matched(self, engine):
        text = "address 172.15.0.1 is public"
        matches = _find(engine, text, "PRIVATE_IP")
        assert len(matches) == 0

    def test_172_32_not_matched(self, engine):
        text = "address 172.32.0.1 is public"
        matches = _find(engine, text, "PRIVATE_IP")
        assert len(matches) == 0


class TestPrivateKeyRecognizers:
    def test_rsa_private_key(self, engine):
        text = "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAK..."
        matches = _find(engine, text, "PRIVATE_KEY")
        assert len(matches) == 1
        assert "RSA PRIVATE KEY" in matches[0].text

    def test_ec_private_key(self, engine):
        text = "-----BEGIN EC PRIVATE KEY-----\nMHQCAQEE..."
        matches = _find(engine, text, "PRIVATE_KEY")
        assert len(matches) == 1

    def test_generic_private_key(self, engine):
        text = "-----BEGIN PRIVATE KEY-----\nMIIEvgIBADA..."
        matches = _find(engine, text, "PRIVATE_KEY")
        assert len(matches) == 1

    def test_openssh_private_key(self, engine):
        text = "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNz..."
        matches = _find(engine, text, "PRIVATE_KEY")
        assert len(matches) == 1

    def test_encrypted_private_key(self, engine):
        text = "-----BEGIN ENCRYPTED PRIVATE KEY-----\nMIIFHDBA..."
        matches = _find(engine, text, "PRIVATE_KEY")
        assert len(matches) == 1

    def test_public_key_not_matched(self, engine):
        text = "-----BEGIN PUBLIC KEY-----\nMIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8A..."
        matches = _find(engine, text, "PRIVATE_KEY")
        assert len(matches) == 0

    def test_certificate_not_matched(self, engine):
        text = "-----BEGIN CERTIFICATE-----\nMIIDXTCCAkWgAwIBAgIJAJC1..."
        matches = _find(engine, text, "PRIVATE_KEY")
        assert len(matches) == 0

"""Recognizers for API keys and service credentials."""

from __future__ import annotations

from presidio_analyzer import Pattern, PatternRecognizer


def get_api_key_recognizers() -> list[PatternRecognizer]:
    """Return recognizers for common API key formats."""
    return [
        PatternRecognizer(
            supported_entity="ANTHROPIC_API_KEY",
            name="AnthropicApiKeyRecognizer",
            patterns=[Pattern("anthropic_key", r"sk-ant-api03-[A-Za-z0-9_\-]{20,}", 0.95)],
        ),
        PatternRecognizer(
            supported_entity="OPENAI_API_KEY",
            name="OpenAiApiKeyRecognizer",
            patterns=[Pattern("openai_key", r"sk-(?!ant-)[A-Za-z0-9]{20,}", 0.9)],
        ),
        PatternRecognizer(
            supported_entity="GITHUB_TOKEN",
            name="GitHubTokenRecognizer",
            patterns=[
                Pattern("github_pat_classic", r"ghp_[A-Za-z0-9]{36}", 0.95),
                Pattern("github_oauth", r"gho_[A-Za-z0-9]{36}", 0.95),
                Pattern("github_pat_fine", r"github_pat_[A-Za-z0-9_]{22,}", 0.95),
            ],
        ),
        PatternRecognizer(
            supported_entity="AWS_ACCESS_KEY",
            name="AwsAccessKeyRecognizer",
            patterns=[Pattern("aws_access_key_id", r"AKIA[0-9A-Z]{16}", 0.95)],
        ),
        PatternRecognizer(
            supported_entity="GCP_SERVICE_ACCOUNT",
            name="GcpServiceAccountRecognizer",
            patterns=[
                Pattern("gcp_sa_marker", r'"type"\s*:\s*"service_account"', 0.7),
            ],
        ),
        PatternRecognizer(
            supported_entity="STRIPE_API_KEY",
            name="StripeApiKeyRecognizer",
            patterns=[
                Pattern("stripe_live", r"sk_live_[A-Za-z0-9]{24,}", 0.95),
                Pattern("stripe_test", r"sk_test_[A-Za-z0-9]{24,}", 0.9),
            ],
        ),
        PatternRecognizer(
            supported_entity="SLACK_TOKEN",
            name="SlackTokenRecognizer",
            patterns=[
                Pattern("slack_bot", r"xoxb-[0-9]{10,13}-[0-9]{10,13}-[A-Za-z0-9]{24}", 0.95),
                Pattern(
                    "slack_user",
                    r"xoxp-[0-9]{10,13}-[0-9]{10,13}-[0-9]{10,13}-[a-f0-9]{32}",
                    0.95,
                ),
            ],
        ),
    ]

# Redaction (planned)

The redaction layer is **stubbed in v0** — the protocol is defined but no engine
ships yet. This document captures the plan.

## Why this exists

chatstrata's data is unusually rich. Claude Code transcripts include API keys,
file paths revealing your username, internal hostnames, git remotes, and the
contents of any `.env` file you've ever shown the assistant. Standard PII
libraries don't catch most of this.

When the redaction layer ships, it'll let you:
- `chatstrata redact --interactive` to walk through detected entities with confirm/skip
- `chatstrata export --redact` to dump query results with PII masked
- Reverse redactions via a local mapping file (PII stays on your machine; the redacted output is shareable)

## Engine: Microsoft Presidio

We're targeting Presidio as the default engine. Reasoning in
[ADR 0004 (TODO)](./adr/). Briefly:
- Battle-tested and Microsoft-maintained
- Custom recognizers are first-class
- Reversible redaction via mapping

DataFog, GLiNER, and OpenPipe's pii-redaction are tracked as alternative
engines. The `RedactionEngine` protocol exists so users can swap.

## Custom recognizers we plan to ship

Generic PII libraries handle standard entities (names, emails, phones, SSNs,
credit cards). chatstrata adds:

- **API keys**: Anthropic (`sk-ant-...`), OpenAI (`sk-...`), GitHub (`ghp_...`,
  `gho_...`, etc.), AWS (access key ID + secret), GCP service account JSON,
  Stripe, Slack tokens
- **File paths revealing username**: `/Users/*/`, `/home/*/`, `C:\Users\*\`
- **Internal URLs and hostnames**: `*.internal`, `*.local`, RFC1918 IPs
- **Git remotes** with embedded usernames
- **Database connection strings** (postgres://, mysql://, mongodb://)
- **JWT tokens**
- **SSH keys** (when pasted into a conversation)

## Modes

| mode          | behavior                                          |
|---------------|---------------------------------------------------|
| `detect_only` | report entities, don't modify text                |
| `tag`         | wrap in `<PII:TYPE>...</PII:TYPE>`                |
| `mask`        | replace with `[TYPE_N]`                           |
| `remove`      | delete the entity entirely                        |
| `hash`        | replace with stable hash (preserves uniqueness)   |

## Contributing

The redaction work is broken down in
[GitHub issues](https://github.com/YOUR_USERNAME/chatstrata/issues?q=label%3Aredaction)
(once the repo is published). Good first contributions:

- Implement the Presidio engine
- Add a recognizer for a specific token type
- Build the interactive review CLI flow

# Redaction

The redaction layer is an optional, local-only safety tool for reviewing text or
query results before sharing them. Install it with `chatstrata[redact]`.

Redaction is best-effort. It catches common personal-data and developer-secret
patterns, but it is not a guarantee of anonymization. Review output before
publishing it.

## Why this exists

chatstrata's data is unusually rich. Claude Code transcripts include API keys,
file paths revealing your username, internal hostnames, git remotes, and the
contents of any `.env` file you've ever shown the assistant. Standard PII
libraries don't catch most of this.

The CLI supports:

- `chatstrata redact text` to redact a string.
- `chatstrata redact query` to redact SQL query results.
- `chatstrata redact interactive` to review detected entities with confirm/skip.
- Reversible redactions through the returned local mapping.

## Engine: Microsoft Presidio

Presidio is the default engine:

- Battle-tested and Microsoft-maintained
- Custom recognizers are first-class
- Reversible redaction via mapping

The `RedactionEngine` protocol exists so alternative engines can be added later.

## Custom recognizers

Generic PII libraries handle standard entities (names, emails, phones, SSNs,
credit cards). chatstrata adds:

- **API keys**: Anthropic (`sk-ant-...`), OpenAI (`sk-...`), GitHub (`ghp_...`,
  `gho_...`, etc.), AWS access key IDs, GCP service account markers, Stripe,
  Slack tokens
- **File paths revealing username**: `/Users/*/`, `/home/*/`, `C:\Users\*\`
- **Database connection strings** (postgres://, mysql://, mongodb://)
- **JWT tokens**
- **Bearer tokens**

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
[GitHub issues](https://github.com/brandonbosch/chatstrata/issues?q=label%3Aredaction).
Good first contributions:

- Add a recognizer for a specific token type
- Improve entity confidence scoring
- Add sanitized fixtures for missed sensitive-data patterns

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

## Quick start

```bash
# Install with redact extras
uv pip install "chatstrata[redact]"

# Redact a string
chatstrata redact text "my key is sk-ant-api03-XXXXXXXXXXXX"

# Redact SQL query results
chatstrata redact query "SELECT text FROM content_blocks LIMIT 5"

# Interactively review entities with confirm/skip
chatstrata redact interactive
```

## Engine: Microsoft Presidio

Presidio is the default engine:

- Battle-tested and Microsoft-maintained
- Custom recognizers are first-class
- Reversible redaction via mapping

The `RedactionEngine` protocol exists so alternative engines can be added later.

## What gets detected

### Always detected (custom chatstrata recognizers)

These developer-specific recognizers are always active:

| Entity type | Examples |
|---|---|
| `ANTHROPIC_API_KEY` | `sk-ant-api03-...` |
| `OPENAI_API_KEY` | `sk-...` (excludes Anthropic prefix) |
| `GITHUB_TOKEN` | `ghp_...`, `gho_...`, `github_pat_...` |
| `AWS_ACCESS_KEY` | `AKIA...` |
| `GCP_SERVICE_ACCOUNT` | `"type": "service_account"` |
| `STRIPE_API_KEY` | `sk_live_...`, `sk_test_...` |
| `SLACK_TOKEN` | `xoxb-...`, `xoxp-...` |
| `FILE_PATH` | `/Users/you/...`, `/home/you/...`, `C:\Users\you\...` |
| `CONNECTION_STRING` | `postgresql://...`, `mysql://...`, `mongodb://...` |
| `JWT_TOKEN` | `eyJ...` (three dot-separated base64 segments) |
| `BEARER_TOKEN` | `Bearer <20+ chars>` |
| `GIT_CREDENTIAL_URL` | `https://user:token@host/...`, `git@host:org/repo.git` |
| `INTERNAL_HOSTNAME` | `*.internal`, `*.local`, `*.corp`, `*.lan`, `*.svc.cluster.local` |
| `PRIVATE_IP` | `10.x.x.x`, `172.16-31.x.x`, `192.168.x.x` |
| `PRIVATE_KEY` | `-----BEGIN RSA PRIVATE KEY-----`, etc. |

### Always detected (Presidio built-in)

Presidio's predefined recognizers also detect:

| Entity type | Examples |
|---|---|
| `EMAIL_ADDRESS` | `user@example.com` |
| `PHONE_NUMBER` | `(555) 123-4567` |
| `CREDIT_CARD` | `4111 1111 1111 1111` |
| `US_SSN` | `123-45-6789` |
| `IBAN_CODE` | `DE89 3704 0044 0532 0130 00` |
| `IP_ADDRESS` | Any IP address |
| `URL` | `https://example.com` |

### Denied by default

These entity types are **not** detected by default because they produce too many
false positives in developer transcripts (code discussions mention people, dates,
and companies constantly):

- `PERSON` — names like "John Smith"
- `ORGANIZATION` — company names like "Microsoft"
- `DATE_TIME` — dates and times
- `LOCATION` — place names
- `NRP` — nationalities, religions, political groups

Use `--allow-entity` on the CLI (or pass a custom `deny_entity_types` in Python)
to enable these when you need them.

## Modes

| Mode | Behavior | Reversible? |
|---|---|---|
| `detect_only` | Report entities, don't modify text | N/A |
| `tag` | Wrap in `<PII:TYPE>...</PII:TYPE>` | Yes (text preserved) |
| `mask` | Replace with `[TYPE_N]` (default) | Yes (via mapping) |
| `remove` | Delete the entity text entirely | No |
| `hash` | Replace with stable SHA-256 hash | Yes (via mapping) |

## CLI usage

### Redact text

```bash
# Default mask mode
chatstrata redact text "my email is test@example.com"
# → my email is [EMAIL_ADDRESS_1]

# JSON output with full entity details
chatstrata redact text "my email is test@example.com" --json

# Choose a mode
chatstrata redact text "my email is test@example.com" --mode tag
# → my email is <PII:EMAIL_ADDRESS>test@example.com</PII:EMAIL_ADDRESS>

# Enable normally-denied entity types
chatstrata redact text "Contact John Smith" --allow-entity PERSON
```

### Redact query results

```bash
# Redact all text columns in query output
chatstrata redact query "SELECT text FROM content_blocks LIMIT 5"

# JSON output
chatstrata redact query "SELECT text FROM content_blocks LIMIT 5" --json
```

### Interactive review

```bash
# Walk through detected entities one at a time
chatstrata redact interactive

# Custom SQL and mode
chatstrata redact interactive --sql "SELECT text FROM content_blocks LIMIT 10" --mode tag
```

## Python API

For programmatic use or custom configurations:

```python
from chatstrata.redact import get_engine
from chatstrata.redact.base import RedactionMode

# Default configuration
engine = get_engine()
result = engine.redact("my key is sk-ant-api03-XXXX", RedactionMode.MASK)
print(result.redacted_text)   # "my key is [ANTHROPIC_API_KEY_1]"
print(result.mapping)         # {"[ANTHROPIC_API_KEY_1]": "sk-ant-api03-XXXX"}
```

### Customizing detection

```python
from chatstrata.redact.presidio_engine import PresidioEngine, DEFAULT_DENY_ENTITY_TYPES

# Add more entity types to the deny list
engine = PresidioEngine(
    deny_entity_types=DEFAULT_DENY_ENTITY_TYPES | {"INTERNAL_HOSTNAME", "PRIVATE_IP"},
)

# Enable normally-denied types (e.g., detect person names)
engine = PresidioEngine(
    deny_entity_types=DEFAULT_DENY_ENTITY_TYPES - {"PERSON"},
)

# Detect everything — no deny list
engine = PresidioEngine(deny_entity_types=frozenset())

# Adjust sensitivity (default: 0.35)
engine = PresidioEngine(score_threshold=0.5)  # fewer but higher-confidence matches

# Fixed salt for consistent hashing across runs
engine = PresidioEngine(hash_salt="my-stable-salt")
```

### Detection only

```python
entities = engine.detect("some text with test@example.com")
for e in entities:
    print(f"{e.type}: {e.text} (confidence: {e.confidence:.2f})")
```

## Limitations

- **Best-effort, not guaranteed.** Regex-based recognizers miss novel formats and
  context-dependent secrets. Always review output before publishing.
- **English only.** The NLP engine (spaCy) is configured for English. Entity types
  that rely on NLP (PERSON, ORGANIZATION, LOCATION) won't work well in other
  languages.
- **No deny-entity CLI flag yet.** You can allow denied types via `--allow-entity`,
  but you can't deny additional types from the CLI — use the Python API for that.
- **No persistence.** The interactive reviewer shows redacted output but doesn't
  write changes back to the database.
- **Score threshold is global.** You can't set per-entity-type thresholds; the same
  `score_threshold` applies to all recognizers.
- **Pattern-only for custom recognizers.** The chatstrata-specific recognizers use
  regex patterns, not NLP. They won't catch secrets that don't match known formats.

## Contributing

Good first contributions:

- Add a recognizer for a specific token type
- Improve entity confidence scoring
- Add sanitized fixtures for missed sensitive-data patterns
- Add a `--deny-entity` CLI flag (mirrors `--allow-entity`)

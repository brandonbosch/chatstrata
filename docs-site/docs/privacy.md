---
title: Privacy and Redaction
description: Detecting and removing sensitive data from LLM transcripts.
---

# Privacy and Redaction

LLM transcripts are unusually rich in sensitive data. A single Claude Code session might contain your Anthropic API key, file paths that reveal your OS username, database connection strings from `.env` files, JWT tokens from debugging auth flows, and internal hostnames from `curl` commands you ran. Standard PII libraries handle names, emails, and phone numbers -- but they miss all of this developer-specific data.

chatstrata's redaction layer exists for the moment you want to **share** something from your archive. Your transcript content stays local: standard ingestion, querying, and redaction do not send it to a hosted service. Semantic search uses local embeddings; the first embedding run may download the configured sentence-transformers model if it is not already cached. The redaction engine scans text for sensitive entities and replaces them according to one of five modes, with an optional reversal mapping so you can undo the redaction later.

## The redaction engine

The engine is built on [Microsoft Presidio](https://microsoft.github.io/presidio/), which provides battle-tested NLP-based entity recognition and a first-class system for plugging in custom recognizers.

```python title="chatstrata/redact/base.py"
@runtime_checkable
class RedactionEngine(Protocol):
    """A pluggable redaction engine."""

    name: str

    def detect(self, text: str) -> list[Entity]:
        """Return entities found in text without modifying it."""
        ...

    def redact(self, text: str, mode: RedactionMode = RedactionMode.MASK) -> RedactionResult:
        """Apply redaction. Returns both redacted text and a mapping for reversal."""
        ...
```

The `RedactionEngine` protocol is defined separately from the Presidio implementation, so alternative engines (regex-only, DataFog, fine-tuned local models) can be swapped in by implementing `detect` and `redact`.

The default `PresidioEngine` loads all of Presidio's predefined recognizers **plus** chatstrata's custom ones, resolves overlapping entity spans by preferring longer matches with higher confidence, and applies replacements right-to-left to preserve character offsets.

!!! warning
    Install the redact extras: `uv tool install "chatstrata[redact]"`

## Custom recognizers

Presidio's built-in recognizers handle standard PII (emails, phone numbers, credit cards, SSNs). chatstrata adds seven recognizer modules targeting developer-specific secrets that appear constantly in LLM transcripts:

**API keys** (`chatstrata/redact/recognizers/api_keys.py`):

| Entity type | Pattern prefix | Confidence |
|---|---|---|
| `ANTHROPIC_API_KEY` | `sk-ant-api03-` | 0.95 |
| `OPENAI_API_KEY` | `sk-` (excluding Anthropic) | 0.90 |
| `GITHUB_TOKEN` | `ghp_`, `gho_`, `github_pat_` | 0.95 |
| `AWS_ACCESS_KEY` | `AKIA` | 0.95 |
| `GCP_SERVICE_ACCOUNT` | `"type": "service_account"` | 0.70 |
| `STRIPE_API_KEY` | `sk_live_`, `sk_test_` | 0.90-0.95 |
| `SLACK_TOKEN` | `xoxb-`, `xoxp-` | 0.95 |

**File paths** (`chatstrata/redact/recognizers/paths.py`) -- matches home-directory paths on Unix (`/Users/<name>/...`, `/home/<name>/...`) and Windows (`C:\Users\<name>\...`). System paths like `/usr/bin/python3` are intentionally excluded.

**Tokens** (`chatstrata/redact/recognizers/tokens.py`) -- matches JWT tokens (the `eyJ...` three-segment format) and `Bearer` authorization headers with sufficiently long token values.

**Connection strings** (`chatstrata/redact/recognizers/connection_strings.py`) -- matches `postgresql://`, `mysql://`, `mongodb://`, and `mongodb+srv://` URLs, which typically contain embedded credentials.

**Git remotes** (`chatstrata/redact/recognizers/git_remotes.py`) -- matches credential-bearing HTTPS URLs (`https://user:token@host/...`) and SSH git URLs (`git@host:org/repo.git`) that appear in git remote output and clone commands.

**Hostnames and private IPs** (`chatstrata/redact/recognizers/hostnames.py`):

| Entity type | What it matches | Confidence |
|---|---|---|
| `INTERNAL_HOSTNAME` | `*.internal`, `*.local`, `*.corp`, `*.lan`, `*.intranet`, `*.svc.cluster.local` (Kubernetes) | 0.80-0.85 |
| `PRIVATE_IP` | RFC 1918 addresses: `10.x.x.x`, `172.16-31.x.x`, `192.168.x.x` | 0.75 |

**Private keys** (`chatstrata/redact/recognizers/private_keys.py`) -- matches PEM-encoded private key headers (`-----BEGIN RSA PRIVATE KEY-----`, `-----BEGIN EC PRIVATE KEY-----`, etc.). Does not match public keys or certificates.

### Filtering noisy entity types

By default, `PresidioEngine` suppresses several standard Presidio entity types that produce excessive false positives in code-heavy text:

```python title="chatstrata/redact/presidio_engine.py"
DEFAULT_DENY_ENTITY_TYPES = frozenset({
    "DATE_TIME",
    "ORGANIZATION",
    "PERSON",
    "LOCATION",
    "NRP",
})
```

You can override this with `--allow-entity` on the CLI (e.g., `--allow-entity PERSON`) or by passing a different `deny_entity_types` set when constructing the engine programmatically.

## Redaction modes

The `RedactionMode` enum defines five ways to handle detected entities:

| Mode | What it does | Example output |
|---|---|---|
| `detect_only` | Reports entities without modifying text | `my email is test@example.com` |
| `tag` | Wraps entities in XML-style PII tags | `my email is <PII:EMAIL_ADDRESS>test@example.com</PII:EMAIL_ADDRESS>` |
| `mask` | Replaces with `[TYPE_N]` placeholders | `my email is [EMAIL_ADDRESS_1]` |
| `remove` | Deletes entity text entirely | `my email is ` |
| `hash` | Replaces with a SHA-256 hash (salted) | `my email is a1b2c3d4...` |

The `mask` and `hash` modes are reversible via the mapping dictionary (see below). The `tag` mode preserves the original text inline. The `remove` mode is destructive -- there is no mapping to reverse it.

## CLI usage

The `chatstrata redact` command group has three subcommands:

**`redact text`** -- redact a single string:

```bash
# Mask mode (default)
chatstrata redact text "My API key is sk-ant-api03-XXXXXXXXXXXXXXXXXXXX"
# Output: My API key is [ANTHROPIC_API_KEY_1]

# Tag mode
chatstrata redact text "DB: postgresql://admin:secret@db.internal:5432/prod" --mode tag

# JSON output with full entity details and reversal mapping
chatstrata redact text "my email is test@example.com" --json
```

**`redact query`** -- run a SQL query against your archive and redact all string columns in the results:

```bash
chatstrata redact query "SELECT text FROM content_blocks LIMIT 5"
chatstrata redact query "SELECT text FROM content_blocks LIMIT 5" --mode tag --json
chatstrata redact query "SELECT text FROM content_blocks LIMIT 5" --db ~/custom.duckdb
```

**`redact interactive`** -- walk through detected entities one at a time with confirm/skip decisions:

```bash
chatstrata redact interactive
chatstrata redact interactive --sql "SELECT text FROM content_blocks WHERE text IS NOT NULL LIMIT 50"
```

The interactive flow highlights each entity in context and offers five choices: `r` (redact this one), `s` (skip this one), `R` (redact all entities of this type), `S` (skip all of this type), or `q` (quit). This lets you quickly triage which detections are true positives.

## Reversible redaction

The `RedactionResult` returned by the engine includes a `mapping` dictionary that maps each placeholder back to the original text:

```python title="chatstrata/redact/base.py"
@dataclass
class RedactionResult:
    original_text: str
    redacted_text: str
    entities: list[Entity] = field(default_factory=list)
    mapping: dict[str, str] = field(default_factory=dict)  # placeholder -> original
```

For `mask` mode, the mapping looks like `{"[EMAIL_ADDRESS_1]": "test@example.com"}`. For `hash` mode, it maps the SHA-256 digest back to the original value. The `--json` flag on CLI commands includes this mapping in the output, so you can store it locally and reverse the redaction later.

The hash mode uses a random salt (generated per engine instance) to prevent rainbow-table attacks against the hashes. You can also supply a fixed salt for deterministic output across runs.

## Customizing detection from Python

The CLI exposes `--allow-entity` to un-deny specific types, but for full control use the Python API:

```python
from chatstrata.redact.presidio_engine import PresidioEngine, DEFAULT_DENY_ENTITY_TYPES

# Add types to the deny list (e.g., skip internal hostnames and private IPs)
engine = PresidioEngine(
    deny_entity_types=DEFAULT_DENY_ENTITY_TYPES | {"INTERNAL_HOSTNAME", "PRIVATE_IP"},
)

# Enable a normally-denied type (e.g., detect person names)
engine = PresidioEngine(
    deny_entity_types=DEFAULT_DENY_ENTITY_TYPES - {"PERSON"},
)

# Detect everything -- no deny list at all
engine = PresidioEngine(deny_entity_types=frozenset())

# Higher threshold = fewer but higher-confidence matches (default: 0.35)
engine = PresidioEngine(score_threshold=0.5)

# Fixed salt for reproducible hash-mode output across runs
engine = PresidioEngine(hash_salt="my-stable-salt")
```

## Limitations

- **Best-effort, not guaranteed.** Regex-based recognizers miss novel formats and context-dependent secrets. Always review output before publishing.
- **English only.** The NLP engine (spaCy) is configured for English. Entity types that rely on NLP (`PERSON`, `ORGANIZATION`, `LOCATION`) won't work well in other languages.
- **No `--deny-entity` CLI flag.** You can allow denied types via `--allow-entity`, but you can't deny additional types from the CLI -- use the Python API for that.
- **No persistence.** The interactive reviewer shows redacted output but doesn't write changes back to the database.
- **Score threshold is global.** You can't set per-entity-type thresholds; the same `score_threshold` applies to all recognizers.
- **Pattern-only for custom recognizers.** The chatstrata-specific recognizers use regex patterns, not NLP. They won't catch secrets that don't match known formats.

## Key entry points

| File | Purpose |
|---|---|
| `chatstrata/redact/base.py` | `RedactionEngine` protocol, `RedactionMode` enum, `Entity` and `RedactionResult` types |
| `chatstrata/redact/presidio_engine.py` | `PresidioEngine` -- the default engine implementation |
| `chatstrata/redact/recognizers/` | Seven custom recognizer modules (API keys, paths, tokens, connection strings, git remotes, hostnames, private keys) |
| `chatstrata/redact/cli.py` | CLI commands: `redact text`, `redact query`, `redact interactive` |

## Related

- [Schema](schema.md) -- the DuckDB tables that redaction queries run against
- [CLI reference](cli.md) -- full option listing for all commands

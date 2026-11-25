# PII sanitization

chatstrata stores conversations as collected. It does not automatically redact
PII, credentials, or other sensitive text. Local archives and observation logs
are plaintext; optional relay sync encrypts segments before upload.

The design is [ADR 0008](adr/0008-local-redaction-boundary.md). The secrets
engine, deterministic PII recognizers, user-defined rules, and the labeling
egress gate are implemented in `internal/redact`. The archive redaction overlay
and destructive purge are not yet.

## Egress gate

Before transcript text is sent to a model provider, `redact.Gate` checks the
project allowlist/denylist and runs every string field through the engine.
Output uses fixed typed placeholders:

```
[REDACTED:OPENAI_PROJECT_KEY]
[REDACTED:PASSWORD_ASSIGNMENT]
[REDACTED:EMAIL]
[REDACTED:USERNAME]
[REDACTED:HOSTNAME]
[REDACTED:IP]
```

Findings carry rule IDs, categories and byte spans, never matched bytes.
Detection is local and deterministic: no network, no cgo, no model downloads.
An engine error, an oversized field, or an invalid policy fails closed; callers
must not send the original text.

`chatstrata redact text` exercises the same gate:

```sh
chatstrata redact text 'mail alice@example.com from /home/alice/work'
chatstrata redact text --json --config ~/.config/chatstrata/redaction.toml ...
chatstrata redact rules --config ...
```

## Built-in PII recognizers

- email addresses;
- IPv4 and IPv6 addresses (including `::ffff:` mapped addresses), skipping
  loopback and unspecified addresses;
- home paths (`/home/<user>`, `/Users/<user>`, `C:\Users\<user>`), replacing
  only the user segment so the path shape survives;
- the current OS username, configured usernames, and configured private
  hostnames;
- private hostnames ending in `.local`, `.internal`, `.lan`, `.home`, or
  `.corp`.

Names, street addresses, medical data, and free-form locations are not
inferred. Use a dictionary or a user rule for those.

## Policy file

`LoadPolicy` reads `$CHATSTRATA_REDACTION_CONFIG`, then
`<XDG_CONFIG_HOME>/chatstrata/redaction.toml` (falling back to
`~/.config`). A missing default file means the built-in policy; an explicit
path that does not exist is an error. Unknown keys are rejected so a typo
cannot silently weaken the policy.

```toml
[redaction]
profile = "balanced"          # fast, balanced, deep
max_input_bytes = 1048576
usernames = ["alice"]         # in addition to the current OS username
private_hosts = ["buildbox"]  # in addition to the hostname suffixes above
disable_local_username = false

[[redaction.rules]]
id = "acme-token"
pattern = '''ACME_KEY=([A-Za-z0-9]{20,})'''
secret_group = 1
category = "ACME_KEY"
priority = 90
entropy = 2.0
allowlist = ["EXAMPLE"]
projects = ["acme"]
paths = ["/home/alice/work"]

[[redaction.dictionaries]]
id = "people"
terms = ["Alice Example", "Acme Corp"]
category = "PERSON"
case_sensitive = false
```

Rule fields:

| Field | Meaning |
|---|---|
| `id` | Stable rule ID, unique across built-in and user rules. |
| `pattern` | Go RE2 pattern. No lookaround. |
| `secret_group` | Capture group to replace; `0` uses the whole match. |
| `category` | Placeholder name; defaults to the uppercase rule ID. |
| `priority` | Overlap precedence, 1–100; default 75. Containing spans still win. |
| `entropy` | Minimum Shannon entropy of the captured value; `0` disables. |
| `allowlist` | Case-insensitive literal substrings that suppress a match. |
| `projects` | Exact project names where the rule applies. |
| `paths` | Path prefixes where the rule applies. |

Dictionaries match literal terms on word boundaries. `case_sensitive` defaults
to false for ASCII terms; non-ASCII terms are matched exactly. A rule or
dictionary with `projects` or `paths` runs only when both supplied fields match
the `redact.Scope`. `Engine.Redact` has no scope and therefore skips scoped
rules; `Engine.RedactScope`, `Gate.RedactScope`, and the CLI's `--project` /
`--path` flags supply one.

`redaction_version` hashes the profile, built-in rule set, PII version, user
rules, dictionaries, and the resolved username/hostname lists. Changing a
policy changes the version, so labels produced from differently sanitized text
are distinguishable.

## Archive redaction

```sh
chatstrata redact scan   # audit only; prints rule counts
chatstrata redact apply  # write a run and redact the projection
chatstrata redact purge --hard --yes  # rewrite local observation segments
```

`redact apply` scans `content_blocks.text`, `content_blocks.payload`,
`raw_events.payload`, and `conversations.title`; it writes an immutable run
under `chatstrata-go.redactions/runs/` and applies it to DuckDB. Records store
the target, a hash of the input text, and payload-free spans. The overlay is
replayed after every re-ingest and `rebuild`, and `redaction_runs` /
`redactions` mirror it for SQL audit.

Runs chain: a later run's input hash may be the previous run's output, so a
policy change can add spans to already-redacted text. A record whose input
hash no longer matches (the source changed) is left in the sidecar and
skipped; the next `redact apply` scans the current text and writes a fresh
run. `rebuild` restores the projection from the log and replays the overlay.

`redact purge --hard` rewrites this device's segments, marks changed
observations `redacted`, and rebuilds the projection. Observation append
offsets are byte positions, so purge markers preserve the original byte length:
a typed placeholder is padded with spaces, and a value shorter than its
placeholder becomes `*` repeated to its length. The projection therefore keeps
the same path and append structure after a rebuild. Observation path and
project hints are redacted too. After rebuilding, purge runs a fresh `redact
apply` over the projection to catch values that only appear after parsing.

Purge refuses a log holding another device's segments, and the CLI refuses a
paired archive (`space.key`): a local rewrite cannot recall relay ciphertext or
other devices. Use a new space and re-collection for a synced archive, or
remove the pairing files first if this archive is no longer synced. The old log is kept as `<log>.purged-<timestamp>` by default; it still
contains the original plaintext, so use `--delete-old` or remove it yourself
after verifying the result. Future collection of a purged conversation
re-applies the policy to the source bytes before logging them, so the original
plaintext does not return.

New conversations are still collected as-is: run `redact apply` or
`redact purge` again after collection. Collection-time redaction for every
source is a later option.

Neither operation can recall copies from other devices, the relay, backups,
or the original source files. Redaction is not secure deletion on modern
storage.

Credential remediation is rotation, not redaction. A key or password found in
history must be considered disclosed.

Historical Python Presidio code is preserved at the `python-final` Git tag;
the Go path does not depend on a Python runtime or model downloads.

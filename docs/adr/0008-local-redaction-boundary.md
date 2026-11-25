# ADR 0008: Local redaction boundary and archive redaction semantics

**Status:** Accepted
**Date:** 2026-11-25
**Extends:** [0005](0005-sync-device-logs.md), [0006](0006-go-only-cutover.md), [0007](0007-go-classifier-labels.md)

## Context

ADR 0007 gates live labeling on a local scrubber: no transcript excerpt may
leave the machine until secrets and PII are removed. The same archive also
contains data the user may want gone from query results, MCP output, and
exports. Those are two different problems:

1. **Egress.** Labeling sends rendered state to TypeSafe. The scrubber must be
   deterministic, local-only, fail-closed, and must not be reversible from what
   is sent.
2. **Removal.** Users want API keys, passwords, or personal text hidden from
   the searchable archive. Because observations are immutable and synced
   (ADR 0005), hiding a value in DuckDB is not the same as destroying its
   plaintext in the observation log or on another device.

The MVP is credentials: provider API keys, tokens, private keys, and password
assignments. Personalized removal and broader PII are required, but should not
force an ML/NER stack into the default path.

The retired Python implementation used Presidio plus spaCy. ADR 0006 makes the
application Go-only; a Python sidecar would reintroduce a second runtime, a
process boundary, model downloads, and non-deterministic false positives on
code. It is not the default.

## Decision

### 1. One local engine, two callers

Add `internal/redact`, a pure-Go, deterministic, offline engine:

```
Policy   -> compiled rules, detectors, mode, version
Detect   -> []Span {Start, End, RuleID, Category, Priority}
Redact   -> Result {Text, Spans, Counts, Version}
```

- No network, no cgo, no model download, no shell. Configuration is local.
- `Policy.Version` is a hash of the active rule-set version, user rules,
  detector configuration, and replacement mode. It is recorded as
  `redaction_version` wherever the redacted text is used (ADR 0007).
- Reports contain rule IDs, categories, byte spans, and counts. They never
  contain matched bytes, and errors never include input text.
- Fail-closed: an invalid rule, an engine error, a panic, or an input over the
  configured size cap aborts the operation. Callers must not send the original
  text on error.

The engine is used by the labeling egress gate and by explicit archive
commands. Other packages never call the third-party detector directly.

### 2. Secrets: `goredact`, pinned and wrapped

Use `github.com/baum-ai/goredact` (MIT, pure Go, no cgo, core is stdlib-only)
for secret detection.

- Run its compiled rule set over the text with `OnFinding` to collect spans and
  rule IDs; ignore its fixed `[REDACTED]` output and apply this package's typed
  placeholders in one pass with the PII and user-rule spans.
- Default to the `balanced` profile. `deep` is available for audit scans and
  for users who prefer over-redaction. Keep decode depth at zero so byte
  offsets refer to the original text.
- `goredact.RuleSetVersion()` is part of `Policy.Version`. Upgrading the
  dependency changes the version, so stale labels are visible rather than
  silently compared with differently sanitized text.
- Pin an exact version. Golden tests cover every built-in rule used by the
  shipped profile with synthetic fixtures, plus negative fixtures for
  placeholders, environment references, documentation examples, and code
  identifiers.
- Wrap the dependency in one file. If it stops being maintained, replace it
  behind the same interface with in-house rules or a different detector; no
  caller changes. Do not use gitleaks (feature-frozen) or Betterleaks (scanner,
  not a redactor; findings carry matched bytes) in the runtime path.
- goredact rejects weak or passphrase-shaped password values as prose. A
  supplemental assignment rule redacts quoted or token values after
  `password`/`passwd`/`pwd` (including `PGPASSWORD` and `DB_PASSWORD`) while
  leaving environment references, function calls, and obvious placeholders
  alone. This covers the MVP's password case without treating every sentence
  after the word "password" as a secret.

### 3. PII and custom rules: in-repo deterministic recognizers

Broad ML NER is not on by default. The ADR 0007 list is mostly deterministic:

- email addresses;
- IPv4 and IPv6 addresses, validated with `net/netip`;
- home paths (`/home/<user>`, `/Users/<user>`, `C:\Users\<user>`), replacing
  only the user segment and preserving the path shape;
- the local username from `os/user` and an explicit config list, matched on
  word boundaries;
- private hostnames: `.local`, `.internal`, `.lan`, `.home`, `.corp`, plus a
  configured list.

Person names, street addresses, medical data, and free-form locations are
non-goals for the default policy. Users who need them can add dictionaries or
rules, or run an optional local model later.

User extension is declarative TOML:

```toml
[[redaction.rules]]
id = "internal-api-key"
pattern = '''(?i)\bACME_(?:PROD|STAGING)_KEY\s*=\s*["']?([A-Za-z0-9_-]{20,})'''
secret_group = 1
category = "ACME_KEY"
priority = 50
entropy = 3.0
```

- `pattern` is Go RE2; no lookaround. `secret_group` selects the bytes to
  replace. Empty or unbounded matches are rejected.
- Optional `entropy`, `allowlist`, `projects`, `paths`, and `priority`.
- Dictionaries are word-boundary matched and can be case-insensitive.
- Patterns, rule counts, match lengths, and input sizes are capped at load or
  run time. An invalid policy refuses to run.
- Optional escape hatch, disabled by default: a local executable receives text
  on stdin and returns redacted text on stdout, with no shell, a timeout, a
  size cap, and fail-closed handling. It cannot report spans; the audit record
  names `external` and its version. This is the path for arbitrary user logic
  (including a Presidio wrapper) and is documented as trusted local code.

An external command is not part of the MVP. Declarative rules and dictionaries
cover the common cases without giving the engine a process to spawn.

### 4. Placeholders

Replace each span with a fixed, typed marker:

```
[REDACTED:GITHUB_PAT]
[REDACTED:OPENAI_PROJECT_KEY]
[REDACTED:PASSWORD]
[REDACTED:EMAIL]
[REDACTED:IP]
[REDACTED:HOME_PATH]
```

Do not preserve length or format on the egress path; length and token shape
are information about the secret. Placeholders are applied in one pass and are
not re-detected.

### 5. Egress gate for labeling

`chatstrata label`:

1. applies the project allowlist/denylist before rendering any state;
2. renders each state value and runs `redact.Redact` over every string;
3. aborts the run on any redaction error or size cap;
4. prints a dry-run with target count, estimated tokens/cost, redacted sample,
   and `redaction_version`;
5. sends only the redacted state and records the version in the run file and
   `label_runs`.

The audit record is payload-free: rule IDs, counts, and version. No matched
bytes, offsets, or reversible mapping are sent or stored with labels.

### 6. Archive redaction: hide, then purge

Two explicit operations, with different guarantees:

- **`chatstrata redact scan`** audits the projection and reports counts by
  rule, project, and conversation. It never prints matched values.
- **`chatstrata redact apply`** writes append-only redaction records to
  `chatstrata-go.redactions/`, alongside the observation and label logs, and
  projects them into DuckDB. `rebuild` replays them after ingest. Queries and
  MCP see redacted text. Each record carries `target_id`, `content_hash`,
  `policy_version`, rule ID, spans, and replacement, but never the matched
  bytes. A record whose content hash no longer matches is stale and is not
  applied; a re-scan produces a new record. `apply` uses the configured policy,
  normally the same `balanced` profile as egress; `scan --profile deep` is an
  audit-only view and does not define what gets applied.
- **`chatstrata redact purge --hard`** is the only operation that removes
  plaintext from the local observation log. It rewrites this device's segments
  into a new log, archives the old directory, and requires `--yes`. Purge
  markers preserve the original byte length because observation append offsets
  are byte positions; a typed marker is space-padded and a value shorter than
  its marker becomes `*` repeated to its length. It cannot
  recall copies on other devices, relay ciphertext, backups, or the original
  source files. For synced archives, the safe procedure is a new space key,
  re-collection with the policy enabled, and re-pairing devices; the old
  ciphertext remains until the old key is discarded.

`redact apply` is the MVP: it makes the archive non-queryable without breaking
the immutability or sync contract. It is not secure deletion. API keys found in
history must be rotated; redaction does not un-leak them.

Collection-time redaction (`collect --redact`) is a later option. It prevents
new plaintext from entering the log, but permanently discards the original and
requires every device to run the same policy version.

### 7. Non-goals

- Guaranteed detection; no detector proves absence.
- Default ML/NER for names, addresses, or medical data.
- Revoking credentials.
- Editing source files owned by Claude Code, Codex, or other tools.
- Cross-device or relay-side deletion.

## Alternatives considered

| Option | Verdict | Reason |
|---|---|---|
| **goredact** | Chosen for secrets | Purpose-built redaction filter, MIT, pure Go, stdlib-only core, 67 rules, custom rules, bounded memory, findings never carry matched bytes. Young (0.x) and low-adoption; mitigated by pinning, wrapping, and golden tests. |
| **Betterleaks** | Audit-only | Maintained successor to gitleaks, MIT, strong rules, but it is a scanner. Findings carry `Match.Value`; no streaming redaction API; ~163 modules in the module graph. |
| **gitleaks** | Reject | Feature-frozen; scanner/report shaped; heavy dependency graph. |
| **Presidio** | Reject default | Python runtime and model downloads contradict ADR 0006; NER false positives on code. Still available through the optional external-command hook. |
| **ferret-scan** | Reject default | Apache-2.0 and offline, but built for files: PDF/EXIF/Office dependencies, findings expose matched text with no byte offsets, and redaction locates values by text search. Its text scan missed SSNs, home paths, and a provider key in a sample. |
| **Alcatraz** | Possible PII swap | MIT, stdlib-only, 52 entity types, byte offsets, custom recognizers. Experimental 0.x and PII-only. Keep behind the same recognizer interface if in-house PII proves insufficient. |
| **go-phileas** | Reject | Archived; no secrets coverage. |
| **deidentify** | Reject | Deterministic pseudonyms, not a secrets/PII removal policy; no audit spans. |
| **packyme/privacy-filter** | Reject | Good shape, but `module privacyfilter` is not importable as a library; it is a service/replace-directive project. |

## Consequences

- One new direct dependency, MIT and pure Go; the Go directive moves to
  `1.26.6`. Upgrading it is a reviewed change because it changes detection and
  therefore `redaction_version`.
- Default PII coverage is deterministic and explainable, not exhaustive. Users
  can add rules and dictionaries; broader NER remains optional and local.
- The egress gate is safe against engine failure, not against a rule that
  misses a secret. Documentation must state that limits, and dry-run output
  must show what will be sent.
- `redact apply` hides content from the query surface while leaving plaintext
  in the observation log and source files. `redact purge` is destructive,
  local-only, and requires re-sync planning.
- Labels are tied to a redaction version. A policy change makes prior labels
  visibly stale and can trigger re-labeling, which costs money.

## Implementation status (2026-11-25)

The secrets engine, deterministic PII recognizers, TOML user rules and
dictionaries, and the egress gate are implemented in `internal/redact`.
`chatstrata redact text` and `chatstrata redact rules` exercise the gate.
The append-only archive overlay (`redact scan` / `redact apply`) and the local
hard purge (`redact purge --hard`) are implemented in `internal/redactions`.
The optional external command hook and the labeling client that consumes the
gate remain.

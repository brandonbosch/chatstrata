# Rolling out redaction across a paired fleet

How to turn on redaction for every device in a sync space without leaving a
plaintext path back into the archive. The design is
[ADR 0008](../adr/0008-local-redaction-boundary.md) and
[ADR 0009](../adr/0009-collection-time-redaction.md).

**Requires chatstrata v2.1.1 or newer on every device.** A v2.1.0 or older
binary rejects the `collect` policy key, so the policy and the upgrade must
land together.

## What each layer does

| Layer | Effect | Reversible? |
|---|---|---|
| `redact apply` overlay | Hides scanned values from `query`, MCP and search | Remove the run + rebuild |
| `collect = true` | New observations are redacted before the log append | No local plaintext to restore |
| `redact purge --hard` | Rewrites this device's log segments | Only from a backup |
| New space + re-collection | Removes historical plaintext from the source of truth | Old space/backups remain until destroyed |

`collect` is per device. Observations merge verbatim over sync, so one device
still collecting plaintext puts secrets back into every other device's
projection. The fleet is redacted only when every device is upgraded and
configured.

## 1. Record the starting state

On each device:

```sh
chatstrata version
chatstrata devices
chatstrata doctor
chatstrata redact rules      # note redaction_version
```

`redaction_version` identifies the detection policy, not where it is applied.
It must match on every device once the rollout is complete.

## 2. Upgrade and configure one device

```sh
# pinned release
curl -fsSL https://chatstrata.sprawlcommons.com/install.sh | CHATSTRATA_VERSION=v2.1.1 sh
chatstrata version

mkdir -p ~/.config/chatstrata
cat > ~/.config/chatstrata/redaction.toml <<'EOF'
[redaction]
profile = "balanced"
max_input_bytes = 67108864
collect = true
EOF

chatstrata schedule install   # points the unit at this binary and restarts it
```

`max_input_bytes` must cover the largest single decoded string; source records
that embed whole transcripts need more than the 1 MiB default. The cap applies
per JSON string value, not per file.

If the daemon does not come back, check that the policy file has no unknown
keys — an older binary fails policy loading rather than collecting plaintext.

## 3. Verify the device

After the next collection cycle:

```sh
journalctl --user -u chatstrata-daemon -n 20 --no-pager
chatstrata query "select source_id, kind, redacted, observed_at
                  from observations order by observed_at desc limit 5"
```

The newest local observations must have `redacted = true`. To prove the bytes
rather than the flag, rebuild into a scratch archive and inspect a sampled
record; the log segment itself contains markers, not plaintext:

```sh
chatstrata rebuild --db /tmp/redacted-check.duckdb
chatstrata query --db /tmp/redacted-check.duckdb \
  "select count(*) from content_blocks where text like '%[REDACTED:%'"
```

MCP clients that still launch the retired Python `chatstrata-mcp` see the
legacy archive. Point them at the Go server instead:

```sh
chatstrata serve
```

## 4. Complete the fleet

Repeat steps 2–3 on every device. Order does not matter for correctness, but
finish the rollout before treating the space as redacted. Until then,
unupgraded devices continue to upload plaintext segments.

Fleet checks:

- `chatstrata redact rules` reports the same `redaction_version` everywhere.
- `chatstrata devices` shows every device syncing.
- `chatstrata doctor` reports no stalled collection.
- New rows in `observations` for each device carry `redacted = true`.
- Queries for known secrets return placeholders, not plaintext.

If a device cannot be upgraded, remove it from the space or accept that its
segments are plaintext. A device left in the space defeats collection-time
redaction for everything it collects.

## 5. Decide about historical plaintext

Two honest options:

**A. Accept overlay-only history.** Existing observations stay plaintext in
the local logs, relay ciphertext and backups. Query/MCP output is hidden by the
overlay for everything scanned. `redact purge` cannot run while the archive is
paired, because it cannot recall the relay or other devices.

**B. Rebuild into a new space.** The only way to make the source of truth
contain no historical plaintext. Conversation and message IDs depend on source
identity and position, not on the space, so saved SQL and agent habits keep
working.

1. Stop the daemon on every device and confirm no `chatstrata` process holds
   the database.
2. On one device, create a fresh archive (new `--db` path) and collect with the
   `collect = true` policy:
   `chatstrata ingest --auto --db /path/chatstrata-go-new.duckdb`.
3. Audit the result: `chatstrata redact scan --db …` should find nothing the
   policy still needs to add, and `observations.redacted` should cover every
   new observation.
4. Pair the new archive (`chatstrata pair --relay … --db …`), then join each
   other device to the new space (`chatstrata join CODE --db …`) and reinstall
   its schedule with the new `--db`.
5. Destroy the old space key and the relay's old `spaces/<id>` directory, and
   delete old archive backups deliberately. Copies already synced to other
   devices and backups remain readable until they are destroyed.

This is a one-way migration for the old archive. Keep the old data offline
until the new space is verified.

## Rollback

There is no in-place un-redaction. Setting `collect = false` stops future
collection redaction; observations already rewritten stay redacted, and the
overlay stays in place. Removing the overlay sidecar and rebuilding would
re-expose the original plaintext of scanned content on devices that still have
it — do not do that unless that is the intent. A full revert means restoring a
pre-change archive and log backup, losing everything collected since.

## Failure modes

| Symptom | Cause | Fix |
|---|---|---|
| Daemon exits with `unknown key "collect"` | Binary older than v2.1.1 | Upgrade the binary first |
| `redaction_version` differs across devices | Different profile, rules, usernames or hosts | Copy one policy file to every device |
| Plaintext reappears in query/MCP | A device in the space still collects plaintext, or new rows arrived before its upgrade | Upgrade/configure it; re-run `redact apply` for the gap |
| `redact purge` refuses | Archive is paired (`space.key`) | Use the new-space path above |
| MCP shows a different archive | Client still runs the Python server | Point it at `chatstrata serve` |

Redaction does not recall copies on other devices, the relay, backups or the
original source files, and it does not un-leak a credential. Rotate every
secret the scan found.

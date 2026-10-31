# Adding a source adapter

Implement `model.Source` in `internal/sources/<source>/` and register it in
`internal/cli/cli.go`. Shared decoding helpers live in `internal/sources/srcutil`.

- `Name`, `DisplayName`, and `Version` identify the source and parser version.
- `Discover(root)` returns conversation handles from the supplied path or the
  tool's default location. Wrap `model.ErrNotFound` when a default is absent.
- `Parse(handle, content)` parses the bytes supplied by the observation log;
  never reread `handle.Path`, which may not exist on the projecting device.
- `AppendOnly` tells collection whether it can record just newly appended bytes.

For a shared SQLite store or export containing multiple conversations, set
`Handle.Content` to each conversation's serialized bytes during discovery.
Discover must open source databases read-only and close them before returning.
One-file-per-conversation sources normally leave Content nil.

Return a `model.Conversation` with ordered messages, typed content blocks,
metadata, and raw events. Preserve unknown structured content in payloads.
Native message identifiers are preferred; fallback identifiers must include
position, never content alone, so repeated messages remain distinct. Keep
parent references and absent versus empty values intact.

Add synthetic/sanitized input under `spec/golden/inputs/`, a case in
`spec/golden/cases.json`, and generate its expected projection with:

```sh
go test ./internal/golden -run TestGolden/YOUR_CASE -update
```

Review the generated JSON. Run `go test ./...`; update legacy-import coverage
in `internal/golden/legacy_test.go` when the source existed in Python.
For format drift diagnostics, implement optional `model.Checker`: return
problems for unreadable layouts and notes for unknown record kinds.

Never commit real transcripts or require network access to parse local data.

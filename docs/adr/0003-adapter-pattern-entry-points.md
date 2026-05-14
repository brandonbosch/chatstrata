# ADR 0003: Adapter pattern via Python entry points

**Status:** Accepted
**Date:** 2026-05

## Context

chatstrata needs to support many sources (Claude Code, claude.ai exports,
ChatGPT exports, Hermes, Cursor, ...) and we want third parties to be able
to add new sources without us reviewing every PR.

## Decision

1. Define a small `SourceAdapter` protocol in `chatstrata.sources.base`.
2. Use Python entry points (`chatstrata.sources` group) for registration.
3. Provide both in-repo adapters and the option for standalone pip packages
   to register their own adapters.

## Why entry points

- Standard library, no extra runtime deps.
- Lets third-party packages publish (e.g.) `chatstrata-cursor` as their own
  pip package and have chatstrata pick it up automatically.
- Reduces governance burden — we don't have to merge every adapter.

## Consequences

- The protocol is the contract. Breaking changes to the protocol require an
  ADR and a major version bump.
- We need a `chatstrata sources` command so users can see what's installed.
- Adapter authors take ownership of their parser; we validate their
  fixture-driven tests but don't audit parsing logic.
- For trust reasons, we'll likely maintain a curated list of recommended
  adapters in the README so users don't accidentally install a malicious
  one. This is policy, not technical enforcement.

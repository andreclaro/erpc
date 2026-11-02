# Response-mark cache admission — Implementation Plan

**Status**: Spec landed; implementation pending
**Last revised**: 2026-11-02
**Base**: `main`
**Spec**: [feature.md](./feature.md) — single source of truth for behavior

Companion to [feature.md](./feature.md). Delivery sequence only.

---

## TLDR

Ship cache `cachingStrategy` (default **`defaultAllow`**) → policy
`cacheableResponses` → mark in `Consensus.Run` → cache Set respects
strategy × mark → Forward uses cache under selector → docs.

---

## Locked decisions

| Topic | Where |
|-------|--------|
| Mark on response; cache ignores consensus types | feature §1, §5 |
| `cachingStrategy`; omit → `defaultAllow` | feature §2.1 |
| `cacheableResponses *bool`; omit default/`true`, named/`false` | feature §2.2 |
| Set in `Consensus.Run` after round | feature §3, §5 |
| Set = strategy × mark; Get available under selector | feature §3 |
| Consensus-only v1 | feature Non-goals / §6 |
| Circle: `defaultAllow` + `fallback` → `cacheableResponses: false` | feature §2 |

---

## Phase 0 — Spec

- [x] Write `specs/cacheable-responses/feature.md`
- [x] Write this plan
- [ ] Review with Aram / maintainers

**Acceptance**: Spec accepted as implementation contract.

---

## Phase 1 — Config + response mark

- Add `CachingStrategy` on `CacheConfig` + SetDefaults → `defaultAllow`
- Add `CacheableResponses *bool` on `ConsensusPolicyConfig` + SetDefaults
- Add getter/setter on `NormalizedResponse` (`nil` = undecided)
- Unit tests for omit defaults (cache strategy, inline vs named policy)

**Acceptance**: `go test` for defaults + response mark helpers green.

---

## Phase 2 — Consensus marks the winner

- In `Consensus.Run`, after `ex.Run`, set mark from `pol`
- Tests: default/`true` marks true; fallback/`false` marks false

**Acceptance**: consensus package tests cover both grades.

---

## Phase 3 — Cache Set + Forward Get

- Evm/Svm `Set`: apply strategy × mark table (feature §3)
- Stop skipping cache under selector; use `cacheDal` whenever present
- Tests:
  - `defaultAllow` + nil → write
  - `defaultAllow` + false → skip
  - `defaultIgnore` + nil → skip
  - `defaultIgnore` + true → write
  - selector + default caches; selector + fallback does not write

**Acceptance**: mark-based admission green end-to-end.

---

## Phase 4 — Docs

- Update consensus-policies + cache AISections
- Examples: `cachingStrategy: defaultAllow`; `cacheableResponses` on inline
  default vs `fallback`

**Acceptance**: docs match code.

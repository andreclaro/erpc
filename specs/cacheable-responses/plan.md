# Response-mark cache admission — Implementation Plan

**Status**: Spec landed; implementation pending
**Last revised**: 2026-11-02
**Base**: `main`
**Spec**: [feature.md](./feature.md) — single source of truth for behavior

Companion to [feature.md](./feature.md). Delivery sequence only.

---

## TLDR

Ship config → mark in `Consensus.Run` → cache Set respects mark → Forward uses
cache under selector → docs.

---

## Locked decisions

| Topic | Where |
|-------|--------|
| Mark on response; cache ignores consensus types | feature §1, §5 |
| `cacheableResponses *bool`; omit default/`true`, named/`false` | feature §2 |
| Set in `Consensus.Run` after round | feature §3, §5 |
| Set skips `false`; nil allows; Get available under selector | feature §3 |
| Consensus-only v1 | feature Non-goals / §6 |

---

## Phase 0 — Spec

- [x] Write `specs/cacheable-responses/feature.md`
- [x] Write this plan
- [ ] Review with Aram / maintainers

**Acceptance**: Spec accepted as implementation contract.

---

## Phase 1 — Config + response mark

- Add `CacheableResponses *bool` on `ConsensusPolicyConfig` + SetDefaults
- Add getter/setter on `NormalizedResponse` (`nil` = undecided)
- Unit tests for omit defaults (inline vs named)

**Acceptance**: `go test` for defaults + response mark helpers green.

---

## Phase 2 — Consensus marks the winner

- In `Consensus.Run`, after `ex.Run`, set mark from `pol`
- Tests: default/`true` marks true; fallback/`false` marks false

**Acceptance**: consensus package tests cover both grades.

---

## Phase 3 — Cache Set + Forward Get

- Evm/Svm `Set`: skip when mark == `false` only
- Stop skipping cache under selector; use `cacheDal` whenever present
- Tests: selector + default caches; selector + fallback does not write; no-consensus unchanged

**Acceptance**: mark-based admission green end-to-end.

---

## Phase 4 — Docs

- Update consensus-policies + cache AISections
- Example: `cacheableResponses` on inline default vs `fallback`

**Acceptance**: docs match code.

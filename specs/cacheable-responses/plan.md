# Response-mark cache admission — Implementation Plan

**Status**: Spec landed; implementation pending
**Last revised**: 2026-11-02
**Spec**: [feature.md](./feature.md) — single source of truth for behavior
**Related**: [erpc/erpc#1387](https://github.com/erpc/erpc/pull/1387), [crcl-main#273](https://github.com/crcl-main/erpc/pull/273)

Companion to [feature.md](./feature.md). Delivery sequence only.

---

## TLDR

Land the spec, then replace the allowlist PR with response marks + policy bools.
Ship config → mark → cache Set → Forward Get reopen → docs; delete allowlist.

---

## Locked decisions

| Topic | Where |
|-------|--------|
| Mark on response; cache ignores consensus types | feature §1, §5 |
| `cacheableResponses *bool`; omit default/true, named/false | feature §2 |
| Set in `Consensus.Run` after round | feature §3, §5 |
| Set skips `false`; nil allows; reopen Get under selector | feature §3 |
| No `cachingStrategy`; consensus-only v1 | feature Non-goals / §6 |
| Upgrade from allowlist → policy bools | feature §2 |

---

## Phase 0 — Spec

- [x] Write `specs/cacheable-responses/feature.md`
- [x] Write this plan
- [ ] Review with Aram / maintainers; supersede allowlist approach on #1387

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
- Remove allowlist field, `AllowsSelectorCache`, `cacheEligible` selector gate
- Reopen Get under selector when `cacheDal` present
- Replace allowlist tests with mark-based tests

**Acceptance**: selector + default caches; selector + fallback does not write; no-selector unchanged.

---

## Phase 4 — Docs + PRs

- Update consensus-policies + cache AISections; migration note
- Reshape #1387 / #273 (or replace) to this design
- Stg: policy bools instead of `cacheableConsensusPolicies`

**Acceptance**: docs match code; PRs describe response-mark model.

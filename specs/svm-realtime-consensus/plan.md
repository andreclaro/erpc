# SVM Slot-Grouped Consensus — Implementation Plan

**Last revised**: 2026-09-16
**Spec**: [feature.md](./feature.md) — single source of truth for behavior
**Gaps**: [svm-consensus-gaps.md](./svm-consensus-gaps.md)

Companion to [feature.md](./feature.md). Phased so each step ships value
independently; slot-grouped voting is the headline correctness piece.

---

## Decisions

Settled choices carried from the feature spec — implement these unless new
evidence forces a reopen.

| Decision | Choice |
|----------|--------|
| Pin location | Response `context.slot` (not request rewrite) |
| Agreement / hashing | Full result minus per-method `ignoreFields`. End-state SVM envelope defaults: ignore only `context.apiVersion` |
| Winner among groups | **Count-first:** max count among groups ≥ `agreementThreshold`; among that set, highest `context.slot` if present. Slot never outranks a larger group |
| Mixed slotted / non-slotted | Prefer slotted qualifying groups; non-slotted only if none slotted qualifies |
| Wait / short-circuit | Wait/short-circuit only while remaining participants can still form equal top count at a higher slot (count remaining only) |
| Cross-slot lag | Not misbehavior |
| Same-slot value split | Real dispute / misbehavior (winning slot cohort) |
| Activation | Auto when ≥1 success has parseable `context.slot` under active consensus |
| Rollout | Binary/network canary; no flag to restore ignoring `context.slot`; rollback = redeploy |
| Paired finality (§4.1) | Optional with/after §3; tip = `SvmHighestFinalizedSlot` (`PickServedTip`) |
| Slot-aware cache (§4.2) | **Deferred** until §3 soak + neverCache open topic |
| Financial threshold | No code default change; docs may recommend raising `agreementThreshold` |
| Nested preferHighestValueFor / SVM leader / bare-0 emptyish | Out of scope (gaps doc) |
| Operator failsafe / helm wiring | Out of scope |

---

## Phase 0 — Spec (this PR)

Write and land the behavior contract before code.

- [x] Write `specs/svm-realtime-consensus/feature.md`
- [x] Write this plan
- [x] Write `svm-consensus-gaps.md`
- [ ] Land draft for review

**Acceptance**: Spec reviewed and accepted as the implementation contract.

---

## Phase 1 — Moving-head consensus defaults + count-first winner

Narrow enveloped SVM `ignoreFields` defaults and apply **count-first /
slot-tiebreak** winner selection; keep EVM and non-envelope paths untouched.

1. **Defaults**: in `common/defaults.go`, change enveloped-method
   `ignoreFields` from `["context.slot","context.apiVersion"]` to
   `["context.apiVersion"]` only (see feature.md §3.0). Update
   `defaults_test.go` and consensus docs. Hashing stays
   `CanonicalHashWithIgnoredFields`; do not add a parallel hash path.
2. **Winner selection** (feature.md §3.1): among groups ≥
   `agreementThreshold`, take max count; among that set, highest
   `context.slot` if present. Prefer slotted groups over non-slotted when
   both qualify.
3. **Misbehavior**: only same-slot dissenters vs winning cohort; cross-slot
   is not misbehavior.
4. **Composition**: `minAgreement` only among agreeing members of the
   winning slot cohort.
5. **Wait / short-circuit**: only while remaining participants can still
   form equal top count at a higher slot (count remaining only; injectable
   clock in tests).
6. **Rollout**: canary binary/network; no restore-old-ignore flag; rollback =
   redeploy.
7. **Tests**:
   - No `context.slot` on any success → legacy path.
   - Equal counts, different slots → highest slot wins.
   - **3× V@1000 vs 2× V'@1050** → V@1000 wins (count-first security).
   - Same slot, different values → dispute under `returnError`.
   - Mixed slotted + non-slotted qualifying → slotted wins.
   - Wait/short-circuit with fake clock.
   - Mix quota on winning slot cohort; cross-slot ≠ misbehavior.
   - Default `ignoreFields` for `getBalance` is `["context.apiVersion"]` only.

**Acceptance**: `go test ./consensus/...` + `./common/...` green; EVM /
non-envelope SVM broadcast paths unchanged.

---

## Phase 2 — Paired finality (§4.1)

Classify rooted enveloped successes as `finalized` (see feature.md §4.1).
Optional with/after Phase 1.

1. After a successful enveloped response, if
   `context.slot ≤` `SvmHighestFinalizedSlot` (PickServedTip) **and**
   `effectiveCommitment == finalized`, set response finality to `finalized`.
2. Do not promote confirmed/processed via this path.
3. Tests: finalized commitment + slot ≤ tip → `GetFinality` finalized;
   confirmed → remains realtime.

**Acceptance**: Finality/metrics correct; no implication that never-cache
methods are stored.

---

## Phase 3 — Slot-aware cache (§4.2) — deferred

**Do not start** until Phase 1 soak is healthy and feature.md §8 neverCache
topic is settled. Then wire cache keys to served finalized tip / response
slot (see feature.md §4.2).

1. Get: for finalized-commitment moving-head reads, partition `slotRef` from
   network served finalized tip (not only `minContextSlot` / `*`).
2. Set: store under response `context.slot` when §4.1 classified finalized.
3. Tip advance → miss; never serve slot N for tip N+1.
4. `neverCacheMethods` unchanged unless open topic settles otherwise.
5. Tests: hit while tip stable; miss after tip advance; confirmed not
   permanently cached via this path.

**Acceptance**: Soak shows tip-keyed hits; no cross-tip stale serves.

---

## Phase 4 — Docs (ride along with Phase 1–2; Phase 3 when un-deferred)

1. Update `docs/pages/config/failsafe/consensus.mdx` — count-first winner,
   wait/short-circuit, misbehavior, canary rollout note, optional recommended
   threshold for financial methods.
2. Update `docs/pages/config/database/svm-json-rpc-cache.mdx` when §4.1/§4.2
   land.

**Acceptance**: Agent/docs panels match shipped behavior.

---

## Non-goals in this plan

Work that must not block or inflate this implementation track:

- Operator / helm failsafe enablement (separate from this source change)
- SVM `*BlockHeadLeader` leader selection
- Nested field paths for `preferHighestValueFor`
- Architecture-aware emptyish for bare `0`
- Custom consensus policies engine (#1088) — orthogonal; may compose later

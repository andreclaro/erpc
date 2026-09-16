# SVM Slot-Grouped Consensus — Implementation Plan

**Last revised**: 2026-09-16
**Spec**: [feature.md](./feature.md) — single source of truth for behavior
**Gaps**: [svm-consensus-gaps.md](./svm-consensus-gaps.md)

Companion to [feature.md](./feature.md). Phased so each step ships value
independently; slot-grouped voting is the headline correctness piece.

---

## Decisions

Settled choices — implement these unless new evidence forces a reopen.
Behavior detail lives in feature.md; this table is the implementer’s checklist.

| Decision | Choice |
|----------|--------|
| Pin location | Response `context.slot` (not request rewrite) |
| Agreement / hashing | Full result minus per-method `ignoreFields`. End-state SVM envelope defaults: ignore only `context.apiVersion` (§3.0) |
| Winner / wait / short-circuit / misbehavior / mix | Count-first + slot-tiebreak; wait/short-circuit only for equal top count at higher slot; cross-slot ≠ misbehavior; mix on winning slot cohort (§3.1–3.6) |
| Mixed slotted / non-slotted | Prefer slotted qualifying groups (§3.1) |
| Activation / rollout | Auto on parseable `context.slot`; binary/network canary; rollback = redeploy (§3.3) |
| Financial threshold | No code default change; **docs recommend** raising `agreementThreshold` above 2 for `getBalance` / `getAccountInfo` / other financial soak methods when upstreams allow (§3.3) |
| Paired finality (§4.1) | Optional with/after §3; tip = `SvmHighestFinalizedSlot` (`PickServedTip`) |
| Slot-aware cache (§4.2) | **Deferred** until §3 soak + neverCache open topic (§8) |
| Nested preferHighestValueFor / SVM leader / bare-0 emptyish | Out of scope (gaps doc) |
| Operator failsafe / helm wiring | Out of scope |

---

## Phase 0 — Spec (this PR)

- [x] Write `specs/svm-realtime-consensus/feature.md`
- [x] Write this plan
- [x] Write `svm-consensus-gaps.md`
- [ ] Land draft for review

**Acceptance**: Spec reviewed and accepted as the implementation contract.

---

## Phase 1 — Moving-head consensus defaults + count-first winner

Implement feature.md §3 (defaults, winner, wait/short-circuit, misbehavior,
composition, canary rollout). Keep EVM and non-envelope paths untouched.

1. **Defaults** (§3.0): enveloped `ignoreFields` → `["context.apiVersion"]`
   only; update `defaults_test.go` and consensus docs. No parallel hash path.
2. **Winner / misbehavior / composition / wait / short-circuit** (§3.1–3.6).
3. **Rollout** (§3.3): canary binary/network; no restore-old-ignore flag.
4. **Tests** (map to §7 acceptance):
   - No `context.slot` → legacy path.
   - Equal counts, different slots → highest slot wins.
   - **3× V@1000 vs 2× V'@1050** → V@1000 (count-first security).
   - Same slot, different values → dispute under `returnError`.
   - Mixed slotted + non-slotted → slotted wins.
   - Wait/short-circuit with fake clock.
   - Mix quota on winning slot cohort; cross-slot ≠ misbehavior.
   - Default `ignoreFields` for `getBalance` is `["context.apiVersion"]` only.

**Acceptance**: `go test ./consensus/...` + `./common/...` green; EVM /
non-envelope SVM broadcast paths unchanged.

---

## Phase 2 — Paired finality (§4.1)

Optional with/after Phase 1. Implement feature.md §4.1.

1. After successful enveloped response: `context.slot ≤ SvmHighestFinalizedSlot`
   (PickServedTip) **and** `effectiveCommitment == finalized` → finality
   `finalized`.
2. Do not promote confirmed/processed via this path.
3. Tests: finalized + slot ≤ tip → `GetFinality` finalized; confirmed →
   remains realtime.

**Acceptance**: Finality/metrics correct; no implication that never-cache
methods are stored.

---

## Phase 3 — Slot-aware cache (§4.2) — deferred

**Do not start** until Phase 1 soak is healthy and feature.md §8 neverCache
topic is settled. Then implement feature.md §4.2.

1. Get: partition `slotRef` from network served finalized tip.
2. Set: store under response `context.slot` when §4.1 classified finalized.
3. Tip advance → miss; never serve slot N for tip N+1.
4. `neverCacheMethods` unchanged unless open topic settles otherwise.
5. Tests: hit while tip stable; miss after tip advance; confirmed not
   permanently cached via this path.

**Acceptance**: Soak shows tip-keyed hits; no cross-tip stale serves.

---

## Phase 4 — Docs (ride along with Phase 1–2; Phase 3 when un-deferred)

1. Update `docs/pages/config/failsafe/consensus.mdx` — count-first winner,
   wait/short-circuit, misbehavior, canary rollout, **recommended**
   `agreementThreshold` above 2 for financial methods (§3.3).
2. Update `docs/pages/config/database/svm-json-rpc-cache.mdx` when §4.1/§4.2
   land.

**Acceptance**: Agent/docs panels match shipped behavior.

---

## Non-goals in this plan

- Operator / helm failsafe enablement (separate from this source change)
- SVM `*BlockHeadLeader` leader selection
- Nested field paths for `preferHighestValueFor`
- Architecture-aware emptyish for bare `0`
- Custom consensus policies engine (#1088) — orthogonal; may compose later

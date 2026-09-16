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
| Agreement / hashing | Full result minus per-method `ignoreFields` (unchanged mechanism). End-state SVM envelope defaults: ignore only `context.apiVersion` (drop `context.slot` from the default list) |
| Winner among groups | When responses carry `context.slot`, highest slot among groups that meet `agreementThreshold` (not largest count) |
| Wait default | Wait up to `maxWaitOnResult` for a higher qualifying slot; then best agreed (`0` = no time cap / full collection) |
| Cross-slot lag | Not misbehavior |
| Same-slot value split | Real dispute / misbehavior (existing majority rules within cohort) |
| Activation | Auto when ≥1 success has parseable `context.slot` under active consensus |
| Finality/cache promotion | Phase 2 only; require effective commitment `finalized` |
| Nested preferHighestValueFor / SVM leader / bare-0 emptyish | Out of scope (gaps doc) |
| Operator failsafe / helm wiring | Out of scope — this plan is source behavior once consensus already matches |

---

## Phase 0 — Spec (this PR)

Write and land the behavior contract before code.

- [x] Write `specs/svm-realtime-consensus/feature.md`
- [x] Write this plan
- [x] Write `svm-consensus-gaps.md`
- [ ] Land draft for review

**Acceptance**: Spec reviewed and accepted as the implementation contract.

---

## Phase 1 — Moving-head consensus defaults + winner policy

Narrow enveloped SVM `ignoreFields` defaults and prefer highest
`context.slot` among qualifying hash groups; keep EVM and non-envelope paths
untouched.

1. **Defaults**: in `common/defaults.go`, change enveloped-method
   `ignoreFields` from `["context.slot","context.apiVersion"]` to
   `["context.apiVersion"]` only (see feature.md §3.0). Update
   `defaults_test.go` and consensus docs. Hashing stays
   `CanonicalHashWithIgnoredFields` (full result minus that method’s
   `ignoreFields`); do not add a parallel hash path.
2. **Winner selection**: among hash groups with `count ≥ agreementThreshold`
   that expose `context.slot`, pick the group with the **highest slot**
   (not largest count).
3. **Misbehavior**: only compare dissenters inside the winning slot cohort;
   cross-slot participants are not misbehaving.
4. **Composition**: `minAgreement` counts tags only among agreeing members of
   the winning slot cohort.
5. **Wait caps**: `maxWaitOnResult: 0` = no time cap (full collection);
   bounded waits tune p99. Do not short-circuit a lone tip-slot vote while a
   higher slot can still qualify.
6. **Tests** (fallthrough first):
   - No `context.slot` → legacy hash / count-winner path unchanged.
   - Same value, different slots → separate buckets under end-state defaults;
     highest agreed wins after wait.
   - Same slot, different values → dispute under `returnError`.
   - Lone tip + agreed older → wait; second tip vote → freshest agreed.
   - Mix quota: internal+external only count in winning slot cohort.
   - Misbehavior metric: cross-slot does not increment misbehavior.
   - Default `ignoreFields` for `getBalance` (etc.) is `["context.apiVersion"]` only.

**Acceptance**: `go test ./consensus/...` + `./common/...` green; EVM /
non-envelope SVM broadcast paths unchanged.

---

## Phase 2 — Paired finality / cache

Promote rooted enveloped winners to slot-keyed finalized cache (see
feature.md §4). May ship **after** Phase 1 soak **or in the same first
release** as Phase 1 if capacity allows.

1. After a slot-grouped winner is chosen, if
   `context.slot ≤` network finalized tip **and**
   `effectiveCommitment == finalized`, set response finality to `finalized`.
2. SVM JSON-RPC cache: key includes slot for those responses; do not permanently
   cache confirmed/processed answers via this path.
3. Tests: finalized commitment + slot ≤ root → cache hit by slot; confirmed
   commitment → remains realtime / short TTL.

**Acceptance**: No permanent-cache bug for moving-head at confirmed; soak
metrics show finalized cache hits for rooted enveloped reads.

---

## Phase 3 — Docs (ride along with Phase 1 or 2)

Document shipped behavior in the public consensus failsafe page.

1. Update `docs/pages/config/failsafe/consensus.mdx` — SVM slot-grouped
   behavior, wait-cap note, misbehavior caveat.

**Acceptance**: Agent/docs panel matches shipped behavior.

---

## Non-goals in this plan

Work that must not block or inflate this implementation track:

- Operator / helm failsafe enablement (separate from this source change)
- SVM `*BlockHeadLeader` leader selection
- Nested field paths for `preferHighestValueFor`
- Architecture-aware emptyish for bare `0`
- Custom consensus policies engine (#1088) — orthogonal; may compose later

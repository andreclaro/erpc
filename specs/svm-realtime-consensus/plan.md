# SVM Slot-Grouped Consensus — Implementation Plan

**Last revised**: 2026-09-16
**Spec**: [feature.md](./feature.md) — single source of truth for behavior
**Gaps**: [svm-consensus-gaps.md](./svm-consensus-gaps.md)

Companion to [feature.md](./feature.md). Phased so each step ships value
independently; slot-grouped voting is the headline correctness piece.

---

## Locked decisions

| Decision | Choice |
|----------|--------|
| Pin location | Response `context.slot` (not request rewrite) |
| Winner among slots | Highest slot that meets `agreementThreshold` on value |
| Wait default | Wait up to `maxWaitOnResult` for a higher qualifying slot; then best agreed |
| Cross-slot lag | Not misbehavior |
| Same-slot value split | Real dispute / misbehavior (existing majority rules within cohort) |
| Activation | Auto when ≥1 success has parseable `context.slot` under active consensus |
| Finality/cache promotion | Phase 2 only; require effective commitment `finalized` |
| Nested preferHighestValueFor / SVM leader / bare-0 emptyish | Out of scope (gaps doc) |
| Operator failsafe / helm wiring | Out of scope — this plan is source behavior once consensus already matches |

---

## Phase 0 — Spec (this PR)

- [x] Write `specs/svm-realtime-consensus/feature.md`
- [x] Write this plan
- [x] Write `svm-consensus-gaps.md`
- [ ] Land draft for review

**Acceptance**: Spec reviewed and accepted as the implementation contract.

---

## Phase 1 — Slot-grouped voting in `consensus/`

1. **Extract slot** from successful responses (`PeekStringByPath` /
   `context.slot`) during analysis — discover from the body. The known
   envelope inventory is `contextSlotMethods`
   ([`hooks.go` L654–L672](https://github.com/erpc/erpc/blob/e8a375a1d5b740fe13c1d50a9f3b06758fa7c933/architecture/svm/hooks.go#L654-L672));
   do not require re-listing those names in the consensus hot path.
2. **Partition** then hash: group by slot first; within each partition use
   existing value hashing (`ignoreFields` still strips `context.*` from the
   hash). Slot is the partition key only.
3. **Winner selection**: among partitions with a value-group ≥
   `agreementThreshold`, pick highest slot; feed that group into existing
   dispute / prefer / composition pipeline where applicable.
4. **Misbehavior**: only compare dissenters inside the winning slot cohort;
   cross-slot participants are not misbehaving.
5. **Composition**: `minAgreement` counts tags only among agreeing members of
   the winning slot cohort.
6. **Wait caps**: document that this path needs non-zero `maxWaitOnResult`;
   no special executor fork beyond not short-circuiting a lone tip-slot vote
   while a higher slot can still qualify.
7. **Tests** (fallthrough first):
   - No `context.slot` → legacy hash path unchanged.
   - Same value, different slots → no dispute; highest agreed wins after wait.
   - Same slot, different values → dispute under `returnError`.
   - Lone tip + agreed older → wait; second tip vote → freshest agreed.
   - Mix quota: internal+external only count in winning slot cohort.
   - Misbehavior metric: cross-slot does not increment misbehavior.

**Acceptance**: `go test ./consensus/...` green; EVM / non-envelope SVM
broadcast paths unchanged.

---

## Phase 2 — Paired finality / cache

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

1. Update `docs/pages/config/failsafe/consensus.mdx` — SVM slot-grouped
   behavior, wait-cap note, misbehavior caveat.

**Acceptance**: Agent/docs panel matches shipped behavior.

---

## Non-goals in this plan

- Operator / helm failsafe enablement (separate from this source change)
- SVM `*BlockHeadLeader` leader selection
- Nested field paths for `preferHighestValueFor`
- Architecture-aware emptyish for bare `0`
- Custom consensus policies engine (#1088) — orthogonal; may compose later

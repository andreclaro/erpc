# SVM Consensus Gaps Inventory

**Last revised**: 2026-09-16
**Feature in scope**: [feature.md](./feature.md) — slot-grouped moving-head reads
**Plan**: [plan.md](./plan.md)

This file separates **source-code gaps**, **wire/protocol facts**, and
**deployment/config gaps** so the slot-grouped feature stays narrow.

---

## 1. In scope for this feature

| Gap | Kind | Notes |
|---|---|---|
| Naive hash consensus on enveloped moving-head reads false-disputes across slots | **Source** | Fix: slot-grouped voting ([feature.md](./feature.md) §3) |
| Strict slot-pinned rule lists moving-head methods but excludes `realtime` from `matchFinality` | **Config** | Intentional for strict rule; add a **new** realtime rule after code lands — do not widen the slot-pinned rule |
| Slot-pinned / soak uses `maxWaitOnResult: 0` | **Config** | Slot-grouped path needs non-zero wait |
| Mix `minAgreement` not yet applied on Solana deployments with mixed upstreams | **Config** | Enable after code; enforce on winning **slot** cohort |
| No finalized cache key by `context.slot` for rooted enveloped reads | **Source** | Phase 2 only ([feature.md](./feature.md) §4) |

---

## 2. Wire / protocol (not a missing eRPC API)

| Fact | Implication |
|---|---|
| `getBalance` / `getAccountInfo` / … have **no** slot pin in the request | Cannot mirror EVM tag→block rewrite on the request |
| `commitment: finalized` = latest **rooted** head (~400ms), not immutability | `GetFinality` correctly keeps these **realtime** at every commitment |
| `minContextSlot` is a floor, not a pin | Must not be used as a fake pin |
| Envelope carries `context.slot` | Response-side pinning is the weakest correct design |

Commitment **does not** change realtime classification for these methods
(processed / confirmed / finalized alike).

---

## 3. Related source gaps — **out of scope** for this feature

| Gap | Where | Why out of scope |
|---|---|---|
| `onlyBlockHeadLeader` / `preferBlockHeadLeader` only resolve on EVM | `consensus/analysis.go` | Production SVM policy uses `returnError`, not leader behaviors |
| `preferHighestValueFor` resolves only top-level / `"result"` | `consensus/utils.go` | Tip/highest-value path; non-goal for moving-head hash agreement |
| Bare `0` treated as emptyish | `util/bytes.go` | Envelope results are not bare `0`; tip integers are a separate edge |

Track separately if a future ticket needs them; do not block this feature.

---

## 4. Already shipped (not gaps)

| Capability | Location |
|---|---|
| Slot-pinned strict consensus + finality | `finality.go` `slotPinnedMethods`; typical slot-pinned failsafe rule |
| Tx broadcast first-success (`sendTransaction` / `sendRawTransaction`) | `consensus/rules.go` `isTxBroadcastMethod` |
| `requestAirdrop` single-dispatch (never consensus fan-out) | `erpc/network_executor.go` + `svm.IsSingleDispatchWriteMethod` |
| Default `ignoreFields` for envelope `context.slot` / `apiVersion` | `common/defaults.go` |
| Finalized-commitment slot-lag prefilter under consensus | `architecture/svm/slot_lag.go`, `erpc/networks.go` |
| Commitment injection for cross-upstream lockstep | `architecture/svm/hooks.go` |

---

## 5. Review checklist (razor)

For each proposed fix ask: *what unseen-but-plausible input does this silently
mishandle, and what in today's data forces that commitment?*

- Slot-grouped voting is forced by observed false disputes on adjacent roots.
- Widening the slot-pinned rule with `realtime` is **not** forced — it
  reintroduces the bug.
- A knob to “disable slot grouping but keep returnError on envelopes” is a
  knob for never-right behavior — reject.

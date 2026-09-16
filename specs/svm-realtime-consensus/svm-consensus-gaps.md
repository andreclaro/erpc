# SVM Consensus Gaps Inventory

**Last revised**: 2026-09-16
**Feature in scope**: [feature.md](./feature.md) — slot-grouped moving-head reads
**Plan**: [plan.md](./plan.md)

This file lists **source-code gaps** and **wire/protocol facts** that force
the slot-grouped design. Operator / helm failsafe wiring is out of scope.

---

## 1. In scope for this feature

Source gaps this feature is meant to close (or defer to §4.1 / §4.2).

| Gap | Kind | Notes |
|---|---|---|
| Naive hash consensus ignores `context.slot`, collapsing adjacent tips; count-winner prefers stale majorities | **Source** | Fix: end-state `ignoreFields` (slot in digest) + highest qualifying slot ([feature.md](./feature.md) §3) |
| Default `ignoreFields` still strips `context.slot` for enveloped methods | **Source** | End state: ignore only `context.apiVersion` (`common/defaults.go`; feature.md §3.0) |
| Moving-head enveloped reads always `realtime` even when `context.slot ≤` served tip under finalized commitment | **Source** | §4.1 paired finality ([feature.md](./feature.md) §4.1) |
| Cache keys use `minContextSlot` or `*` — no served-tip slot dimension | **Source** | §4.2 slot-aware cache ([feature.md](./feature.md) §4.2), separate from §4.1 |

---

## 2. Wire / protocol (not a missing eRPC API)

Solana facts that force response-side pinning — not something eRPC can “add”
as a request param.

| Fact | Implication |
|---|---|
| `getBalance` / `getAccountInfo` / … have **no** slot pin in the request | Cannot mirror EVM tag→block rewrite on the request |
| `commitment: finalized` = latest **rooted** head (advances every slot; mainnet ~**300ms** today, was 400ms, target 200ms — [Reduced Slot Times](https://solana.com/upgrades/reduced-slot-times)), not immutability | `GetFinality` correctly keeps these **realtime** at every commitment |
| `minContextSlot` is a floor, not a pin | Must not be used as a fake pin |
| Envelope carries `context.slot` | Response-side pinning is the weakest correct design |

Commitment **does not** change realtime classification for these methods
(processed / confirmed / finalized alike).

---

## 3. Related source gaps — **out of scope** for this feature

Nearby consensus rough edges that must not block slot-grouped voting.

| Gap | Where | Why out of scope |
|---|---|---|
| `onlyBlockHeadLeader` / `preferBlockHeadLeader` only resolve on EVM | `consensus/analysis.go` | Production SVM policy uses `returnError`, not leader behaviors |
| `preferHighestValueFor` resolves only top-level / `"result"` | `consensus/utils.go` | Tip/highest-value path; non-goal for moving-head hash agreement |
| Bare `0` treated as emptyish | `util/bytes.go` | Envelope results are not bare `0`; tip integers are a separate edge |

Track separately if a future ticket needs them; do not block this feature.

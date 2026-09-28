# 07 — Consensus Failsafe: Role & Evolution

## 1. Why consensus remains required

Anchor + proofs (P1–P3) cover **finalized, provable** data. Everything outside that surface still needs the strongest non-cryptographic control available — and that is the existing `consensus` failsafe:

1. **Unfinalized head** (V-provisional) — there is nothing to prove against until finality; consensus + optimistic-update attestation is the ceiling.
2. **V-execution methods** (`eth_call`, `eth_estimateGas`, traces) — until P4 (StatelessExecutor) ships, consensus is the interim tier.
3. **Quorum-tier chains** — chains with no anchor adapter (BSC, SVM, and the razor's unknown-chain fallthrough) rely on consensus as their *only* integrity mechanism.

It also keeps its original job: availability and latency fan-out. What changes is labeling: consensus outcomes are at best `provisional`/`quorum`, **never** `verified` (INV-1).

## 2. New interaction model: "consensus proposes, anchor disposes"

For provable classes (V-inclusion, V-state) served under consensus (balanced mode, unfinalized-permitting configs):

1. Consensus picks a winner exactly as today → served per mode, labeled.
2. The winner is **asynchronously verified against the anchor** (at finality for provisional data).
3. Outcomes:
   - **Winner verifies** → neutral/positive score signal for the agreeing group.
   - **Winner fails verification** → every upstream in the winning group served bad data (collusion or shared poisoned backend) → evidence bundles + hard cordon for *all* group members; if a minority response verifies, it was the truth — record it, serve it on retry.

This converts voting into **eventual proof**: even lies that win a vote become detectable and punishable once finality lands. It also closes the collusion hole — k-of-N agreement stops being the final word for provable data.

## 3. Conflict policy: anchor vs consensus (the deliberate asymmetry)

| Case | Interpretation | Action |
|---|---|---|
| Minority conflicts with anchor | minority lying/stale | evidence + cordon minority; serve verified winner |
| Winning group conflicts with anchor | group-wide lie or shared infra | evidence + cordon whole group; promote the verifying minority; page (this is the popular attack pattern) |
| **Unanimous** cross-vendor consensus conflicts with anchor | **probably our anchor is wrong** (checkpoint, fork schedule, store bug) | fail-closed for the network + page humans; do **NOT** mass-cordon; anchor state review before re-enabling |

The third row is intentional: the anchor is a *single* component and can be buggy. Unanimous independent disagreement is the canary for anchor failure — rare, always loud, never auto-punished.

## 4. Improvement catalog for the current consensus implementation

| # | Improvement | What / why | Depends on |
|---|---|---|---|
| **C1** | Async winner verification | §2 — anchor adjudicates provable-method winners after the fact; voting becomes provisional, proof becomes final | P1, P3 |
| **C2** | Proof-adjudicated disputes | For V-state disputes (incl. 2-of-2 ties and empty-vs-nonempty), fetch `eth_getProof` and adjudicate **deterministically** instead of by `disputeBehavior`/threshold politics | P1, P2 |
| **C3** | Shrink `ignoreFields` | Fields currently ignored as "vendor variance" (`blockTimestamp`, `blockHash`, `baseFeePerGas`, `miner`, `gasUsed`…) are committed in the anchored header → **verify them instead of ignoring them**. Keep `ignoreFields` only for genuinely chain-specific metadata (e.g. `l1Fee` on OP Stack receipts) | P1 |
| **C4** | Provisional-accuracy reputation | Track each upstream's provisional (head) responses against the eventual finalized verified values; persistent accuracy score feeds selection. Catches "honest-at-finality, sloppy-at-head" upstreams that pure voting cannot see | P1, P3 |
| **C5** | Independence-aware quotas | Extend `requiredParticipants` tag quotas to enforce vendor/infra diversity per round; detect correlated participants (identical lag steps, identical error timing, shared-backend fingerprints) and collapse their votes to one | standalone |
| **C6** | Freshness-weighted voting | Weight votes on head data by the state poller's per-upstream sync height/lag — a stale upstream's old-truth should not outvote live upstreams | standalone |
| **C7** | Persistent misbehavior scores | Extend the shared-cordon-counter pattern (#1137) to reputation: scores survive restarts and replicas, decay over time | standalone |

C1–C4 ship with the anchor phases (they are where the anchor and consensus meet); C5–C7 are standalone hardening that pays off with or without the anchor.

## 5. Non-goals (razor)

- Consensus does **not** become a proof system — provable data is adjudicated by the anchor, not by better voting. Voting stays for the unverifiable surface.
- Nothing here narrows the method set consensus can handle: unknown methods still fall through to the existing safe defaults, unchanged.

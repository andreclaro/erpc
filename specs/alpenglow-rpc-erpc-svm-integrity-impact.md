# Alpenglow Impact on eRPC's SVM Integrity Checks

> Companion to [alpenglow-rpc-erpc-impact.md](alpenglow-rpc-erpc-impact.md) — that doc covers Alpenglow's RPC-protocol changes and main-branch eRPC impact; this one covers the **unmerged integrity-check work** only.
> Verified against `andreclaro/erpc` **main @ `14c268f0`** (2026-10-08).

## 1. Scope: what this applies to

The SVM integrity checks (`architecture/svm/integrity/`, the `svm.*` checks designed in `specs/svm-integrity` and tracked as eRPC issue #1203) are **not on main**. They exist only on the fork's branch family:

| Branch | Content |
|---|---|
| `feat/svm-integrity` | Phase 0/1 implementation — the 8 Deterministic checks + hooks |
| `specs/svm-integrity` | Design spec files |
| `feat/svm-getblock-finalized-bound` / `-v2` | getBlock finalized-bound work |
| `feat/svm-match-commitment` / `feat/svm-consensus-match-commitment` | Commitment matching / consensus |
| `feat/svm-confirmed-slot-poller` | Confirmed-slot polling |
| `svm-slot-grouped-consensus` | Slot-grouped consensus experiments |

Everything below is **design input to be baked in before this family merges to main**, not a description of deployed code.

Baseline context on Alpenglow itself (Votor, ~150ms finality, votes leaving the block, `getAgGenesisCert`, slot time 400→200ms): see the companion doc §1–2. This doc assumes it.

## 2. Per-check impact

### 2.1 `svm.shape.commitmentParam` — economics flip, check stays valid
The check polices the commitment parameter in requests/responses. Both levels still parse post-activation, so the check mechanically works. But what it's *protecting* changes meaning:
- Pre-activation: `confirmed` = optimistic (~400ms), `finalized` = 12.8s lockout. Mismatched commitment = real freshness hazard.
- Post-activation: `confirmed` and `finalized` are the same state (~150ms). A mismatch becomes cosmetically wrong but practically harmless — until `confirmed` is removed in a later release, at which point sending it upstream becomes a hard error path.

**Design input:** keep validating commitment values; add a per-upstream mode (gated on `getAgGenesisCert`) that treats the levels as equivalent and rewrites `confirmed` → `finalized` for safety ahead of removal.

### 2.2 Finality-window retry/failover logic — re-tune hedging strategies
Anything that waits, hedges, or times out around the 12.8s TowerBFT finality window needs re-tuning:
- A `finalized` hedge that cost 12.8s now costs ~150ms — strategies that were irrational at 12.8s become rational at 150ms (e.g. always hedging balance reads at finalized), and vice versa (retry budgets tuned in "slots" shrink in wall-clock terms).
- Timeouts expressed in wall-clock around finality ("wait up to N seconds for finalized") need re-baselining against ~150ms.

**Design input:** express finality-related timeouts/budgets in **slots**, not seconds, and make the conversion factor Alpenglow-aware per upstream.

### 2.3 `svm.struct.txShape` / tx-count heuristics — re-baseline for vote-free blocks
Per-block transaction counts drop ~75% the day votes leave the block. Any threshold, anomaly detector, or benchmark keyed to historical tx volumes will false-alarm on "collapsing activity" unless re-baselined.

**Design input:** block-shape expectations must be version-aware: TowerBFT-era baselines vs Alpenglow-era baselines, selected by per-upstream `getAgGenesisCert` state — not a single global constant.

### 2.4 Vote-based signals — go blind, re-source or drop
Anything inferring validator participation or fork health from Vote program instructions in blocks loses its input entirely. The data moves to footer certificates (`notar_reward_cert`, `skip_reward_cert`, `block_final_cert`) which are Geyser-only — **not present in JSON-RPC responses** (Yellowstone's gRPC footer forwards metadata but not certificates yet).

**Design input:** participation-health checks that run over JSON-RPC data must be dropped or re-scoped; do not pretend footer certs are reachable via JSON-RPC.

### 2.5 `svm.shape.slotEncoding` / magnitude checks — re-scale for doubled slot rate
Slot cadence shrinks 400→200ms in stages (SIMD-0525, partially activated — independent of Alpenglow but lands on the same timeline). Slot-drift checks between upstreams and time-based magnitude bounds (e.g. "slot N is plausible at wall-clock T") need re-scaling: a fixed per-second drift allowance covers half the slot depth after the change.

**Design input:** derive rate bounds from chain constants (slots-per-second) read at runtime — e.g. from `getEpochSchedule` + recent slot cadence — rather than compiled-in 400ms assumptions.

### 2.6 `svm.auth.signatureVerify` / `svm.auth.genesisHash` — unchanged
BLS aggregation is internal to consensus; user-space ed25519 transaction signatures and the genesis hash are untouched by Alpenglow. These checks carry over as-is. (Note: BLS keys on vote accounts — SIMD-0387 — are a validator-admission concern, not a response-integrity concern for user transactions.)

### 2.7 Equivocation detection — becomes cryptographically provable
Under Alpenglow, two blocks can never both cross the 60% notarization threshold for the same slot (that would need 120% of stake). Conflicting certified blocks for one slot are therefore **proof** of misbehavior by ≥20% of stake, not ambiguous evidence.

**Design input:** if the branch ever surfaces equivocation evidence, post-activation it becomes *stronger* misbehavior evidence than today — worth wiring to `RecordUpstreamMisbehavior` with higher confidence weighting.

### 2.8 Chain-safety invariant — extend to Alpenglow-only fields
The design's core invariant (unparseable/unmodelled data → Skipped, never Reject) must explicitly cover Alpenglow-introduced data: block footers, certificates, `bank_id` in Geyser payloads. Until modelled: Skip. This is especially important during the rollout window where upstreams on different versions return different shapes for the same method.

## 3. Pre-merge checklist for the `feat/svm-integrity` family

1. **Per-upstream Alpenglow state** — probe `getAgGenesisCert` (distinct handling of `-32601` / `null` / certificate) and key all version-dependent behavior off it.
2. **Dual-baseline block-shape checks** — TowerBFT vs Alpenglow tx-volume baselines (§2.3), selected by that state.
3. **Slot-rate bounds derived, not hardcoded** — re-read slots-per-second from the chain; re-scale magnitude/drift checks (§2.5).
4. **Finality budgets in slots** — hedging/timeout logic expressed in slots with Alpenglow-aware conversion (§2.2).
5. **Drop or re-source vote-instruction participation signals** (§2.4); do not rely on footer certs via JSON-RPC.
6. **Commitment rewrite mode** — post-activation, normalize `confirmed` → `finalized` upstream (§2.1).
7. **Extend chain-safety invariant** to footers/certs/`bank_id` (§2.8).
8. **Equivocation confidence weighting** — stronger post-activation (§2.7).
9. **Re-run the full test matrix against an Agave 4.3 node** (testnet/devnet already have the gate active) before merging — several checks' fixtures embed TowerBFT-era blocks with vote transactions; they will silently pass against synthetic data and fail against live Alpenglow data.

## 4. Sources
- Companion doc: [alpenglow-rpc-erpc-impact.md](alpenglow-rpc-erpc-impact.md) (Alpenglow protocol changes, main-branch impact, rollout status)
- eRPC design spec: `specs/svm-integrity` branch (`specs/svm-integrity/` files)
- eRPC issue #1203 (generic `jsonrpc` architecture / SVM integrity)
- https://solana.com/upgrades/alpenglow — official breaking-change detail and migration checklists
- Feature gate: `A1pengvuM6JEcyNuTnMqepBKhwHE3N6PmUrdATGawhJS` (testnet/devnet active; mainnet pending Agave 4.3)

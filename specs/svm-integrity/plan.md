# SVM Data Integrity — Implementation Plan

**Status**: Draft — for review
**Last revised**: 2026-09-24

Companion to [feature.md](./feature.md). Phased so each step ships value independently: the free deterministic tiers land first, stateful commitment next, stake-weighted finality evidence last (it is the strongest and the most protocol-coupled). Every phase keeps the chain-safety invariant: **unmodelled data skips, never rejects.**

---

## Phase 0 — Skeleton + engine (no checks)

1. **Package**: `architecture/svm/integrity/` mirroring the EVM layout — `check.go` (Check + `register`), `decoded.go` (lazy accessors over gagliardetto `rpc.GetBlockResult` / `TransactionWithMeta` / `Account` / `GetBlockCommitmentResult`), `levels.go` (level→check-set, membership test), `config.go`, `engine.go` (`Validate`), `resolver.go` (aux-request dispatch), `follower.go` (per-upstream slot→bankhash store), `chain_safety.go` (skip sentinels + parse-failure policy).
2. **Wiring**: `HandleUpstreamPostForward` + `HandleProjectPostForward` in `architecture/svm/hooks.go`, with the SVM method tables (feature.md §5) and the same reject→`ErrEndpointContentValidation`→failover path as EVM. Internal (aux) requests skip the engine — no recursion.
3. **Config surface**: `svm.integrity` block (feature.md §8) with load-time validation (`common.RegisterIntegrityCheckID`, unknown id/level/behavior fails boot).
4. **Metrics**: reuse the `erpc_integrity_*` family with SVM check ids; `overhead_seconds` from day one so every later phase is measurable.

Acceptance: engine compiles, wires, validates a trivial no-op check end-to-end on a local `getBlock`, metrics emit, zero behavior change.

## Phase 1 — Intrinsic tier (free, pure, deterministic)

The highest value-per-line phase; the EVM engine's production history says genuine catches concentrate here.

1. **Authenticity**: `svm.auth.signatureVerify` (ed25519 batch over full blocks and single txs; secp256k1 signer keys; v0 message reserialization incl. resolved lookup addresses) and `svm.auth.genesisHash` (cluster constant table). Signature material unavailability (`transactionDetails: "none"`, missing `maxSupportedTransactionVersion`) → skip, logged as `skip`, never `pass`.
2. **Structural**: `blockShape`, `txShape`, `sigUniqueness`, `tokenOwner`, `ataDerivation`, `rewardShape` (feature.md §4.1). All pure functions, unit-testable from recorded mainnet fixtures (freeze real `getBlock` responses as testdata).
3. **Shape**: `magnitude`, `commitmentParam`, `slotEncoding`.
4. **Shadow window discipline**: new checks log `soft_flag` (never reject) for a configurable burn-in per network; promote per `network_profiles.go` only after a clean window (EVM finding §10.5 — a check meeting an unfamiliar network is more likely to be protocol-invalid than to be catching corruption).

Acceptance: with `level: intrinsic`, a block with one corrupted signature/field/decimal is rejected and failovered; recorded fixtures pass untouched; per-check outcome metrics show earned `pass` vs `skip`.

## Phase 2 — Commitment tier (follower + chain anchoring)

1. **Follower store** (feature.md §7): hash-keyed bankhash entries, slot pins adopted only from validated blocks, group scoping, reorg rollback, singleflight resolve, fetch-anchor enforcement on every aux answer.
2. **Checks**: `chainLink`, `chainFollower`, `heightMonotonic`, `slotEpoch`, `timeWindow` — plus **corroborate-before-verdict**: slot-targeted pin re-confirm with cooldown-bounded degradation (feature.md §6). This lands *with* the checks, not after an incident: strict-at-the-tip without reconfirm is a known self-block (EVM lived it; Solana's slot cadence is faster).
3. **Continuity**: `headProgression`, `cacheGuard`.

Acceptance: a spliced chain (foreign `previousBlockhash`) is rejected on finalized pins; a routine fork within the reorg window soft-flags, re-confirms, and clears (`reconfirmed`); the follower recovers without operator action.

## Phase 3 — Finality evidence tier (stake-weighted, SVM-specific)

1. **Aux plumbing**: epoch-cached `getVoteAccounts` stake table + `getEpochSchedule`; stake table refresh at epoch boundaries via `getEpochInfo`.
2. **Checks**: `commitmentQuorum` (32-depth histogram semantics), `stakeTableJoin`, `rootSlotSanity`, then `voteEvidence` — vote instruction decoder (program id, opcode family, pinned bincode layouts against agave; parse-failure = skip), bank-hash extraction, stake-weighted tally, cross-check vs `getBlockCommitment` and follower depth.
3. **Alpenglow probe** (feature.md §10): `getVersion`-based feature flag + vote-absence streak; probe ships *before* mainnet activation so the degradation path is exercised on test clusters, not discovered in production.

Acceptance: a node claiming `finalized` for a block whose commitment histogram can't reach ⅔ at root depth is flagged; on a finalized pin, rejected. Vote tally and commitment endpoint agree on every finalized block in a 24h shadow run (or the disagreement is explained and fixed).

## Phase 4 — Corroboration joins + write/simulate shape

1. **Joins**: `balanceJoin`, `sigStatusJoin`, `sigListingJoin` (sampled), `blockhashJoin`, `supplyJoin`, `leaderSchedule` (recent-epoch-only skip semantics).
2. **Write/simulate**: `nonRetryableGuard` shape + opt-in read-back corroboration; `simulateShape`. Both recordOnly at moving head by definition.
3. **Exhaustion detection**: `RecordExhaustion` + `erpc_integrity_protocol_suspect_total` wired for SVM from day one of this phase.

Acceptance: balance-only and status-only tampering on a single upstream is detected and scored; honest upstreams show zero soft-flags in shadow; aux request budget stays within feature.md §9.

## Phase 5 — DAS module (optional, only if asked)

`assetProofPath` (Merkle recompute vs tree-account join) + `assetShape`, behind an explicit enable; vendor profiles first (DAS responses vary across providers).

---

## Risks / watch-items (carried across phases)

- **Provenance discipline**: never hard-fail against single-source observed data; enforced by construction (joins hard-fail only at finalized pins with anchor/evidence, else soft-flag).
- **Protocol drift**: vote layout churn (Vote → CompactUpdateVoteState → TowerSync), Token-2022 extensions, Alpenglow. Chain-safety + skip-sentinel policy is the seatbelt; profiles record the per-network *why*.
- **Latency**: `signatureVerify` is batch-fast; aux fetches are cached/coalesced. Measure via `overhead_seconds`; if user-path latency regresses, consider verify-after-serve (cordon) later — same option as EVM.
- **Scope creep**: the module rejects provably-wrong data. Quorum/agreement stays in consensus; trust configuration stays in network config. No execution replay in v1.

# SVM Data Integrity — Specification

**Status**: As-built (implemented on this branch)
**Last revised**: 2026-11-22
**Scope**: `architecture/svm/integrity/` (+ `architecture/svm/integrity_hooks.go`, `architecture/svm/integrity_config.go`)

---

## 1. Purpose

The **SVM integrity module** validates that the data a Solana upstream returns is internally consistent and consistent with the ledger it claims to describe, and rejects responses that provably cannot be correct. A rejected response becomes a standard content-validation error (`ErrEndpointContentValidation`), so the existing retry/failover machinery routes around the offending upstream and — in almost every observed case — serves the client a correct response from another one.

The module is a self-contained check engine (`architecture/svm/integrity/`) with its own decoded view of responses, its own base58 and transaction wire-format parsers (zero dependencies, stdlib-only — the repo deliberately avoids `solana-go`), a per-network **verified-block index**, and a finality resolver backed by the upstream's own `SvmStatePoller`. It is invoked from the SVM upstream post-forward hook on every applicable response. The taxonomy (families, failure classes, levels, verdicts, invalid-behavior split) mirrors the EVM `data-integrity` engine one-for-one so operators get one mental model across architectures.

### What it catches that consensus cannot

- Corruption the upstreams *agree* on (shared client/indexer bugs) — consensus is majority-blind.
- Corruption on requests with no fan-out at all — intrinsic checks are free and always-on per response.
- Field-level provable inconsistencies (a block whose signatures do not verify under the claimed signers; a response whose slot is not yet finalized by the node's own tip evidence) that byte-comparison voting can't see.

## 2. Non-goals

- **Not** a quorum mechanism (consensus's job) and **not** a data-rewriting layer: the module rejects, it never edits.
- **Not** a new trusted source: finality ground truth comes from the upstream's own state-poller tips that the router already trusts; chain anchors come from blocks that passed every enabled check.
- **Not** a consensus participant: no TowerBFT/Votor participation, no stake, no replay, no bank-hash recomputation (impossible without full account state — §12).
- **Not** a validator of self-reported fields that cannot be cross-checked (`isBlockhashValid.contextualValidity`, exact `blockTime`, simulation correctness).
- **Not** Alpenglow-aware yet: the one check that would need it (`svm.final.voteEvidence`) is not implemented; `getBlockCommitment` shape checks survive the Votor transition unchanged.

## 3. Check engine

Checks self-register (`init()` → `register()`, mirroring the consensus-rules pattern) into a per-method registry. `Validate` (engine.go) runs every enabled, applicable check over a **single decode** of the response (`decoded.go` — lazy per-shape accessors for block, transactions-for-verification, epoch info, block commitment, signature lists, context slot, and the originating request's params).

Each check declares:

- **ID** — stable, `svm.<family>.<name>`, also fed to `common.RegisterIntegrityCheckID` so a typo'd id in config fails boot validation instead of silently disabling.
- **Family** — `structural | authenticity | shape | commitment | continuity | corroboration` (descriptive only: metrics, docs, levels).
- **Class** — `Deterministic` (a correct upstream can never fail it) or `ReorgSensitive` (head-of-chain data can legitimately change).
- **Methods** — the JSON-RPC methods it applies to.
- **`Run`** — inspects the decoded response; returns `nil` (verified), a `Violation`, or the **`Skipped` sentinel** (could not evaluate).
- **`AfterPass`** — optional; runs **only after the response passed every enabled check with zero recorded mismatches**. The single point where cross-request state may be committed (head high-water marks, block pinning). A rejected or soft-flagged response never becomes ground truth for later requests.

### 3.1 Chain-safety invariant

> Anything a check cannot fully model **skips, never rejects.** Unparseable transaction, unknown encoding variant, absent field, unmodelled client extension → `Skipped`, not a violation.

This is what makes the module safe to enable fleet-wide: it can never reject valid data it simply doesn't understand. Operating corollary (from the EVM engine): a check rejecting across **all** upstreams of one network is a protocol quirk; scattered per-upstream rejects are real catches.

### 3.2 Levels

`levelMembership` (levels.go) is the single auditable map of which checks each level introduces; `TestLevelMembershipCoversAllChecks` enforces that every registered ID appears in exactly one row.

| Level | Count | What it adds |
|---|---|---|
| `off` | 0 | — |
| `intrinsic` | 15 | pure self-consistency; no upstream cost, no state, always safe |
| `corroborated` | 12 | compares against ground truth already available (the verified-block index, the upstream's own state-poller tips) |
| `authoritative` | 0 | reserved; `svm.final.voteEvidence` lands here — no SVM check requires force-fetching yet |

### 3.3 Verdict machinery

- **Behavior**: `reject` (→ content-validation error → failover) · `recordOnly` (metric + warn log, response served) · `off`.
- **ReorgPolicy** `invalidBehavior {finalized, unfinalized}` maps finality → behavior for ReorgSensitive checks; Deterministic checks always reject. Default: `{finalized: reject, unfinalized: recordOnly}`. Unknown finality records, never rejects.
- **`ObserveOnly`** rollout mode suppresses every rejection (including Deterministic) while still counting `would_reject` — enforce exactly what observe-only proved clean.
- **`finalityOnce`** resolves the response slot's finality exactly once per `Validate` call, so a violation's verdict and its metric label can never disagree.
- Emptyish results (null / `[]` / `""`): result-content checks skip; only request-judging checks flagged `AllowEmptyish` (e.g. `commitmentParam`) still run.
- Deterministic rejects also feed `RecordUpstreamMisbehavior` so routing learns to avoid a chronically corrupt node; ReorgSensitive rejects don't score (may be transient).

### 3.4 Response flow

```
upstream response
  → HandleUpstreamPostForward → upstreamPostForward_integrity
    → resolveIntegrity (level preset + per-check overrides + genesis binding)
    → integrity.Validate
        → single decode (Decoded)
        → for each enabled check: Run → pass / skip / violation
            → violation: Deterministic → reject (or would_reject in observe-only)
                         ReorgSensitive → per-finality via poller-backed resolver
        → clean pass → AfterPass commits (head marks, block observation)
    → reject  → ErrEndpointContentValidation → retry/failover (other upstream)
    → record  → served + metric + warn log
```

## 4. The check catalog (27 checks)

**By level:** 15 intrinsic · 12 corroborated · 0 authoritative.
**By class:** 18 Deterministic · 9 ReorgSensitive.
**By family:** 8 structural · 3 authenticity · 4 shape · 8 commitment · 4 continuity.

### 4.1 Authenticity — SVM's pillar-1 (the ed25519 substitute for EVM's Merkle membership)

| ID | Level | Class | Methods | What it verifies |
|---|---|---|---|---|
| `svm.auth.signatureVerify` | intrinsic | Det | `getBlock`, `getConfirmedBlock`, `getTransaction` | Rebuilds each transaction's exact signed wire message from the RPC JSON (v0/v1, compact-u16 shortvecs, v0 `0x80` prefix *included* in the signed payload, resolved lookup addresses) and stdlib-verifies `signatures[i]` against `accountKeys[i]` for `i < numRequiredSignatures`. **The single highest-value check**: a fabricated, field-tampered, or injected transaction fails outright. ~105 µs per signature; a full block (~2–4k signatures) is milliseconds. Skips `transactionDetails:"signatures"` (bare strings, no message material) — skip, never pass silently |
| `svm.auth.genesisHash` | intrinsic | Det | `getGenesisHash` | Equals the cluster's known genesis (mainnet-beta `5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2Nyd`, devnet/testnet constants). Bound at config-compile from `common.KnownGenesisHash`; unknown cluster → skip. Catches an upstream serving the *wrong cluster entirely* |
| `svm.auth.tokenProgram` | intrinsic | Det | `getTokenAccountsByOwner`, `getTokenAccountsByDelegate` | Token-specific queries must return token-program accounts: owner ∈ {`Tokenkeg...VQo5DA`, `TokenzQ...EpPxuEB`} (binary encodings) or jsonParsed program ∈ {`spl-token`, `spl-token-2022`}. A bogus owner = synthesized token data |

Tx-level signatures are always ed25519 (secp256k1 appears only inside instruction data), so the check is ed25519-only by protocol truth, not by simplification debt.

### 4.2 Structural — cross-reference invariants over the response itself

| ID | Level | Class | Methods | What it verifies |
|---|---|---|---|---|
| `svm.struct.blockShape` | intrinsic | Det | `getBlock`, `getConfirmedBlock` | `blockhash`/`previousBlockhash` are 32-byte base58 and distinct (genesis's all-zero self-parent admitted); `parentSlot` non-negative and `<` requested slot (skipped-slot gaps legal — parentSlot+1 NOT required); `blockHeight` non-negative; `blockTime` within [mainnet genesis 2020-03-16, now+2s skew]; null `transactions` only legal for `transactionDetails: "signatures"` (which must still carry the sig array) or `"none"` |
| `svm.struct.txShape` | intrinsic | Det | `getBlock`, `getConfirmedBlock`, `getTransaction` | Header consistency: `numRequiredSignatures > 0` and ≤ account keys; readonly counts in bounds; `len(signatures) == numRequiredSignatures`; `recentBlockhash` and every `accountKey` 32-byte base58; instruction program-id/account indices in range; v0 lookups well-formed |
| `svm.struct.sigUniqueness` | intrinsic | Det | `getBlock`, `getConfirmedBlock` | No duplicate or empty signature in a block — double-inclusion is leader misbehavior or forgery. Covers both full-tx and `transactionDetails:"signatures"` shapes |
| `svm.struct.requestedSigMatch` | intrinsic | Det | `getTransaction`, `getConfirmedTransaction` | **Request↔response binding**: the served tx's signature list must include the requested signature (any-of matching — multi-sig wallets legitimately query by any member's sig). A match to none = the upstream answered a cheaper/cached/invented transaction. Object-encoding envelopes only; base58 wire form carries no inline sigs → skip |
| `svm.struct.rewardsShape` | intrinsic | Det | `getBlock`, `getConfirmedBlock` | Each reward's payee is a decodable 32-byte base58 pubkey and lamports a signed integer (rent rewards legitimately negative). Reward *vocabulary* is cluster-evolving — deliberately not pinned |
| `svm.struct.heightVsSlot` | intrinsic | Det | `getBlock`, `getConfirmedBlock` | `blockHeight ≤ slot`: Solana ticks every slot but skips unproduced ones, so produced-block count can never exceed tick count. Genesis equality admitted |
| `svm.struct.tokenMintShape` | intrinsic | Det | `getAccountInfo` | A token-program-owned account with exactly the canonical 82-byte payload must be a structurally valid mint: mintAuthority/freezeAuthority COption tags ∈ {0,1}, `isInitialized` byte ∈ {0,1}. Longer payloads are extension-carrying/ambiguous → skip, never guess |
| `svm.struct.tokenAccountShape` | intrinsic | Det | `getTokenAccountsByOwner`, `getTokenAccountsByDelegate` | Binary-payload entries must carry the base SPL token-account layout: ≥165 bytes; delegate tag (offset 72) and close-authority tag (offset 129) valid COption tags; state byte (offset 108) ∈ {0,1,2}. Offsets verified against `@solana/spl-token` AccountLayout. jsonParsed entries are RPC-asserted structure → skip |

### 4.3 Shape — cheap syntactic sanity

| ID | Level | Class | Methods | What it verifies |
|---|---|---|---|---|
| `svm.shape.magnitude` | intrinsic | Det | `getBlock`, `getBlockCommitment`, `getSignaturesForAddress` | Physical bounds: ≤500k txs/block, commitment histogram exactly 32 tiers, non-negative `totalStake`, ≤1000 signatures/page, no empty signature entries |
| `svm.shape.commitmentParam` | intrinsic | Det | all envelope methods | Every commitment in the request params ∈ the known vocabulary {processed, confirmed, finalized + legacy aliases recent/single/singleGossip/root/max}. Also validates `getSignaturesForAddress` `limit ≤ 1000`. `AllowEmptyish` — judges the request, not the result. Catches **commitment smuggling** |
| `svm.shape.slotEncoding` | intrinsic | Det | `getBlocks`, `getBlocksWithLimit`, `getSlot`, `getBlockHeight`, `getTransactionCount`, `getEpochInfo` | Slot lists strictly increasing; numeric results are actually numeric; getEpochInfo fields non-negative and `absoluteSlot ≥ blockHeight` |
| `svm.shape.blocksLimit` | intrinsic | Det | `getBlocks`, `getBlocksWithLimit` | The returned slot list may not exceed what was asked for (limit param, or explicit end slot; unbounded-to-tip requests skip) — a longer list than asked is a fabrication vector |

### 4.4 Commitment — chain anchoring against the verified-block index

All require the verified-block index; skip when the parent/slot was never verified (unverifiable ≠ violation).

| ID | Level | Class | Methods | What it verifies |
|---|---|---|---|---|
| `svm.commit.parentLink` | corroborated | RS | `getBlock`, `getConfirmedBlock` | `previousBlockhash` == the hash of the **verified parent block at `parentSlot`, looked up by slot against the index** (never slot arithmetic — skipped slots are legal). Genesis self-parent skips |
| `svm.commit.heightMonotonic` | corroborated | RS | `getBlock`, `getConfirmedBlock` | `blockHeight == verified parent's blockHeight + 1` exactly, along the hash chain |
| `svm.commit.chainFollower` | corroborated | RS | `getBlock`, `getConfirmedBlock` | The index pins the **first verified block per slot**; a later served block disagreeing with the pinned entry (different bankhash, or different parent hash) is divergence — double-produce / foreign-chain splice / slot-shift evidence. First sight of a slot skips (the block is pinned by `observeBlock` after a clean pass) |
| `svm.commit.timeWindow` | corroborated | RS | `getBlock`, `getConfirmedBlock` | `blockTime` inside [cluster genesis bound (param, mainnet default), now + 2s skew (param)] and not >10s (param) before its verified parent's time. Null blockTime (very old blocks) skips |
| `svm.commit.slotEpoch` | corroborated | Det | `getEpochInfo` | `epoch × slotsPerEpoch + slotIndex == absoluteSlot`, `slotIndex ∈ [0, slotsPerEpoch)`. `slotsPerEpoch` is an operator param (default 432000) until the aux-fetch machinery lands (spec ground truth: `getEpochSchedule`) |

### 4.5 Finality — consistency against the upstream's own state-poller tips

The resolver is the upstream's `SvmStatePoller` (`FinalizedSlot()` / `LatestSlot()`) — ground truth the router already trusts. All ReorgSensitive (a poller can lag the node's true tip).

| ID | Level | Class | Methods | What it verifies |
|---|---|---|---|---|
| `svm.final.finalizedBound` | corroborated | RS | all envelope methods | A response served under a finalized commitment (request-named, or a method whose bare-call default is finalized) whose slot the resolver knows is **not** finalized → false-finality claim |
| `svm.final.slotAhead` | corroborated | RS | all envelope methods | Response `context.slot` never ahead of the upstream's latest observed slot (+1) |
| `svm.final.tipBound` | corroborated | RS | `getSlot`, `getBlockHeight` | A reported head never implausibly beyond the tip evidence |
| `svm.final.commitmentQuorum` | corroborated | Det | `getBlockCommitment` | The stake histogram wasn't invented: exactly 32 integer tiers, each ≥ 0, ≤ `totalStake`, **non-increasing with depth** (stake unlocks monotonically), positive `totalStake`. The ⅔-quorum confirmed/finalized *semantics* stay with the consumer — this check proves shape, not classification |
| `svm.final.rootSlotSanity` | corroborated | RS | `getVoteAccounts` | Per entry: `rootSlot ≤ lastVote` (you cannot root a slot you never voted through); `lastVote` not >128 slots (param) beyond our observed head (table from the future / wrong cluster). Null rootSlot (new validator) skips its comparison |

### 4.6 Continuity — cross-request progression

| ID | Level | Class | Methods | What it verifies |
|---|---|---|---|---|
| `svm.cont.headProgression` | corroborated | RS | `getSlot`, `getBlockHeight`, `getEpochInfo` | Heads move forward within their commitment bucket and never invert across buckets (`finalized ≤ confirmed ≤ processed`); a bare head poll is a **finalized claim** (Agave defaults) and is judged in the finalized bucket. High-water mark committed in `AfterPass` only after a fully clean pass — a tainted sighting never becomes the baseline |
| `svm.cont.minContextSlot` | corroborated | Det | all envelope methods | A request carrying `minContextSlot` must be answered from a bank whose `context.slot ≥ floor` — serving older is a **silent downgrade to a stale snapshot** |

*(Family assignments in code: `parentLink`/`heightMonotonic`/`headProgression`/`minContextSlot` are `FamilyContinuity`; `chainFollower`/`timeWindow`/`slotEpoch`/`finalizedBound`/`slotAhead`/`tipBound`/`commitmentQuorum`/`rootSlotSanity` are `FamilyCommitment`.)*

## 5. Verified-block index (`chainstate.go`)

Per-network, in-memory. A block enters **only after passing every enabled check** — the index is ground truth by construction. **First-seen pinning** per slot: a later different block at an anchored slot is judged by the link checks, never silently replaces the entry. Bounded: 8192 entries / 4096-slot span pruning. Also holds per-commitment-bucket head high-water marks (`NoteHead` only advances).

Phase-2 scope notes: single-process memory (multi-replica deployments don't share anchors yet); re-confirmation of disputed anchors via singleflighted canonical fetch lands with the aux machinery; the misbehaviors archive export is not wired.

## 6. Configuration

```yaml
projects:
  - id: solana
    networks:
      - architecture: svm
        svm:
          integrity:
            level: intrinsic            # off | intrinsic | corroborated | authoritative
            invalidBehavior:            # reorg-sensitive verdict map
              finalized: reject         # mismatch on finalized data = rejection
              unfinalized: recordOnly   # might be a reorg — record, serve
            observeOnly: true           # rollout mode: count would_reject, serve everything
            checks:
              svm.commit.timeWindow:
                enabled: true
                params:
                  minBlockTime: "1584230400"   # cluster genesis bound
                  futureSkewSec: "2"
                  parentTimeTolSec: "10"
              svm.commit.slotEpoch:
                params:
                  slotsPerEpoch: "432000"
              svm.final.rootSlotSanity:
                params:
                  maxVoteAhead: "128"
              svm.final.commitmentQuorum:
                onFailure: recordOnly    # per-check verdict override
```

- Boot validation fails on unknown level / check id / behavior string (a typo'd id can never silently disable the layer).
- The known cluster's genesis hash binds automatically from `common.KnownGenesisHash(chain, cluster)` — `svm.auth.genesisHash` skips (never self-reports) on unknown clusters.

## 7. Metrics

Reuses the EVM `erpc_integrity_*` series unchanged: `erpc_integrity_check_total{check, outcome}` (pass/skip/reject/would_reject/...), `erpc_integrity_violation_total{check, verdict, finality}`, plus fallback/saved counters shared with the EVM engine. Per-check overhead is observable via `erpc_integrity_overhead_seconds`.

## 8. Test & performance evidence

| Suite | Count | What it proves |
|---|---|---|
| Unit tests (`architecture/svm/integrity/*_test.go`) | 199 | every check's verdict matrix, the level-membership invariant, chain-state pinning/pruning, base58/wire-parser round-trips, config resolution, benchmark matrix |
| E2E (`erpc/svm_integrity_e2e_test.go`) | 12 | the full stack over simulated upstreams: corrupt-signature failover, self-parent rejection, genesis mismatch, parent-link break, chain-follower divergence, commitment-quorum fabrication, finalized-bound lie, token-program forgery, requested-sig mismatch, head-progression regression, clean-block pass, off-by-default |
| Benchmarks (`bench_test.go`) | 7 | production hot paths: BlockTxs parse caching, ContextSlot envelope caching, per-check costs |

Cost: `signatureVerify` ≈ **105 µs per signature** — the only material per-check cost; everything else is shape arithmetic on an already-decoded response. Steady-state aux requests: **zero** (no force-fetch, no aux fetches at this phase).

## 9. Phased roadmap (specified, not yet implemented)

- **Corroboration tier (6)** — `svm.corr.balanceJoin`, `blockhashJoin`, `leaderSchedule`, `sigListingJoin`, `sigStatusJoin`, `supplyJoin` (all need the aux-fetch machinery: cached partner joins, sampled cross-requests).
- **DAS tier (2)** — `svm.das.assetProofPath`, `svm.das.assetShape` (ship off until per-deployment vendor profiles exist).
- **Finality upgrade (3)** — `svm.final.stakeTableJoin`, `svm.final.voteEvidence` (the authoritative-tier resident; pre-Alpenglow only), `svm.final.votorCertificate` (post-Alpenglow BLS aggregate verification).
- **Write path (2)** — `svm.write.nonRetryableGuard`, `svm.write.simulateShape`.
- **Cache guard (1)** — `svm.cont.cacheGuard` (needs the cache-invalidation contract decided first).
- **Machinery** — singleflighted canonical fetches, per-epoch cached stake tables, aux-request budget, network profiles for cluster-specific quirk disables, misbehaviors archive export, multi-replica anchor sharing.

## 10. Honest limits — what cannot be checked without consensus

1. **Inclusion proofs for tx→block membership.** Solana RPC exposes no tx Merkle root. Authenticity (§4.1) + chain links (§4.4) + poller tips (§4.5) cover the threat surface differently, but "is this tx provably in *this* block" is unverifiable from RPC alone. Future: shred-level verification — the leader-signed Merkle root per FEC set is a client-verifiable inclusion commitment.
2. **Bank-hash recomputation** needs full account state; impossible for a gateway. We verify links, not hashes.
3. **Double-vote / duplicate-block fraud proofs** live in the shred layer, not RPC.
4. **Historical leader schedules** — `getLeaderSchedule` cannot serve old epochs' stake; leader verification only near head.
5. **Self-reported fields** (`isBlockhashValid.contextualValidity`, exact `blockTime`, simulation correctness) — shape checks and joins only.
6. **Post-Alpenglow finality evidence** — `voteEvidence` dies with Votor (off-chain BLS votes); the design degrades to `commitmentQuorum` + content-hash corroboration until Votor certificate RPCs land (track SIMD-0326). `getBlockCommitment` semantics may change under Votor.

## 11. Sources

- Solana JSON-RPC reference (getBlock, getBlockCommitment, getVoteAccounts, getEpochSchedule): https://solana.com/docs/rpc/http
- Agave commitment semantics (confirmed = ⅔ stake; finalized = 32-lockout root ancestry)
- SPL Token / Token-2022 AccountLayout and MintLayout (offsets verified against `@solana/spl-token`)
- Agave vote program instruction layouts (bincode; Vote/CompactUpdateVoteState/TowerSync carry the voted bank hash)
- SIMD-0326 / Alpenglow (Votor BLS off-chain votes; Rotor)
- EVM data-integrity engine: `specs/data-integrity/feature.md`, `architecture/evm/integrity/`

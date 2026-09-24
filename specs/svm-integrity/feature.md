# SVM Data Integrity — Specification

**Status**: Draft — design, pre-implementation
**Last revised**: 2026-09-24
**Companion**: [plan.md](./plan.md), [svm-evm-integrity-checks.md](./svm-evm-integrity-checks.md) (EVM ↔ SVM mapping)

---

## 1. Purpose

The **SVM data-integrity module** is the Solana counterpart of the EVM `data-integrity` engine (`architecture/evm/integrity/`): it validates that an upstream's response is internally consistent and consistent with the ledger it claims to describe, and rejects responses that provably cannot be correct — **without erpc participating in Solana consensus** (no TowerBFT/Votor voting, no stake, no replay). A rejected response becomes the standard content-validation error (`ErrEndpointContentValidation`) and the existing retry/failover machinery routes around the offending upstream.

Checks are one of two kinds, same contract as the EVM engine:

- **Deterministic** — a correct upstream can never fail it (crypto verification, schema/shape, arithmetic).
- **ReorgSensitive** — head-of-chain data can legitimately change (forks within the reorg window, skipped slots, commitment races); verdict resolved per finality (§6).

### What it catches that consensus cannot

- Corruption the upstreams *agree* on — consensus is majority-blind; the finality-evidence checks (§4.5) additionally verify claims *about* the quorum itself (a node asserting `finalized` for an unfinalized block).
- Corruption on requests with no fan-out at all — intrinsic checks are free and per-response.
- Field-level provable inconsistencies byte-comparison voting can't see: an ed25519-forged transaction, a block whose `previousBlockhash` doesn't chain to the verified parent, a token balance with the wrong decimals, a vote tally that doesn't reach the claimed commitment.

### Non-goals

- **Not** a quorum mechanism, **not** a data-rewriting layer (reject, never edit).
- **Not** a new trusted source: aux fetches go through the normal network path and the same upstream pool (group-scoped, §7).
- **Not** full node behavior: no bank-hash recomputation (needs full account state), no execution replay (except optional local simulation), no shred ingestion in v1 (future work, §12).

## 2. Protocol primer — why SVM checks are not EVM checks

Six protocol deltas drive the whole catalog. Details live in the per-check entries (§4).

| # | Delta | Design consequence |
|---|-------|-------------------|
| D1 | **No tx/receipt Merkle roots in RPC.** `getBlock` exposes `transactions`, `signatures`, `blockhash` — no `transactionsRoot`, no accumulator, no sync committee. | EVM's strongest pillar (`transactionsRootConsistency` / `receiptsRootRecompute`) has **no SVM equivalent**. Inclusion of a tx in a specific block is unprovable from RPC data alone. The substitute pillars are signature authenticity (D2), bank-hash chain anchoring (D3), and finality evidence (D5). |
| D2 | **Every transaction carries real signatures** (ed25519; secp256k1 for a small class). Signer pubkeys are `accountKeys[0..numRequiredSignatures)`; `signatures[i]` covers the serialized message. Signers are never in address lookup tables (protocol rule), so signer index math holds for v0 messages. | Authenticity is *stronger* than EVM sender-recovery: a fabricated tx fails ed25519 verification outright. This is SVM's pillar-1 substitute for the missing Merkle membership proof — on the authenticity axis. What it does not give: inclusion in a specific block. |
| D3 | **The RPC `blockhash` is the bank hash** — the value transaction `recentBlockhash` references and the value vote instructions reference. `previousBlockhash` links parent→child. PoH hashes are not exposed over RPC. | Chain integrity = linking `previousBlockhash` to a locally cached, previously verified parent **by hash**, never by slot arithmetic (skipped slots are legal gaps: `getBlock` returns null for them). Bank hashes cannot be recomputed client-side; we verify *links*, not *hashes*. |
| D4 | **Three clocks: `slot`, `blockHeight`, `epoch`.** `blockHeight` increments only for produced blocks (skipped slots don't advance it); `epoch = floor(slot / slotsPerEpoch)` post-warmup (432,000 slots ≈ 2 days on mainnet). | Shape and continuity checks must never confuse the counters (erpc already lived this bug: a slot-tip floor applied to `getBlockHeight` broke transaction-expiry checks). `blockHeight == parent.blockHeight + 1` along the hash chain is exact; slot gaps along the same chain are legal. |
| D5 | **Finality is stake-weighted and visible in-band.** `confirmed` ≈ ≥⅔ of activated stake voted the bank hash; `finalized` = the bank is an ancestor of a rooted (32-lockout) vote. RPC serves `getBlockCommitment` (32-depth stake histogram + `totalStake`), `getVoteAccounts` (per-validator `activatedStake`), and — pre-Alpenglow — the blocks themselves carry vote program transactions that embed the voted bank hash. | SVM can do something EVM cannot: **verify finality evidence** against a stake table over ordinary RPC, partially compensating for D1. |
| D6 | **Program-derived state.** SPL Token / Token-2022 accounts must be owned by their token program; ATAs are PDAs `findProgramAddress([wallet, tokenProgram, mint], ATAProgram)`; v0 messages add address lookup tables (`loadedAddresses` in meta). | A second deterministic structural family: owner-program consistency, ATA derivation, decimals consistency. Cheap; aux cached. |
| D7 | **Alpenglow (SIMD-0326, mainnet late 2026).** Replaces PoH + TowerBFT with Votor (BLS-aggregated, **off-chain votes — per-slot vote transactions disappear**) and Rotor. | `voteEvidence` dies with Alpenglow. The engine must detect the upgrade and degrade to commitment-quorum + corroboration until Votor finality certificates are exposed over RPC (§10). |

## 3. Check engine

Same contract as the EVM engine, one mental model for operators:

- Checks self-register (`register()` in `check.go`) into a per-method registry. `Validate` runs every enabled, applicable check over a **single decode** of the response (`Decoded`, decode.go — lazy accessors over gagliardetto `rpc.GetBlockResult` / `TransactionWithMeta` / `Account` plus the originating request's params).
- Each check declares **ID** (stable, config + `common.RegisterIntegrityCheckID`), **Family** (`structural | authenticity | shape | commitment | continuity | corroboration`), **Class** (`Deterministic | ReorgSensitive`), **Methods**, and aux requests.
- Levels: `off → intrinsic → corroborated → authoritative`, each enabling its row plus all lower rows (membership test-enforced, as in EVM `levels.go`).

### Outcomes

Identical to EVM (`erpc_integrity_check_total{check,outcome}`): `pass` (earned), `skip` (cold cache / unmodelled — `Skipped` sentinel), `reject`, `soft_flag`, `reconfirmed`, `off`. The pass/skip split is what turns "no rejects" into "N verified against anchors/evidence, 0 mismatches".

### Chain-safety invariant (do not break)

Anything a check cannot fully model **skips, never rejects**: Firedancer-vs-Agave response deltas (null `blockTime`, partitioned rewards via `numRewardPartitions`), `encoding` variants (`json` vs `jsonParsed` vs `base64`), `maxSupportedTransactionVersion` unset (v0 blocks error rather than parse), warmup-epoch history, DAS vendor extensions. An integrity module must never reject valid data. Per the EVM engine's operating rule: **a check rejecting across ALL upstreams of one network is a protocol quirk; scattered per-upstream rejects are real catches.**

## 4. Check catalog

IDs are `svm.<family>.<name>`; stable once shipped. **Aux** = extra upstream requests (cached where noted). Level column: `I` intrinsic, `C` corroborated, `A` authoritative.

### 4.1 structural — Deterministic

| ID | L | Methods | Verification (protocol detail) | Catches |
|----|---|---------|-------------------------------|---------|
| `svm.struct.blockShape` | I | `getBlock`, `getConfirmedBlock` | `blockhash`/`previousBlockhash` base58 32-byte, distinct; `parentSlot < slot`; `blockHeight` ≥ parent `blockHeight`+1 (exact +1 when the chain anchor is known, §4.4); `transactions`/`signatures` non-nil; `blockTime` null-or-plausible (leader-recorded, tolerance not exactness); `version` ∈ {"legacy", 0}. | Fabricated blocks, self-loop blocks, slot/height confusion |
| `svm.struct.txShape` | I | `getBlock`, `getTransaction` | Message header consistent (`numRequiredSignatures ≤ len(accountKeys)`, readonly counts in bounds); `len(signatures) == numRequiredSignatures`; `recentBlockhash` is a valid 32-byte base58 bank hash; v0 messages carry the `0x80` prefix + `addressTableLookups`; meta carries `loadedAddresses` for v0; signer indices never resolve into lookup tables (D2 rule). | Truncated/corrupt txs, version confusion, underflow garbage |
| `svm.struct.sigUniqueness` | I | `getBlock` | All signatures unique within a block (duplicate = leader misbehavior or forgery). With `transactionDetails: "signatures"`, `signatures[]` must equal the txs' signatures element-wise (count + order). | Double-inclusion, proxy list fabrication |
| `svm.struct.tokenOwner` | I | `getBlock` (meta), `getAccountInfo`, `getTokenAccountBalance`, `getTokenAccountsByOwner`, `getTokenSupply` | Token-account `owner` ∈ {`TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA`, `TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb`}; `pre/postTokenBalances` entries for one mint agree on `decimals`; `uiTokenAmount.uiAmount == amount · 10^-decimals` computed in integer-safe arithmetic (never float). | Phantom token accounts, decimal-shift scams, balance fabrication |
| `svm.struct.ataDerivation` | I | `getAccountInfo`, `getTokenAccountsByOwner`, `getTokenAccountBalance` | If an account is ATA-shaped (token-program-owned PDA), its address must equal `findProgramAddress([wallet, tokenProgram, mint], ATokenGPvbdGVxr1b2hvZbsiqW5xWH25efTNsLJA8knL)` for the wallet/mint its state names. Pure local derivation; no aux. | Address-squatting, lookalike account injection |
| `svm.struct.rewardShape` | I | `getBlock`, `getInflationReward` | `commission ≤ 100`; `rewardType` ∈ {Fee, Rent, Staking, Voting}; lamports/`postBalance` parse as u64; the same (account, epoch) pair agrees between `getBlock.rewards` and `getInflationReward` when both are seen. | Reward fabrication, inflation tampering (weak solo — corroborate) |

### 4.2 authenticity — Deterministic (SVM pillar 1)

| ID | L | Methods | Verification | Catches |
|----|---|---------|-------------|---------|
| `svm.auth.signatureVerify` | I | `getBlock`, `getTransaction` | **ed25519** (plus secp256k1 where the signer key is a secp key) batch verification: `signatures[i]` over the deterministically reserialized message bytes, pubkey `accountKeys[i]`, `i < numRequiredSignatures`; for v0 the verified payload includes the resolved lookup addresses per the message-format spec. Whole-block batch (~2–4k sigs) is milliseconds. | Fabricated/spoofed transactions, injected history, relay tampering of any tx field |
| `svm.auth.genesisHash` | I | `getGenesisHash` | Equals the hardcoded cluster genesis — mainnet-beta `5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2Nyd`, devnet `EtWTRABZaYq6iMfeYKouRu166VU2xqa1wcaWoxPkrZBG`, testnet `4uhcVJyU9pJkvQyS88uRDiswHXSCkY3zQawYjkWH1v7F`; unknown clusters → skip. | Serving the wrong cluster entirely |

`signatureVerify` is the single highest-value check in this spec. It replaces, for the authenticity axis, what EVM gets from `transactionsRoot` membership: a response whose transactions all verify is provably signed by the claimed keys. What it deliberately does **not** claim: that the tx is included in *this* block (D1) — that's §4.4 + §4.6.

### 4.3 shape — Deterministic

| ID | L | Methods | Verification | Catches |
|----|---|---------|-------------|---------|
| `svm.shape.magnitude` | I | `getBlock`, `getBlocks`, `getSignaturesForAddress`, `getBlockCommitment`, `getEpochInfo` | Counts/indices within physical bounds: `len(transactions)` under the per-block ceiling, `limit` params ≤ 1,000, `getBlockCommitment.commitment` length exactly 32, `absoluteSlot ≥ 0`, no u64 wraparound signatures (`0xffffffff…`). | 32-bit underflow garbage (same class as EVM `indexMagnitude`) |
| `svm.shape.commitmentParam` | I | all commitment-bearing methods | Param ∈ {processed, confirmed, finalized} + legacy aliases normalized exactly like agave; post-injection shape verified (erpc's commitment injection already defaults it — assert, don't re-do). | Commitment smuggling (processed data served as finalized) |
| `svm.shape.slotEncoding` | I | slot/`context.slot`-bearing methods | Base58/numeric fields decode; `result.context.slot` consistent with method semantics (skipped-slot aware). | Type confusion |

### 4.4 commitment — Deterministic given local anchor (ReorgSensitive at head)

| ID | L | Methods | Verification | Catches |
|----|---|---------|-------------|---------|
| `svm.commit.chainLink` | C | `getBlock`, `getConfirmedBlock` | `previousBlockhash` == locally cached bank hash of the parent **block, looked up by hash** (never slot arithmetic — skipped slots make slot math wrong). First sight of a block seeds the follower store. | Orphan fabrication, foreign-chain splicing, slot-shift attacks |
| `svm.commit.chainFollower` | C | background (all getBlock traffic) | Per-upstream follower (§7): each new head anchor must extend the previous anchor; same-slot-different-bankhash from one upstream = fork evidence → flag + cross-upstream confirm; anchors deepen to "safe" only after finality evidence (§4.5) or `finalityDepth` slots. | Long-range splicing, double-produce evidence, stale-head replay |
| `svm.commit.heightMonotonic` | C | `getBlock` | Along the anchor chain: `blockHeight == parent.blockHeight + 1`; `slot` strictly increasing along the hash chain (gaps legal). | Height/slot rewinds |
| `svm.commit.slotEpoch` | C | `getBlock`, `getEpochInfo`, `getEpochSchedule` | `epoch == floor(slot / slotsPerEpoch)` from `getEpochSchedule` (cached, immutable; warmup epochs predate mainnet history). Aux: `getEpochSchedule` once. | Epoch mislabeling |
| `svm.commit.timeWindow` | C | `getBlock` (`blockTime`), `getBlockTime` | Within [genesis 2020-03-16, now + 2-slot skew]; non-decreasing along the anchor chain with tolerance (leader-recorded, not trustworthy to the second); null legal. | Time travel, backdated history |

### 4.5 finality evidence — the SVM-specific pillar (Deterministic given aux stake data)

| ID | L | Methods | Verification (protocol detail) | Aux | Catches |
|----|---|---------|-------------------------------|-----|---------|
| `svm.final.commitmentQuorum` | C | `getBlockCommitment` | Shape: `commitment` length 32, u64s, `commitment[i] ≤ totalStake`, non-increasing with depth. Semantics: the array is a stake histogram by vote-lockout depth; **confirmed ⇔ `commitment[0] ≥ ⅔·totalStake`; finalized ⇔ the deepest tier (32 confirmations, rooted) `commitment[31] ≥ ⅔·totalStake`**. The served block's claimed commitment (from context/our follower) must match what the histogram supports. | none | False finality claims, commitment histogram tampering |
| `svm.final.stakeTableJoin` | C | `getVoteAccounts`, `getBlockCommitment` | `Σ activatedStake(current ∪ delinquent) ≥ totalStake` (reported total may not exceed the table sum; tolerate small drift for activation lag); table cached per epoch (epoch boundary via `getEpochInfo`). | `getVoteAccounts` (cached/epoch) | Invented `totalStake`, stale stake tables |
| `svm.final.voteEvidence` | A | `getBlock` (pre-Alpenglow only) | Vote txs are txs to the vote program `Vote111111111111111111111111111111111111111` (opcode ∈ Vote / CompactUpdateVoteState / TowerSync family, bincode layout); the instruction payload carries **the voted bank hash** — the same value as the block's `blockhash` field (D3). Tally per bank hash weighted by `activatedStake` (join on the vote account pubkey, which is a signer/account of its own vote tx); corroborates `getBlockCommitment` and the follower's finalized classification. Implementation note: pin the bincode layout against agave's vote program and **skip on parse failure** (chain-safety), never half-parse. | `getVoteAccounts` (cached/epoch) | Deep-finality spoofing when the commitment endpoint lies |
| `svm.final.rootSlotSanity` | C | `getVoteAccounts` | `rootSlot ≤ lastVote`; `lastVote` not implausibly ahead of our head slot (> N slots → table from the future / wrong cluster); delinquent entries keep shape. | none | Future-dated stake tables |

### 4.6 corroboration — needs a second source (aux upstream or join)

`corroborated` level. Never hard-fail on their own at moving head; on finalized pins, disagreement → `soft_flag` + suspect score (it proves one side wrong, not which).

| ID | L | Methods | Verification | Aux | Catches |
|----|---|---------|-------------|-----|---------|
| `svm.corr.balanceJoin` | C | `getBalance` ↔ `getAccountInfo` | Same pubkey+commitment → same lamports. | `getAccountInfo` (cached) | Balance-only tampering |
| `svm.corr.sigStatusJoin` | C | `getSignatureStatuses`, `getTransaction` | Status slot/commitment consistent with `getBlock(slot)` containing the signature; `err` consistent with `meta.err`; per-signature `confirmationStatus` monotone over time (processed→confirmed→finalized, never backwards). | `getBlock` | Fake confirmations, status forgery |
| `svm.corr.sigListingJoin` | C | `getSignaturesForAddress` | Sampled signatures resolve via `getTransaction` and the claimed address ∈ accountKeys ∪ loadedAddresses; listing slot/blockTime consistent with the fetched tx. | `getTransaction` (sampled) | Listing fabrication |
| `svm.corr.blockhashJoin` | C | `isBlockhashValid`, `getLatestBlockhash`, `getRecentBlockhash` | Returned blockhash valid 32-byte base58 **and** present in our follower cache or matching a second upstream's head. `contextualValidity` is self-reported by definition — join-verified only. | cache / 2nd upstream | Expired-blockhash claims, head substitution |
| `svm.corr.supplyJoin` | C | `getTokenSupply`, `getTokenLargestAccounts` | Supply ≥ Σ top accounts within sanity band; amount/decimals consistent with the mint account. | `getAccountInfo` (mint) | Supply inflation |
| `svm.corr.leaderSchedule` | C | `getLeaderSchedule`, `getSlotLeaders` | Schedule length == `slotsPerEpoch`; every leader pubkey ∈ cached validator set. **Current/recent epoch only** — RPC cannot serve historical stake distributions, so old epochs are unverifiable by design (skip, not pass). | `getVoteAccounts` (cached) | Schedule fabrication near head |

**Reuse, don't duplicate**: erpc's consensus executor already does content-hash quorum across upstreams; `svm.corr.*` covers only what *one* upstream plus erpc-controlled aux can verify. Multi-upstream agreement stays in the consensus path, enabled or not.

### 4.7 continuity — ReorgSensitive

| ID | L | Methods | Verification | Catches |
|----|---|---------|-------------|---------|
| `svm.cont.headProgression` | C | `getSlot`, `getEpochInfo`, `getBlockHeight`, `getBlocks`, `getFirstAvailableBlock` | Slot ≥ last-seen (per upstream, reorg tolerance); `blockHeight ≤ slot` always and `slot − blockHeight` within the network-observed band (mainnet: tens of millions — config param); `getBlocks` strictly increasing and inside `[firstAvailableBlock, head]`; `getFirstAvailableBlock` ≤ min served slot. | Stale-head replay, height/slot swaps |
| `svm.cont.cacheGuard` | C | cache write path (all reads) | Cached responses served only if their pin (slot) is outside the reorg window **or** finality-stamped by the follower; moving-head TTLs remain owned by the existing SVM cache policy (`neverCacheMethods`, finality TTLs) — this is the integrity-side assertion. | Serving rolled-back data as fresh |

### 4.8 write path & simulation — recordOnly by definition

| ID | L | Methods | Verification | Catches |
|----|---|---------|-------------|---------|
| `svm.write.nonRetryableGuard` | I | `sendTransaction`, `sendRawTransaction`, `requestAirdrop` | Existing non-retryable-write guard stays; integrity adds shape (signature is valid base58 32B; airdrop signature decodes) and optional **read-back corroboration**: post-send, `getSignatureStatuses` on the same upstream must land the sig within N slots (config, off by default). | Broken write plumbing (double-spend risk is un-rulable-out solo — see §11) |
| `svm.write.simulateShape` | C | `simulateTransaction` | `err` xor (`accounts` + `returnData`); `unitsConsumed > 0` on Ok; log array shape; `replacementBlockhash` fields sane. Simulation *correctness* is unverifiable without local execution. | Malformed sim responses |

### 4.9 DAS / Metis module — optional, off by default

| ID | L | Methods | Verification | Catches |
|----|---|---------|-------------|---------|
| `svm.das.assetProofPath` | C | `getAssetProof`, `getAssetProofs` | Recompute the Merkle path leaf→root from `proof` + `node_index` (keccak256 pair ordering per the compression program); root stable across repeated calls; root vs tree account via `getAccountInfo` join. | Proof forgery (path math is self-verifying against the provided root) |
| `svm.das.assetShape` | C | `getAsset`, `getAssetsByOwner` | Leaf schema version known; `id`/`owner`/`data.struct` decodable; compression fields consistent. | Garbage assets |

## 5. Method → checks matrix

| Method | hardReject guards (finalized pin) | recordOnly at moving head |
|---|---|---|
| `getBlock` / `getConfirmedBlock` | blockShape, txShape, sigUniqueness, signatureVerify, chainLink, heightMonotonic, slotEpoch, timeWindow, tokenOwner, rewardShape, voteEvidence | fork-window chainLink disputes |
| `getTransaction` | txShape, signatureVerify, sigStatusJoin | — |
| `getAccountInfo` | tokenOwner, ataDerivation | balanceJoin partner |
| `getBalance` | balanceJoin | head staleness |
| `getTokenAccountBalance`, `getTokenAccountsByOwner`, `getTokenSupply`, `getTokenLargestAccounts` | tokenOwner, ataDerivation, supplyJoin | — |
| `getLatestBlockhash`, `getRecentBlockhash`, `isBlockhashValid` | shape | blockhashJoin |
| `getBlockCommitment` | commitmentQuorum, stakeTableJoin | — |
| `getVoteAccounts` | shape, rootSlotSanity | stakeTableJoin partner |
| `getEpochInfo`, `getEpochSchedule`, `getSlot`, `getBlockHeight`, `getBlocks`, `getFirstAvailableBlock`, `getBlockTime` | shape, slotEpoch, headProgression | time window near head |
| `getSignaturesForAddress`, `getSignatureStatuses` | shape | sigListingJoin, sigStatusJoin |
| `getLeaderSchedule`, `getSlotLeaders` | shape | leaderSchedule (recent epochs only) |
| `getInflationReward` | rewardShape | epoch-boundary joins |
| `getGenesisHash` | genesisHash | — |
| `sendTransaction`, `sendRawTransaction` | nonRetryableGuard | read-back corroboration (opt-in) |
| `simulateTransaction` | — | simulateShape |
| `getAsset`, `getAssetProof(s)` (DAS) | assetProofPath (vs root) | assetShape |

## 6. Verdicts, reorg policy, corroborate-before-verdict

`invalidBehavior: {finalized, unfinalized}` maps finality → `reject | soft-flag | off` for **ReorgSensitive** checks (Deterministic checks always reject; per-check `onFailure` overrides). Safe default: `{finalized: reject, unfinalized: soft-flag}`.

**Corroborate-before-verdict** carries over from the EVM engine, retargeted at slots: a `chainLink` violation anchored to a cached parent may mean the follower is stale after a routine fork. Before applying a strict verdict, the engine re-confirms the disputed parent via a singleflighted, cooldown-bounded canonical fetch (by slot), adopts whatever the network now serves, and re-runs the check. A fork clears (`reconfirmed`); a mismatch that survives the fresh anchor is genuine. Without this, strict `unfinalized: reject` self-blocks after every fork (the anchor adopts only from passing responses, so it never recovers — the EVM engine observed exactly this failure mode as all-upstream reject bursts; SVM's faster slot cadence makes the same trap sharper).

Inside the fetch-rate cooldown the re-confirm reports its cached answer and the engine degrades would-be rejects to soft-flags rather than rejecting on unverified state.

**Finality semantics for the split** (D5): a read pinned `finalized` may be strictly rejected because Solana finality is cryptoeconomic (rooted ⅔-stake lockouts); a read pinned `confirmed` or `processed` is still reorgable and soft-flags.

## 7. Follower store (SVM ChainView analog)

A bounded, reorg-aware, **group-scoped** store (same scoping rationale as EVM §6: heterogeneous node families produce cross-family false mismatches otherwise):

- `bankhash → {slot, blockHeight, blockTime, txCount}` content-addressed entries;
- `slot → bankhash` pins only for blocks that passed validation (hash-keyed adoption, so orphans can't be adopted by slot collision — skipped slots mean a slot pin alone is ambiguous);
- window-bounded by `integrity.reorgWindow` measured in **slots**;
- a changed bankhash at an anchored slot is a fork: adopt and roll back descendants;
- resolve-on-miss is singleflighted and multiplexed with concurrent user requests.

**Fetch-anchor invariant** (hard-learned on EVM, same shape here): an aux fetch must **prove it returned the requested entity** before being trusted, observed, or cached — a by-slot block fetch must return that slot; mismatched answers are "canonical unavailable" (skip), never evidence.

## 8. Configuration

```yaml
projects:
  - id: solana
    networks:
      - architecture: svm
        svm:
          integrity:
            level: authoritative          # off | intrinsic | corroborated | authoritative
            invalidBehavior: {finalized: reject, unfinalized: soft-flag}
            reorgWindow: 32               # slots
            finalityDepth: 32             # follower deepens anchors after N slots w/o evidence
            checks:
              svm.auth.signatureVerify: {enabled: true}
              svm.final.voteEvidence: {enabled: true}
            # misbehaviorsDestination, headerMode, profiles: same as EVM integrity
```

- **Validation at load**: unknown level / check id / behavior string fails boot (a typo'd level enabling *zero* checks was a real EVM incident — the workspace doc `level: intrinsic` gotcha).
- **Network profiles** (`svm/network_profiles.go`, mirroring EVM chain profiles): protocol-quirk disables ship as defaults per cluster (devnet/testnet tolerance bands, DAS availability, Alpenglow activation state).

## 9. Observability

Reuses the EVM metric surface unchanged, SVM check ids as labels:
`erpc_integrity_check_total{check,outcome}`, `erpc_integrity_violation_total{check,verdict,finality}`, `erpc_integrity_saved_total`, `erpc_integrity_failed_total`, `erpc_integrity_fallback_served_total`, `erpc_integrity_protocol_suspect_total` (+ `RecordExhaustion` all-upstream-reject detection), `erpc_integrity_aux_request_total{group,kind,method,finality,outcome}`, `erpc_integrity_overhead_seconds`. WARN log per hard reject / soft-flag with verbatim expected-vs-actual; `misbehaviorsDestination` JSONL archive.

Aux-request budget (steady state, per finalized block): `getVoteAccounts` 0 (cached/epoch), `getBlockCommitment` 0–1, `getAccountInfo` 0–2 (cache hits), `getEpochSchedule` 0 (cached). `signatureVerify` cost ≈ one ed25519 batch verify per block — negligible vs RPC latency.

## 10. Alpenglow transition (D7)

1. **Probe**: `getVersion` (Agave ≥ 4.x) + vote-tx absence streak on recent blocks → feature-flag table entry (mirrors how the EVM engine handles fork-activation rules: gated on an authoritative cutoff, never inferred from a single response).
2. **Degrade**: `svm.final.voteEvidence` → `off` automatically; finality stance falls back to `commitmentQuorum` + content-hash corroboration + follower depth.
3. **Upgrade**: when Votor finality certificates become RPC-queryable (SIMD-0326 follow-up), add `svm.final.votorCertificate` (BLS aggregate verification against the epoch validator set — a true `authoritative` check, and the closest SVM will have ever come to an EVM-style light-client proof).

## 11. Risks / protocol watch-items

- **Vote bincode drift**: vote instruction layouts have churned (Vote → CompactUpdateVoteState → TowerSync). Pin layouts against agave; parse-failure = skip (chain-safety), never half-parse. This is the SVM analog of the EVM engine's typed-envelope / hardfork-activation discipline.
- **blockTime nullability & client skew** (Firedancer-era gaps): plausibility bands, never exactness.
- **getBlock size**: full-block signature verification requires `maxSupportedTransactionVersion` set and `transactionDetails` ≠ "none"; when the operator config doesn't allow sig material, `signatureVerify` skips (never silently "passes").
- **Leader schedule historical unavailability** (D5): old-epoch schedule checks skip by design; documented, not a gap we can close from RPC.
- **isBlockhashValid.contextualValidity, blockTime, simulation results, node health** are self-reported by definition — shape + joins only.
- **False-positive asymmetry** (EVM finding §10.4 applies verbatim): all-upstream rejects defeat failover 1:1 → new checks ship `soft-flag` per network until they have a clean window, then promote (`network_profiles.go` records *why*).
- **Write-path double-spend**: read-back corroboration narrows but cannot eliminate the risk that a send landed on a fork — only the wallet's own resubmission discipline can.

## 12. Known limitations / future work

1. **Inclusion proofs**: none exist over RPC (D1). The upgrade path is shred-level: Turbine shreds are leader-signed Merkle FEC sets, so a Shredstream/geyser ingestion channel would give erpc a *client-verifiable inclusion commitment* per block — the closest thing SVM can have to an authoritative anchor without consensus.
2. **Bank-hash recomputation**: impossible without full account state; we verify links, not hashes.
3. **Double-vote / duplicate-block fraud proofs**: slashable evidence lives in the shred layer, not RPC.
4. **Votor certificates**: pending Alpenglow RPC surface (§10).
5. **DAS vendor variance**: Metis/DAS responses vary across providers; the module ships `off` until profiles exist per deployment.

## 13. Implementation status (updated 2026-09-25)

Lands on branch `feat/svm-integrity` (fork `andreclaro/erpc`), opt-in per
network, nothing runs without config. Every group has unit suites plus an
end-to-end failover test (`erpc/svm_integrity_e2e_test.go`) proving a
violating upstream is rejected and the request lands on the honest one.

| Commit | Group | Checks |
|---|---|---|
| `ecdd1d5` | Phase 0/1 | engine, wiring, config; `blockShape`, `txShape`, `sigUniqueness`, `signatureVerify`, `genesisHash`, `magnitude`, `commitmentParam`, `slotEncoding` |
| `50e7729` | A — slot-chain continuity | `commit.parentLink`, `commit.heightMonotonic` |
| `8242843` | B — finality vs poller tips | `final.finalizedBound`, `final.slotAhead`, `final.tipBound`; **handler fix: integrity judges a response BEFORE its context slot is harvested** (a rejected response must never seed the poller with poison) |
| `853cd3f` | C — token authenticity | `auth.tokenProgram`, `struct.tokenMintShape`, `struct.tokenAccountShape` (extension-carrying mints skipped, not guessed) |
| `0472deb` | D — request/response binding | `struct.requestedSigMatch`, `shape.blocksLimit`, `struct.rewardsShape` |
| `df3b3eb` | E — follower / time / epoch | `commit.chainFollower` (per-network first-verified pin, not the spec's per-upstream follower — documented deviation), `commit.timeWindow` (bounds are params: cluster genesis times differ), `commit.slotEpoch` (slotsPerEpoch param until aux fetch) |
| `1d6fd62` | F — finality-evidence shape | `final.commitmentQuorum` (32 tiers, ≤ totalStake, non-increasing), `final.rootSlotSanity` (rootSlot ≤ lastVote; lastVote ≤ head + maxVoteAhead) |
| `5d9a756` | G — continuity | `cont.headProgression` (per-commitment head store, backwards = reorg evidence, record-by-default), `cont.minContextSlot`, `struct.heightVsSlot` |

**22 checks total** (8 intrinsic + 14 across groups A–G).

**Deferred, with rationale** (all noted in `levels.go`):
- `final.stakeTableJoin`, `corr.balanceJoin/sigStatusJoin/blockhashJoin/supplyJoin`
  — require cross-request aux fetch/caching machinery; the module's contract
  is "no force-fetch at corroborated tier", so these land with the
  authoritative tier (Phase 3+).
- `final.voteEvidence` — authoritative tier, needs vote-instruction bincode
  parsing pinned against agave layouts (§11 drift risk).
- cacheGuard — cache-path concern, out of scope for the serving-path module.

Battle-scars worth keeping (each cost a debugging round): first-verified-wins
pinning means reorgs are recorded evidence, not replacements; integrity must
judge before the poller harvests a response's context slot; scan ALL views —
one clean view must not mask a poisoned sibling; ambiguous wire shapes
(extension mints vs token accounts, base58 wire txs) skip rather than guess.

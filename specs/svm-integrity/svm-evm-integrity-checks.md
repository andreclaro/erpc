# SVM ↔ EVM Integrity Checks — Comparison

**Status**: Reference — companion to [feature.md](./feature.md)
**Last revised**: 2026-09-24

One-sentence thesis: **EVM integrity anchors on recomputed Merkle roots (transactionsRoot/receiptsRoot/blockHash); SVM has no tx/receipt roots in RPC, so it anchors on signature batch verification + bank-hash chain links + stake-weighted finality evidence — trading EVM's *membership proofs* for *authenticity proofs* and in-band quorum evidence.**

## 1. Pillar mapping

| EVM pillar | SVM equivalent | Why |
|---|---|---|
| Commitment recompute (`transactionsRootConsistency`, `receiptsRootRecompute`, `blockHashRecompute`) | **None** — replaced by `svm.auth.signatureVerify` + `svm.commit.chainLink/chainFollower` | Solana RPC exposes no tx/receipt Merkle roots and the bank hash is not recomputable without full account state (feature.md D1/D3). Authenticity (every tx signed) + chain links (bankhash↔parent) + corroboration cover the same threat surface differently. |
| Membership proof (a tx is provably in a block) | **None over RPC** — closest: sig verify (tx is authentic) + chain anchor (block is chained) + content-hash corroboration. Future: shred-level Merkle (Shredstream). | See feature.md §12. |
| Authenticity (`senderRecovery`) | `svm.auth.signatureVerify` — **stronger** | ed25519 batch verification rejects forgeries outright; EVM sender recovery only proves a recoverable sender, not a signature. |
| Continuity (`parentHashLinkage`, `hashStability`, `timestampMonotonicity`) | `svm.commit.chainLink`, `chainFollower`, `timeWindow`, `heightMonotonic`, `cont.headProgression` | Same idea; SVM adds skipped-slot legality and slot/height/epoch three-clock discipline (D4). |
| Structural cross-reference (`sameBlockHash`, `txHashUniqueness`, `transactionIndexConsistency`, `logIndexContiguity`) | `svm.struct.blockShape`, `sigUniqueness`, `txShape` | Solana txs have no `logIndex`/`transactionIndex` fields (position = index), no logs bloom. |
| Finality / canonical grounding (`txPinConsistency`, `receiptVsBlock`, `getLogsCompleteness`) | `svm.final.*` (commitment histogram, stake-table join, vote tally) + `svm.corr.*` joins | EVM finality is eventual (and post-Merge checkpointed); SVM finality is stake-weighted and *verifiable from RPC* — an evidence tier EVM doesn't have. |

## 2. Check-by-check mapping

**Direct analogs** (same job, SVM mechanics):

| EVM check | SVM analog |
|---|---|
| `indexMagnitude` | `svm.shape.magnitude` |
| `headerFieldShapes` | `svm.struct.blockShape` |
| `schemaConformance` | `svm.struct.txShape` (+ `svm.shape.*`) |
| `txHashUniqueness` / `txFieldUniqueness` | `svm.struct.sigUniqueness` |
| `parentHashLinkage` | `svm.commit.chainLink` |
| `hashStability` | `svm.commit.chainFollower` |
| `timestampMonotonicity` | `svm.commit.timeWindow` |
| `blockByHashIdentity` / `blockByNumberIdentity` | *planned* block-by-slot identity assertions inside `chainLink` (requested slot must equal response slot) |
| `txByHashIdentity` / `receiptIdentity` | *planned* tx-by-signature identity inside `signatureVerify` wiring (requested sig must equal response sig) |
| `txPinConsistency` | `svm.commit.chainLink` + follower pins |
| `receiptVsBlock` | `svm.corr.sigStatusJoin` (tx vs containing block) |

**Replaced-by** (job exists, mechanism differs):

| EVM check | SVM replacement | Note |
|---|---|---|
| `senderRecovery` | `svm.auth.signatureVerify` | strictly stronger |
| `blockHashRecompute` | `svm.commit.chainLink` + follower | we verify links, not hashes — bank hash not recomputable client-side |
| `getLogsCompleteness` | *partial* `svm.corr.sigListingJoin` | no log bloom; block tx list is the canonical carrier; Solana has no `getLogs` |
| `transactionsRootConsistency` / `transactionsRootRecompute` | — (no protocol equivalent) | the D1 gap; see feature.md §12 for the shred-level future |
| `receiptsRootRecompute` / `receiptVsBlock` (root axis) | — | receipts don't exist as a root-committed structure in SVM |

**EVM-only** (no SVM counterpart, none needed):

| EVM check | Why absent |
|---|---|
| `bloomEmptiness`, `bloomMatch`, `logFieldShapes`, `logIndexContiguity`, `logMetadata` | no logs/bloom in Solana blocks (`meta.logMessages` shape is covered by `txShape`) |
| `getLogsFilterSanity` | no `getLogs`; closest filter surface (`getSignaturesForAddress`, `getProgramAccounts` memcmp) unguarded in v1 |
| `baseFeeDerivation` | no EIP-1559 base fee; fee = signatures + priority fee (50% burn). *Future:* `svm.shape.feeSanity` vs compute-budget instructions |
| `traceBlockGasReconciliation`, `traceFrameShape` | no `debug_trace*` on Solana; `simulateTransaction` shape only (`svm.write.simulateShape`) |

## 3. SVM-only checks (no EVM counterpart)

| Check | What EVM can't do this |
|---|---|
| `svm.auth.genesisHash` | cluster genesis constants per network |
| `svm.struct.tokenOwner`, `ataDerivation` | program-derived state invariants (SPL Token owner, ATA PDA derivation) |
| `svm.final.commitmentQuorum` | verify `getBlockCommitment` histogram (⅔ stake at depth) — finality evidence from RPC |
| `svm.final.stakeTableJoin`, `rootSlotSanity` | stake-table consistency for the quorum math |
| `svm.final.voteEvidence` | tally in-block vote txs (bank-hash-carrying) weighted by stake — **dies at Alpenglow**, probe planned (feature.md §10) |
| `svm.corr.balanceJoin` | direct balance↔account join (EVM leaves state to consensus) |
| `svm.corr.blockhashJoin` | recent-blockhash cache join (EVM has no expiry blockhash) |
| `svm.corr.leaderSchedule` | leader schedule vs validator set (recent epochs only) |
| `svm.cont.headProgression` | slot/height/epoch three-clock progression with skipped-slot band |
| `svm.das.assetProofPath` | DAS Merkle proof recomputation (EVM NFTs have no RPC-served proofs) |

## 4. Machinery shared unchanged

Levels (`off→intrinsic→corroborated→authoritative`), `invalidBehavior {finalized: reject, unfinalized: soft-flag}`, Deterministic/ReorgSensitive classes, the six outcome metrics (`pass/skip/reject/soft_flag/reconfirmed/off`), `corroborate-before-verdict` with cooldown degradation, group scoping, fetch-anchor invariant, chain profiles, shadow-window-then-promote discipline, exhaustion detection (`erpc_integrity_protocol_suspect_total`), and the full `erpc_integrity_*` metric surface. One config schema, one mental model — the protocol specifics live entirely inside the checks.

# 09 — Existing eRPC Integrity Checks vs. This Proposal

- **Status:** DRAFT v0.1
- **Date:** 2026-09-18
- **Code basis:** upstream `erpc/erpc` main @ `38db1695` (branch rebased 2026-09-18). All `file:line` references are against that tree.
- **Audience:** implementers, security reviewers
- **Companion docs:** this proposal (`00`–`08`); the user-facing description of the existing system is `docs/pages/config/failsafe/integrity.mdx`.

---

## 1. Thesis: one difference reorganizes everything

The existing engine (`architecture/evm/integrity/`) has **33 checks**, and most of them are *good checks* — this proposal keeps their algorithms. What it changes is **what each check compares against**:

| | Existing engine | This proposal |
|---|---|---|
| Block hash / roots / bloom | Recomputed from the **response's own fields**; the recomputed hash is trusted because… it matches the response | Recomputed from the response's fields, compared to an **anchor-attested header** at that height |
| Continuity (parentHash, hash stability, pins) | Compared against the **ChainView** — a number→hash pin built from *other RPC-supplied blocks* cross-checked against each other | ChainView pin is subordinated to the **HeaderOracle**; a conflict with an anchored header is *evidence of lying*, not a reorg question |
| Finality | Read from the **serving upstream's self-reported** finalized head (`integrity_resolver.go:37-41`) | Defined **only** by the anchor (`FinalityUpdate`); upstream block tags never set finality |
| State reads (`eth_getBalance` etc.) | Not verified in the request path at all; a background probe checks only `keccak(accountProof[0]) == stateRoot` (`integrity_stateprobe.go:314`) — proves the node *holds a trie*, nothing about the value | Full **MPT proof verification** (`trie.VerifyProof`) against the anchored `stateRoot`, value-bound |
| `eth_call` / traces | Nothing (trace checks validate *shape* and gas reconciliation, not correctness) | Stateless **re-execution** over proven state (P4) |
| Fabricated-but-self-consistent chain | **Passes every check** — the docs' own defense is consensus voting, which the threat model (A3) shows fails under collusion/shared infra | Structurally impossible: fabricated headers conflict with the sync-committee-attested chain |

So the comparison below is not "their checks vs. our checks" — it is **their checks, re-anchored**, plus a set of new checks for surfaces that today have none.

---

## 2. The existing catalog, check by check

Level membership per `architecture/evm/integrity/levels.go:40-62`. "Det" = deterministic (always rejects on violation); "Reorg" = reorg-sensitive (governed by `invalidBehavior`). Abbreviated methods: **block** = `eth_getBlockByNumber`/`eth_getBlockByHash`, **receipts** = `eth_getBlockReceipts`, **receipt** = `eth_getTransactionReceipt`, **logs** = `eth_getLogs`, **txByHash** = `eth_getTransactionByHash`.

### 2a. Cryptographic recomputes — algorithm kept, comparison target changes

| Check (level, class) | Verifies today | Delta under proposal |
|---|---|---|
| `blockHashRecompute` (intrinsic, Det) — `checks_recompute.go:29-80` | keccak(RLP(header)) == claimed hash; comparison target is the response itself | Same algorithm; hash must additionally equal the **anchored header at that height**. Today a fabricated fork passes because its internal hash is consistent. |
| `transactionsRootRecompute` (intrinsic, Det) — `:90-127` | MPT root of full tx objects == header `transactionsRoot` | Unchanged algorithm; the header's root now comes from the anchor. Same skip-guards (hashes-only responses, txs the reference decoder can't model → skip, never pass). |
| `receiptsRootRecompute` (**authoritative**, Det) — `:137-180` | MPT root of response receipts == `receiptsRoot`, header **force-fetched by hash through the normal network path** (`resolveHeaderKind`, `integrity_chainview.go:541-588`) — i.e. corroborated against *another RPC fetch* | The force-fetched "canonical" header is today just a second upstream opinion. Under the proposal the comparison root is anchor-attested — this check upgrades from "two RPC sources agree" to "proved". Also becomes the ingest-time **log-completeness proof**. |
| `bloomMatch`, `bloomEmptiness` (intrinsic, Det) — `checks_receipts.go:142-185` | logsBloom recomputed from logs == declared; zero bloom ⟺ no logs | Kept as the cheap pre-screen before root recompute (`02` §4). |
| `senderRecovery` (intrinsic, Det) — `checks_authenticity.go:23-58` | ecrecover(sig) == reported `from`, gated on the tx hash recomputing | Kept unchanged; now runs inside an anchored context. |

### 2b. Structural / identity / shape — unchanged (nothing to re-anchor)

These checks validate internal consistency and request/response identity. They are orthogonal to the root of trust and survive verbatim:

| Check (level) | What it pins down |
|---|---|
| `schemaConformance` (intrinsic) — `checks_schema.go:17-34` | result decodes to the expected shape for aggregate methods |
| `indexMagnitude` (intrinsic) — `checks_index.go:12-33` | logIndex/transactionIndex ≤ plausible max (32-bit underflow; "the Amoy incident") |
| `sameBlockHash` / `txHashUniqueness` / `transactionIndexConsistency` / `logFieldShapes` / `logMetadata` (intrinsic) — `checks_receipts.go` | receipts of one block are mutually consistent; duplicate tx hashes; position/index match; field shapes; log↔receipt coordinates |
| `bloomEmptiness`, `logIndexContiguity` (intrinsic) — `checks_receipts.go:142-210` | zero bloom ⟺ no logs; global logIndex exactly 0..N-1 with no gaps |
| `transactionsRootConsistency` (intrinsic) — `checks_block.go:27-57` | non-empty `transactionsRoot` ⟺ block has txs; phantom/system-tx aware (Polygon/BSC state-sync, HyperEVM r=0x1) |
| `headerFieldShapes`, `txFieldUniqueness`, `txBlockInfo` (intrinsic) — `checks_block.go:61-139` | header hash sizes; unique 32B tx hashes; tx block coordinates == enclosing header |
| `getLogsFilterSanity` (intrinsic) — `checks_getlogs.go:37-64` | every returned log satisfies the request's own filter/range |
| `blockByHashIdentity` / `blockByNumberIdentity` / `txByHashIdentity` / `receiptIdentity` (intrinsic) — `checks_identity.go` | you got the block/tx/receipt you *asked for* (identity, not canonicality) |
| `headerConsensusInvariants` (intrinsic) — `checks_consensus_header.go:33-79` | per-chain consensus constants (emptyUncles hash, zeroDifficulty, blobGasMultiple) from chain profiles; unprofiled chain → **skip, never pass** |
| `traceFrameShape` (intrinsic) — `checks_trace.go:129-183` | callTracer frames well-formed; bounded depth/frame counts (CPU guard) |

Proposal note: these keep running at whatever level the operator selects; the proposal's per-method verification classes (`03` §2) subsume them under **V-inclusion**, verified at ingest and served from the verified cache.

### 2c. Continuity / corroborated — the largest semantic change

These are the checks whose *meaning* changes most, because their comparison target (the ChainView pin) is exactly the component whose root of trust the proposal replaces.

| Check (level, class) | Verifies today | Delta under proposal |
|---|---|---|
| `parentHashLinkage` (corroborated, Reorg) — `checks_continuity.go:32-75` | block N's parentHash == pin at N−1; skips inside the followed segment | Pin becomes anchor-descended. A linkage break against the *anchored* fork is no longer ambiguous ("reorg?") — it's evidence (`01` §6). Reorg-sensitive verdict stays for the unfinalized suffix. |
| `hashStability` (corroborated, Reorg) — `:80-106` | number's hash unchanged vs prior observation | Same; prior observation is now anchored-finalized data, so stability below finality is a *lie*, not a reorg. |
| `txPinConsistency` (corroborated, Reorg) — `checks_identity.go:158-182` | mined tx's blockHash == committed pin at its number | Same, re-anchored pin. |
| `getLogsCompleteness` (corroborated, Reorg) — `checks_getlogs.go:66-173` | response logs == filter ∩ **cached** canonical receipts (cache-only, zero fetches); finalized absent-block sweep | Cache-only corroboration is the weakest form of the idea ("our own traffic saw different logs once"). Under the proposal, `receiptsRootRecompute` against the anchored header is the completeness proof at ingest — omission is *detected*, not just cross-noted; the existing check remains as the opportunistic layer. |
| `baseFeeDerivation` (corroborated, Det) — `checks_consecutive.go:38-72` | baseFee == exact function of verified parent's gas params; needs parent inside the follower's contiguous segment | Same algorithm; "verified parent" becomes "anchor-verified parent". Chain-profile gating unchanged (check deleted for chains without a characterized fee model). |
| `timestampMonotonicity` (corroborated, Det) — `:80-105` | timestamp ≥ parent's (non-strict for batched L2s) | Same, re-anchored segment. |
| `traceBlockGasReconciliation` (corroborated, Reorg) — `checks_trace.go:79-124` | len(traces)==len(txs); Σ gasUsed ≥ header.gasUsed (lower bound — refund accounting makes equality false on honest data, measured) | Same; unchanged by anchoring. |

### 2d. The state probe — upgraded into the request path

| Component today | Proposal |
|---|---|
| `probeProof` (`integrity_stateprobe.go:271-323`): background-only, per-upstream, checks `keccak(accountProof[0]) == stateRoot` against the **follower-verified** header — i.e. a header that is itself just more RPC data cross-checked against itself. Telemetry only: advances a "state-proven head" gauge; a streak of 10 mismatches records misbehavior for scoring. No MPT path verification, no value binding, no storage proofs, never in the request path. | `ProofVerifier` (`02` §2): full `trie.VerifyProof` in the **request path**, account + storage, value-bound, against the **anchored** `stateRoot`; non-existence answers verified identically (closes the proof-of-empty lie); mismatch → evidence → hard cordon. The old probe's evidence streak is subsumed: a proof mismatch is *self-authenticating evidence*, no streak threshold needed. |

---

## 3. What the proposal adds that has **no** existing check

| New surface | Existing coverage | Proposal control | Spec |
|---|---|---|---|
| `eth_getBalance` / `getTransactionCount` / `getCode` / `getStorageAt` / `getProof` (V-state) | **None in the request path** (probe is telemetry, not gating) | MPT proof verification bound to anchored `stateRoot`; +1 `eth_getProof` per uncached read | `02` §2–3 |
| `eth_chainId` / `net_version` (V-static) | None | Compare to configured chainId; mismatch = lie | `03` §2 |
| `eth_call` / `eth_estimateGas` (V-execution) | None (shape/gas checks only) | Stateless re-execution over proven state; interim: consensus, labeled | `02` §6 |
| Finality honesty | None — finality is the upstream's self-reported tag (`integrity_resolver.go:37-41`) | Finality defined only by the anchor | `01` §6 |
| Freshness / anti-replay of proofs & responses | Partial (Layer-1 tip enforcement; cache age guards) | Proof ↔ requested block ↔ anchored header binding is mandatory | `02` §2 |
| L2 chains | "Leave integrity off" is the documented advice | `l1Committed` anchoring via L1 contracts, read through our own L1 anchor | `04` |
| BSC / SVM / unknown chains | Consensus voting (only mechanism) | Explicit `quorum` tier, labeled as such — same mechanism, honest labeling | `01` §2 |
| Punishment | Retryable error → scoring; dispute token bucket in consensus | Cryptographic evidence → **hard cordon** (bypasses voting thresholds); reuse of sit-out + misbehavior export | `03` §4 |
| Cache | Re-org aware | + **verified-bit**; no unverified→verified upgrade (INV-4) | `03` §4 |
| Client visibility | None (client must trust eRPC) | `X-ERPC-Verification` label; P7 proof-carrying responses | `03` §3, `08` |
| Advisory signals | None | P6 (dRPC signatures, Ankr vRPC/TEE) as *scoring inputs*, never a gate | `05` |

---

## 4. Enforcement model comparison

| Dimension | Existing engine | Proposal |
|---|---|---|
| Activation | Opt-in per project/network; no `integrity:` block ⇒ zero checks | Same opt-in shape; absence of config = `balanced` everywhere (weakest *safe* default — note the difference: the proposal's default still labels, the existing default runs nothing) |
| Granularity | Per-check: level presets + `checks.<id>.{enabled,onFailure,params}` | Per-class: three modes (`strict`/`balanced`/`observe`) + selector-shaped overrides (`matchMethod`/`matchFinality`) |
| Verdict vocabulary | `recordOnly \| hardReject \| off` (+ per-check `onFailure`); `reject`/`soft-flag` fail validation loudly since `2ae9a76f` (#1104) | Mode matrix + labels (`verified`/`provisional`/`quorum`/`meta`); fail-closed is a *first-class mode* (`strict`), not a per-check accident |
| Corrective behavior | `autoCorrectWhenPossible` (default true): recordOnly escalates to failover hunt; on exhaustion serves the flagged original (never an error) | Violation → evidence + cordon + retry another upstream; `balanced` serves provisional/quorum data **labeled**, degrades to consensus if the anchor is down |
| Rollout gate | `observeOnly` — absolute, outranks future checks | `observe` mode — equivalent; the absolute "no verdict may touch the response" property is preserved deliberately |
| Per-request client selection | Profiles via `X-ERPC-Integrity` header (`headerMode`) | **Removed, deliberately**: verification is not client-skippable; policy floor can only be tightened per request, never loosened (`03` §3) |
| Violation error | `ErrEndpointContentValidation` — retryable toward network, not same upstream (`common/errors.go:2670-2692`) | Same error for provable-class failures (kept, so retry/consensus machinery routes around); new typed `ErrEndpointContentNotVerified` for strict-mode fail-closed (non-retryable toward upstream) |
| Consensus interaction | Content-validation errors classed `ResponseTypeInfrastructureError`, excluded from `validParticipants` — a corrupt-but-larger response can't dispute an honest majority (`consensus/analysis.go:485-489`) | Kept unchanged; consensus narrows to the unverifiable surface ("consensus proposes, anchor disposes", C1–C7 in `07`) |

## 5. Layer 1 — block-tip & availability enforcement: orthogonal, unchanged

The proposal does not touch Layer 1 (`docs/pages/config/failsafe/integrity.mdx`, "Layer 1"): highest-block enforcement for `eth_blockNumber`/`eth_getBlockByNumber` (`architecture/evm/eth_blockNumber.go:40-162`, `eth_getBlockByNumber.go:111-283`), the stale-tip cache-write guard (`json_rpc_cache.go:1179-1203`), block-range availability (`block_range.go:19-85`), and the empty-result confidence guard (`common.go:55-68`). These are on by default, independent of the check engine, and stay as-is; under the proposal they gain a trustworthy tip source (the anchor) instead of the poller-derived network tip.

---

## 6. What the proposal does **not** carry over

Honest accounting for the review:

1. **Per-check `onFailure` granularity** (28+ independently-tunable checks) collapses into 3 modes × selector overrides. Simpler and razor-compliant, but a real reduction in operator fine-tuning — a check that today can be singled out (`bloomMatch: { onFailure: recordOnly }`) becomes a mode-level decision.
2. **Profile/header per-request selection** (`headerMode`) is removed by design — verification is never client-loosenable. Callers who today downgrade themselves per request lose that ability.
3. **ChainView's per-node-group scoping** (`EvmUpstreamGroupForSelector`, `integrity_chainview.go:701-741`) and the `PinReconfirmer` machinery (1s cooldown, singleflight, `SkipCacheRead` echo-avoidance) exist because today's pin is disputable. Under the proposal the *finalized* prefix is anchored (undisputable); the follower keeps tracking only the unfinalized suffix, where the reconfirm machinery still applies.
4. **`autoCorrectWhenPossible`'s "serve the flagged original on exhaustion"** semantics: `balanced` mode serves labeled provisional/quorum data instead, which is the same operator-visible outcome with an honest label.
5. **The `consensus` failsafe is not replaced** — it narrows to unfinalized head, V-execution interim, and quorum-tier chains (`07` §1). Deployments on BSC/SVM see no verification upgrade until/unless an anchor kind exists.

---

## 7. Source review notes (upstream main @ `38db1695`)

Findings from the line-by-line review that should shape implementation and later docs:

1. **`integrity.budget` is config-only.** `maxPerSecond`/`maxConcurrent` are defined, deep-copied, and validated (`common/config_integrity.go:172-176`), but **no runtime enforcement exists** — nothing in the engine/resolver/ChainView reads it. The published docs' "token-bucket cap" claim is aspirational. The proposal's `02` §7 budgets must actually be enforced; today the only aux-fetch bounds are singleflight dedup and the 1s reconfirm cooldown.
2. **Finality is upstream self-reported** (`integrity_resolver.go:37-41`); unknown finality is treated as unfinalized (`resolver.go:52-58`). This is the single line that makes "finality honesty" unenforceable today and is the cleanest argument for the anchor.
3. **The published check table is stale.** The docs list ~25 checks and omit `baseFeeDerivation`, `timestampMonotonicity`, `headerConsensusInvariants`, `traceFrameShape`, `traceBlockGasReconciliation`; the reorg-sensitive list omits `getLogsCompleteness`, `txPinConsistency`, `traceBlockGasReconciliation`. The real catalog is **33 checks** (`levels.go:40-62`, locked by `TestLevelMembershipCoversAllChecks`). Any proposal-side docs should regenerate the table from `levels.go`.
4. **Vocabulary drift is settled in code, not docs examples:** current words are `recordOnly | hardReject | off`; `reject`/`soft-flag` now fail config validation (`common/validation.go:1696-1707`). Some docs worked examples still show the old words; metric help-strings still say `soft_flag` (`telemetry/metrics.go:372-377`).
5. **State probe is evidence, not routing** since `694268aa` (#1109): a hook-level refusal keyed to the state-proven head was tried and misfired structurally (`hooks.go:117-127`). The proposal's in-path ProofVerifier must not repeat that mistake — proof verification belongs in `HandleUpstreamPostForward`, not as a pre-forward routing gate.
6. **Keep the consensus exclusion**: `content_validation_error_test.go` locks in that `ErrEndpointContentValidation` responses are excluded from agreement/`preferLargerResponses`. The proposal's new checks must emit through the same error class (or the new typed fail-closed error where appropriate) to inherit this.
7. **Recent integrity history to build on:** unified engine `178a8f19` (#948) · verdict split + `autoCorrect` `2ae9a76f` (#1104) · skip-not-pass `7d99b82d` (#1107) · probe-as-evidence `694268aa` (#1109) · getProof-unsupported latch `f6d493c7` (#1133) · served-tip work `8caac812` (#1063).

---

## 8. Attack coverage delta

Mapping the threat-model catalog (`00` §4) onto what exists today:

| Attack | Existing coverage | Proposal |
|---|---|---|
| Fabricated self-consistent chain (A1–A3) | **None** — passes all 33 checks; only countermeasure is consensus voting, defeated by collusion/shared infra (A3) | Closed structurally by P1 (anchor) |
| Forged state values | **None** (shallow probe is telemetry) | P2 MPT proofs |
| Proof-of-empty lie | **None** | P2 non-existence proofs |
| Stale-but-valid replay | Partial (Layer-1 tip enforcement, cache guards; nothing binds a proof to the requested block) | P1+P2 freshness binding |
| Log/tx omission | Cross-noted only (`getLogsCompleteness`, cache-dependent) | Detected: anchored `receiptsRootRecompute` at ingest |
| Fake `eth_call` result | **None** | P4 re-execution (interim: labeled consensus) |
| Finality mislabeling | **None** (self-reported) | Anchor-only finality |
| Wrong `from` / signature spoof | Covered today (`senderRecovery`) | Kept, re-anchored |
| Malicious beacon source (A4) | n/a | Updates are self-verifying; withholding only → degrade per policy |
| DoS via verification cost | Partial (singleflight + 1s cooldown; budget unenforced) | `02` §7 enforced budgets + verify-at-ingest |

---

## 9. Summary

- **Kept verbatim (algorithms):** all 33 existing checks' logic; only the comparison targets for recomputes and continuity change (sections 2a–2c).
- **Re-anchored:** block hash/roots/bloom, ChainView pin, finality, state-probe comparison header.
- **New:** request-path MPT proofs (V-state), V-static, V-execution re-execution, evidence-driven hard cordon, verified-bit cache, response labels, L2 anchoring, honest quorum tier.
- **Removed by design:** per-check onFailure granularity, client-side profile downgrade.
- **Unchanged:** Layer-1 tip/availability enforcement, consensus exclusion of content-validation errors, `ErrEndpointContentValidation` retry semantics.
- **The existing system's own gap, in one sentence:** every commitment it verifies is recomputed from the response's own fields or corroborated against another eRPC-fetched view of the same chain — nothing is anchored to a trust root outside the RPC layer, so a fabricated-but-internally-consistent chain (or colluding upstreams) passes everything.

# 02 — Deep Proof Verification (P2) and Stateless `eth_call` (P4)

## 1. Purpose

Today the only proof-aware code is a background probe that checks `keccak256(accountProof[0]) == stateRoot` (`integrity_stateprobe.go:314`). That proves the *node holds* the trie root; it does **not** prove anything about the account — an upstream can return any balance/storage value with a valid-looking first node. This spec upgrades proofs to full Merkle-path verification and wires them into the request path, plus closes the last unverifiable read surface (`eth_call`) via stateless re-execution.

Prerequisite: anchored headers (`01`). Every proof below is verified against an **anchored** `stateRoot`/commitment for the **requested block** — never against a header supplied by the upstream being checked.

## 2. ProofVerifier — full MPT verification

New component `architecture/evm/integrity/proof.go`, built on go-ethereum `trie.VerifyProof` (battle-tested MPT verification; no custom crypto).

**Account proof** (for `eth_getBalance`, `eth_getTransactionCount`, `eth_getCode`, `eth_getProof` passthrough):

```
verifyAccount(anchoredHeader, address, accountProof) → account | NONEXISTENT | INVALID
  key     = keccak256(address)
  proofDB = { keccak256(node) → node  for each RLP node in accountProof }
  leaf    = trie.VerifyProof(anchoredHeader.stateRoot, key, proofDB)
  leaf == nil            → proven non-existence (a valid response, not an error)
  account = RLP-decode(leaf) → [nonce, balance, storageRoot, codeHash]
```

**Storage proof** (for `eth_getStorageAt`, `eth_getProof` storage slots):

```
verifyStorage(account.storageRoot, slot, storageProof) → value | NONEXISTENT | INVALID
  key   = keccak256(pad32(slot))
  leaf  = trie.VerifyProof(account.storageRoot, key, proofDB)
  value = RLP-decode-scalar(leaf)
```

**Response binding** — the value the upstream returned must equal the proven value:

| Method | Bound to |
|---|---|
| `eth_getBalance` | `account.balance` |
| `eth_getTransactionCount` | `account.nonce` |
| `eth_getCode` | `keccak256(code) == account.codeHash` |
| `eth_getStorageAt` | storage-proof value (incl. proven zero/non-existence) |
| `eth_getProof` (passthrough) | the entire proof is verified before returning |

Non-existence answers ("account doesn't exist", "slot is zero", "no code") are verified identically — this closes the proof-of-empty lie.

**Anti-replay (INV freshness):** verification binds `proof ↔ requested block number ↔ anchored header at that number`. A proof valid at block N served for block M≠N fails, because the anchored `stateRoot` differs. Block tag `finalized`/`safe` resolves via the anchor only.

## 3. Request-path flow (state reads, `strict`/`balanced` modes)

```
1. forward request → upstream U returns value V at block B
2. require anchored header H(B)            (from HeaderOracle; B finalized in strict mode)
3. fetch eth_getProof(addr, slots, B) FROM THE SAME upstream U
4. ProofVerifier: verify proof against H(B).stateRoot; bind V to proven value
5a. match     → serve V, mark verified, cache with verified-bit
5b. mismatch  → evidence bundle → hard cordon U; retry another upstream
5c. U lacks getProof → capability-latch unsupported (pattern of #1133), fall to policy
```

Why step 3 uses the *same* upstream: the proof makes the response **self-authenticating** — U cannot forge a proof against an anchored root it doesn't control. No extra trust, no cross-upstream correlation needed.

**Cost:** +1 RPC per verified state read. Amortization: cache verified state keyed `(chain, block, address, slot)` with the verified-bit; `eth_getProof` supports multi-slot proofs for batch reads. Deep-history caveat: full nodes serve state only for recent blocks — detect the state-missing error shape and route to archive-capable upstreams or degrade per policy (capability detection, not chain config).

## 4. Inclusion & completeness at ingest (deltas to existing checks)

Existing recompute checks stay; the anchor changes what they verify *against*:

| Existing check | Delta |
|---|---|
| `blockHashRecompute` (`checks_recompute.go:52-80`) | Compare recomputed hash to **anchored** header at that height (not follower's) |
| `transactionsRootRecompute` (:89-127) | Unchanged algorithm; root source = anchored header |
| `receiptsRootRecompute` (:136-180) | Unchanged; this is also the **log-completeness** proof for `eth_getLogs`/`eth_getBlockReceipts` (receipts trie commits to every log — omission is detectable) |
| `bloomMatch` / `bloomEmptiness` (`checks_receipts.go:140+`) | Keep as cheap pre-screen before root recompute |
| `senderRecovery` (`checks_authenticity.go:22-58`) | Keep |
| `probeProof` (`integrity_stateprobe.go:271-323`) | Replace shallow check with `ProofVerifier`; probe output feeds the same evidence path |

Known chain-quirk handling stays as today: unknown header fields → check *skips* (never passes vacuously) via the existing allowlist (`checks_recompute.go:29-48`); documented ZK-rollup incompatibilities remain skip-not-fail.

## 5. Finality rule

A response is marked **verified** only when the block it is bound to is finalized per the anchor. Unfinalized-bound data (valid proofs against a reorg-able header) is at best **provisional** — `03` defines labeling and re-verification at finality. This is what keeps a valid-proof-against-reorged-block from being served as final truth.

## 6. P4 — StatelessExecutor: verifying `eth_call` / `eth_estimateGas`

Execution results carry no chain commitment, so the only ground truth short of a full node is **re-execution over proven state**. Design (phase 4; heavy — interim tier is the existing `consensus` failsafe, explicitly labeled):

```
1. trace = debug_traceCall(call, B, { tracer: "prestateTracer", tracerConfig: { diffMode: false } })
           → all accounts/storage slots/code the execution touches
2. proofs = eth_getProof(each account, touched slots, B)   [batched where supported]
3. verify all proofs against anchored H(B).stateRoot        (ProofVerifier)
4. stateDB = ProvenStateDB{accounts, storage, code} — reads outside the proven set
   ABORT (fall to consensus tier); never fetch unproven state silently
5. ctx = EVM block context from anchored H(B): number, time, coinbase, gasLimit,
   baseFee, blob fields; BLOCKHASH opcode served from ChainView's verified last-256 headers
6. localResult = go-ethereum core/vm execution over stateDB+ctx
7. compare localResult with upstream's return data (and gasUsed for eth_estimateGas)
   match → serve (verified)   mismatch → evidence → hard cordon
```

**Honesty notes:**
- Tracer support varies by vendor/node → capability detection + latch, fallthrough = consensus tier (razor: enumerated tracers are an optimization, not a requirement).
- Equivalence target is the *return value and gas*, not trace-internal details; node EVM-version differences are protocol-scheduled (fork schedule data), not vendor quirks.
- Cost is high (trace + proofs + local exec): gate to `strict` mode and/or high-value methods; observe mode samples.

## 7. DoS controls

- Proof-size caps (max nodes per proof, max slots per request) — reject oversized proofs as invalid, not fatal.
- Per-upstream verification budgets (proof fetches count against existing rate-limit/CU machinery).
- Verify-at-ingest preferred wherever data is cacheable: verification cost is O(blocks), requests are served from verified cache (INV-4).
- StatelessExecutor bounded by call gas cap + wall-clock timeout; aborts never block the serving path.

## 8. Config & metrics

```yaml
integrity:
  proofs:                     # NEW
    enabled: true
    maxProofNodes: 64
    maxSlotsPerRequest: 32
  statelessExec:              # NEW (P4)
    enabled: false            # strict-mode only when true
    timeout: 10s
```

- `erpc_integrity_proof_verifications_total{network, method, verdict}` — verdict ∈ verified|mismatch|invalid|unsupported
- `erpc_integrity_proof_mismatches_total{network, upstream, method}` — proven lies (→ cordon)
- `erpc_integrity_reexec_total{network, verdict}` — P4 results
- `erpc_integrity_reexec_aborts_total{network, reason}` — unproven-read, tracer-unsupported, timeout

## 9. Testing

- Mainnet fixtures: real `eth_getProof` responses (existing + non-existent accounts, multi-slot, deep tries) against anchored headers.
- Adversarial gock mocks: correct value + wrong proof; wrong value + valid proof for a *different* account; proof for block N−1 replayed at N; truncated proof; fabricated `eth_call` result (P4: must mismatch).
- Equivalence: `ProofVerifier` output cross-checked against a full node's state for sampled blocks.
- Conventions per repo rules (`util.ConfigureTestLogger()`, gock-before-init, no `t.Parallel()` with gock).

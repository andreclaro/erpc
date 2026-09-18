# 04 — Multichain Anchoring (P5)

## 1. Principle

Every chain eRPC serves resolves into exactly one anchor kind (`01` §2): `syncCommittee`, `l1Committed`, or the **`quorum` fallthrough**. No chain-ID switches in core packages: anchor adapters are edge components selected by config + capability metadata, and any chain without an adapter gets `quorum` — which must be safe with zero per-chain code. This keeps the provider/chain list freely extensible: adding a chain never silently weakens the model, it just lands on the weakest tier until an adapter exists.

## 2. `l1Committed` — the generic L2 pattern

Most L2s anchor to Ethereum by posting commitments into an L1 contract. Since eRPC anchors L1 (`01`), those commitments are readable **trustlessly via `eth_getProof` against the anchored L1 `stateRoot`** — one generic mechanism covers the whole family:

```
verifyL2Block(l2Block):
  1. commitment = L1 storage read: outputRoot / assertion / finalizedStateRoot for l2Block
     (eth_getProof on the L1 contract slot, verified against anchored L1 header)
  2. check l2Block.hash (or state root) is bound by the commitment     (adapter-specific decoding)
  3. check the commitment is FINAL per the L2's own rules               (challenge window / proof finalized)
```

Adapter-specific surface is deliberately small and lives at the edge: **which contract, which slot layout, what "final" means**. If any of the three steps can't be satisfied (contract layout unknown, window not elapsed, proof unsupported), the chain degrades to `quorum`/provisional — never to "trusted".

## 3. Per-family anchor data

| Chain family | L1 commitment | Finality rule | Assurance |
|---|---|---|---|
| **OP Stack** (OP Mainnet, Base, …) | Output roots in `L2OutputOracle` / `DisputeGameFactory` (per-version contract data) | After dispute-game resolution / challenge window | High — L2 block hash bound to L1-anchored output root. Phase A alternative: Helios OP-Stack mode |
| **Arbitrum** (One/Nova) | Assertions in the Rollup contract (`latestConfirmed` node → block hash) | After challenge period (~6.4 d) | High for confirmed; head is provisional (sequencer feed) |
| **zk rollups** (Linea, zkSync Era, Scroll, Polygon zkEVM) | Finalized state roots in L1 verifier/rollup contracts | Once the L1 validity-proof tx is final | Strongest L2 tier — L1 contract state already reflects a verified validity proof |
| **Polygon PoS** | Heimdall checkpoints posted to L1 staking contract | Checkpoint finality on L1 | Medium-high via `l1Committed` adapter; else quorum |
| **Gnosis** | — | — | `syncCommittee` directly (beacon-spec consensus, own chain constants) |
| **BSC, other sidechains** | none usable | — | `quorum` (explicit) |
| **Solana / SVM** | none usable client-side | — | `quorum` (explicit); research note §5 |

Contract addresses, slot layouts, and window lengths are **chain-registry/config data (protocol facts at the edge)**, not code constants scattered in core logic. Unknown family → `quorum`.

## 4. Interaction with existing integrity checks

- The documented ZK-rollup incompatibility of recompute checks (`docs/pages/config/failsafe/integrity.mdx` — chain-specific header fields) stays **skip-not-fail** via the existing allowlist; for those chains the `l1Committed` anchor is the primary integrity mechanism, and V-inclusion checks contribute where they don't skip.
- `latest`/sequencer-fed L2 data is V-provisional by construction; the anchor only certifies what has been committed and finalized on L1. Expect a finality lag (minutes for OP Stack output proposals, days for Arbitrum confirmations) — policy (`03` §3) decides who may consume provisional data.

## 5. SVM / Solana note

No practical client-verifiable light protocol exists for Solana today (no sync-committee equivalent; stake-weighted vote verification requires replaying Tower BFT). eRPC's SVM traffic therefore remains on the `quorum` tier: cross-upstream consensus with misbehavior cordon, commitment-aware finality, and explicit `quorum` labeling. Track as research; do not special-case beyond the existing SVM machinery.

## 6. Assurance labeling

`X-ERPC-Verification` values map to anchor kind so clients can reason about assurance:

| Anchor kind | Label on verified | Meaning |
|---|---|---|
| `syncCommittee` / `l1Committed` | `verified` | Bound to consensus-attested commitment |
| `quorum` | `quorum` | k-of-N upstream agreement (never upgraded to `verified`) |
| unfinalized any kind | `provisional` | Optimistic attestation + agreement; re-verified at finality |

This labeling is the honest core of the "100% zero-trust" claim: *everything* is classified, and nothing below proof-grade is ever presented as verified.

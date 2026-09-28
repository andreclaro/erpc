# eRPC Integrity Enhancements — Zero-Trust Verification

- **Status:** DRAFT v0.1
- **Date:** 2026-09-10
- **Scope:** eRPC (Go) — extends `architecture/evm/integrity/` and the failsafe pipeline
- **Audience:** implementers, security reviewers

---

## 1. Problem statement

eRPC's existing integrity module (`architecture/evm/integrity/`) verifies the **self-consistency** of upstream data:

- block hash recomputation (RLP + keccak) — `integrity/checks_recompute.go:52-80`
- `transactionsRoot` / `receiptsRoot` recomputation — `checks_recompute.go:89-180`
- sender recovery (ecrecover vs `from`) — `integrity/checks_authenticity.go:22-58`
- logs bloom recomputation — `integrity/checks_receipts.go:140+`
- shallow `eth_getProof` state probe — `integrity_stateprobe.go:271-323`

Its root of trust, however, is the **chain follower** (`integrity_chainfollower.go:165-182`), which builds the "verified" chain purely by parent-hash linkage of **RPC-supplied blocks**. A malicious or compromised upstream can therefore serve a *fabricated but internally consistent* chain that passes every existing check. The `consensus` failsafe adds Byzantine voting across upstreams, but voting is not proof: it fails under collusion or shared backend infrastructure.

**Goal:** every response eRPC returns is either (a) cryptographically bound to a consensus-attested chain commitment, or (b) explicitly classified and handled as *provisional* or *unverifiable* by policy. No RPC provider is ever a trust input. Providers become interchangeable, and the provider list can be extended freely without changing the security model.

## 2. What "zero trust / 100% security" means here

Absolute "100% security" is not a meaningful cryptographic claim. This spec defines the achievable target precisely:

> **Zero trust in RPC providers.** A response is served as *verified* only if a proof binds it to a header attested by the chain's consensus. Everything else is labeled and enforced by policy — nothing unverifiable is ever silently served as verified.

**Irreducible residual trust assumptions:**

1. The chain's consensus itself (Ethereum PoS: ≥2/3 honest sync committee per period; per-chain equivalents in `04-multichain.md`).
2. One **weak-subjectivity checkpoint** per chain, obtained out-of-band and cross-validated (needed only at cold start; see `01-trust-anchor.md` §5).
3. Correctness of our own code, go-ethereum, and the BLS12-381 library.
4. TLS provides transport confidentiality/integrity in transit only — it is **not** part of the data-authenticity argument.

**Honest limits (handled by policy, not proofs):**

| Surface | Why not provable | Handling |
|---|---|---|
| Unfinalized head (`latest`, `pending`) | Not yet attested | Provisional tier: optimistic-update attestation + consensus; verified at finality |
| `eth_call` / `eth_estimateGas` | Execution results carry no commitment | Phase 4: stateless re-execution over proven state; interim: consensus |
| Chains without a consensus anchor (BSC, Solana, …) | No client-verifiable light protocol | Quorum tier, explicitly labeled |

## 3. Design pillars

| # | Pillar | Spec | Gap closed |
|---|---|---|---|
| P1 | **Trust anchor** — sync-committee light client feeding the chain follower | `01-trust-anchor.md` | Fabricated-chain attacks; makes all existing checks trustless |
| P2 | **Deep proof verification** — full MPT proofs for state reads; inclusion proofs at ingest | `02-proof-verification.md` | Lying balances/storage/code; shallow probe limitation |
| P3 | **Enforcement pipeline** — strict/balanced/observe modes, verified cache, evidence-driven cordon | `03-verification-pipeline.md` | Operationalizes P1+P2; per-method policy |
| P4 | **`eth_call` verification** — stateless re-execution over proven state | `02-proof-verification.md` §6 | Last unverifiable read surface |
| P5 | **Multichain anchoring** — L2s via L1 commitments, per-chain assurance tiers | `04-multichain.md` | Extends guarantee beyond Ethereum mainnet |
| P6 | **Advisory provider signals** — dRPC signatures/quorum, Ankr vRPC (TEE) | `05-optional-signals.md` | Defense-in-depth, never a gate |
| P7 | **Client-side verifiability** — proof-carrying responses + anchor transparency so clients need not trust eRPC either | `08-client-trust.md` | Last-mile trust (eRPC → client) |

## 3a. Conventions (repo rules compliance)

This spec follows the repo design razor (`.cursor/rules/erpc.md`): open-ended sets (JSON-RPC methods, chains, vendors) resolve into a **small bounded interface** with the **unknown-input fallthrough as the primary, safe-by-default path** — enumerated methods/chains below are optimizations and examples, not the design. New `erpc.yaml` schema ships with version markers, and every implementation phase rides along with `docs/pages/` updates per `AGENTS.md`.

## 4. Architecture overview

```
                          ┌────────────────────────────────────────────────────┐
 clients ──► eRPC HTTP ──►│ Policy engine (strict / balanced / observe)        │
                          └───────┬────────────────────────────────────────────┘
                                  │
        ┌─────────────────────────┼──────────────────────────────────┐
        ▼                         ▼                                  ▼
┌───────────────┐      ┌────────────────────┐             ┌──────────────────┐
│ P1 TRUST      │      │ P2/P4 VERIFIERS    │             │ Verified cache   │
│ ANCHOR        │      │                    │             │ (re-org aware,   │
│               │      │ · IngestVerifier   │  verified   │  verified-bit)   │
│ beacon API ──►│ hdrs │   (existing        │────────────►│                  │
│ pool → Sync   │─────►│    recompute       │  only       └──────────────────┘
│ Committee     │      │    checks, now     │
│ Verifier →    │      │    anchored)       │
│ HeaderOracle  │      │ · ProofVerifier    │             ┌──────────────────┐
└──────┬────────┘      │   (MPT/state)      │  evidence   │ P6 Advisory      │
       │               │ · StatelessExec    │────────────►│ signals (dRPC,   │
       ▼               │   (eth_call, P4)   │             │ vRPC) → scoring  │
┌───────────────┐      └─────────┬──────────┘             └──────────────────┘
│ ChainView /   │                │
│ Follower      │                ▼
│ (anchor wins  │      ┌────────────────────┐
│  conflicts)   │      │ Evidence → cordon  │──► consensus/misbehavior export
└───────────────┘      │ (hard, immediate)  │    (S3/file JSONL)
                       └────────────────────┘
```

The anchor is the only new *trust-relevant* component; everything else is wiring existing eRPC machinery (chain view, recompute checks, cache, cordon, export) to it.

### 4a. Composition with existing failsafe machinery

The new layers do not replace the failsafe stack — they sit on top of it and change what its signals mean. Request flow:

```
request → policy mode → verified-cache lookup ──hit──► serve (verified)
                        │ miss
                        ▼
        forward: consensus(...) or retry(hedge(upstream))     [existing failsafe]
                        ▼
        post-forward verification (anchor/proofs) ──fail──► evidence → cordon → next upstream
                        ▼
        enforce mode + label (X-ERPC-Verification) → respond [+ optional proof envelope, P7]
```

| Existing feature | Role today | Role with this spec |
|---|---|---|
| retry / hedge / timeout | availability, latency | unchanged; also routes around verification-failing upstreams (failures surface as `ErrEndpointContentValidation`) |
| circuit breaker | upstream health | unchanged |
| **consensus failsafe** | de-facto integrity mechanism (voting) | narrows to the unverifiable surface (below); provable winners get async anchor verification (`07` §2) |
| selection & scoring | routing | new inputs: verification failures (hard penalty), advisory signals (P6 boost), provisional-accuracy reputation (`07` §4) |
| re-org-aware cache | performance | gains verified-bit (INV-4); serves verified data |
| cordon / misbehavior export | punishment | upgraded trigger: cryptographic evidence → immediate hard cordon (no dispute token bucket) |
| rate limits | cost control | also bounds verification-cost DoS (`02` §7) |

**Is consensus still required? Yes.** Three surfaces are unverifiable by construction or by deployment state:

1. **Unfinalized head** (V-provisional) — nothing to prove against until finality; consensus + optimistic attestation is the strongest available control.
2. **`eth_call` / execution methods** — until P4 (StatelessExecutor) ships, consensus is the interim tier.
3. **Quorum-tier chains** (BSC, SVM, unknown chains — the razor's fallthrough) — consensus is the *only* integrity mechanism there.

What changes is its role: from *the* integrity mechanism to **provisional-tier control + availability fabric + early-warning**. For provable data it becomes redundant-but-useful — and one asymmetry must be handled explicitly: if the anchor conflicts with *unanimous* cross-vendor consensus, suspect an **anchor bug** (fail-closed + page humans) instead of mass-cordoning honest upstreams (`07` §3).

## 5. Document index

| Doc | Content |
|---|---|
| `00-threat-model.md` | Adversaries, assets, attack catalog, residual trust |
| `01-trust-anchor.md` | P1: light-client anchor — Helios sidecar phase, native Go phase, checkpoint management, ChainView integration |
| `02-proof-verification.md` | P2+P4: MPT proof verification, ingest inclusion proofs, stateless `eth_call` re-execution |
| `03-verification-pipeline.md` | P3: per-method matrix, enforcement modes, config schema, cache/verified-bit, cordon integration, metrics |
| `04-multichain.md` | P5: L2 anchoring (OP Stack, Arbitrum, zk rollups), sidechains, SVM |
| `05-optional-signals.md` | P6: provider-signature adapters as advisory signals |
| `06-implementation-plan.md` | Phases, exit criteria, testing/conformance, rollout, risks, red-team checklist |
| `07-consensus-evolution.md` | Consensus's narrowed-but-required role; "consensus proposes, anchor disposes"; conflict policy; improvement catalog C1–C7 |
| `08-client-trust.md` | P7: proof-carrying responses, anchor transparency, client SDK, signed/TEE advisory layers, trust matrix |

## 6. Glossary

- **Anchor** — the sync-committee-verified header source; the only component allowed to declare a header "verified".
- **Verified response** — response bound by proof to an anchored header.
- **Provisional response** — response for unfinalized data, attested only by an optimistic update and/or cross-upstream consensus.
- **Evidence** — machine-checkable record (request, response, proof, anchored header) proving an upstream lied; drives hard cordon.
- **Weak-subjectivity checkpoint** — a recent trusted beacon block root used to bootstrap the light client.

# 00 — Threat Model

## 1. Assets

| Asset | Meaning |
|---|---|
| **Response integrity** | Returned data equals what the canonical chain commits to (balances, blocks, txs, receipts, logs, storage). |
| **Freshness** | Data is not replayed from an older (valid but stale) height than requested/implied. |
| **Finality honesty** | Data labeled `finalized`/`safe` actually is; reorg-able data is never served as final. |
| **Availability honesty** | "Empty" / "not found" / "no logs" responses are genuine — omission is detectable. |
| **Non-censorship** | An upstream cannot silently drop txs/logs it dislikes (completeness). |

## 2. Adversaries

| # | Adversary | Capability |
|---|---|---|
| A1 | **Malicious upstream** (one of N providers) | Arbitrary responses: fabricated chains, forged proofs, stale data, omissions. |
| A2 | **Compromised upstream** | Same as A1, on a provider otherwise considered reputable (Alchemy/QuickNode/etc.). |
| A3 | **Colluding subset** (< k of N upstreams, or N sharing hidden common infra) | Coordinated consistent lies; defeats pure voting. |
| A4 | **Malicious beacon-API source** for light-client data | Serves wrong `LightClientUpdate`s — but updates are self-verifying; worst case is *withholding* (DoS), not forgery. |
| A5 | **Network MITM** | Covered by TLS; out of scope except downgrade of *policy* headers, which are signed/checked locally, not transported. |
| A6 | **Replay** | Re-serving previously valid responses/proofs for a different (later) request context. |

Explicitly out of scope: compromise of the eRPC host itself, ≥1/3 Ethereum consensus failure, breaks of keccak/BLS12-381/ECDSA.

## 3. Residual trust (irreducible)

1. Ethereum PoS consensus: ≥2/3 of each 512-validator sync committee is honest (per period). This is the same assumption that secures the chain.
2. One weak-subjectivity checkpoint per chain at cold start, cross-validated from ≥2 independent out-of-band sources. See `01-trust-anchor.md` §5.
3. Implementation correctness (this code, go-ethereum, BLS lib) — mitigated by spec-vectors, fuzzing, audits (`06-implementation-plan.md` §3).
4. Local clock within tolerance for slot/signature validation (NTP-grade).

## 4. Attack catalog → controls

| Attack | Example | Control | Spec |
|---|---|---|---|
| **Fabricated self-consistent chain** | Upstream serves a fake fork with valid internal hashes | P1 anchor: chain follower accepts only headers descending from sync-committee-attested headers; conflict = cryptographic proof of lying | `01` §4 |
| **Stale-but-valid data (replay)** | Last week's valid `eth_getProof` re-served today | Proofs are verified **against the requested block's anchored `stateRoot`**; block binding is mandatory; finality tracked by anchor | `02` §3 |
| **Forged state values** | Wrong balance/storage/code | Full MPT proof-path verification (`trie.VerifyProof`), not just `keccak(proof[0]) == stateRoot` | `02` §2 |
| **Log/tx omission (censorship)** | `eth_getLogs` silently missing events | Completeness = receiptsRoot recomputation over the range + bloom pre-screen at ingest; single-receipt checks via MPT | `02` §4 |
| **Fake `eth_call` result** | Upstream returns attacker-chosen output | P4 stateless re-execution over proven state; interim: consensus (labeled) | `02` §6 |
| **Wrong `from` / signature spoof** | Forged sender | Existing sender-recovery check (kept, now anchored) | existing `checks_authenticity.go` |
| **Finality mislabeling** | Reorg-able block served as `finalized` | Finality defined solely by anchor (`FinalityUpdate`), never by upstream tags | `01` §4, `03` §2 |
| **Provisional downgrade** | Client tricked into treating head data as final | Response classification header; policy floor per project; `strict` fails closed | `03` §2 |
| **DoS via verification cost** | Forcing proof fetches per request | Proof-size caps, per-upstream proof budgets, verify-at-ingest + verified cache so cost is O(blocks) not O(requests) | `02` §7, `03` §4 |
| **Checkpoint staleness → long-range attack** | Bootstrapping from a year-old checkpoint | Checkpoint age policy (hard alert/fail-closed beyond threshold); checkpoint rotation from verified finalized headers | `01` §5 |
| **Beacon-source eclipse** | All light-client endpoints malicious/down | Updates are self-verifying (forgery impossible); multi-source pool + cached state; degrade per policy | `01` §6 |
| **Proof-of-empty lie** | "Account doesn't exist" for an existing one | Non-existence proofs are verifiable MPT proofs too — verified identically | `02` §2 |

## 5. Security invariants (testable)

1. **INV-1** — No response is marked `verified` unless bound by a valid proof to an anchored header.
2. **INV-2** — The anchor's finalized head only advances with a valid ≥2/3 sync-committee signature and valid finality/next-committee branches.
3. **INV-3** — A header conflicting with the anchor can never enter ChainView as verified; the serving upstream is cordoned with exported evidence.
4. **INV-4** — Cache entries carry a `verified-bit`; a cache read can never upgrade unverified → verified.
5. **INV-5** — `strict` mode never returns provisional or unverifiable data (fails closed with a typed error).
6. **INV-6** — Checkpoint age beyond policy threshold blocks anchor cold-start (never silently bootstraps stale).

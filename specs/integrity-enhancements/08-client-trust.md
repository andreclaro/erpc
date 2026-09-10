# 08 — Client-Side Trust (P7): Verifying eRPC Itself

## 1. The last-mile problem

P1–P6 remove trust in *providers*, but eRPC's own clients still trust eRPC today (TLS + operator reputation). End-to-end zero trust means clients verify eRPC responses with the **same two-layer stack eRPC uses internally**: consensus-attested headers + proofs. Done fully, eRPC becomes just another untrusted data server — the Helios model, one boundary further out.

## 2. Proof-carrying responses (PCR)

eRPC already produces the proofs during verification — attach them to the response instead of discarding them:

- **Opt-in** via request header `X-ERPC-Proofs: true` (wire-compatible default: off).
- Response carries a **proof envelope** (JSON-RPC extension field or headers), per class:
  - **V-inclusion** — data is self-verifying given the header (client recomputes header hash / `transactionsRoot` / `receiptsRoot`); envelope = anchored header (or height+hash if the client already tracks the chain).
  - **V-state** — envelope = anchored header + the exact `eth_getProof` payload eRPC verified.
  - **V-execution** (post-P4) — envelope = the proven state set + call context used for re-execution (client re-executes or spot-checks).
  - **V-provisional / quorum** — no proof exists → **label only**; clients must treat accordingly.
- The envelope binds proof ↔ requested block ↔ anchored header (the anti-replay rule from `02` §2).

## 3. Anchor transparency

So clients can verify envelopes **without trusting eRPC's anchor claim**, eRPC exposes the anchor's outputs:

- **Light-client object passthrough** (bootstrap / finality_update / optimistic_update — standardized, self-verifying formats) so clients sync their *own* light client: Helios-WASM in browsers, Nimbus/Lodestar embedded in wallets/mobile.
- eRPC serving *wrong* updates is impossible (self-verifying objects); *withholding* is detectable, and clients can fall back to any public beacon endpoint — the API is standardized, eRPC is not a special source.
- Result: the client independently holds anchored headers → verifies PCR envelopes → **zero trust in eRPC**.

## 4. Client SDK

- TS package (`@erpc-cloud/verify`) + Go package: input = eRPC response + PCR envelope (+ client's synced header or embedded light client); output = `verified | invalid | provisional`.
- Mirrors the Helios architecture at the eRPC boundary; aligns with the Portal Network direction (verified-data serving) long-term.

## 5. Optional operational layers (advisory only, never substitutes)

For clients that **cannot** verify cryptographically:

- **Signed responses** — eRPC signs `(request, response, proof-envelope hash)` with a published, rotated key (JWKS at a well-known path). Proves origin and makes lies *non-repudiable evidence* (SLA/disputes). It does **not** prove truth — a signed lie is still a lie, just an attributable one.
- **TEE attestation** — run eRPC in an Intel TDX enclave; the attestation quote binds a response-signing key to a reproducible eRPC build → proves responses were produced by unmodified code that performed verification. Trust shifts to Intel + build transparency (the Ankr vRPC model, one layer up).

## 6. Trust matrix

| Client type | What they verify | Residual trust |
|---|---|---|
| PCR + own light client | anchor signatures + proofs | chain consensus only |
| PCR + operator-pinned headers | proofs against pinned headers | the header source |
| Signed responses only | origin accountability | eRPC honesty (mitigated: lies become attributable evidence) |
| Plain JSON-RPC (today) | — | eRPC + TLS |

## 7. Config & metrics (sketch)

```yaml
verification:
  clientProofs:                  # NEW; absent = off (no-op default)
    enabled: true                # serve PCR envelopes on X-ERPC-Proofs: true
    anchorPassthrough: true      # expose light-client objects
  signing:
    enabled: false               # advisory signed responses
    jwksPath: "/etc/erpc/jwks.json"
```

- `erpc_integrity_pcr_served_total{network, class}`
- `erpc_integrity_pcr_invalid_total{network}` — envelope assembly bugs (target ~0)
- `erpc_integrity_anchor_passthrough_total{network}`

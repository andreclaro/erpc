# 03 — Verification Pipeline & Enforcement (P3)

## 1. Purpose

Operationalize the anchor (`01`) and verifiers (`02`) inside eRPC's existing request flow: classify every request into a bounded set of verification classes, enforce per-network policy modes, stamp cache entries with a verified-bit, and turn verification failures into machine-checkable evidence that cordons lying upstreams.

## 2. Verification classes (bounded interface, razor-compliant)

Methods are an open set; the design must not be a method table. Classification is **capability-derived**: given a method's request/response shape, what commitment can its result be bound to? Any method — including tomorrow's unknown ones — resolves into exactly one class:

| Class | Binding | Examples (illustrative, not exhaustive) |
|---|---|---|
| **V-static** | Local config truth | `eth_chainId`, `net_version` — compared to configured chainId; mismatch = lie |
| **V-inclusion** | Block commitments (`transactionsRoot`, `receiptsRoot`, `logsBloom`, header hash) — verified at ingest, served from verified cache | block/tx/receipt/log reads (`eth_getBlock*`, `eth_getTransaction*`, `eth_getLogs`, `eth_getBlockReceipts`, `eth_feeHistory` base-fee derivation) |
| **V-state** | MPT proof against anchored `stateRoot` | `eth_getBalance`, `eth_getTransactionCount`, `eth_getCode`, `eth_getStorageAt`, `eth_getProof` |
| **V-execution** | Stateless re-execution over proven state (P4); interim: consensus, labeled | `eth_call`, `eth_estimateGas`, trace methods |
| **V-provisional** | Unfinalized data: optimistic-update attestation + consensus; re-verified at finality | any of the above at `latest`/`pending`/unfinalized heights; `eth_blockNumber` |
| **V-meta** | No chain commitment exists; never security-gated | `web3_clientVersion`, `eth_syncing`, `eth_gasPrice` (advice, not state) |

**Fallthrough is the primary path:** an unrecognized method defaults to the weakest applicable class — V-meta if the response carries no block binding, otherwise V-provisional (consensus-compared, labeled, never "verified"). Enumerated methods above are optimizations layered on that default. Classification rules live at the edge (method-shape matching: "does the response commit to block N? does it read state at N?") and are config-overridable, never hard-coded per vendor.

## 3. Enforcement modes

| Mode | Verified classes | Provisional | Unverifiable/meta | Verification failure |
|---|---|---|---|---|
| `strict` | Serve | **Fail closed** (`ErrEndpointContentNotVerified`) | Fail closed or require explicit per-method opt-out | Hard cordon + evidence |
| `balanced` (default) | Serve | Serve, labeled `provisional`; async re-verify at finality | Serve via consensus, labeled | Hard cordon + evidence |
| `observe` | Serve | Serve, labeled | Serve | Log + metrics only (no cordon) |

- Modes set per network, overridable per method/finality using the **existing** failsafe selector shape (`matchMethod` / `matchFinality`) — reuse, no parallel selector machinery.
- Policy floor: projects may tighten, never loosen below the network floor (prevents downgrade via request headers; the existing `X-ERPC-Skip-Consensus` directive must not bypass verification — verification is not skippable by clients).
- **Response labeling:** local header `X-ERPC-Verification: verified|provisional|quorum|meta` (+ `X-ERPC-Verified-Height` when bound). This is observability for clients, not a transported security claim.

## 4. Integration points (existing eRPC machinery)

| Point | Reference | Change |
|---|---|---|
| Post-forward per-upstream hook | `architecture/evm/hooks.go:162-202` (`integrity.Validate` at :202), wired from `erpc/networks.go:2700-2714` | Add anchor/proof checks to the same validation pipeline; failures keep producing `ErrEndpointContentValidation` so retry/consensus route around the upstream |
| Response finalization before client | `erpc/projects.go:357-372` | Apply mode enforcement + labeling headers |
| Cache fill | `erpc/networks.go:2426-2456` (async `cacheDal.Set`) → `architecture/evm/json_rpc_cache.go:630`, finality policy at :676-703 | Cache entry gains **verified-bit** + bound-height metadata; a cached entry can never be upgraded unverified→verified without re-verification (INV-4); reorg invalidation unchanged |
| ChainView anchor swap | `integrity_chainview.go`, `integrity_chainfollower.go` | `HeaderOracle` becomes the privileged source (`01` §6) |
| Misbehavior export | `consensus/export.go`, `export_s3.go` | Evidence bundles reuse the JSONL/S3 exporter, extended with proof context (`02` §3 step 5b) |
| Cordon | existing cordon/sit-out machinery (operator cordon sharing, #1137) | Verification failure = **hard cordon**: immediate, bypasses the consensus dispute token bucket (a proof of lying needs no voting threshold) |

## 5. Config schema (new section; ships with `erpc.yaml` schema version marker)

```yaml
networks:
  - evm:
      chainId: 1
    integrity:
      enabled: true
      anchor: { … }              # see 01-trust-anchor.md §8
      proofs: { … }              # see 02-proof-verification.md §8
    verification:                # NEW
      mode: balanced             # strict | balanced | observe
      anchorMaxStaleness: 5m
      overrides:                 # existing failsafe selector shape
        - matchMethod: "eth_call|eth_estimateGas"
          matchFinality: "finalized"
          mode: strict
        - matchMethod: "eth_getLogs"
          matchFinality: "unfinalized"
          mode: balanced
```

Razor notes: three modes are the full closed set (no per-class knobs); `overrides` reuse existing matcher infrastructure; absence of config = `balanced` everywhere = weakest safe default.

## 6. Metrics & alerts

- `erpc_integrity_verification_total{network, class, verdict}` — verdict ∈ verified|provisional|quorum|meta|failed
- `erpc_integrity_verification_failures_total{network, upstream, check}` — proven lies (page)
- `erpc_integrity_provisional_total{network, method}` — provisional-served volume
- `erpc_integrity_anchor_staleness_seconds{network}` — from `01`; alert when exceeded
- `erpc_integrity_cache_verified_ratio{network}` — gauge; drift-down = upstream quality signal
- Existing `erpc_consensus_upstream_misbehavior*` family stays for non-cryptographic disputes

## 7. Latency & cost budget

| Path | Added cost | When |
|---|---|---|
| V-inclusion cache hit | 0 | steady state (verify-once-at-ingest) |
| V-inclusion cache miss | recompute at ingest (existing) | first fetch per block |
| V-state | +1 `eth_getProof` per uncached (block, addr, slots) | strict/balanced state reads |
| V-execution | trace + proofs + local exec (seconds) | strict only, high-value |
| V-provisional | consensus fan-out (existing) | unfinalized |
| Anchor | ~1 BLS verify per epoch-ish update (~ms) | background |

Steady-state read-heavy workloads (eRPC's design target) approach zero added cost because verified data is served from the permanent cache.

## 8. Failure semantics

- Anchor down/stale → V-inclusion/V-state/V-execution cannot mark new data verified: `strict` fails closed (`ErrEndpointContentNotVerified`, typed, non-retryable-toward-upstream); `balanced` degrades to consensus with `quorum` label + alert.
- Verifier internal error (bug, parse failure) → never a pass: treat as `failed` for the *verifier* metric, fall back per mode; panics recovered like consensus participants (no deadlocks in the serving path).
- Evidence export failure → cordon still applies; export retried (file) or dropped with error log (S3, matching existing exporter semantics).

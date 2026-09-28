# 05 — Advisory Provider Signals (P6)

## 1. Principle

Some providers offer their own response-authenticity schemes. These are **advisory signals only**: they may improve upstream selection and scoring, but they never gate responses, never substitute for anchor-based verification, and in any conflict the anchor wins (a provider signature proves *who* said something, not that it's *true* — it authenticates the liar).

Razor note: vendor schemes are an open set. The adapter interface below is the bounded interface; **absence of an adapter for a vendor is the default no-op path**, not an error. No vendor special-casing in core packages.

## 2. SignalAdapter interface

```go
// Advisory only. Called post-forward, off the critical path (observe/async).
type SignalAdapter interface {
    // Detect reports whether this upstream speaks the scheme (capability detection, cached/latched).
    Detect(upstream) bool
    // Verify checks the response's attached authenticity metadata.
    Verify(req *common.JsonRpcRequest, resp *common.JsonRpcResponse, headers http.Header) SignalResult
}

type SignalResult struct {
    Vendor    string // e.g. "drpc", "ankr-vrpc"
    Valid     bool
    Evidence  []byte // raw signature/quote context for export
}
```

Outcomes feed **upstream scoring/selection only**: valid signals can prefer an upstream for *new* requests; invalid signals are logged + metric + (on repeated failure) a soft score penalty. Disagreement between a valid signal and the anchor → anchor wins, evidence exported, hard cordon per `03` §4.

## 3. Known schemes (edge adapters)

### 3.1 dRPC response signatures + quorum

- **Signatures:** each provider's Dshackle signs the raw JSON-RPC `result` bytes — SHA-256 over `DSHACKLESIG/<nonce>/<upstream_id>/<hex(sha256(result))>`, ECDSA P-256 or RSA PKCS#1 v1.5 — delivered in `QR<N>-id-<request_id>` HTTP headers; verified against a provider key registry (`provider_keys.yaml`, X.509 SPKI).
- **Trust caveat:** the key registry is distributed by dRPC itself → dRPC is the trust anchor for key authenticity. Acceptable for an *advisory* signal; would be unacceptable as a gate. Pin the registry in config (TOFU) and alert on changes.
- **Quorum:** dRPC's `quorum=N&quorum_required=M` request params fan out server-side and return only on M-of-N agreement — a convenient extra vote source for the V-provisional class.
- Refs: `https://drpc.org/docs/gettingstarted/verification`, `github.com/drpcorg/nodecore`.

### 3.2 Ankr vRPC (TEE-attested signatures)

- Node + sidecar run in an Intel TDX confidential VM; an enclave Ed25519 key signs every response over `chain-id + request + response + timestamp` (`vRPC-Signature` header); the pubkey is bound into the TDX attestation quote; open-source SDK verifies locally.
- Proves the response came from approved, unmodified node software — **not** consensus-correctness. Advisory tier.
- Quote verification (TDX attestation chain to Intel) is heavyweight: optional config flag, off by default; without it, verify the Ed25519 signature against the pinned key only (weaker signal).
- Refs: `https://www.ankr.com/docs/verifiable-rpc/overview/`, `github.com/w3tech/verifiable-rpc-sdk`.

### 3.3 Blockdaemon HTTP message signatures

- IETF HTTP Message Signatures (ECDSA P-521, `keyid="blockdaemon-ecdsa-p521"`, key distributed via account manager) — but scoped to the **Staking API**, not RPC reads. Largely not applicable to eRPC's read traffic; implement only if write-path traffic is proxied. Ref: `https://docs.blockdaemon.com/reference/http-message-signatures.md`.

## 4. What signals must never do

1. Never promote a response to `verified` (anchor-only privilege).
2. Never override an anchor conflict verdict (signature-valid + anchor-conflicting = proven lie by an *identified* upstream — worse, not better).
3. Never add latency to the serving path (async verification only; results affect future routing).
4. Never require vendor support to function — a pool of signal-less upstreams is the norm, and the zero-trust guarantee must hold regardless.

## 5. Config & metrics

```yaml
integrity:
  signals:                      # NEW; absent = all off (no-op default)
    drpcSignatures: { enabled: true, providerKeysPath: "/etc/erpc/drpc_provider_keys.yaml" }
    ankrVrpc:       { enabled: false, verifyTdxQuote: false }
```

- `erpc_integrity_signal_verifications_total{network, vendor, verdict}`
- `erpc_integrity_signal_anchor_conflicts_total{network, upstream, vendor}` — signed-but-wrong (page)

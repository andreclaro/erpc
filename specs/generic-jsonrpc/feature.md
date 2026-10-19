# Generic JSON-RPC — Specification

**Status**: Draft — v1 ready for implementation
**Owner**: TBD
**Last revised**: 2026-10-09
**Reference**: <https://erpc.featurebase.app/p/support-for-generic-json-rpc-protocols>

---

## 1. Purpose

Add a **`jsonrpc` network architecture** so eRPC can proxy any JSON-RPC
endpoint — other blockchain protocols (Bitcoin, TON, …), non-blockchain
JSON-RPC services, and custom implementations — with the protocol-agnostic
machinery eRPC already has: load balancing, fault tolerance, rate limiting,
singleflight, and monitoring.

The architecture carries **no protocol logic**: no block tracking, no
finality, no chain identity, no method semantics. Functionality that cannot
be derived without protocol semantics is explicitly off (§5). Caching is off
by default and available only as an explicit per-method TTL opt-in (§7).

Design razor: the unknown-input fallthrough is the *only* path. No method
lists, no chain enums, no vendor special-cases — a generic network knows
nothing about the protocol it proxies.

### Validated example targets

Verified against official documentation (2026-10-06); each is JSON-RPC 2.0
over HTTP POST and needs zero protocol logic to proxy:

| Chain | Fit | Tip method (for the future §7 hint) | Notes |
|---|---|---|---|
| **Starknet** | Clean | `starknet_blockNumber` → `$.result` (plain integer) | Versioned `starknet_*` API; finality tags `latest`/`pre_confirmed`/`l1_accepted`; batching supported; custom error codes are small positive ints |
| **Stellar** | Clean | `getLatestLedger` → `$.result.sequence` | Stellar RPC (ex-Soroban-RPC) is the recommended API; Horizon (REST) is being deprecated in favor of it; ledgers are final on close — no finality tags; some failures arrive inside `result` (`status: "FAILED"`) |
| **NEAR** | Yes, with quirks | `status` → `$.result.sync_info.latest_block_height` (nested) | `finality` request tag (`optimistic`/`near-final`/`final`); `query` is polymorphic via `params.request_type`, so per-method routing/caching granularity is coarse and params-hashed keys matter; batching executes `query` items only |

Evaluated and **not** onboardable (would need separate `rest`/`grpc`
architectures, out of scope here): **Sui** (JSON-RPC deprecated and already
disabled on official endpoints, removed from node code; replacements are
gRPC/GraphQL) and **Aptos** (fullnode API is REST + GraphQL + gRPC — never
had JSON-RPC).

---

## 2. Non-goals

- Interpreting method names, params, or results in any way.
- Block/slot/height tracking of any kind in v1 (see §7 future work for the
  optional tip-method hint).
- Consensus, data-integrity checks, re-org awareness.
- Write-guard semantics (eRPC cannot know which methods are non-idempotent —
  see §11.3).
- Third-party vendor integrations (`thirdparty/` is EVM-flavored; generic
  networks use explicit endpoint upstreams).

---

## 3. Configuration

```yaml
projects:
  - id: main
    networks:
      - architecture: jsonrpc
        jsonrpc:
          networkId: pricing        # slug → full ID "jsonrpc:pricing"
        upstreams:
          - id: svc-a
            type: jsonrpc           # explicit; never inferred
            endpoint: https://svc-a.example/rpc
          - id: svc-b
            type: jsonrpc
            endpoint: https://svc-b.example/rpc
        failsafe:
          - timeout: 10s
            retry:
              maxAttempts: 3
```

| Field | Type | Description |
|---|---|---|
| `architecture` | `string` | New enum value `jsonrpc` alongside `evm`/`svm` (`common/network.go:14-17`). |
| `jsonrpc.networkId` | `string` | User-chosen slug. Identifier charset mirrors the SVM chain/cluster rules (`common/network.go:130-144`): letters, digits, `-`, `_`, `.`; non-empty. |
| `upstreams[].type` | `string` | New upstream type `jsonrpc`. **Explicit only** — the existing omitted-type default of `evm` (`common/defaults.go:1913`) is back-compat legacy and must not apply here. |

Clients reach the network at `/main/jsonrpc/<slug>`, mirroring
`/main/evm/<chainId>` (`erpc/networks_registry.go:213-215`).

A small `jsonrpc` config block (rather than a flat field) mirrors
`evm.chainId` / `svm.chain+cluster` and gives future fields a home without
schema churn.

---

## 4. Design

The codebase already isolates architecture-specific behavior behind the
`ArchitectureHandler` registry (`common/architecture.go:11-34`). The generic
architecture is a new registration; pipeline files do not change.

1. **`architecture/jsonrpc` package** with `init()` calling
   `common.RegisterArchitecture(common.ArchitectureJsonRpc, …)`, mirroring
   `architecture/svm/handler.go:10-12`. All five pre/post-forward hooks are
   no-ops (pass-through).
2. **Generic error extractor** (`NewJsonRpcErrorExtractor` hook) — see §6.
3. **No state poller.** Pollers are created per upstream type in
   `upstream/upstream.go:336-343`; the `jsonrpc` type matches no case, so no
   poller exists and no block tracking happens — by construction.
4. **No feature probing.** `detectFeatures` only probes `evm` upstreams
   (`upstream/upstream.go:1324`); `jsonrpc` upstreams boot with zero
   protocol calls.
5. **Network ID composition**: add the `jsonrpc` case to
   `NetworkConfig.NetworkId()` (`common/config.go:3177-3195`) returning
   `jsonrpc:<slug>`; extend `IsValidArchitecture` / `IsValidNetwork`
   (`common/network.go:88-148`) and `util.IsValidNetworkId` accordingly.
6. **Finality is always `Unknown`.** `Network.GetFinality`
   (`erpc/networks.go:2651-2678`) starts at `DataFinalityStateUnknown`; the
   SVM switch and EVM block-ref extraction never apply. This is the signal
   the v1 cache keys on (§7).
7. **TypeScript config package**: extend `TsNetworkArchitecture`
   (`common/config.go:2259` tstype tag) — rides along with the Go change.

---

## 5. Feature matrix

### Works (protocol-agnostic machinery, inherited unchanged)

| Capability | Why it works |
|---|---|
| Load balancing / upstream selection | Scoring is latency + error-rate driven (`health/tracker.go`); block-height components never activate |
| Retry | Executors built from config with no architecture gate (`erpc/networks_registry.go:122-145`); retryability decided by the generic error taxonomy (`common/errors.go:2550`, `:2636`) |
| Hedge | `failsafe/hedge.go` is a generic library driven by request latency/quantiles |
| Timeout / circuit breaker | Same generic `failsafe/` package |
| Rate limiting | Budgets and per-method rules operate on requests, not protocol semantics |
| Singleflight | In-flight dedup keyed on request hash (`erpc/networks_registry.go:169`) |
| Monitoring / metrics / health / cordons | Per-upstream generic telemetry |
| Auth, CORS, batching/multiplexing, aliases, static responses | HTTP-layer / config-layer features |

### Off or degraded (underivable without protocol logic)

| Capability | Behavior on `jsonrpc` networks |
|---|---|
| Block/slot tracking, finality | No poller; `GetFinality` always `Unknown` |
| Consensus | **v1**: rejected at config load (§8) — dispute/missing-data semantics are block-shaped; silent half-behavior is worse than a clear error. **v2**: consensus configuration supported on `jsonrpc` networks (§7 v2) |
| Data-integrity checks | EVM-only subsystem (`architecture/evm/integrity/`); not wired |
| Block-availability gates, served-tip floors, leader election | EVM/SVM hook logic; generic hooks are no-ops |
| Feature detection / vendor probing | Skipped (`upstream/upstream.go:1324`) |
| Non-idempotent write guard | Impossible to derive — documentation directs users to per-method failsafe overrides (§11.3) |
| Protocol directives | No block-ref directives, commitment injection, or range limits |
| `matchFinality` failsafe scoping | Never matches (finality is always `Unknown`) — per-method scoping (`matchMethod`) is the equivalent |
| Third-party vendor integrations | Explicit endpoint upstreams only |

---

## 6. Error semantics

The generic extractor maps only the JSON-RPC 2.0 spec envelope
(`error.code/message/data`). No vendor mapping, no message-text heuristics.
Chain error taxonomies are too divergent to normalize generically — Starknet
uses small positive custom codes, NEAR funnels handler errors through
`-32000` with a structured `cause`, Stellar sticks to spec codes but embeds
some failures inside `result` (`status: "FAILED"`):

| Shape | Classification | Retryable |
|---|---|---|
| Transport error, timeout, HTTP 5xx | Server-side (existing behavior) | Yes |
| HTTP 429 | Rate-limited (existing behavior) | Per rate-limit policy |
| `-32700` parse error, `-32600` invalid request, `-32601` method not found, `-32602` invalid params | Client-side exception | **No** — no upstream will accept these; retrying is pure waste |
| Any other JSON-RPC error code | Server-side exception (same fallthrough as today, `clients/http_json_rpc_client.go:932-944`) | Yes |

Nothing is ever classified as missing-data or finality-related.

---

## 7. Caching

Off by default; opt-in only. eRPC cannot derive freshness without protocol
semantics, so **the user declares cacheability and its TTL bound**. The
connectors, policies, TTLs, compression, and metrics are already
protocol-agnostic (`data/cache_policy.go`, `data/cache_executor.go`); only
the DAL key partitioning and finality gating are architecture-specific.

### v1 — TTL-only opt-in (in scope)

- New `GenericJsonRpcCache` implementing `common.CacheDAL`
  (`common/cache_dal.go:7-11`). Key = `{networkId}:{method}:{paramsHash}`
  (honoring the existing `cacheKeySuffix`, `common/config.go:2273-2276`).
  No blockRef segment.
- Every generic request resolves to `finality: unknown`, so policies select
  generic traffic with `finality: unknown` — a valid config value
  (`common/data.go:70-72`). `MatchesForSet` requires exact finality equality
  (`data/cache_policy.go:121`).
- Wired as a `case common.ArchitectureJsonRpc` in the cache switch
  (`erpc/networks_registry.go:322-331`), fed by a new
  `database.jsonRpcCache` block (same schema as `evmJsonRpcCache`:
  connectors, policies, compression).
- No matching policy → no caching. Default stays off.

```yaml
database:
  jsonRpcCache:
    connectors:
      - { id: memory, driver: memory, memory: { maxItems: 10000 } }
    policies:
      - network: "jsonrpc:pricing"
        method: "getQuote"
        finality: unknown
        connector: memory
        ttl: 5s                    # user-asserted staleness bound
```

What this gives: cross-request dedup (singleflight only dedups in-flight),
upstream cost reduction, latency on hot repeats.
What it does not give: freshness tied to chain state, invalidation, re-org
awareness.

### v2 — config-declared protocol behaviors

**Full design: [`feature-v2.md`](feature-v2.md).** The v2 idea in one line:
**pass configuration that enables usage of existing machinery — caching,
consensus, etc. — on `jsonrpc` networks**, without adding protocol logic.
The v2 spec covers, grounded in per-chain research (Starknet, Stellar, NEAR,
Noble/CometBFT, Polkadot Asset Hub, XRPL):

- **Caching** — finality declarations unlock permanent + finality-aware
  cache policies (below; mechanism verified, no research needed).
- **Consensus** — failsafe `consensus` configuration on declared methods
  (below; semantics gated on the open questions).
- **Param-conditional declarations** — per-call finality signals such as
  NEAR's `finality` param or Starknet block tags (resolved in
  feature-v2 §4.2 as `finalityFromParam`).
- **Result-embedded status classification** — treating result-carried
  failure states (e.g. Stellar's `NOT_FOUND`, XRPL's `result.error`) as
  uncacheable errors before declaring such methods (resolved in
  feature-v2 §4.3).
- **State poller** — promoted into v2 scope as config-declared
  tip/finalized-tip/floor/health probes (feature-v2 §4.4); the §7-future
  exploration below remains the design notes for it.

`methods.definitions.<method>.finalized: true` (immutable per params →
permanent caching of e.g. reference data) and `.realtime: true`
(finality-aware policy matching). `GetFinality` already honors these flags
before any architecture logic (`erpc/networks.go:2662-2670`), so v2 is a
config-surface change, not new machinery.

v2 also enables **failsafe `consensus` configuration on `jsonrpc`
networks**: with explicit per-method finality declarations in place,
consensus gains enough signal to compare responses meaningfully
(response-equality on `finalized` methods, bounded-staleness comparison on
`realtime` ones). The v1 config-load rejection (§8) is lifted only where the
method set it applies to carries a finality declaration.

The flag mechanism above reuses existing machinery and needs no research;
the surrounding semantics do. Open questions to resolve before v2 is
implementable:

1. **Param-conditional finality.** NEAR's `finality` param and Starknet's
   block tags make one method name volatile *or* immutable per call. Whether
   config can (or should) express param-conditional declarations — rather
   than the weakest safe answer of "don't declare polymorphic methods" — is
   undecided.
2. **Result-embedded failure semantics.** Stellar's `getTransaction` returns
   pending/not-found as a *result* (`status: NOT_FOUND`), not an error.
   Caching and consensus both need a classification story before `finalized`
   declarations are safe on such methods.
3. **Per-chain finality mapping.** `finalized`/`realtime` map differently
   per chain (Stellar: ledger close = final; NEAR: three commitment levels).
   The declarations' meaning must be validated against each target chain's
   finality model before consensus trusts them.
4. **Scope of consensus.** Whether bounded-staleness comparison on
   `realtime` methods is meaningful, or consensus should be restricted to
   `finalized`-declared methods only.

### Future — tip-method hint (exploration only)

Optional `tipMethod` + JSON path (`tipMethod: getBlockHeight`,
`tipPath: "$.result"`) giving generic networks a minimal poller and
height-bucketed keys. Real protocol logic; not committed.

Extraction-path examples from the validated targets show why the path must
be configurable: `starknet_blockNumber` → `$.result` (plain integer),
`getLatestLedger` → `$.result.sequence`, `status` (NEAR) →
`$.result.sync_info.latest_block_height`. Parsing must tolerate
string-encoded integers (several chains serialize u64s as decimal strings).

The tip hint is the seed of a broader **generic state poller** idea: a
per-network tracker (analogous to the EVM state poller) polling the
configured tip method on an interval. It would unlock lag-based upstream
scoring and health, tip-bucketed cache keys for tip-relative ("latest")
reads, and concrete staleness bounds for v2 consensus on `realtime`
methods. Reorg detection stays out of reach — it needs block-hash chains,
i.e. real protocol logic. Exploration only: poll lifecycle, interval and
failure handling, and per-chain extraction variance are machinery that must
be designed, not inherited.

---

## 8. Validation

Config load rejects, with direct errors:

1. `evm` or `svm` blocks present on a `jsonrpc` network (and vice versa:
   `jsonrpc` block on `evm`/`svm` networks).
2. Upstreams under a `jsonrpc` network whose `type` is not `jsonrpc`
   (including omitted — no defaulting).
3. Failsafe policies with `consensus` on a `jsonrpc` network in v1
   (`failsafeExecutor.HasConsensus()`, `erpc/networks.go:2297`); v2 lifts
   this for methods carrying a finality declaration (§7 v2).
4. Empty or invalid-character `jsonrpc.networkId` (§3 charset).
5. Duplicate `jsonrpc.networkId` within a project (same as any network ID).

---

## 9. Observability

All existing metric families apply unchanged; the `finality` label reads
`unknown` on generic networks. No new metric families in v1. Cache
hit/miss metrics (`data/cache_executor.go`) apply once policies exist.

---

## 10. Versioning summary

| Version | Scope |
|---|---|
| **v1** | `jsonrpc` architecture + upstream type, no-op handler, generic error extractor (§6), validation (§8), TTL-only opt-in cache (§7 v1), docs page + TS config |
| **v2** | Config-declared protocol behaviors ([`feature-v2.md`](feature-v2.md)): `methods.definitions` `finalized`/`realtime` flags, param-conditional finality (`finalityFromParam`), result-embedded failure classification, generic state poller (tip/finalized-tip/floor/health), tip-bucketed caching, and failsafe `consensus` on `jsonrpc` networks |
| **Future** | Dynamic finality promotion from poller state (feature-v2 §8.1–8.2), traffic-harvested floors (§8.3) |

Docs ride along with each implementation PR per `AGENTS.md` (new page under
`docs/pages/` for generic networks; `.llms.txt`/`_meta.js` generated, never
hand-edited).

---

## 11. Edge cases & gotchas

1. **Retries on application errors.** Unknown JSON-RPC error codes are
   retryable server-side by default (§6). A generic service whose business
   errors must not be retried should use the four spec codes, or scope
   retries via per-method failsafe config.
2. **`matchFinality` silently never matches** on generic networks (finality
   always `Unknown`). Validation warns when a `jsonrpc` network's failsafe
   config uses `matchFinality` with anything other than `unknown`.
3. **Non-idempotent methods.** Hedge and retry duplicate side effects
   (e.g. a `submit`-style method). eRPC cannot detect these — the docs must
   direct users to disable hedge/retry for such methods via per-method
   failsafe (`matchMethod`), the same answer SVM encodes in
   `IsNonRetryableWriteMethod` (`architecture/svm/handler.go:96-102`).
4. **Empty/null results pass through as-is.** `retryEmpty`-style directive
   defaults are EVM-tuned; generic networks inherit the global defaults, so
   operators relying on empty-result retries must configure
   `directiveDefaults` explicitly.
5. **Batching is JSON-RPC-level** and works, but batch element errors
   classify per-element (§6); a single spec-code element does not poison the
   batch response.
6. **Aliases** work unchanged (`jsonrpc:<slug>` is a two-part ID, which the
   alias registration path already handles, `erpc/networks_registry.go:333`).
7. **Shared project-level upstreams** serving multiple networks must repeat
   `type: jsonrpc` per upstream — there is no inheritance from the network
   (§3).

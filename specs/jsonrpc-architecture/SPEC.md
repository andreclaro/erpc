# Spec: Generic `jsonrpc` Architecture (Issue #1203)

## Goal
A minimal generic `jsonrpc` architecture so ANY JSON-RPC 2.0 endpoint (Starknet,
Stellar RPC, NEAR, …) gets eRPC's transport-grade features — load balancing,
retry/hedge/timeout/circuit-breaker, rate limiting, singleflight, upstream
scoring, metrics, auth, batching, aliases, static responses — with **zero
protocol logic**.

## Non-goals (v1)
- No state poller, no feature probing, no integrity checks.
- No consensus / failsafe `consensus` policies (validation rejects them).
- No cache wiring by default. Opt-in only: per-method cache TTL via the existing
  generic policy machinery, keyed `finality: unknown` (see Finality below).
- No `finality` param semantics, no block/slot tracking, no normalization of
  requests or responses.

## Config shape

### Network
```yaml
projects:
  - id: main
    networks:
      - architecture: jsonrpc
        jsonRpc:
          slug: starknet
        alias: starknet-mainnet
        failsafe:
          - retry:
              maxAttempts: 3
```
- `architecture: jsonrpc` is **always explicit**; never inferred from blocks.
- `NetworkConfig.JsonRpc *JsonRpcNetworkConfig` (`yaml:"jsonRpc"`), required
  for jsonrpc networks, holds `slug` (required, identifier-validated:
  alphanumeric/dash/underscore, no dots — it must survive as a single URL path
  segment).
- Network ID: `jsonrpc:<slug>` via `util.JsonRpcNetworkId(slug)`;
  `NetworkConfig.NetworkId()` gains the `ArchitectureJsonRpc` case.
- Routed generically at `/<project>/jsonrpc/<slug>` — the existing
  `architecture/chainId` URL parsing and `networkId` body hint both work
  unchanged (two-part ID; `SplitN(..., 2)` already handles it).

### Upstream
```yaml
    upstreams:
      - id: starknet-1
        type: jsonrpc            # ALWAYS required, never defaulted/inferred
        endpoint: https://…
        jsonRpc:
          slug: starknet         # pairs upstream to network jsonrpc:starknet
          supportsBatch: true    # existing transport knobs unchanged
          batchMaxSize: 10
          batchMaxWait: 50ms
```
- `UpstreamTypeJsonRpc UpstreamType = "jsonrpc"` (common/architecture_jsonrpc.go).
- The upstream→network pairing key lives on the existing transport block
  (`JsonRpcUpstreamConfig.Slug`). Rationale: `jsonRpc:` is already the
  per-upstream JSON-RPC transport block shared by all architectures; adding
  the pairing `slug` there avoids a colliding second `jsonrpc:` key and
  mirrors how `evm.chainId`/`svm.cluster` pair upstreams to networks.
  - `slug` **required** when `type: jsonrpc`; **rejected** on evm/svm
    upstreams (catches mis-paired/forgotten-type configs early).
- Bootstrap: no RPC probes. `detectFeatures` stores
  `networkId = jsonrpc:<slug>` directly from config (SVM-style static
  binding: identity asserted by configuration, not discovered).
- No state poller is created for `type: jsonrpc` (the Bootstrap switch only
  arms evm/svm pollers; jsonrpc upstreams get a nil poller and all
  poller-gated code already nil-falls-through).
- HTTP client: same `NewGenericHttpJsonRpcClient` transport as evm/svm with
  the composite error extractor (batching/gzip/headers/proxy knobs all apply
  unchanged via `cfg.JsonRpc`).

## Handler & pipeline
- `architecture/jsonrpc/handler.go`: no-op `ArchitectureHandler`
  implementation registered in `init()` via `common.RegisterArchitecture` —
  mirrors `architecture/svm/handler.go` minus all protocol hooks. Constant
  name follows repo idiom: `common.ArchitectureJsonRpc`.
- `erpc/init.go` blank-imports `architecture/jsonrpc` so the registration
  runs in every binary.
- `erpc/networks.go prepareRequest`: `case common.ArchitectureJsonRpc` —
  validate the body parses as JSON-RPC (same as svm) and pass through with
  no normalization.
- `Network.Architecture()` and `GetArchitectureHandler` dispatch work via
  the registry — no changes needed beyond registration.
- Lazy network creation (`erpc/networks_registry.go`) parses the networkId
  prefix; a `jsonrpc:<slug>` ID materializes a `JsonRpcNetworkConfig{Slug}`
  network, mirroring the svm case (so upstreams-only configs work).
- `clients/registry.go`: `case common.UpstreamTypeJsonRpc` building the
  generic HTTP JSON-RPC client (http/https only).

## Error taxonomy
`architecture/jsonrpc/error_extractor.go` — `JsonRpcErrorExtractor`,
returned by the handler's `NewJsonRpcErrorExtractor()`, picked up by the
composite extractor's registry iteration, guarded by
`cfg.Type == common.UpstreamTypeJsonRpc` so it never fires on evm/svm:

| Signal | Classification |
|---|---|
| JSON-RPC code `-32700` (parse), `-32600` (invalid request), `-32601` (method not found), `-32602` (invalid params) | **client-side, non-retryable** (`ErrEndpointClientSideException`) |
| JSON-RPC code `-32603` and all application-defined server codes | **server-side, retryable** (`ErrEndpointServerSideException`) |
| HTTP 401/403 | `ErrEndpointUnauthorized` (auth verdict outranks body) |
| HTTP 429 | `ErrEndpointCapacityExceeded` |
| HTTP ≥500 with no usable JSON-RPC error object | `ErrEndpointServerSideException` |
| HTTP other 4xx with no usable JSON-RPC error object | `ErrEndpointClientSideException` |
| A synthesized `-32700` next to HTTP ≥400 (HTML error pages, plaintext 429s) | classified from HTTP status, not condemned as non-retryable parse |
| 2xx with no error object | nothing extracted (nil) |

The original numeric code passes through verbatim in the wrapped
`ErrJsonRpcExceptionInternal` so chain-native clients keep dispatching on
their own error numbers.

## Finality
- `Network.GetFinality`: jsonrpc returns `DataFinalityStateUnknown`
  (early return before the EVM block-ref extraction fallthrough). The
  per-method `methods:` config check above it stays generic — operators opt
  into caching per method via existing `database` cache policies with
  `finality: unknown` TTL; no architecture-level cache wiring, no jsonrpc
  cache DAL in v1 (EVM/SVM caches key on blockRef/slotRef respectively;
  neither applies, and requirement 6 forbids default wiring).

## Validation (all with tests)
Type-level rules cover everything; a project-level cross-validator (svm
style) is deliberately NOT added: slug pairing is single-dimensional, so
the svm two-dimensional (chain, cluster) silent-mismatch failure mode does
not exist. Rules:

**NetworkConfig.Validate**
1. jsonrpc network with `evm:` block → rejected.
2. jsonrpc network with `svm:` block → rejected.
3. jsonrpc network missing `jsonRpc` block → rejected ("jsonRpc is required").
4. `jsonRpc` block on an evm/svm-architecture network → rejected (no
   accidental no-op blocks).
5. `jsonRpc.slug` missing/invalid chars → rejected (identifier-validated).
6. failsafe `consensus` policy on a jsonrpc network → rejected.

**UpstreamConfig.Validate**
7. `type: jsonrpc` missing `jsonRpc.slug` → rejected.
8. `jsonRpc.slug` on a non-jsonrpc-type upstream → rejected (catches the
   forgot-`type` case: SetDefaults would otherwise silently flip the type
   to evm).
9. `evm:`/`svm:` block on a `type: jsonrpc` upstream → rejected.
10. failsafe `consensus` policy on a `type: jsonrpc` upstream → rejected.

## Defaults
- `NetworkConfig.SetDefaults`: `networkDefaults.evm` must NOT inject an evm
  block into jsonrpc networks (guard mirrors the existing svm guard — the
  derivation checks Evm first and would silently flip the architecture).
  `ArchitectureJsonRpc && JsonRpc == nil` gets an empty block for nil-safety,
  mirroring the evm/svm auto-create.
- `UpstreamConfig.SetDefaults` keeps defaulting an unset `type` to evm — for
  jsonrpc, `type` must be explicit; rule 8 turns a forgotten type into a
  clear validation error instead of silent mis-binding.

## Docs & types
- `docs/pages/config/projects/networks.mdx` gets a `jsonrpc` section
  (matches existing architecture documentation pattern).
- `typescript/config/src/types/generic.ts` unions (`NetworkArchitecture`,
  `UpstreamType`) gain `"jsonrpc"`; `typescript/config/src/generated.ts`
  (tygo output, committed) gains the new config blocks.

## Test plan
- `architecture/jsonrpc/error_extractor_test.go`: every row of the error
  taxonomy table (spec-code classification, -32603 retryable, app-defined
  codes, HTTP 401/403/429/5xx/4xx, synthesized-parse-error next to HTTP
  error, 2xx pass-through, non-jsonrpc upstream no-op).
- `common` validation tests: rules 1–10 (reject + accept cases).
- `util` + `common` id tests: `JsonRpcNetworkId`, `IsValidNetworkId` /
  `IsValidNetwork` / `IsValidArchitecture` jsonrpc cases.
- `common` defaults tests: no evm-block injection into jsonrpc networks;
  empty JsonRpc block auto-created.
- `erpc` end-to-end (httptest upstreams): config with a jsonrpc network +
  two upstreams → forward over `/main/jsonrpc/<slug>`, retry across
  upstreams on server-side error, no retry on -32602, alias routing, static
  response short-circuit, batch request forwarded.

## Files touched (planned)
- `util/ids.go` — `JsonRpcNetworkId`, `IsValidNetworkId` case
- `common/network.go` — `ArchitectureJsonRpc` const, `IsValidArchitecture`,
  `IsValidNetwork`
- `common/architecture_jsonrpc.go` — **new**: `UpstreamTypeJsonRpc` +
  `JsonRpcNetworkConfig`
- `common/config.go` — `JsonRpcUpstreamConfig.Slug`, `NetworkConfig.JsonRpc`,
  `NetworkId()` case
- `common/defaults.go` — network defaults guard + auto-create
- `common/validation.go` — rules 1–10
- `architecture/jsonrpc/` — **new package**: handler, error extractor
- `erpc/init.go` — blank import
- `erpc/networks.go` — prepareRequest case, GetFinality early return
- `erpc/networks_registry.go` — lazy network creation case
- `upstream/upstream.go` — detectFeatures static networkId for jsonrpc
- `clients/registry.go` — jsonrpc HTTP client case
- docs + typescript + tests

## Design-conflict notes (conservative choices)
- Upstream pairing via `jsonRpc.slug` on the shared transport block instead
  of a new colliding top-level key (see Config shape).
- No project-level pairing validator: single-dimensional slug pairing is
  fully enforced by type-level rules (see Validation).
- Lazy network creation supports `jsonrpc:<slug>` (mirrors svm) so
  upstreams-only configs keep working; the always-explicit rule applies to
  `architecture:` on declared networks and to upstream `type:`, not to the
  generic networkId-derived lazy path that all architectures share.
- v1 adds no `NetworkConfig.Methods` changes and no cache connector changes;
  opt-in TTL reuses the existing generic `CachePolicyConfig.finality`
  machinery. No jsonrpc cache DAL: EVM/SVM cache key partitioning
  (blockRef/slotRef) is protocol-shaped and requirement 6 forbids default
  wiring.
- Naming: `ArchitectureJsonRpc` (not `ArchitectureJsonrpc`) to match the
  repo's `JsonRpc*` idiom.

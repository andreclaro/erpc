# Generic JSON-RPC v2 — Config-Declared Protocol Behaviors

**Status**: Draft — design for review
**Owner**: TBD
**Last revised**: 2026-10-09
**Builds on**: [`feature.md`](feature.md) (v1 spec) — v1 ships the architecture,
routing, error envelope, and TTL-only caching; v2 adds no new protocol logic,
only **config surfaces that let operators declare protocol facts** which the
existing machinery then consumes.
**Reference**: <https://erpc.featurebase.app/p/support-for-generic-json-rpc-protocols>

---

## 1. Purpose

v1 makes a `jsonrpc` network a correct but conservative proxy: no finality
signal, no cache beyond TTL opt-in, no consensus, no tip tracking. v2 unlocks
the existing protocol-agnostic machinery on generic networks by letting the
**operator declare, per network and per method, the facts eRPC cannot derive**:

- which methods are immutable per params (`finalized`) or moving-head
  (`realtime`) — already-existing config flags, zero new code (§4.1);
- which request **param carries finality** and how its values map to eRPC's
  finality enum — NEAR's `finality` param, Starknet's block tags, XRPL's
  `ledger_index` (§4.2);
- which **result-embedded shapes are failures** — Stellar's `status:
  "NOT_FOUND"`, XRPL's `result.error` envelope (§4.3);
- how to **poll the chain tip** (and optionally the finalized tip, the
  availability floor, and a health probe) — unlocking lag-based scoring,
  tip-bucketed cache keys, and staleness bounds (§4.4);
- when **consensus** is meaningful on a generic network (§4.6).

Everything is declared configuration consumed by generic code paths. No method
lists, chain enums, or vendor tables ship in the binary — the design razor from
v1 holds: the unknown-input fallthrough stays the primary path, and an
undeclared method behaves exactly as in v1.

---

## 2. Research findings (the evidence base)

usdc.cool lists every chain with native USDC. Most are EVM (served by the
`evm` architecture) or Solana (`svm`). The remaining roster, re-verified
against official docs on 2026-10-09:

| Chain | Transport | Tip method → path (encoding) | Finality expression | Failure shape |
|---|---|---|---|---|
| **Starknet** | JSON-RPC 2.0 POST | `starknet_blockNumber` → `$.result` (number) | `block_id` tag param: `latest` / `pre_confirmed` / `l1_accepted` | standard error envelope, custom **positive** codes |
| **Stellar RPC** | JSON-RPC 2.0 POST | `getLatestLedger` → `$.result.sequence` (number) | ledgers final on close (SCP); no finality params | `result.status: "NOT_FOUND"` on `getTransaction`/`getTransactions` |
| **NEAR** | JSON-RPC 2.0 POST | `status` → `$.result.sync_info.latest_block_height` (number) | `finality` param: `optimistic` / `near-final` / `final` | `-32000` with structured `cause` object |
| **Noble / CometBFT** | JSON-RPC 2.0 POST (+ GET) | `status` → `$.result.sync_info.latest_block_height` (**decimal string**) | instant single-block BFT finality; no tags | spec codes + app codes nested in `data` |
| **Polkadot Asset Hub (Substrate)** | JSON-RPC 2.0 POST (legacy API) | `chain_getHeader` → `$.result.number` (**hex string**); finalized tip is **two-hop**: `chain_getFinalizedHead` → `$.result` (hash) → `chain_getHeader(hash)` → `$.result.number` | best vs GRANDPA-finalized heads; block-hash params pin | spec codes + client errors |
| **XRP Ledger** | JSON-RPC 2.0 POST (params = array with one object) | `server_info` → `$.result.info.validated_ledger.seq` (number); floor: `$.result.info.complete_ledgers` (`"32570-8812345"` range string) | `ledger_index` param: `validated` / `current` / `closed` / number | **every** error is result-embedded: HTTP 200, `result.status: "error"`, `result.error: <code>` |

Excluded, with reasons:

- **Tron** — `/wallet/*` is REST-style POST without a JSON-RPC envelope (a
  future `rest` architecture); `/jsonrpc` is Ethereum-compatible and belongs
  to the `evm` architecture ([Tron API docs](https://developers.tron.network/docs/api)).
- **Algorand, Aleo** — REST APIs, no JSON-RPC surface.
- **Aptos, Sui** — already excluded in v1 (REST/GraphQL/gRPC; Sui's JSON-RPC
  deprecated and removed).
- **Hedera** — its JSON-RPC relay is EVM-flavored → `evm` architecture.

### 2.1 Starknet

- Block tags are `latest`, `pre_confirmed`, and `l1_accepted`
  ([starknet_getStorageAt](https://www.quicknode.com/docs/starknet/starknet_getStorageAt),
  [starknet_estimateFee](https://www.quicknode.com/docs/starknet/starknet_estimateFee));
  `block_id` may also be `{block_number}` or `{block_hash}` objects. Only
  `l1_accepted` is immutable; a pinned `block_number`/`block_hash` is accepted
  on L2 but still replaceable in principle → `unfinalized`, not `finalized`.
- Block responses carry `status: PRE_CONFIRMED | ACCEPTED_ON_L2 |
  ACCEPTED_ON_L1` ([starknet_getBlockWithTxs](https://www.quicknode.com/docs/starknet/starknet_getBlockWithTxs))
  — a result-embedded finality signal if response-side classification is ever
  wanted; v2 does not consume it.
- Errors use small positive custom codes (e.g. `24` BlockNotFound, `32` "no
  blocks", [dwellir](https://www.dwellir.com/docs/starknet/starknet_blockNumber)).
  Mapping those to missing-data is an operator decision → §4.3.

### 2.2 Stellar RPC

- `getLatestLedger` result: `{ id: <hex>, protocolVersion: <number>,
  sequence: <number> }` ([docs](https://developers.stellar.org/docs/data/apis/rpc/api-reference/methods/getLatestLedger)).
- `getTransaction` never errors for a pending/unknown hash: it returns
  `result.status: "SUCCESS" | "FAILED" | "NOT_FOUND"`, plus `latestLedger` and
  `oldestLedger` — the node's retention window, inline in every response
  ([docs](https://developers.stellar.org/docs/data/apis/rpc/api-reference/methods/getTransaction)).
  A `FAILED` transaction **is** a final, cacheable result; `NOT_FOUND` is
  transient (pending) or permanent (pruned) and must never be cached. This
  asymmetry is the motivating case for §4.3.
- Retention: RPC nodes keep a sliding window of ledgers (default ~7 days);
  `oldestLedger` exposes the floor → §4.4 floor extraction.

### 2.3 NEAR

- Finality is a per-request param `finality: "optimistic" | "near-final" |
  "final"` (`optimistic` may be skipped; `near-final` = DoomSlug, irreversible
  unless a block producer is slashed; `final` = irreversible —
  [docs.near.org](https://docs.near.org/data-infrastructure/tutorials/listen-to-realtime-events)),
  or an explicit `block_id`. Documented default for `query` is `final`.
- Handler errors funnel through `-32000` with a structured `cause` (e.g.
  `UNKNOWN_BLOCK`), so "not found" classification needs sub-error matching →
  §4.3 `dataPath`.
- Non-archival nodes garbage-collect state after ~5 epochs — pruned reads are
  a missing-data signal, retryable against archival peers.
- `query` is polymorphic via `params.request_type` — params-hashed cache keys
  (v1) already keep variants apart; per-method declarations apply to `query`
  as a whole, so declare it `realtime` or leave undeclared.

### 2.4 Noble / CometBFT

- `status` returns `sync_info.latest_block_height` as a **string-encoded**
  integer ([CometBFT RPC](https://docs.cosmos.network/cometbft/latest/api-reference/rpc))
  — the §4.4 parser must accept decimal strings, not just JSON numbers.
- Instant single-slot BFT finality: latest tip **is** the finalized tip. A
  one-counter poller is sufficient; `finalizedTip` can alias `tip`.
- Application reads go through the polymorphic `abci_query(path, data)` —
  same coarse-granularity story as NEAR `query`.
- `health` (empty 200 result) is a ready-made health probe for §4.4.

### 2.5 Polkadot Asset Hub (Substrate legacy JSON-RPC)

- Best tip: `chain_getHeader` (no params) → `$.result.number`, a **hex**
  string. Finalized tip is two hops: `chain_getFinalizedHead` → `$.result`
  (block hash), then `chain_getHeader(hash)` → `$.result.number`
  ([PSP-6](https://github.com/w3f/PSPs/blob/master/PSPs/drafts/psp-6.md)).
  The §4.4 poller config must express an optional resolve hop.
- GRANDPA finality lags the best head; hash-pinned reads (`chain_getBlock`,
  `state_getStorage` with a hash) are immutable once finalized →
  `unfinalized` declarations.
- The newer `chainHead_*` API is websocket-first; the legacy HTTP JSON-RPC
  remains the deployable target for a POST-only proxy.

### 2.6 XRP Ledger

- JSON-RPC requests use method names like `ledger`, `tx`, `account_info`,
  `server_info` with params as an **array containing one object**.
- Errors are result-embedded: HTTP 200, `result.status: "error"`,
  `result.error: "<code>"` ([error formatting](https://xrpl.org/docs/references/http-websocket-apis/api-conventions/error-formatting)).
  Codes are strings: `txnNotFound`, `lgrNotFound`, `actNotFound`
  (missing-data), `tooBusy` (server/busy), `noCurrent`/`noClosed`/`noNetwork`
  (node not synced), `unknownCmd` (client). This is the motivating case for
  network-level §4.3 rules.
- `ledger_index: "validated"` is the final level; `current`/`closed` are not;
  defaults vary per method, so explicit pinning is the recommended config.
- `server_info.result.info.complete_ledgers` is a `"start-end"` range string
  exposing the availability floor ("empty" when unsynced) → §4.4 floor
  extraction.

---

## 3. Existing machinery v2 reuses (verified)

No new subsystems. Each v2 mechanism feeds an already-existing consumer:

| Mechanism | Consumer | Where |
|---|---|---|
| `finalized`/`realtime` method flags | `GetFinality` checks config **first**, before any architecture logic | `erpc/networks.go:2662-2670`, `common/config.go:313-328` (`CacheMethodConfig`) |
| Finality-aware cache policies | `MatchesForSet` exact finality equality; `MatchesForGet` wildcarding | `data/cache_policy.go:82-170` |
| Cache key partitioning | `CachePartitionKey(networkId, suffix, ref)` + params-hashed range key | `common/config.go:3168-3175`, `common/json_rpc.go:1466-1499` |
| Tip tracking | `Tracker.SetLatestBlockNumber` / `SetFinalizedBlockNumber` → `BlockHeadLag`/`FinalizationLag` | `health/tracker.go:1406`, `:1627` |
| Lag-based selection | policy engine reads `BlockHeadLag`, `BlockHeadLagSeconds`, quantiles | `internal/policy/eval.go:67-99` |
| Block-time-relative behavior | network block-time EMA from tip samples | `health/tracker.go:1469-1477` |
| Consensus | protocol-neutral executor (response equality, protocol-neutral response classes) | `consensus/executor.go`, `consensus/analysis.go` |
| Poller lifecycle | per-upstream-type poller construction switch | `upstream/upstream.go:336-343` |

Two gates are EVM-shaped and v2 must work around, not into:

- `GetFinality`'s fallthrough after the config check runs **EVM block-ref
  extraction for any non-SVM architecture** (`erpc/networks.go:2680+`). For
  `jsonrpc` networks this yields empty refs → `Unknown` — harmless today, but
  §4.2 must hook *into the config-driven section*, not the EVM path.
- Consensus **leader election is EVM-gated** (`consensus/analysis.go:114-117`);
  `preferBlockHeadLeader` / `onlyBlockHeadLeader` would silently degrade on a
  `jsonrpc` network → validation rejects them (§6).

---

## 4. v2 config schema

All v2 fields are optional; absence = v1 behavior. Everything lives under the
existing `jsonrpc` network block or `methods.definitions` — no new top-level
blocks.

### 4.1 Method finality declarations (recap — existing fields)

```yaml
methods:
  definitions:
    getLedgerEntry:            # Stellar: immutable once answered
      finalized: true
    getHealth:                 # moving-head liveness
      realtime: true
```

`finalized` = response uniquely determined by the params, immutable once it
exists → permanent-cache eligible via `finality: finalized` policies.
`realtime` = moving-head → TTL policies keyed on `finality: realtime`.
Neither = v1 `unknown`. These flags work on any architecture today
(`erpc/networks.go:2662-2670`); v2 adds the validation rules (§6) and the
param-conditional refinement below.

### 4.2 Param-conditional finality

One method name, per-call finality. Declared on the method definition:

```yaml
methods:
  definitions:
    block:                          # NEAR
      finalityFromParam:
        key: finality               # find any object param holding this key
        map:
          final: finalized
          near-final: unfinalized
          optimistic: realtime
        default: realtime           # param absent or value unmapped
    starknet_getBlockWithTxs:       # Starknet: positional, value may be a tag
      finalityFromParam:            # string or an object
        index: 0
        map:
          l1_accepted: finalized
          latest: realtime
          pre_confirmed: realtime
        default: unfinalized        # {block_number}/{block_hash} objects land here
    ledger:                         # XRPL
      finalityFromParam:
        key: ledger_index
        map:
          validated: finalized
          current: realtime
          closed: realtime
        default: realtime           # numeric indexes land here; see §8.2
```

| Field | Type | Description |
|---|---|---|
| `key` | `string` | Match any object param containing this key (mirrors how `resolveCommitment` scans params, `architecture/svm/hooks.go:278-284`). Mutually exclusive with `index`. |
| `index` | `int` | Positional param index. |
| `map` | `map[string]string` | Param value (stringified) → finality enum (`finalized`/`unfinalized`/`realtime`/`unknown`). |
| `default` | `string` | Finality when the param is absent, non-string (e.g. an object block id), or unmapped. Required — no silent `unknown`. |

Semantics:

- Evaluated inside `GetFinality`'s config-driven section (before the EVM
  fallthrough), so it applies exactly where `finalized`/`realtime` flags are
  honored today.
- `finalityFromParam` is mutually exclusive with the static `finalized` /
  `realtime` flags on the same method (§6).
- The mapping table is the operator's assertion about the chain's finality
  model; eRPC never validates values against a chain enum (none exists).
- Weakest-design note: `unfinalized` (not `finalized`) is the right landing
  spot for pinned-but-maybe-replaceable refs (Starknet object block ids,
  XRPL numeric indexes) — a wrong promotion is a permanent wrong cache entry,
  a wrong demotion costs one re-fetch.

### 4.3 Result-embedded failure classification

Teaches the generic extractor (v1 §6) shapes that are failures despite
arriving as HTTP 200 with a `result`. Ordered rule lists, definable at
network level (applies to every method) and refined per method:

```yaml
networks:
  - architecture: jsonrpc
    jsonrpc:
      networkId: xrpl
      errorRules:                       # network level
        - match: { resultPath: "$.result.status", equals: "error" }
          codePath: "$.result.error"    # extract a sub-code for finer mapping
          codeMap:
            txnNotFound: missingData
            lgrNotFound: missingData
            actNotFound:  missingData
            tooBusy:      serverError    # retryable, penalizes the upstream
            noCurrent:    missingData    # node unsynced → try another
            unknownCmd:   clientError    # non-retryable
          default: serverError
    methods:
      definitions:
        getTransaction:                 # Stellar example (per-method level)
          resultClassifications:
            - match: { resultPath: "$.result.status", in: ["NOT_FOUND"] }
              classifyAs: missingData
```

| Field | Type | Description |
|---|---|---|
| `match.resultPath` | `string` | JSON path into the result object. |
| `match.errorPath` / `match.dataPath` | `string` | Paths into the JSON-RPC `error` object instead — covers NEAR's `-32000` + structured `cause` (`errorPath: "$.error.data.cause.name", in: ["UNKNOWN_BLOCK"]`). |
| `match.equals` / `match.in` | `string` / `[]string` | Value match. |
| `codePath` + `codeMap` | `string` + `map[string]string` | Two-stage matching: rule matches the envelope, then the extracted sub-code picks the class (XRPL). |
| `classifyAs` / `default` | `string` | One of `missingData`, `clientError`, `serverError` — the three actionable classes in the existing taxonomy (`common/errors.go`); everything else stays the v1 fallthrough. |

Semantics:

- `missingData` → retryable missing-data error: triggers the
  retry-with-next-upstream path (catch-up semantics) and is **never written to
  cache**. This is what makes a `finalized` declaration on Stellar's
  `getTransaction` safe: `SUCCESS`/`FAILED` cache permanently, `NOT_FOUND`
  never enters the cache.
- `clientError` → non-retryable client fault (joins the four spec codes).
- `serverError` → retryable server-side exception (v1 default for unknown
  codes; the rule exists for the *sub-code* extraction, not the class).
- Rules are evaluated in order, first match wins; no match → v1 behavior.
- A matched rule converts the response into an error *before* the cache layer
  sees it — so cacheability never needs a separate override.

### 4.4 Generic state poller

Per-network, under the `jsonrpc` block:

```yaml
jsonrpc:
  networkId: starknet
  statePoller:
    interval: 6s                       # optional; default 2x expected block time or 5s
    tip:
      method: starknet_blockNumber
      path: "$.result"                 # JSON path to the height
      encoding: auto                   # auto | number | string | hex
    finalizedTip:                      # optional second counter
      method: chain_getFinalizedHead   # Substrate example — two-hop:
      path: "$.result"                 #   ... a block hash
      thenCall:                        #   resolve hash → height
        method: chain_getHeader
        passValueAsParam: true         #   first call's value becomes sole param
        path: "$.result.number"
        encoding: hex
    floor:                             # optional availability floor
      method: server_info              # XRPL example
      path: "$.result.info.complete_ledgers"
      rangeStart: true                 # value "32570-8812345" → floor 32570
    health:                            # optional liveness probe
      method: health                   # CometBFT example (empty result = ok)
```

| Field | Description |
|---|---|
| `tip.method` / `tip.path` / `tip.encoding` | The tip probe. `encoding: auto` accepts a JSON number, a decimal string (CometBFT), or a `0x` hex string (Substrate) — all three appear in §2. |
| `finalizedTip` | Optional second counter. Optional `thenCall` expresses the Substrate-style hash→header resolve hop; `passValueAsParam` sends the first value as the sole param. |
| `floor` | Optional lower availability bound (Stellar `oldestLedger`, XRPL `complete_ledgers`). `rangeStart` parses the head of a `"a-b"` range string. |
| `health` | Optional probe method; a JSON-RPC error (or transport error) marks the upstream unhealthy. Without it, liveness is inferred from tip progress. |

What the poller feeds — all existing consumers, no new scoring code:

- `Tracker.SetLatestBlockNumber` / `SetFinalizedBlockNumber`
  (`health/tracker.go:1406`, `:1627`) → per-upstream `BlockHeadLag` /
  `FinalizationLag` atomics and the network block-time EMA.
- The selection-policy engine already reads those atomics
  (`internal/policy/eval.go:67-99`) → lag-aware scoring (`maxBlockHeadLag`,
  `blockHeadLagBelow`, …) starts working on generic networks.
- Tip progress doubles as a liveness signal; a stalled tip cordons exactly
  like the SVM poller's health verdict.

Explicitly not fed: EVM leader election, tip ballots, and
`EvmEffective*`/served-tip machinery read the poller *interfaces*, not the
tracker — they stay architecture-specific. Reorg detection stays out of
scope: it needs block-hash chains, i.e. real protocol logic.

Construction: a `case common.UpstreamTypeJsonRpc` in the poller switch
(`upstream/upstream.go:336-343`), created only when `statePoller` is
configured; without config there is still no poller (v1 property preserved).
Implementation reuses the SVM poller's shape (shared counters, debounce
gate, traffic-skip) minus the protocol-specific probes.

### 4.5 Tip-bucketed caching

With a poller, `realtime` reads gain freshness-tied caching: the v1 cache key
`{networkId}:{method}:{paramsHash}` gains a tip segment —
`{networkId}:{tipBucket}:{method}:{paramsHash}` — where `tipBucket = tip /
bucketSize`. Config per method:

```yaml
methods:
  definitions:
    getNetwork:
      realtime: true
      cacheTipBucket: 1        # partition by tip / N; requires statePoller
```

- Keys roll forward with the chain: stale buckets expire by TTL, never by
  invalidation logic (no protocol semantics needed).
- `bucketSize` trades freshness for hit rate; `1` = per-tip.
- Validation requires a configured `statePoller` (§6); without one the flag
  is rejected, because a frozen bucket would silently cache forever.
- Composes with `finality: realtime` policies whose TTL bounds the worst-case
  staleness within a bucket.

### 4.6 Consensus on `jsonrpc` networks

The executor is already protocol-neutral (response-equality hashing,
protocol-neutral response classes; finality used only for telemetry —
`consensus/executor.go:1516`). v2 lifts the v1 config-load rejection under
declared conditions:

- **`finalized`-declared methods** — consensus compares immutable responses;
  meaningful without any poller. No further requirement.
- **`realtime`-declared methods** — meaningful only within a tip bucket, so
  validation requires a configured `statePoller`; responses are compared only
  among upstreams whose tracked tip agrees within the policy's lag bounds
  (the same `BlockHeadLag` atomics the selection engine uses).
- **Undeclared methods** — consensus stays rejected (v1 behavior).
- `preferBlockHeadLeader` / `onlyBlockHeadLeader` behaviors are rejected on
  `jsonrpc` networks — leader election is EVM-gated
  (`consensus/analysis.go:114-117`) and would silently degrade.
- Result-embedded failure rules (§4.3) run *before* consensus comparison, so
  a `NOT_FOUND` never counts as a valid response in the agreement set.

---

## 5. Worked configurations

### Starknet

```yaml
networks:
  - architecture: jsonrpc
    jsonrpc:
      networkId: starknet
      statePoller:
        tip: { method: starknet_blockNumber, path: "$.result" }
      errorRules:
        - match: { errorPath: "$.error.code", in: ["24", "20"] }   # BlockNotFound, ContractNotFound
          classifyAs: missingData
    methods:
      definitions:
        starknet_getBlockWithTxs:
          finalityFromParam:
            index: 0
            map: { l1_accepted: finalized, latest: realtime, pre_confirmed: realtime }
            default: unfinalized
        starknet_getTransactionReceipt: { finalized: true }   # hash-pinned
        starknet_blockNumber: { realtime: true }
```

### Stellar

```yaml
networks:
  - architecture: jsonrpc
    jsonrpc:
      networkId: stellar
      statePoller:
        tip:  { method: getLatestLedger, path: "$.result.sequence" }
        floor: { method: getLatestLedger, path: "$.result.sequence" }  # see note
    methods:
      definitions:
        getLedgerEntries: { finalized: true }     # hash/seq-pinned entries
        getTransaction:
          finalized: true                          # safe ONLY with the rule below
          resultClassifications:
            - match: { resultPath: "$.result.status", in: ["NOT_FOUND"] }
              classifyAs: missingData
        getLatestLedger: { realtime: true, cacheTipBucket: 1 }
```

Note: Stellar's floor (`oldestLedger`) is returned on `getTransaction`, not
`getLatestLedger` — floor extraction from a *tip* method that doesn't carry
it is open question §8.3; a stub config like the above is invalid until
resolved.

### NEAR

```yaml
networks:
  - architecture: jsonrpc
    jsonrpc:
      networkId: near
      statePoller:
        tip: { method: status, path: "$.result.sync_info.latest_block_height" }
      errorRules:
        - match: { errorPath: "$.error.data.cause.name", in: ["UNKNOWN_BLOCK", "GARBAGE_COLLECTED_BLOCK"] }
          classifyAs: missingData
    methods:
      definitions:
        block:
          finalityFromParam:
            key: finality
            map: { final: finalized, near-final: unfinalized, optimistic: realtime }
            default: realtime
        tx: { finalized: true }                     # hash-pinned receipts
        query: { realtime: true }                   # polymorphic; params-hashed keys
```

### Noble / CometBFT

```yaml
networks:
  - architecture: jsonrpc
    jsonrpc:
      networkId: noble
      statePoller:
        tip:       { method: status, path: "$.result.sync_info.latest_block_height", encoding: string }
        health:    { method: health }
    methods:
      definitions:
        block:  { finalized: true }    # height-pinned, instantly final
        tx:     { finalized: true }
        status: { realtime: true }
        abci_query: { realtime: true } # polymorphic; coarse granularity
```

### Polkadot Asset Hub

```yaml
networks:
  - architecture: jsonrpc
    jsonrpc:
      networkId: polkadot-asset-hub
      statePoller:
        tip: { method: chain_getHeader, path: "$.result.number", encoding: hex }
        finalizedTip:
          method: chain_getFinalizedHead
          path: "$.result"
          thenCall: { method: chain_getHeader, passValueAsParam: true, path: "$.result.number", encoding: hex }
    methods:
      definitions:
        chain_getBlock: { unfinalized: true }   # hash-pinned, GRANDPA-lagged
        chain_getFinalizedHead: { realtime: true }
```

### XRP Ledger

```yaml
networks:
  - architecture: jsonrpc
    jsonrpc:
      networkId: xrpl
      statePoller:
        tip:   { method: server_info, path: "$.result.info.validated_ledger.seq" }
        floor: { method: server_info, path: "$.result.info.complete_ledgers", rangeStart: true }
      errorRules:
        - match: { resultPath: "$.result.status", equals: "error" }
          codePath: "$.result.error"
          codeMap:
            txnNotFound: missingData
            lgrNotFound: missingData
            actNotFound:  missingData
            tooBusy:      serverError
            noCurrent:    missingData
            noClosed:     missingData
            unknownCmd:   clientError
          default: serverError
    methods:
      definitions:
        ledger:
          finalityFromParam:
            key: ledger_index
            map: { validated: finalized, current: realtime, closed: realtime }
            default: realtime
        tx: { finalized: true }
```

---

## 6. Validation (v2 additions)

Config load rejects, with direct errors (all evaluated in
`common/validation.go` network/methods validation — new architecture-aware
rules, same style as v1 §8):

1. `finalityFromParam` combined with `finalized` or `realtime` on the same
   method definition (contradictory declarations).
2. `finalityFromParam` with both/neither of `key`/`index`, an empty `map`, a
   missing `default`, or a value outside the finality enum.
3. A failsafe `consensus` policy matching methods with **no** finality
   declaration on a `jsonrpc` network; consensus on `realtime`-declared
   methods without a configured `statePoller`.
4. `preferBlockHeadLeader` / `onlyBlockHeadLeader` consensus behaviors on a
   `jsonrpc` network.
5. `cacheTipBucket` without a configured `statePoller`; `bucketSize < 1`.
6. `statePoller.tip` without both `method` and `path`; `thenCall` without
   `passValueAsParam` (the only supported parameterization in v2);
   `encoding` outside `auto|number|string|hex`.
7. `errorRules` / `resultClassifications` with an empty match, an unknown
   `classifyAs`, or `codeMap` without `codePath`.
8. A rule whose match path targets `$.error.*` on a network and
   `$.result.*` per method is allowed — network rules run first, method rules
   refine.

---

## 7. What v2 still does not do

- No block-hash chaining, no reorg detection, no integrity checks — a
  tip *number* cannot prove ancestry.
- No served-tip floors or block-availability guards — those are
  architecture-hook features (EVM/SVM) that presume richer per-block
  semantics. The poller's tip feeds *scoring* only.
- No new write-guard: non-idempotent methods remain the operator's
  responsibility via per-method failsafe (`matchMethod`) — unchanged from
  v1 §11.3. (Starknet `starknet_addInvokeTransaction`, XRPL `submit`, NEAR
  `broadcast_tx_commit` are the examples; XRPL submissions are
  sequence-idempotent at the ledger level, but eRPC cannot know that.)
- No EVM-only consensus leader election (rejected, §6.4).

---

## 8. Open questions

1. **Numeric `ledger_index` / `block_id` refinement.** A numeric XRPL
   `ledger_index ≤ validated tip` is in fact final; likewise a Starknet
   `block_number` below the L1-accepted tip. Comparing params against the
   poller's live counters would promote those to `finalized` — but makes
   finality depend on mutable poller state, so an identical request changes
   class over time. v2 takes the static answer (`default: unfinalized` /
   `realtime`); dynamic promotion is deferred.
2. **Response-side finality signals.** Starknet block responses carry
   `status: ACCEPTED_ON_L1`; Stellar `getTransaction` carries `ledger`. A
   `finalityFromResult` rule could promote per-response. Same mutable-state
   concern as (1), plus cache writes racing classification; deferred.
3. **Floor extraction when the tip method doesn't carry it** (Stellar's
   `oldestLedger` lives on `getTransaction`). Options: allow `floor.method` ≠
   `tip.method` (already in schema), or harvest floors from live traffic.
   Schema supports both; harvesting is deferred.
4. **Per-upstream `statePoller` overrides** (a slow vendor needing a longer
   interval). v2 keeps the block network-level only.
5. **Consensus on `realtime` methods**: whether tip-bucket agreement bounds
   are sufficient or consensus should be restricted to `finalized`-declared
   methods in practice. The validation gate (§6.3) ships conservative; relax
   only with production evidence.

---

## 9. Edge cases & gotchas

1. **Encoding variance in one value.** CometBFT heights are decimal strings,
   Substrate numbers hex strings, XRPL's `complete_ledgers` a range — all
   under the same `path` schema. `encoding: auto` must never throw; an
   unparseable sample is a skipped tick, not a crash (mirrors the SVM
   poller's per-fetch isolation, `architecture/svm/svm_state_poller.go:340-383`).
2. **`finalityFromParam` + batching.** Batch elements classify individually,
   exactly as errors do per element in v1 (§11.5 there).
3. **Stellar `FAILED` is a success shape.** The §4.3 rule maps `NOT_FOUND`
   only; `FAILED` must remain a cacheable, finalized result — over-broad `in:`
   lists (`["NOT_FOUND", "FAILED"]`) would silently never-cache.
4. **XRPL `params` shape.** Params is an array with a single object —
   `finalityFromParam.key` scanning finds it, but `index: 0` would yield the
   whole object, not the tag. Validation warns when `index` extracts an
   object and no `valuePath` exists (§4.2's `default` covers this, but the
   warning saves a misconfiguration).
5. **Poller cost on paid vendors.** The tip probe fires per upstream per
   interval; on metered vendors this is the dominant background cost (the SVM
   poller measured the same, `architecture/svm/svm_state_poller.go:33-65`).
   `interval` should be ≥ the chain's block time; the traffic-skip/debounce
   pattern from the SVM poller applies unchanged.
6. **`thenCall` failure isolation.** A failed resolve hop invalidates only
   the finalized counter for that tick; the tip counter still updates.
7. **Unsynced XRPL nodes** report `complete_ledgers: "empty"` — `rangeStart`
   parsing must treat non-range values as "no floor", never as floor 0.

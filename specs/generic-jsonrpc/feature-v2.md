# Generic JSON-RPC v2 — Config-Declared Protocol Behaviors

**Status**: Draft — design for review
**Owner**: TBD
**Last revised**: 2026-10-11
**Builds on**: [`feature.md`](feature.md) (v1 spec) — v1 ships the architecture,
routing, error envelope, and TTL-only caching; v2 adds no new protocol logic,
only **config surfaces that let operators declare protocol facts** which the
existing machinery then consumes.
**Research**: every wire-format claim below was verified against official
documentation and, where marked, live probes (2026-10-09/11) by a 7-agent
research pass: Starknet (starknet-specs OpenRPC 0.10.x), Stellar RPC
(docs + OpenRPC + SDF testnet live), NEAR (docs.near.org + openapi.json +
nearcore source), Noble/CometBFT (v0.38 spec + Noble mainnet live), Polkadot
Asset Hub (polkadot-sdk docs + public endpoint live), XRPL (xrpl.org +
s1.ripple.com live), Tron (developers.tron.network + TronGrid live).
**Reference**: <https://erpc.featurebase.app/p/support-for-generic-json-rpc-protocols>

---

## 1. Purpose

v1 makes a `jsonrpc` network a correct but conservative proxy: no finality
signal, no cache beyond TTL opt-in, no consensus, no tip tracking. v2 unlocks
the existing protocol-agnostic machinery on generic networks by letting the
**operator declare, per network and per method, the facts eRPC cannot derive**:

- which methods are immutable per params (`finalized`), pinned-but-fork-
  droppable (`unfinalized`), or moving-head (`realtime`) (§4.1);
- which request **param carries finality** and how its values map to eRPC's
  finality enum — NEAR's `finality` param, Starknet's block tags, XRPL's
  `ledger_index` (§4.2);
- which **result-embedded shapes are failures** — Stellar's `status:
  "NOT_FOUND"`, XRPL's `result.error` envelope, CometBFT/Substrate null-body
  not-founds (§4.3);
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
against official docs and live endpoints (2026-10-09/11):

| Chain | Transport | Tip method → path (encoding) | Finality expression | Failure shape |
|---|---|---|---|---|
| **Starknet** | JSON-RPC 2.0 POST | `starknet_blockNumber` → `$.result` (number) | `block_id` tag: `latest` / `pre_confirmed` / `l1_accepted` (**required** param) | standard error envelope, custom **positive** codes; receipts/statuses also embed finality fields |
| **Stellar RPC** | JSON-RPC 2.0 POST (by-name params) | `getLatestLedger` → `$.result.sequence` (number) | none — ledgers final on close (SCP); no finality params | `result.status: "NOT_FOUND"` on `getTransaction`; `-32600` carries out-of-range |
| **NEAR** | JSON-RPC 2.0 POST | `status` → `$.result.sync_info.latest_block_height` (number) | `finality` param (**required**): `optimistic` / `near-final` / `final` | structured `error.cause.name` (verror); failed txs inside `result.status.Failure` |
| **Noble / CometBFT** | JSON-RPC 2.0 POST (+ GET) | `status` → `$.result.sync_info.latest_block_height` (**decimal string**) | instant single-block BFT finality; no tags | spec codes + `result.code` / `result.response.code` embedded; null-body `result.block` |
| **Polkadot Asset Hub (Substrate)** | JSON-RPC 2.0 POST (legacy API) | `chain_getHeader` → `$.result.number` (**hex string**); finalized tip is **two-hop**: `chain_getFinalizedHead` → `$.result` (hash) → `chain_getHeader(hash)` → `$.result.number` | best vs GRANDPA-finalized heads; block-hash params pin | spec codes + module codes (`4003` UnknownBlock); `result: null` not-found |
| **XRP Ledger** | **JSON-RPC-flavored** POST — *not* spec-2.0 (no `jsonrpc` field; params = one-item array; **no batching**) | `server_info` → `$.result.info.validated_ledger.seq` (number); floor: `$.result.info.complete_ledgers` (disjoint range string) | `ledger_index` param: `validated` / `current` / `closed` / number; **default differs xrpld (`current`) vs Clio (`validated`)** | **every** error is result-embedded: HTTP 200, `result.status: "error"`, `result.error: <string code>` |

Excluded, with reasons:

- **Tron** — two disjoint surfaces. `/wallet/*` is REST-style POST without a
  JSON-RPC envelope (a future `rest` architecture; finality is selected by the
  `/wallet/` vs `/walletsolidity/` path prefix, not-found is HTTP 200 `{}`).
  `/jsonrpc` is genuine JSON-RPC 2.0 `eth_*` — but read-mostly (no
  `eth_sendRawTransaction`; broadcast stays on the REST surface) — so it is
  fronted by the `evm` architecture, not `jsonrpc`
  ([Tron API docs](https://developers.tron.network/docs/api)). Tron's
  `{}`-as-not-found, `result.result` boolean wrappers, and capital-`E`
  `{"Error": …}` rate-limit bodies are the motivating case for the REST
  profile's own classification config, out of scope here.
- **Algorand, Aleo** — REST APIs, no JSON-RPC surface.
- **Aptos, Sui** — already excluded in v1 (REST/GraphQL/gRPC; Sui's JSON-RPC
  deprecated and removed).
- **Hedera** — its JSON-RPC relay is EVM-flavored → `evm` architecture.

### 2.1 Starknet

- Current full-node spec is **0.10.x** ([starknet-specs](https://github.com/starkware-libs/starknet-specs));
  nodes multiplex spec versions by URL path (`/rpc/v0_8`, `/rpc/v0_9`,
  `/rpc/v0_10` — [Juno docs](https://juno.nethermind.io/json-rpc)), and tag
  semantics shifted across versions (`pending` → `pre_confirmed` in 0.9). A
  proxy must forward the path verbatim; version-mixed upstream pools are a
  consensus hazard.
- `BLOCK_TAG` enum, verbatim from the spec: `l1_accepted` = "included in a
  state update on L1 and finalized by the consensus on L1"; `latest` =
  "finalized by the consensus on L2" (per-block Tendermint finality since
  SN 0.14, but the protocol formally admits reorgs — `starknet_subscriptionReorg`
  exists); `pre_confirmed` = "the block currently being built … `latest` + 1"
  (a node-local view — "accuracy depends on its polling interval").
  `block_id` is **required** on every state-scoped read; there is no node
  default ([spec BLOCK_TAG](https://raw.githubusercontent.com/starkware-libs/starknet-specs/master/api/starknet_api_openrpc.json)).
  Mapping: `l1_accepted → finalized`, `latest → realtime` (L2-final but
  formally reorg-admissible), `pre_confirmed → realtime`, object block
  refs → `unfinalized`.
- **Result-embedded statuses** ([spec](https://raw.githubusercontent.com/starkware-libs/starknet-specs/master/api/starknet_api_openrpc.json)):
  `starknet_getTransactionReceipt` → `$.result.finality_status`
  (`PRE_CONFIRMED|ACCEPTED_ON_L2|ACCEPTED_ON_L1`) + `execution_status`
  (`SUCCEEDED|REVERTED`) — a REVERTED receipt is a *successful JSON-RPC
  response describing a failed transaction*; `starknet_getTransactionStatus`
  → the wider enum incl. `RECEIVED` (node-local mempool state — responses
  legitimately differ across upstreams); block results embed `$.result.status`.
  Not-founds are proper errors: `TXN_HASH_NOT_FOUND` (29), `BLOCK_NOT_FOUND` (24).
- Writes: `starknet_addInvokeTransaction` etc. are **idempotent for the
  identical signed payload** — resubmission yields `59 DUPLICATE_TX` (proof of
  prior acceptance, no double-execution); a *different* payload with the same
  nonce is a replacement requiring bumped fees (`64 REPLACEMENT_TRANSACTION_UNDERPRICED`).
- Batching: supported by implementations (Juno default-on), **not mentioned in
  the spec** — per-provider variance.

### 2.2 Stellar RPC

- `getLatestLedger` result: `{ id: <hex>, protocolVersion: <number>,
  sequence: <number>, closeTime: <string> }` ([docs](https://developers.stellar.org/docs/data/apis/rpc/api-reference/methods/getLatestLedger));
  params are **by-name objects only** (positional arrays are deprecated).
- **One tip counter; no finality params anywhere.** SCP never reorganizes
  served data ("there is no branching in SCP" — [SDF](https://stellar.org/blog/developers/issuer-enforced-finality-explained));
  ledgers close ~every 5 s and are final at close. `latest == finalized`.
- `getHealth` returns `latestLedger`, **`oldestLedger`**, and
  `ledgerRetentionWindow` ([docs](https://developers.stellar.org/docs/data/apis/rpc/api-reference/methods/getHealth))
  — the node's retention floor in the same call, resolving the floor problem
  noted in earlier drafts.
- `getTransaction` never errors for a pending/unknown hash: `result.status ∈
  {SUCCESS, FAILED, NOT_FOUND}` + inline `latestLedger`/`oldestLedger`
  ([docs](https://developers.stellar.org/docs/data/apis/rpc/api-reference/methods/getTransaction)).
  Live-verified quirk: the NOT_FOUND shape carries **zero-value junk fields**
  (`ledger: 0`, `createdAt: "0"`) — gate field reads on `status`. A `FAILED`
  transaction **is** a final, cacheable result; `NOT_FOUND` is transient
  (pending) or permanent (pruned) and must never be cached.
- Retention: `HISTORY_RETENTION_WINDOW = 120960` ledgers (~7 days), **operator-
  configurable per node** ([config](https://developers.stellar.org/docs/data/apis/rpc/admin-guide/configuring));
  out-of-range → `-32600` with the node's live window in the message —
  the identical request succeeds on one upstream and errors on another, so
  range errors are per-upstream state, not deterministic client faults.
- Parse errors break the envelope entirely: HTTP 500 body `[-32700] invalid
request value` (not a JSON-RPC object) — live-verified.
- `sendTransaction` results embed `status: PENDING | DUPLICATE |
  TRY_AGAIN_LATER | ERROR` — identical-payload resubmission returns
  `DUPLICATE` (no double-apply); `TRY_AGAIN_LATER` is a per-account sequencing
  retry signal.
- Batch: undocumented but live-verified working on SDF endpoints; third-party
  variance likely.

### 2.3 NEAR

- Finality is a **required** per-request param `finality: "optimistic" |
  "near-final" | "final"` (mutually exclusive with `block_id`; omitting both is
  a request-validation error, not a default — [block-chunk](https://docs.near.org/api/rpc/block-chunk)).
  Semantics: `optimistic` may be skipped; `near-final` = DoomSlug, irreversible
  unless a block producer is slashed; `final` = irreversible
  ([setup](https://docs.near.org/api/rpc/setup)); ~1.2–3 s to final.
- Tip: `status` → `$.result.sync_info.latest_block_height` (plain number);
  sibling fields `syncing` (bool), `earliest_block_height` (nullable GC
  watermark) — [network API](https://docs.near.org/api/rpc/network).
  **Finalized tip:** `block` with `{"finality":"final"}` →
  `$.result.header.height` ([block-chunk](https://docs.near.org/api/rpc/block-chunk)).
- Errors are a structured **verror envelope**: `error.name`
  (`HANDLER_ERROR|REQUEST_VALIDATION_ERROR|INTERNAL_ERROR`) + actionable
  `error.cause.name` in SCREAMING_SNAKE (`UNKNOWN_BLOCK`, `GARBAGE_COLLECTED_BLOCK`,
  `UNAVAILABLE_SHARD`, `NOT_SYNCED_YET`, `NODE_IS_SYNCING`, …) with structured
  `cause.info`; numeric `code`/`data`/`message` are documented as legacy —
  match on paths, not codes ([errors](https://docs.near.org/api/rpc/errors)).
  Node-local causes (`UNAVAILABLE_SHARD`, `NO_SYNCED_BLOCKS`, …) mean "this
  node, not this chain" — retryable elsewhere and score against the upstream.
- Failed transactions return in a successful envelope: `$.result.status =
  {"Failure": …}` vs `{"SuccessValue": …}`; `wait_until` defaults to
  `EXECUTED_OPTIMISTIC`, and `SuccessValue` below `FINAL` is not yet final —
  caching a `Failure` outcome as success is the classic poisoning trap.
- Batching: **query-only** — only the exact `query` method executes inside a
  batch; anything else gets `-32601`; disabled servers answer a single
  non-array `-32005` ([batching](https://docs.near.org/api/rpc/batching)).
- Retention: non-archival nodes GC after **5 epochs (~1.5 days)** →
  `UNKNOWN_BLOCK`; `status.sync_info.earliest_block_height` exposes the
  watermark. `query` is polymorphic via `params.request_type`;
  `changes` via `changes_type` — params-hashed keys keep variants apart.
- Writes: `send_tx` / `broadcast_tx_*` — identical signed tx resubmission is
  safe ("unique transactions apply exactly once"); `TIMEOUT_ERROR` resolution
  is literally "resubmit the identical transaction". Tx expiry bounds the
  retry window.

### 2.4 Noble / CometBFT

- `status` → `$.result.sync_info.latest_block_height` as a **decimal string**
  (all int64 fields are strings; uint32 `code` fields are numbers — mixed
  encodings inside one response) + `catching_up` (bool) +
  `earliest_block_height` ([schema](https://github.com/cometbft/cometbft/blob/v0.38.x/rpc/openapi/openapi.yaml);
  live-verified on Noble). Instant single-block BFT finality; one tip counter
  suffices. `health` returns `{"result":{}}` — content-free; liveness is
  `catching_up == false` + tip progress, not the probe body.
- **Result-embedded failures** (live-verified): `broadcast_tx_sync` →
  `result.code != 0` (+ `codespace`, `log`); `broadcast_tx_commit` →
  `result.check_tx.code` / `result.deliver_tx.code`; `abci_query` →
  `result.response.code` (6 = unknown path, 18 = pruned state);
  `block_results` → `result.txs_results[].code`; **`block`/`header` not-found
  is a null body** (`result.block: null`), not an error; `tx_search`
  not-found is an empty array + `total_count: "0"`. App codes are
  application-defined and open-ended — never enumerated.
- Errors collapse into `-32603` with a text `data` payload: future height,
  pruned height ("height N is not available, lowest height is M" — live),
  tx not found, indexing disabled. `subscribe` is WS-only → `-32601` over HTTP.
- `abci_query` is the polymorphic mega-method (path + opaque protobuf `data`).
- Byte-array params are **base64** in JSON-RPC POST mode (`0x…` hex is
  rejected with `-32602`), uppercase hex without `0x` in responses.
- Writes: `broadcast_tx_*` — duplicate of an in-mempool tx → `-32603 "tx
  already exists in cache"`; committed-and-evicted re-broadcast fails at app
  layer (sequence mismatch), not a double side effect.

### 2.5 Polkadot Asset Hub (Substrate legacy JSON-RPC)

- Best tip: `chain_getHeader` (no params) → `$.result.number`, a **hex**
  string [live]. Finalized tip is two hops: `chain_getFinalizedHead` →
  `$.result` (hash), then `chain_getHeader(hash)` → `$.result.number`
  ([PSP-6](https://github.com/w3f/PSPs/blob/master/PSPs/drafts/psp-6.md)) —
  the §4.4 `thenCall` resolve hop. (Archive nodes expose
  `archive_v1_finalizedHeight`, a plain-integer monotonic finalized height —
  an alternative when all upstreams support it.)
- GRANDPA: best-head blocks **can** be reorganized; finalized blocks cannot
  ([wiki](https://wiki.polkadot.network/learn/learn-consensus/)). Live on
  Asset Hub (2026-10): finalized lagged best by **18–24 blocks** (~35–50 s at
  ~2 s blocks). No finality params/tags in the legacy API — levels are
  selected by method choice; node default with no `at` param = **best
  (unfinalized)**. Hash-pinned reads → `unfinalized`.
- **Result-embedded failures** [live]: `chain_getHeader`/`chain_getBlock`/
  `chain_getBlockHash` at unknown or future positions → **`result: null`**
  (never cache as permanent absence); `state_getStorage` unset key →
  `result: null`; pruned state → error **`4003` "Client error: UnknownBlock:
  State already discarded"** — retriable elsewhere, never cacheable as
  absence. Default full nodes keep ~256 blocks of state (~8 min) but
  full block history.
- Error modules: STATE base 4000 (`4003` = client catch-all), AUTHOR base
  1000 (`1013` "Transaction Already Imported" = duplicate submission,
  hash-deduped), rate-limit `-32999`. `rpc_methods` is a live introspection
  method — usable for upstream capability detection. Subscriptions
  (`chain_subscribe*`) are WS-only → `-32603` over HTTP.
- Writes: `author_submitExtrinsic` — resubmitting the identical extrinsic
  returns `1013` (no second side effect); only one instance can ever be
  included.

### 2.6 XRP Ledger

- **Not JSON-RPC 2.0-compliant** ([request formatting](https://xrpl.org/docs/references/http-websocket-apis/api-conventions/request-formatting)):
  no `jsonrpc` field; `params` is a **one-item array** wrapping a single
  object; `api_version` (default v1) changes wire shapes (e.g. inner
  `ledger.ledger_index` is a decimal **string** in v1, a number in v2).
  **Batching is not supported** — an array payload gets HTTP 400 with a
  plain-text body. A spec-strict JSON-RPC layer mis-parses all of this; the
  proxy must tolerate the dialect.
- **Every error is result-embedded** at HTTP 200: `result.status: "error"` +
  `result.error: <string code>` ([error formatting](https://xrpl.org/docs/references/http-websocket-apis/api-conventions/error-formatting);
  live-verified `txnNotFound` code 29, `lgrNotFound` code 21). String codes are
  canonical. Universal: `tooBusy`, `noCurrent`, `noClosed`, `noNetwork`,
  `unknownCmd`, `failedToForward` (Clio).
- Finality: `ledger_index: "validated"` = final (no depth ladder; ~3–5 s);
  `"current"`/`"closed"` are not; numeric indexes are per-request pinning.
  **`$.result.validated` (bool)** is a response-embedded finality flag on
  `ledger`, `tx`, `account_info`. **The default differs by server software:**
  xrpld defaults to `current` (non-final), Clio to `validated` — "Do not rely
  on the default behavior… Always specify a ledger version in the request if
  you can." Identical unpinned requests to different upstreams therefore
  return different finality — a consensus and caching hazard that only
  explicit pinning (config injection or `finalityFromParam`) removes.
- Tip: `server_info` → `$.result.info.validated_ledger.seq` (number) +
  `complete_ledgers` (range string, may be **disjoint** `"a-b,c-d"` or
  `"empty"`) + `server_state` (`disconnected|connected|syncing|tracking|full|
  validating|proposing`) ([server_info](https://xrpl.org/docs/references/http-websocket-apis/public-api-methods/server-info-methods/server_info);
  live-verified). Clio's `server_info` omits `server_state` and exposes
  `cache.latest_ledger_seq` instead — poller health rules must handle both.
- Retention: online deletion defaults to the **most recent 2000 ledgers**
  (~2.5 h; min 256); pruned → `lgrNotFound`/`txnNotFound` (live-verified);
  `complete_ledgers` exposes the per-node window. Full-history servers exist
  (s2.ripple.com).
- Rate limiting is **transport-level**: HTTP 503 plain-text `Server is
  overloaded` (not JSON), plus top-level `"warning": "load"` on responses —
  the generic extractor must handle non-envelope bodies.
- Writes: `submit` — identical `tx_blob` resubmission is safe (account
  sequence dedup); the response's `engine_result` (e.g. `tesSUCCESS`) is
  **provisional** — finality only via `tx` against a validated ledger.

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

### 4.1 Method finality declarations

```yaml
methods:
  definitions:
    getLedgerEntry:            # Stellar: immutable once answered
      finalized: true
    starknet_getBlockWithTxs:  # pinned to an id, fork-droppable below final
      unfinalized: true
    getHealth:                 # moving-head liveness
      realtime: true
```

| Flag | Semantics | Status |
|---|---|---|
| `finalized` | Response uniquely determined by the params, immutable once it exists → permanent-cache eligible via `finality: finalized` policies. | **Exists today** (`erpc/networks.go:2662-2670`) |
| `realtime` | Moving-head read; TTL policies keyed on `finality: realtime`. | **Exists today** |
| `unfinalized` | Pinned to a slot/height/hash/signature but fork-droppable until the chain's finality catches up → TTL policies keyed on `finality: unfinalized` (a `finalized` policy's Get-matching already covers unfinalized responses, `data/cache_policy.go:155-159`). | **v2 addition** — extends the same config-flag short-circuit; no new machinery |

The three flags are mutually exclusive per method (§6). Neither flag set =
v1 `unknown`. The existing two flags work on any architecture today; v2 adds
the `unfinalized` flag, the missing member of the quartet the finality enum
already has (`common/data.go:20-27`) — Substrate hash-pinned reads and
Starknet object `block_id`s are the motivating cases.

### 4.2 Param-conditional finality

One method name, per-call finality. Declared on the method definition:

```yaml
methods:
  definitions:
    block:                          # NEAR (param is required by nearcore;
      finalityFromParam:            # `default` covers unmapped values only)
        key: finality               # find any object param holding this key
        map:
          final: finalized
          near-final: unfinalized
          optimistic: realtime
        default: realtime
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
        default: realtime           # numeric indexes land here; see §8.1
```

| Field | Type | Description |
|---|---|---|
| `key` | `string` | Match any object param containing this key (mirrors how `resolveCommitment` scans params, `architecture/svm/hooks.go:278-284`). Mutually exclusive with `index`. |
| `index` | `int` | Positional param index. |
| `map` | `map[string]string` | Param value (stringified) → finality enum (`finalized`/`unfinalized`/`realtime`/`unknown`). |
| `default` | `string` | Finality when the param is absent, non-string (e.g. an object block id), or unmapped. Required — no silent `unknown`. |

Semantics:

- Evaluated inside `GetFinality`'s config-driven section (before the EVM
  fallthrough), so it applies exactly where the config flags are honored today.
- `finalityFromParam` is mutually exclusive with the static `finalized` /
  `unfinalized` / `realtime` flags on the same method (§6).
- The mapping table is the operator's assertion about the chain's finality
  model; eRPC never validates values against a chain enum (none exists).
- Weakest-design note: `unfinalized` (not `finalized`) is the right landing
  spot for pinned-but-maybe-replaceable refs — a wrong promotion is a
  permanent wrong cache entry, a wrong demotion costs one re-fetch.

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
        block:                          # CometBFT example: null-body not-found
          resultClassifications:
            - match: { resultPath: "$.result.block", isNull: true }
              classifyAs: missingData
```

| Field | Type | Description |
|---|---|---|
| `match.resultPath` | `string` | JSON path into the result object. |
| `match.errorPath` | `string` | Path into the JSON-RPC `error` object — covers NEAR's verror (`errorPath: "$.error.cause.name", in: ["UNKNOWN_BLOCK", "GARBAGE_COLLECTED_BLOCK", "UNAVAILABLE_SHARD", "NOT_SYNCED_YET"]`) and Substrate's module codes (`in: ["4003"]`). |
| `match.equals` / `match.in` | `string` / `[]string` | Value match. **Values are stringified on both sides before comparison** — chain codes arrive as numbers (Starknet `24`) or strings (Substrate `"4003"`) depending on the wire. |
| `match.isNull` | `bool` | Matches a JSON `null` value — the CometBFT/Substrate/Tron not-found shape (`result.block: null`, `result: null`). |
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
- Retention-range errors that embed per-node live state (Stellar `-32600`,
  CometBFT `-32603` "lowest height is M") are per-upstream state: classify as
  `missingData`, never as client faults — the identical request can succeed on
  a fuller upstream.

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
      # unhealthyWhen:                 # optional sync-flag extraction from the
      #   path: "$.result.sync_info.catching_up"   # same response (CometBFT)
      #   equals: true
    finalizedTip:                      # optional second counter
      method: chain_getFinalizedHead   # Substrate example — two-hop:
      path: "$.result"                 #   ... a block hash
      thenCall:                        #   resolve hash → height
        method: chain_getHeader
        passValueAsParam: true         #   first call's value becomes sole param
        path: "$.result.number"
        encoding: hex
    floor:                             # optional availability floor
      method: getHealth                # Stellar example — oldestLedger lives here
      path: "$.result.oldestLedger"
      # rangeStart: true               # XRPL: value "32570-8812345" → floor 32570
    health:                            # optional liveness probe
      method: status                   # NEAR example; CometBFT `health` is
      # unhealthyWhen: { path: "$.result.sync_info.syncing", equals: true }
      #                                  # content-free — prefer catching_up via tip
```

| Field | Description |
|---|---|
| `tip.method` / `tip.path` / `tip.encoding` | The tip probe. `encoding: auto` accepts a JSON number, a decimal string (CometBFT), or a `0x` hex string (Substrate) — all three appear in §2. Never apply EVM-style "QUANTITY is hex" decoding blindly: Starknet heights are plain integers, and mixed encodings exist inside single responses (CometBFT strings for int64, numbers for uint32). |
| `tip.unhealthyWhen` | Optional matcher on the tip response (e.g. `catching_up`, `syncing`, XRPL `server_state in [connected, syncing, disconnected]`) — when it matches, the upstream is treated unhealthy for this tick exactly like a failed probe. |
| `finalizedTip` | Optional second counter (NEAR `block{finality:final}` → `$.result.header.height`; XRPL `server_info.validated_ledger.seq`). Optional `thenCall` expresses the Substrate-style hash→header resolve hop; `passValueAsParam` sends the first value as the sole param. (Archive-only alternatives like `archive_v1_finalizedHeight` work too when all upstreams support them.) |
| `floor` | Optional lower availability bound. Stellar: `getHealth.oldestLedger`; XRPL: `server_info.complete_ledgers` with `rangeStart` parsing the head of a (possibly disjoint) `"a-b,c-d"` range string; NEAR: `status.sync_info.earliest_block_height` (nullable — absent means unknown, never 0); CometBFT: `status...earliest_block_height` (live anomaly observed reporting `"0"` while enforcing a higher floor — treat 0/absent as "unknown", never as a real floor). |
| `health` | Optional probe method; a JSON-RPC/transport error marks the upstream unhealthy for the tick. Probes with content-free success bodies (CometBFT `health` → `{"result":{}}`) still work as up/down checks; sync judgment belongs in `tip.unhealthyWhen`. |

What the poller feeds — all existing consumers, no new scoring code:

- `Tracker.SetLatestBlockNumber` / `SetFinalizedBlockNumber`
  (`health/tracker.go:1406`, `:1627`) → per-upstream `BlockHeadLag` /
  `FinalizationLag` atomics and the network block-time EMA.
- The selection-policy engine already reads those atomics
  (`internal/policy/eval.go:67-99`) → lag-aware scoring (`maxBlockHeadLag`,
  `blockHeadLagBelow`, …) starts working on generic networks.
- Tip progress doubles as a liveness signal; a stalled tip (or a matching
  `unhealthyWhen`) cordons exactly like the SVM poller's health verdict.

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
- **`unfinalized`-declared methods** — meaningful with a poller (staleness
  bound) or without (pure response equality, degraded);
  `preferNonEmpty`/`preferLargerResponses`/`preferHighestValueFor` do the
  heavy lifting.
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
- **Per-upstream divergence is expected and declared, not anomalous:**
  Starknet `pre_confirmed`/`RECEIVED` are node-local views; Stellar retention
  is operator-configurable; XRPL xrpld vs Clio default to different ledgers.
  Declarations + rules make these cases classify correctly instead of
  dissenting.

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
        - match: { errorPath: "$.error.code", in: ["24", "29"] }   # BlockNotFound, TxnHashNotFound
          classifyAs: missingData
    methods:
      definitions:
        starknet_getBlockWithTxs:
          finalityFromParam:
            index: 0
            map: { l1_accepted: finalized, latest: realtime, pre_confirmed: realtime }
            default: unfinalized
        starknet_getTransactionReceipt: { finalized: true }   # hash-pinned; REVERTED is a cacheable result
        starknet_blockNumber: { realtime: true }
```

Note: `starknet_blockNumber` reports the L2-finalized `latest` head — one
counter is enough unless L1-finality bounds are wanted (then add a
`finalizedTip` via `starknet_getBlockWithTxHashes` at `block_id: l1_accepted`
→ `$.result.block_number`).

### Stellar

```yaml
networks:
  - architecture: jsonrpc
    jsonrpc:
      networkId: stellar
      statePoller:
        tip:   { method: getLatestLedger, path: "$.result.sequence" }
        floor: { method: getHealth, path: "$.result.oldestLedger" }
        health: { method: getHealth }
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

### NEAR

```yaml
networks:
  - architecture: jsonrpc
    jsonrpc:
      networkId: near
      statePoller:
        tip:
          method: status
          path: "$.result.sync_info.latest_block_height"
          unhealthyWhen: { path: "$.result.sync_info.syncing", equals: true }
        finalizedTip:
          method: block
          path: "$.result.header.height"          # params: {"finality":"final"} — see §8.4
        floor: { method: status, path: "$.result.sync_info.earliest_block_height" }
      errorRules:
        - match: { errorPath: "$.error.cause.name", in: ["UNKNOWN_BLOCK", "GARBAGE_COLLECTED_BLOCK", "UNAVAILABLE_SHARD", "NO_SYNCED_BLOCKS", "NOT_SYNCED_YET", "NODE_IS_SYNCING"] }
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
        tip:
          method: status
          path: "$.result.sync_info.latest_block_height"
          encoding: string
          unhealthyWhen: { path: "$.result.sync_info.catching_up", equals: true }
    methods:
      definitions:
        block:  { finalized: true }    # height-pinned, instantly final
        tx:     { finalized: true }
        status: { realtime: true }
        abci_query:                    # polymorphic; coarse granularity
          realtime: true
          resultClassifications:
            - match: { resultPath: "$.result.response.code", equals: "18" }   # pruned state
              classifyAs: missingData
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
      errorRules:                                # network level
        - match: { errorPath: "$.error.code", equals: "4003" }   # UnknownBlock / pruned state
          classifyAs: missingData
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
        # No health probe method carries server_state on Clio — see §9.8.
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

1. More than one of `finalized` / `unfinalized` / `realtime` on the same
   method definition, or `finalityFromParam` combined with any of them
   (contradictory declarations).
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
   `classifyAs`, `codeMap` without `codePath`, or `isNull` combined with
   `equals`/`in` on the same matcher.
8. Floor values that parse to 0 or are absent (XRPL `"empty"`, CometBFT `"0"`
   anomaly) are treated as "floor unknown" at runtime — never as a real
   availability bound; validation does not reject them (they are live state).

---

## 7. What v2 still does not do

- No block-hash chaining, no reorg detection, no integrity checks — a tip
  *number* cannot prove ancestry.
- No served-tip floors or block-availability guards — those are
  architecture-hook features (EVM/SVM) that presume richer per-block
  semantics. The poller's tip feeds *scoring* only.
- No new write-guard machinery. Cross-chain research finding: **every
  researched chain is idempotent under byte-identical resubmission**
  (Starknet `59 DUPLICATE_TX`, Stellar `DUPLICATE`, NEAR "apply exactly
  once", CometBFT tx-cache, Substrate `1013`, XRPL account-sequence) — so
  retry/hedge of the identical signed payload is safe on all validated
  targets, while *distinct* payloads (replacement, re-signed, same-nonce
  different-content) can double-apply or silently fail. eRPC still cannot
  tell which methods are non-idempotent or distinguish payload identity, so
  the guidance stays v1 §11.3: disable retry/hedge per method via
  `matchMethod` for non-idempotent traffic. (XRPL `submit` results are
  provisional — `engine_result` ≠ final.)
- No EVM-only consensus leader election (rejected, §6.4).
- No websocket-only method surfaces (Starknet/Substrate/XRPL subscriptions)
  — eRPC is HTTP POST; those methods stay unroutable.

---

## 8. Open questions

1. **Numeric `ledger_index` / `block_id` refinement.** A numeric XRPL
   `ledger_index ≤ validated tip` is in fact final; likewise a Starknet
   `block_number` below the L1-accepted tip. Comparing params against the
   poller's live counters would promote those to `finalized` — but makes
   finality depend on mutable poller state, so an identical request changes
   class over time. v2 takes the static answer (`default: unfinalized` /
   `realtime`); dynamic promotion is deferred.
2. **Response-side finality signals.** Starknet block `status` /
   receipt `finality_status`, Stellar `getTransaction.ledger`, XRPL
   `result.validated` all declare per-response finality. A
   `finalityFromResult` rule could promote per-response. Same mutable-state
   concern as (1), plus cache writes racing classification; deferred.
3. **Floor staleness.** Floors are snapshots at poll time; a fast-pruning
   upstream can drop below its last-published `oldestLedger` between polls.
   Acceptable for scoring; a Get-side floor guard (like EVM availability
   bounds) would need hook machinery v2 does not add. The Stellar `getHealth`
   case (floor in the probe response) is resolved; CometBFT's
   `earliest_block_height: "0"` anomaly is handled by the §6.8 rule.
4. **Static params on poller probes.** NEAR's finalized tip requires
   `params: {"finality":"final"}` and XRPL's `server_info` takes `[{}]`; the
   schema above shows only method/path. v2 adds an optional `params` field on
   `tip`/`finalizedTip`/`floor`/`health` probes (verbatim JSON, sent as-is) —
   trivial, but must be specced before implementation.
5. **Per-upstream `statePoller` overrides** (a slow vendor needing a longer
   interval). v2 keeps the block network-level only.
6. **Consensus on `realtime` methods**: whether tip-bucket agreement bounds
   are sufficient or consensus should be restricted to `finalized`-declared
   methods in practice. The validation gate (§6.3) ships conservative; relax
   only with production evidence.

---

## 9. Edge cases & gotchas

1. **Encoding variance in one value.** CometBFT heights are decimal strings,
   Substrate numbers hex strings, XRPL's `complete_ledgers` a range, Starknet
   heights plain integers — all under the same `path` schema. `encoding:
   auto` must never throw; an unparseable sample is a skipped tick, not a
   crash (mirrors the SVM poller's per-fetch isolation,
   `architecture/svm/svm_state_poller.go:340-383`).
2. **`finalityFromParam` + batching.** Batch elements classify individually,
   exactly as errors do per element in v1 (§11.5 there) — including NEAR's
   query-only batches, where non-`query` elements return `-32601`.
3. **Stellar `FAILED` is a success shape.** The §4.3 rule maps `NOT_FOUND`
   only; `FAILED` must remain a cacheable, finalized result — over-broad `in:`
   lists (`["NOT_FOUND", "FAILED"]`) would silently never-cache. Same class
   of trap: Starknet `execution_status: REVERTED` and NEAR
   `result.status.Failure` are cacheable outcomes of *failed* transactions,
   not failures of the request.
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
7. **Unsynced XRPL nodes** report `complete_ledgers: "empty"` and Clio omits
   `server_state` — `rangeStart` parsing must treat non-range values as "no
   floor", never as floor 0; health rules referencing Clio-absent fields must
   default to "unknown, not unhealthy".
8. **Non-envelope error bodies exist on the wire.** Stellar parse errors →
   HTTP 500 body `[-32700] invalid request value`; XRPL → plain-text 400
   (`Null method`, `Unable to parse JSON`) and 503 (`Server is overloaded`);
   CometBFT parse errors → HTTP 500. The v1 transport layer must treat these
   as server-side retryables without assuming a JSON-RPC envelope — XRPL's
   503 is its rate-limit signal and TronGrid's REST `{"Error": …}` (future
   REST profile) arrives HTTP 200.
9. **Batch support is a spectrum, not a boolean.** CometBFT and Substrate
   support arrays; Starknet implements but doesn't spec it; Stellar works but
   is undocumented; NEAR executes only `query` inside batches (`-32601` for
   others, `-32005`/`-32010`/`-32011` limits); **XRPL rejects arrays
   outright** (HTTP 400 plain text). Multiplexing config must treat batching
   as per-upstream capability, exactly as v1's generic path already does for
   unknown capabilities.
10. **Error bodies echo secrets and live state.** XRPL error results copy the
    full request (incl. signing secrets in sign-and-submit mode); Stellar and
    CometBFT messages embed live ledger windows. Sanitize before logging;
    never pattern-match on message text — key on codes/paths only.

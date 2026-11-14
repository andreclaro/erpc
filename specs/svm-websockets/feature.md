# Solana Virtual Machine WebSocket Support — Specification

**Status**: Implemented in `feat/svm-websocket`. The specification was updated after implementation to reflect the Alpenglow commitment model.
**Last revised**: 2026-11-14
**Implementation plan**: [plan.md](plan.md), maintained in a separate deliverable.

Solana Virtual Machine (SVM) support uses the existing WebSocket transport. It adds direct SVM subscription proxying. It does not reuse Ethereum Virtual Machine (EVM) chain feeds.

## 1. Objective

eRPC must accept SVM subscriptions on client WebSocket connections. Each subscription must send 1 upstream subscribe request to 1 selected upstream. Agave can return the same upstream ID for equivalent subscriptions. eRPC must fan out that shared notification stream to every local route.

eRPC must also accept `ws` and `wss` endpoints for SVM upstreams. Ordinary unary calls must work through the same upstream client.

The design has 4 main promises:

1. Preserve the SVM JSON Remote Procedure Call (JSON-RPC) wire contract.
2. Reuse shared server security, limits, delivery, metrics, and cleanup.
3. Keep subscription ownership explicit from subscribe through cleanup.
4. Leave EVM behavior unchanged.

## 2. Current code findings

The current transport provides most shared infrastructure. The current subscription protocol remains EVM-specific.

| Area | Current finding | Required behavior |
|---|---|---|
| SVM upstream creation | The registry accepts only `http` and `https` for SVM ([`clients/registry.go:154-174`](../../clients/registry.go#L154-L174)). | Add `ws` and `wss` through the existing client. |
| Upstream client reuse | The registry caches 1 client per upstream ([`clients/registry.go:57-65`](../../clients/registry.go#L57-L65)). | Use that client for unary calls and subscriptions. |
| WebSocket proxy pools | The EVM WebSocket path rejects proxy pools ([`clients/registry.go:113-131`](../../clients/registry.go#L113-L131)). | Preserve the same rejection for SVM. |
| Client connection binding | Each client connection binds to 1 architecture and network ([`erpc/ws_server.go:122-137`](../../erpc/ws_server.go#L122-L137)). | Reuse this binding for SVM network resolution. |
| Shared server controls | `server.webSocket` already defines every shared control ([`common/config.go:187-210`](../../common/config.go#L187-L210)). | Reuse the configuration without SVM copies. |
| Request dispatch | Only `eth_subscribe` and `eth_unsubscribe` enter the subscription manager ([`erpc/ws_server.go:378-418`](../../erpc/ws_server.go#L378-L418)). | Add architecture-aware SVM dispatch. |
| EVM method classification | The shared classifiers recognize only 2 EVM methods ([`erpc/subscription_manager.go:19-56`](../../erpc/subscription_manager.go#L19-L56)). | Classify known and generic SVM pairs separately. |
| Batch handling | Batch rejection depends on the EVM-only classifier ([`erpc/ws_server.go:430-483`](../../erpc/ws_server.go#L430-L483)). | Reject every SVM subscription item before execution. |
| HTTP handling | HTTP rejection also depends on the EVM-only classifier ([`erpc/http_server.go:697-706`](../../erpc/http_server.go#L697-L706)). | Reject SVM subscriptions before upstream contact. |
| Upstream subscription ownership | The upstream client recognizes only EVM subscription methods ([`clients/ws_json_rpc_client.go:289-304`](../../clients/ws_json_rpc_client.go#L289-L304)). | Give SVM requests explicit connection-scoped ownership. |
| Upstream subscription IDs | The client stores string IDs and parses string notification IDs ([`clients/ws_json_rpc_client.go:114-164`](../../clients/ws_json_rpc_client.go#L114-L164)). | Support the full unsigned 64-bit integer (`u64`) range. |
| Notification routing | The upstream client accepts only `eth_subscription` ([`clients/ws_json_rpc_client.go:669-696`](../../clients/ws_json_rpc_client.go#L669-L696)). | Route SVM notifications through the registered upstream ID. |
| Orphan cleanup | The client sends `eth_unsubscribe` for orphan subscriptions ([`clients/ws_json_rpc_client.go:738-756`](../../clients/ws_json_rpc_client.go#L738-L756)). | Use SVM method and ID metadata instead. |
| Pending request correlation | Numeric response IDs currently pass through `float64` normalization ([`clients/ws_json_rpc_client.go:786-801`](../../clients/ws_json_rpc_client.go#L786-L801)). | Remove floating-point conversion from ID correlation. |
| Client subscription IDs | EVM IDs contain random hexadecimal strings ([`indexer/adapters/wsclient/params.go:60-67`](../../indexer/adapters/wsclient/params.go#L60-L67)). | Generate connection-scoped numeric SVM IDs. |
| Response ID restoration | Normal forwarding restores the exact original ID bytes ([`erpc/networks.go:4801-4825`](../../erpc/networks.go#L4801-L4825)). | Use equivalent restoration for local subscription responses. |
| Early error responses | Unsupported-method responses lack the connection architecture hint ([`erpc/ws_server.go:486-500`](../../erpc/ws_server.go#L486-L500)). | Use the bound architecture for exact SVM IDs. |
| Local subscription responses | The EVM helper copies the typed internal request ID ([`erpc/subscription_manager.go:353-358`](../../erpc/subscription_manager.go#L353-L358)). | Do not use that helper for SVM responses. |
| SVM capacity mapping | Local admission remapping excludes subscription-limit errors ([`erpc/http_server.go:2061-2075`](../../erpc/http_server.go#L2061-L2075)). | Map local SVM subscription limits to `-32000`. |
| Negative request IDs | SVM already defines Agave-compatible parse errors ([`architecture/svm/hooks.go:161-205`](../../architecture/svm/hooks.go#L161-L205)). | Apply this rule before subscription mutation. |
| Commitment injection | Unary injection uses a method table that excludes subscriptions ([`architecture/svm/hooks.go:54-92`](../../architecture/svm/hooks.go#L54-L92)). | Add separate known-subscription metadata. |
| Client notification envelopes | The server hard-codes `eth_subscription` and string IDs ([`erpc/ws_server.go:695-742`](../../erpc/ws_server.go#L695-L742)). | Preserve the SVM method and raw result payload. |
| Acknowledgment gating | The server opens delivery only after writing the subscribe response ([`erpc/ws_server.go:297-321`](../../erpc/ws_server.go#L297-L321)). | Preserve this guarantee for SVM. |
| Writer | 1 goroutine serializes responses and notifications ([`erpc/ws_writer.go:88-107`](../../erpc/ws_writer.go#L88-L107)). | Reuse the bounded writer for SVM. |
| Writer backpressure | Current producers can wait 10 seconds for queue capacity ([`erpc/ws_writer.go:155-191`](../../erpc/ws_writer.go#L155-L191)). | Add a nonblocking SVM upstream-callback path. |
| EVM feed setup | Chain trackers, sources, taps, and mempool exist only for EVM ([`erpc/networks_registry.go:221-233`](../../erpc/networks_registry.go#L221-L233)). | Keep SVM direct proxying outside these components. |
| Unary SVM bootstrap | Genesis validation already uses ordinary upstream forwarding ([`upstream/svm_upstream_ops.go:65-85`](../../upstream/svm_upstream_ops.go#L65-L85)). | Let it use the WebSocket client after registry enablement. |
| Metrics | Shared WebSocket metrics already have fixed names and labels ([`telemetry/metrics.go:132-182`](../../telemetry/metrics.go#L132-L182)). | Reuse them without architecture labels. |

## 3. Goals

- Accept all 9 documented Solana subscription pairs.
- Accept vendor and future `*Subscribe` and `*Unsubscribe` extensions.
- Preserve native unsupported-method responses for every method.
- Send exactly 1 upstream subscribe request per accepted client subscription.
- Fan out duplicate upstream IDs with independent client lifecycles.
- Select the upstream once through the existing policy-aware path.
- Pin that upstream for the complete subscription lifetime.
- Preserve exact request IDs and full `u64` subscription IDs.
- Preserve notification methods, payload shapes, unknown fields, and large integers.
- Apply existing authentication, directives, filters, limits, and rate limits.
- Reuse existing metrics and cost frames with SVM method values.
- Close affected clients explicitly after upstream failure or downstream overload.
- Preserve all EVM wire behavior and tests.
- Update operator documentation and TypeScript configuration tests.

## 4. Non-goals

The initial version does not include these capabilities:

- No production implementation in this document.
- No new dependency.
- No SVM copy of `server.webSocket` configuration.
- No generated Go or TypeScript configuration schema change.
- No eRPC parameter-based subscribe coalescing before upstream contact.
- No cache, request multiplexing, or consensus for subscription calls.
- No retry, hedging, or automatic upstream failover for subscription calls.
- No automatic resubscription after an upstream disconnect.
- No replay, resume, or missed-notification recovery contract.
- No EVM chain-tracker, reorg, mempool, or quorum promises for SVM.
- No SVM finality-tracker updates from notifications.
- No new confirmed-tip tracker.
- No local validation of open-ended filters, encodings, or notification payloads.
- No capability negotiation beyond the native response from each upstream.
- No migration of a live client connection between eRPC instances.

## 5. Configuration

The existing configuration needs no new fields.

```yaml
server:
  webSocket:
    enabled: true
    maxSubscriptionsPerConnection: 100
    maxSubscriptions: 10000

projects:
  - id: main
    upstreams:
      - id: solana-wss
        type: svm
        endpoint: wss://rpc.example.com
        svm:
          cluster: mainnet-beta
    networks:
      - architecture: svm
        svm:
          cluster: mainnet-beta
          commitment: finalized
```

Configuration rules:

- `server.webSocket.enabled` controls client WebSocket upgrades.
- This flag does not control WebSocket upstream clients.
- A WebSocket upstream must work when the client server is disabled.
- Unary SVM calls and subscriptions share 1 client.
- Existing `jsonRpc.headers` apply to the upstream WebSocket handshake.
- Existing `jsonRpc.proxyPool` rejection applies to SVM WebSocket endpoints.
- `svm.commitment` applies only to known methods that accept commitment.
- An omitted network commitment leaves the upstream default unchanged.
- Each upstream still has 1 `endpoint` field.
- Providers with separate HTTP and WebSocket URLs need separate upstream entries.

## 6. Architecture and lifecycle

### 6.1 Component boundaries

| Component | Responsibility |
|---|---|
| Client WebSocket server | Upgrades, authentication, directives, dispatch, limits, responses, and cleanup |
| SVM method metadata | Known pairs, commitment support, terminal events, and bounded metric kinds |
| SVM subscription owner | Downstream IDs, upstream selection, route fanout, reference counts, lifecycle, and accounting |
| Upstream WebSocket client | Request correlation, numeric IDs, notification routing, epochs, and reconnects |
| Downstream writer | Ordered socket writes, bounded queues, terminal-frame delivery, and close frames |
| EVM subscription manager | Existing EVM feeds and behavior without SVM cases |

The SVM path must not enter EVM chain-tracker or mempool taps. The SVM path must not instantiate the EVM `wsupstream` reconciler.

### 6.2 Subscription model

The direct proxy model has these invariants:

- 1 client subscription sends 1 upstream subscribe request.
- Identical client parameters still send separate subscribe requests.
- The upstream selection policy runs once during subscribe.
- Request directives can constrain that selection.
- The selected upstream remains pinned until cleanup.
- An upstream can return an ID that another local route already uses.
- Every local route for that upstream ID receives each upstream notification.
- Each local route keeps an independent downstream ID and lifecycle.
- Only the final local route removal sends an upstream unsubscribe.
- No other upstream receives any subscription lifecycle call.

### 6.3 Subscribe lifecycle

1. Authenticate the upgrade request before the WebSocket handshake.
2. Bind the downstream connection to its URL network.
3. Authenticate and authorize every subscribe message again.
4. Reject invalid transport, batch, request ID, filter, and limit cases locally.
5. Classify the method through known metadata or the generic SVM fallthrough.
6. Apply commitment only through known-method metadata.
7. Reserve shared subscription capacity before upstream mutation.
8. Select 1 eligible upstream and pin it.
9. Allocate a unique downstream `u64` subscription ID.
10. Send exactly 1 subscribe request to that upstream.
11. Parse the returned upstream subscription ID without precision loss.
12. Attach the local route to the upstream ID's handler set before acknowledgment release.
13. Preserve existing handler sets when the upstream reuses an ID.
14. Write the acknowledgment with the downstream ID and exact request ID.
15. Open notification delivery only after the client receives that acknowledgment.

A failed subscribe releases every reserved resource. A failed subscribe leaves the downstream connection open unless the transport itself failed.

### 6.4 Notification lifecycle

1. The upstream read loop receives a notification.
2. The client resolves the local route set through the upstream connection epoch and numeric ID.
3. The callback constructs 1 downstream frame per local route without changing the result payload.
4. The callback offers each frame to its bounded downstream writer without blocking.
5. Each writer sends frames in queue order.
6. Each writer records downstream metrics after a successful socket write.
7. Cleanup removes 1 local route and releases its accounting exactly once.

The callback must not perform downstream network input or output. The callback must not wait for downstream capacity.

### 6.5 Unsubscribe lifecycle

1. Parse the downstream numeric ID without floating-point conversion.
2. Resolve the subscription only within the current downstream connection.
3. Apply authentication and method filters to the actual unsubscribe method.
4. Serialize lifecycle changes with other subscriptions on the same upstream client.
5. Detach a non-final local route without an upstream unsubscribe.
6. Return `result:true` for that local removal.
7. Send the actual method only when the final local route leaves the upstream ID.
8. Remove local routing and release capacity exactly once.
9. Never return `result:false`.

An unknown local ID returns the error defined in section 11. It causes no upstream contact. A non-final local unsubscribe also causes no upstream contact. Other local routes remain active.

### 6.6 Connection cleanup

Client disconnect, server shutdown, upstream disconnect, and terminal completion share 1 accounting path.

Cleanup must meet these conditions:

- Remove every affected local route.
- Release the upstream ID after its final local route leaves.
- Send upstream unsubscribe calls only for a final route on a usable epoch.
- Serialize subscribe, final unsubscribe, and route attachment on each upstream client.
- Preserve already queued terminal signature frames.
- Drain queued downstream data before the close frame when possible.
- Decrement global and per-connection counts exactly once per local route.
- Make concurrent unsubscribe and cleanup idempotent.
- Prevent new subscriptions after connection cleanup starts.

Existing server shutdown keeps close code `1001`. Upstream failure and overload use close code `1013`.

## 7. Official Solana method contract

The official index currently documents 9 pairs. Every unsubscribe method takes 1 numeric subscription ID.

| Family | Subscribe / unsubscribe | Subscribe parameters | Notification and `params.result` | Commitment | Availability |
|---|---|---|---|---|---|
| Account | [`accountSubscribe`](https://solana.com/docs/rpc/websocket/accountsubscribe) / [`accountUnsubscribe`](https://solana.com/docs/rpc/websocket/accountunsubscribe) | Account pubkey; optional config | `accountNotification`; `{context:{slot}, value:account}` | `processed` or `finalized` (`confirmed` is an alias for `finalized` and is deprecated) | No unstable notice |
| Program | [`programSubscribe`](https://solana.com/docs/rpc/websocket/programsubscribe) / [`programUnsubscribe`](https://solana.com/docs/rpc/websocket/programunsubscribe) | Program pubkey; optional config | `programNotification`; `{context:{slot}, value:{pubkey,account}}` | `processed` or `finalized` (`confirmed` alias) | No unstable notice |
| Logs | [`logsSubscribe`](https://solana.com/docs/rpc/websocket/logssubscribe) / [`logsUnsubscribe`](https://solana.com/docs/rpc/websocket/logsunsubscribe) | Required filter; optional config | `logsNotification`; `{context:{slot}, value:{signature,err,logs}}` | `processed` or `finalized` (`confirmed` alias) | No unstable notice |
| Signature | [`signatureSubscribe`](https://solana.com/docs/rpc/websocket/signaturesubscribe) / [`signatureUnsubscribe`](https://solana.com/docs/rpc/websocket/signatureunsubscribe) | Required signature; optional config | `signatureNotification`; `{context:{slot}, value}` | `processed` or `finalized` (`confirmed` alias) | Ends after terminal confirmation |
| Block | [`blockSubscribe`](https://solana.com/docs/rpc/websocket/blocksubscribe) / [`blockUnsubscribe`](https://solana.com/docs/rpc/websocket/blockunsubscribe) | Required filter; optional config | `blockNotification`; `{context:{slot}, value:{slot,block,err}}` | `finalized` only (`confirmed` alias) | Unstable and validator-gated |
| Slot | [`slotSubscribe`](https://solana.com/docs/rpc/websocket/slotsubscribe) / [`slotUnsubscribe`](https://solana.com/docs/rpc/websocket/slotunsubscribe) | None | `slotNotification`; `{slot,parent,root}` | Not accepted | No unstable notice |
| Slot updates | [`slotsUpdatesSubscribe`](https://solana.com/docs/rpc/websocket/slotsupdatessubscribe) / [`slotsUpdatesUnsubscribe`](https://solana.com/docs/rpc/websocket/slotsupdatesunsubscribe) | None | `slotsUpdatesNotification`; tagged object with `type` | Not accepted | Unstable; support can vary |
| Root | [`rootSubscribe`](https://solana.com/docs/rpc/websocket/rootsubscribe) / [`rootUnsubscribe`](https://solana.com/docs/rpc/websocket/rootunsubscribe) | None | `rootNotification`; bare `u64` root slot | Not accepted | No unstable notice |
| Vote | [`voteSubscribe`](https://solana.com/docs/rpc/websocket/votesubscribe) / [`voteUnsubscribe`](https://solana.com/docs/rpc/websocket/voteunsubscribe) | None | `voteNotification`; direct vote object | Not accepted | Validator and version dependent |

### 7.1 Method option details

These details define pass-through fixtures. They do not authorize local enum validation.

- Account config accepts `commitment`, `encoding`, and `dataSlice`.
- Program config accepts `commitment`, `filters`, `encoding`, `dataSlice`, and `withContext`.
- Account and program encodings include `base58`, `base64`, `base64+zstd`, `binary`, and `jsonParsed`.
- The documented account-data default remains `binary`.
- Program filters include `dataSize`, `memcmp`, and `tokenAccountState`.
- The node can accept at most 4 program filters.
- Log filters are `"all"`, `"allWithVotes"`, or `{"mentions":[pubkey]}`.
- The `mentions` filter accepts exactly 1 address.
- Signature config accepts `commitment` and `enableReceivedNotification`.
- `enableReceivedNotification` defaults to `false`.
- Block filters are `"all"` or `{"mentionsAccountOrProgram":pubkey}`.
- Block config accepts `commitment`, `encoding`, `transactionDetails`, `showRewards`, and `maxSupportedTransactionVersion`.
- Block encoding defaults to `base64`.
- Block transaction details default to `full`.
- `showRewards` defaults to `false`.
- Slot updates currently define 7 values for `type`.
- Those values are `firstShredReceived`, `completed`, `createdBank`, `frozen`, `dead`, `optimisticConfirmation`, and `root`.

Account `minContextSlot` and program `minContextSlot` and `sortResults` remain pass-through fields. Current PubSub ignores their behavior. Program notifications include context regardless of `withContext`.

Omitting `maxSupportedTransactionVersion` limits block results to legacy transactions. A version mismatch remains an error inside the notification value.

### 7.2 Validator-gated methods

`blockSubscribe` requires `--rpc-pubsub-enable-block-subscription`. That flag also requires `--enable-rpc-transaction-history`.

Agave v4.4.1 gates `voteSubscribe` behind `--rpc-pubsub-enable-vote-subscription`. Other validator versions and vendors can differ.

`slotsUpdatesSubscribe` is unstable. Its payload format and support can change.

Each method fails independently. A rejection from 1 method must not disable another family. eRPC must preserve the native unsupported-method response.

## 8. Generic SVM extension behavior

Known metadata must not become a closed method registry. SVM vendors can add subscription methods.

The generic path follows these rules:

- A case-sensitive terminal `Subscribe` suffix identifies a generic subscribe method.
- A case-sensitive terminal `Unsubscribe` suffix identifies a generic unsubscribe method.
- The expected pair replaces the terminal `Subscribe` with `Unsubscribe`.
- The local downstream ID identifies the subscription during later cancellation.
- eRPC forwards the client's actual unsubscribe method to the pinned upstream.
- eRPC does not require the actual unsubscribe stem to match the subscribe stem.
- Project and upstream filters still check the actual method.
- Generic subscribe parameters pass through verbatim.
- Generic methods receive no commitment injection.
- Generic methods receive no local terminal-event detection.
- Generic successful responses still require a numeric upstream `u64` subscription ID.
- Notification routing uses the upstream ID rather than the notification method name.
- Unknown notification method names pass through unchanged.
- Unknown fields and payload shapes pass through unchanged.
- Native method-not-found and invalid-parameter responses pass through unchanged.
- Unsupported generic subscriptions never enter the ordinary unary forwarding path.

Ordinary unknown unary methods keep existing forwarding behavior. The generic rule intercepts only the 2 subscription suffixes.

Known-method metadata adds only bounded behavior. It supplies commitment rules, terminal semantics, cost values, and metric kinds. It must not reject unknown parameters or notification variants.

## 9. Exact identifier semantics

### 9.1 Request IDs

A request identifier (ID) is distinct from a subscription ID. eRPC must preserve the request ID's exact JSON representation in every local subscription response.

| Request ID case | Required result | Upstream contact |
|---|---|---|
| String | Echo the exact JSON string | Normal subscribe or unsubscribe |
| `null` | Echo JSON `null` | Normal subscribe or unsubscribe |
| Integer within `u64` | Echo the exact integer bytes | Normal subscribe or unsubscribe |
| Integer above `2^53` | Preserve every digit | Normal subscribe or unsubscribe |
| Negative integer | Return `-32700` with message `Parse error` and `id:null` | None |
| Missing ID on subscribe | Produce no response | None |
| Missing ID on unsubscribe | Produce no response and no mutation | None |

Existing SVM rules apply to other invalid ID forms. eRPC must not replace a valid client ID with an internal correlation ID.

A negative ID inside an SVM batch keeps the existing whole-batch parse error. No batch element executes in that case.

### 9.2 Downstream subscription IDs

Each downstream connection owns an independent ID namespace.

- Every allocated ID is a JSON number in the `u64` range.
- ID `0` is valid.
- Every ID is unique within that connection's complete lifetime.
- Unsubscribe does not make an ID eligible for reuse.
- Terminal completion does not make an ID eligible for reuse.
- Different connections can use the same downstream ID.
- ID exhaustion fails the subscribe before upstream mutation.
- The subscribe acknowledgment returns the downstream ID, never the upstream ID.

The implementation can choose the allocation strategy. The strategy must guarantee uniqueness without reuse.

### 9.3 Upstream subscription IDs

The upstream ID remains separate from the downstream ID.

- Parse the upstream ID from raw JSON integer bytes.
- Accept the full range from `0` through `18446744073709551615`.
- Accept values above `2^53` without rounding.
- Never store or recover an SVM ID through `float64`.
- Apply the same rule to responses, notifications, unsubscribe parameters, and pending correlation.
- Key upstream route sets by upstream connection epoch and upstream ID.
- Replace the upstream ID with each downstream ID only at the downstream boundary.
- Allow multiple local routes for the same upstream ID.
- Never overwrite or remove another local route during attachment.
- Fan out each upstream notification to the complete local route set.
- Reference-count local routes for upstream cancellation and active gauges.

Agave deduplicates equivalent normalized parameters. It can return the same ID for separate subscribe requests. Agave also keeps only 1 token for that ID on each connection. eRPC must not treat this documented behavior as a collision.

eRPC must not acknowledge a subscription with an invalid or missing upstream ID. It must clean up any usable upstream registration before returning a local protocol error. That cleanup must preserve other local routes for the same ID.

### 9.4 Registration and acknowledgment ordering

Route registration must happen before acknowledgment release. Delivery gating must remain closed until the acknowledgment write succeeds.

An immediate upstream notification can arrive before the acknowledgment. eRPC must retain or gate that notification behind the acknowledgment. It must never emit that notification before the acknowledgment.

## 10. Notification wire contract

Every downstream notification keeps the upstream notification's method and `params.result` payload.

eRPC replaces only `params.subscription` with the downstream numeric ID.

eRPC must preserve:

- The `jsonrpc` member.
- The notification `method` member.
- The complete `params.result` JSON value.
- Unknown top-level members.
- Unknown members inside `params`.
- Unknown fields inside the result.
- Integer precision throughout the payload.
- The upstream order for each subscription.

eRPC must not add a request ID to a notification. eRPC must not add `context.apiVersion`.

eRPC must not force a `context.value` wrapper around these direct result shapes:

- `rootNotification` bare numbers.
- `slotNotification` objects.
- `slotsUpdatesNotification` tagged objects.
- `voteNotification` objects.

Account, program, logs, signature, and block results retain their existing context envelopes. A block error inside `params.result.value.err` remains a successful notification. It is not a top-level JSON-RPC error.

Raw JSON fields preserve integer precision. eRPC must not decode open-ended payloads through floating-point numbers. Envelope re-encoding can change whitespace and member order without changing the JSON values.

## 11. Unsubscribe and error semantics

### 11.1 Successful and unknown cancellation

A successful unsubscribe returns `result:true`. eRPC never returns `result:false`.

An upstream `false` result becomes the unknown-subscription error. It must not reach the client in that form.

The local ID determines subscription ownership. A non-final local unsubscribe returns `true` after local removal. Only the final local unsubscribe forwards the client's actual method to the pinned upstream. Per-client lifecycle serialization prevents a concurrent attach from hiding behind that final unsubscribe.

An unknown, removed, completed, or different-connection local ID returns:

```json
{
  "jsonrpc": "2.0",
  "error": {
    "code": -32602,
    "message": "Invalid subscription id."
  },
  "id": 1
}
```

The response must preserve the actual request ID. The example uses `1` only for illustration.

Missing, malformed, negative, fractional, or out-of-range subscription IDs also return `-32602`. They use message `Invalid subscription id.`. They cause no upstream contact.

### 11.2 Error matrix

| Case | Required behavior | Upstream mutation |
|---|---|---|
| Negative SVM request ID | `-32700`, `Parse error`, `id:null` | None |
| ID-less SVM subscribe | No response | None |
| ID-less SVM unsubscribe | No response | None |
| SVM subscription item in a batch | `-32601` for that item | None for every subscription item |
| SVM subscription over HTTP | `-32601`; WebSocket required | None |
| Unknown local unsubscribe ID | `-32602`, `Invalid subscription id.` | None |
| Invalid local unsubscribe parameter | `-32602`, `Invalid subscription id.` | None |
| Project method filter rejection | Existing `-32601` behavior with exact request ID | None |
| Upstream method filter rejection | Existing method-eligibility error | None |
| Unsupported known or generic upstream method | Preserve native error code, message, and data | Only the selected subscribe attempt |
| Invalid upstream subscription parameters | Preserve native error code, message, and data | Only the selected subscribe attempt |
| Upstream subscribe transport failure | Return the transport failure without another upstream | At most 1 ambiguous attempt |
| Upstream unsubscribe transport failure | Return the failure without another upstream | At most 1 ambiguous attempt |
| Local SVM subscription capacity error | Use `-32000`, not `-32005` | None |

Solana assigns `-32005` to `NodeUnhealthy`. Local SVM admission errors must not use that code. Native upstream `-32005` responses remain unchanged.

### 11.3 Batch preflight

The server must identify every SVM subscription item before executing any batch item. This preflight prevents concurrent ordinary items from hiding an upstream subscription mutation.

Subscription items receive local unsupported-method responses. Ordinary items retain existing SVM batch behavior. ID-less items produce no response entries.

Existing SVM whole-batch parse errors still take precedence. Existing response ordering remains unchanged.

## 12. Commitment and parameter rules

Only 5 known methods accept commitment in their documented subscription config:

- `accountSubscribe`.
- `programSubscribe`.
- `logsSubscribe`.
- `signatureSubscribe`.
- `blockSubscribe`.

For these methods, the config object occupies parameter index `1`.

The rules are:

1. Preserve every explicit client commitment verbatim.
2. Apply `svm.commitment` only when the client omitted commitment.
3. Do not inject commitment when the network setting is empty.
4. Do not fabricate a missing required pubkey, signature, or filter parameter.
5. Append or update only the documented config object when injection is legal.
6. Preserve every other config field.
7. Do not inject into slot, slot updates, root, or vote subscriptions.
8. Do not inject into generic extension methods.

Since Alpenglow, Solana has only two commitment levels: `processed` and `finalized`. `confirmed` is an alias for `finalized` and is deprecated. For `blockSubscribe`, an injected `processed` default becomes `finalized`. No other injected value changes.

An explicit `processed` value for `blockSubscribe` remains `processed`. The upstream must return its native invalid-parameter response.

When commitment remains absent, the upstream default applies. Solana documents `finalized` for methods that accept commitment.

eRPC must not locally reject new commitment values, encodings, filters, or option fields. The upstream decides their validity.

## 13. Selection and failsafe boundaries

Subscription selection uses the existing policy-aware upstream ordering. It also uses existing architecture and method eligibility checks.

The selection must respect:

- Project method filters.
- Upstream `allowMethods` and `ignoreMethods`.
- `use-upstream` directives.
- Network and upstream eligibility.
- The known or expected unsubscribe method during selection.
- The actual unsubscribe method during cancellation.

A candidate that cannot serve both expected lifecycle methods cannot open the subscription. An actual extension unsubscribe still requires its own filter checks.

Subscribe and unsubscribe calls bypass:

- Cache reads and writes.
- Request multiplexing.
- Consensus.
- Network and upstream retries.
- Hedging.
- Automatic upstream failover.
- eRPC parameter-based subscribe coalescing.

The existing per-attempt timeout still applies. An ambiguous subscribe failure must not trigger another subscribe attempt.

Ordinary unary SVM calls keep every existing cache, failsafe, consensus, and forwarding behavior.

## 14. Disconnect, reconnect, and backpressure

### 14.1 Upstream disconnect

An upstream disconnect invalidates every subscription on that upstream epoch. eRPC must close every affected downstream connection with WebSocket code `1013`.

The close reason must identify the upstream subscription loss. Use this clear reason: `upstream subscription disconnected; reconnect and resubscribe`.

A downstream connection with several subscriptions closes completely when 1 pinned upstream fails. Cleanup then ends every subscription on that downstream connection.

eRPC must not silently resubscribe. Solana provides no replay or resume contract. The client must reconnect and create new subscriptions.

The upstream client can reconnect for later unary calls. That reconnect must not revive old subscription routes.

### 14.2 Downstream backpressure

The current writer holds at most 256 queued messages. It also uses a 64 KiB weighted queue budget ([`erpc/ws_writer.go:23-36`](../../erpc/ws_writer.go#L23-L36)). These bounds do not cap an individual outbound frame.

SVM notification callbacks use a nonblocking offer operation:

- The operation never waits for queue space.
- The operation never writes to the downstream socket.
- A full message queue closes the affected client with `1013`.
- An exhausted weighted budget closes the affected client with `1013`.
- The close reason identifies subscription backpressure.
- eRPC never silently drops an individual notification.
- The close tells the client that notification loss is possible.

Other clients and upstream read loops must continue. A slow client must not stall unrelated traffic.

### 14.3 Terminal signature completion

A `signatureNotification` is terminal when its `result.value` contains the terminal confirmation object. That object contains `err`.

The string value `"receivedSignature"` is not terminal. The subscription remains active after that early notification.

For terminal completion:

1. Mark the upstream route terminal so later events cannot produce new frames.
2. Enqueue 1 terminal frame for every local route before local removal.
3. Let each writer deliver its terminal frame.
4. Remove each local route after its terminal write completes.
5. Release each local route's capacity exactly once.
6. Remove the upstream route after its final local route leaves.
7. Do not require an upstream unsubscribe after natural completion.

A terminal frame already in the writer queue must survive cleanup. A failed terminal write enters normal connection cleanup. Lifecycle serialization must not attach a new local route to a terminal upstream route.

## 15. Security and limits

### 15.1 Authentication and authorization

- Validate credentials before the upgrade.
- Authenticate every message with the upgrade headers, query, and client address.
- Apply method-scoped authorization to subscribe and unsubscribe.
- Apply Cross-Origin Resource Sharing (CORS) origin rules during upgrade.
- Apply existing per-message project and network rate limits.
- Acquire rate-limit permits once for subscribe and once for unsubscribe.
- Do not acquire request permits for notifications.
- Apply upgrade headers and query directives through existing rules.

### 15.2 Shared accounting

Global and per-connection subscription counts include EVM and SVM subscriptions together. Separate SVM counters must not bypass existing limits.

Reserve capacity before upstream mutation. Release it after every failure or cleanup path.

### 15.3 Existing defaults

| Setting | Default |
|---|---:|
| `server.webSocket.enabled` | `false` |
| `readBufferSize` | 4096 bytes |
| `writeBufferSize` | 4096 bytes |
| `maxMessageSize` | 1 MiB |
| `pingInterval` | 30 seconds |
| `maxSubscriptionsPerConnection` | 100 |
| `maxConcurrentRequestsPerConnection` | 250 |
| `maxConnections` | 0, unlimited |
| `maxConnectionsPerIP` | 0, unlimited |
| `maxSubscriptions` | 0, unlimited |

These defaults come from [`common/defaults.go:929-954`](../../common/defaults.go#L929-L954). Existing validation remains unchanged.

Instance and per-address connection caps reject upgrades with HTTP `429`. Shutdown rejects new upgrades with HTTP `503`.

## 16. Observability

### 16.1 Existing metrics

The implementation must reuse these names and exact label sets:

| Metric | Labels | SVM usage |
|---|---|---|
| `erpc_websocket_connections_active` | `project` | Existing client connection count |
| `erpc_websocket_notifications_sent_total` | `project`, `network`, `kind` | Successful downstream notification writes |
| `erpc_websocket_notification_bytes_total` | `project`, `network`, `kind` | Downstream notification JSON bytes |
| `erpc_upstream_websocket_connected` | `project`, `vendor`, `network`, `upstream` | Existing upstream connectivity |
| `erpc_upstream_subscriptions_active` | `project`, `network`, `upstream`, `kind` | Unique active upstream IDs |
| `erpc_upstream_subscription_events_total` | `project`, `network`, `upstream`, `kind` | Received upstream frames before local fanout |
| `erpc_upstream_subscription_event_bytes_total` | `project`, `network`, `upstream`, `kind` | Received upstream frame bytes before local fanout |
| `erpc_websocket_rejected_total` | `project`, `reason` | Existing capacity and shutdown rejection reasons |

Known SVM `kind` values are `account`, `program`, `logs`, `signature`, `block`, `slot`, `slotsUpdates`, `root`, and `vote`.

Generic extension subscriptions use `kind="custom"`. This rule bounds metric cardinality.

Request metrics use the actual subscribe or unsubscribe method. Existing finality labels do not represent subscription commitment.

Do not add an `architecture` label. The existing `network` label already distinguishes SVM networks.

### 16.2 Cost frames

SVM notification cost frames retain the existing fields:

- `X-ERPC-Calls: 1`.
- `X-ERPC-Billable: 1`.
- Existing network ID and alias fields.

`X-ERPC-Methods` carries the actual notification method. Example: `accountNotification`.

`X-ERPC-Subscription` carries the known family name. Generic extensions carry the actual subscribe stem.

Cost caching must use method metadata rather than the EVM event-kind array. Generic frames must not create an unbounded method cache.

### 16.3 Excluded EVM signals

SVM direct proxying must not emit EVM chain-tracker, reorg, mempool-deduplication, or head-stall promises.

In particular, do not emit `erpc_upstream_websocket_heads_stall_reconnects_total` for SVM subscriptions. That metric describes EVM `newHeads` behavior.

Notifications must not update SVM finality trackers in the initial version.

## 17. Documentation and configuration scope

Implementation must update these operator pages:

- `docs/pages/operation/websocket.mdx`.
- `docs/pages/operation/url.mdx`.
- `docs/pages/operation/directives.mdx`.
- `docs/pages/operation/production.mdx`.
- `docs/pages/config/projects/upstreams.mdx`.
- `docs/pages/config/server.mdx`.
- `docs/pages/reference/metrics.mdx`.

The documentation must separate shared transport behavior from EVM feed promises. Current EVM reorg, quorum, deduplication, and reconnect promises must not describe SVM.

The SVM sections must document:

- The 9 official pairs and generic extensions.
- Numeric subscription IDs.
- Notification shapes.
- Commitment injection.
- Terminal signature completion.
- Validator-gated methods.
- Direct upstream pinning and upstream-driven duplicate-ID fanout.
- Close code `1013` and client-driven resubscription.
- Backpressure behavior.
- SVM cost-frame and metric values.

Update `typescript/config/src/__tests__/svm.test-d.ts` with an SVM `wss` upstream. Do not add generated schema changes for this feature.

Do not hand-edit generated `.llms.txt` files.

## 18. Test strategy

### 18.1 Unit tests

Method metadata tests must cover:

- Every known pair and notification name.
- Generic suffix classification and pair derivation.
- Unknown unary fallthrough.
- Commitment support for exactly 5 known methods.
- Block default clamping and explicit-value preservation.
- Terminal signature detection and `receivedSignature` exclusion.
- Bounded known and generic metric kinds.

Identifier tests must cover:

- Downstream ID uniqueness and no reuse.
- Upstream ID `0`.
- Upstream ID `18446744073709551615`.
- Values immediately above and below `2^53`.
- String, `null`, and full-`u64` request IDs.
- Negative and absent request IDs.
- Invalid unsubscribe parameter types.
- Duplicate upstream IDs with complete fanout and no route overwrite.
- No floating-point conversion in pending correlation.

Registry tests must cover:

- SVM `ws` and `wss` client creation.
- Ordinary unary calls through those clients.
- Existing HTTP behavior.
- Proxy-pool rejection.
- Independent operation with `server.webSocket.enabled:false`.

### 18.2 Integration tests

Use a controllable mock SVM WebSocket upstream and the full eRPC server.

The matrix must cover:

1. Every documented subscribe, notification, and unsubscribe pair.
2. Generic vendor methods and unknown notification methods.
3. Native unsupported-method responses for unstable or disabled methods.
4. Exactly 1 upstream subscribe request for each client subscription.
5. Duplicate upstream IDs with fanout and reference-counted cancellation.
6. Upstream pinning across selection-policy changes.
7. No cache, multiplexing, consensus, retry, hedge, or failover calls.
8. Immediate notifications behind each subscribe acknowledgment.
9. Exact result payload preservation for every documented shape.
10. Unknown fields and integers above `2^53`.
11. Actual final unsubscribe method forwarding and `result:true`.
12. Unknown and cross-connection unsubscribe IDs.
13. HTTP and batch rejection before upstream mutation.
14. Ordinary SVM unary and batch behavior.
15. Authentication, directives, filters, and rate limits.
16. Global and per-connection limits and exact counter release.
17. Signature terminal delivery to every local route and cleanup.
18. Upstream disconnect close code `1013` and no automatic resubscription.
19. Full writer queues, close code `1013`, and no silent notification drop.
20. Client disconnect, network shutdown, server shutdown, and orphan cleanup.
21. Existing metric names, label sets, and SVM cost frames.

A race test must combine subscribe responses, immediate notifications, unsubscribe, terminal events, and disconnects.

### 18.3 End-to-end scenario

Use 2 clients and 1 mock SVM upstream:

1. Create identical account subscriptions on both clients.
2. Verify 2 upstream subscribe requests and the same upstream ID.
3. Send 1 upstream notification and verify 2 frames with independent downstream IDs.
4. Unsubscribe 1 client and verify no upstream unsubscribe occurs.
5. Verify the other subscription remains active.
6. Unsubscribe the final client and verify exactly 1 upstream unsubscribe.
7. Repeat both subscriptions and drop the upstream.
8. Verify close code `1013` and no automatic subscription recovery.
9. Reconnect and create fresh subscriptions.
10. Close eRPC and verify existing shutdown cleanup and zero active gauges.

Live public endpoint tests can remain optional under the existing `svm_live` convention. They must not become release gates.

### 18.4 Regression and verification commands

Run focused tests during implementation:

```bash
go test ./architecture/svm ./clients ./erpc
go test -race ./clients ./erpc
pnpm --filter @erpc-cloud/config test
```

Run repository checks before completion:

```bash
make fmt
make test-fast
make test
pnpm -r run build
```

Existing EVM WebSocket tests must remain unchanged and green.

## 19. Acceptance criteria

- [ ] **AC1**: SVM `ws` and `wss` upstreams serve unary calls and subscriptions.
- [ ] **AC2**: WebSocket upstreams work with `server.webSocket.enabled:false`.
- [ ] **AC3**: All 9 official pairs complete subscribe, notification, and unsubscribe flows.
- [ ] **AC4**: Generic subscription pairs pass through with native upstream errors.
- [ ] **AC5**: Ordinary unknown unary methods keep existing forwarding behavior.
- [ ] **AC6**: Each client subscription sends exactly 1 subscribe request to its pinned upstream.
- [ ] **AC7**: Duplicate upstream IDs fan out with independent client IDs and reference-counted cleanup.
- [ ] **AC8**: Subscription calls never use cache, multiplexing, consensus, retry, hedge, or failover.
- [ ] **AC9**: Downstream IDs are unique `u64` values and never repeat within a connection.
- [ ] **AC10**: Upstream ID `0` and values above `2^53` route without precision loss.
- [ ] **AC11**: String, `null`, and full-`u64` request IDs retain exact JSON representations.
- [ ] **AC12**: Negative IDs return the existing SVM parse error without upstream contact.
- [ ] **AC13**: ID-less subscription calls produce no response or upstream mutation.
- [ ] **AC14**: HTTP and batch subscription rejection happens before upstream mutation.
- [ ] **AC15**: Every notification follows its subscribe acknowledgment.
- [ ] **AC16**: Notifications preserve method, raw result shape, unknown fields, and large integers.
- [ ] **AC17**: Direct root, slot, slot update, and vote results receive no context wrapper.
- [ ] **AC18**: Successful cancellation returns `true` and never `false`.
- [ ] **AC19**: Unknown local IDs return `-32602` with `Invalid subscription id.`.
- [ ] **AC20**: Only the final local unsubscribe sends its actual method to the pinned upstream.
- [ ] **AC21**: Commitment injection affects only the 5 documented methods.
- [ ] **AC22**: Only an injected `processed` block default changes to `finalized`.
- [ ] **AC23**: Unknown extension parameters receive no local transformation.
- [ ] **AC24**: Terminal signature delivery precedes cleanup and count release.
- [ ] **AC25**: The `receivedSignature` value does not complete a subscription.
- [ ] **AC26**: Upstream loss closes every affected client with code `1013`.
- [ ] **AC27**: No upstream reconnect silently revives an old subscription.
- [ ] **AC28**: Upstream callbacks never block on downstream socket operations.
- [ ] **AC29**: A full downstream queue closes the affected client with code `1013`.
- [ ] **AC30**: No notification disappears without an explicit connection-level signal.
- [ ] **AC31**: Shared subscription accounting releases exactly once on every cleanup path.
- [ ] **AC32**: Authentication, directives, filters, limits, and rate limits cover both lifecycle methods.
- [ ] **AC33**: Existing metrics and cost frames carry method-specific SVM values without architecture labels.
- [ ] **AC34**: SVM notifications do not update finality trackers or emit EVM feed metrics.
- [ ] **AC35**: Existing EVM WebSocket tests and wire behavior remain unchanged.
- [ ] **AC36**: Operator documentation and TypeScript SVM configuration tests cover `wss`.
- [ ] **AC37**: No generated configuration schema changes occur.
- [ ] **AC38**: Focused tests, race tests, repository tests, and TypeScript builds pass.

## 20. Assumptions

- The official 9-method table describes the baseline protocol at this revision date.
- Validators and vendors can remove or extend methods without notice.
- Native upstream responses remain the authority for method availability.
- Generic extension names follow Solana's terminal suffix convention.
- Generic unsubscribe compatibility follows Agave and does not require matching stems.
- Each upstream configuration continues to expose exactly 1 endpoint URL.
- An upstream can reuse an active ID for equivalent normalized subscriptions.
- All local routes for that ID represent the same upstream notification stream.
- Per-client lifecycle serialization keeps route counts accurate during concurrent calls.
- A selected WebSocket upstream supports unary JSON-RPC and subscriptions on that endpoint.
- Clients own reconnection, resubscription, and any required state reconciliation.
- The protocol provides no missed-event replay after connection loss.
- Delivery order matters only within each upstream subscription.
- Independent subscriptions and requests can complete in different orders.
- A live connection provides no exactly-once guarantee after an explicit overload or disconnect close.
- Public endpoint tests are optional because provider capabilities vary.

## 21. Official references

### Solana protocol

- [Solana WebSocket method index](https://solana.com/docs/rpc/websocket).
- The method table in section 7 links every official subscribe and unsubscribe page.
- [Solana RPC JSON structures](https://solana.com/docs/rpc/json-structures).

### Agave compatibility sources

These links pin Agave v4.4.1 at commit `5bb304027fe1a166b02cb26dc8c161593f416fca`. They define compatibility fixtures, not a universal vendor capability set.

- [`SubscriptionId` uses `u64`](https://github.com/anza-xyz/agave/blob/5bb304027fe1a166b02cb26dc8c161593f416fca/rpc/src/rpc_subscription_tracker.rs#L29-L42).
- [Known subscription parameters and notification names](https://github.com/anza-xyz/agave/blob/5bb304027fe1a166b02cb26dc8c161593f416fca/rpc/src/rpc_subscription_tracker.rs#L44-L101).
- [Equivalent parameters share an upstream ID](https://github.com/anza-xyz/agave/blob/5bb304027fe1a166b02cb26dc8c161593f416fca/rpc/src/rpc_subscription_tracker.rs#L185-L275).
- [A connection replaces duplicate tokens for the same ID](https://github.com/anza-xyz/agave/blob/5bb304027fe1a166b02cb26dc8c161593f416fca/rpc/src/rpc_pubsub.rs#L379-L420).
- [Unknown unsubscribe IDs return `-32602`](https://github.com/anza-xyz/agave/blob/5bb304027fe1a166b02cb26dc8c161593f416fca/rpc/src/rpc_pubsub.rs#L410-L420).
- [Notification envelope serialization](https://github.com/anza-xyz/agave/blob/5bb304027fe1a166b02cb26dc8c161593f416fca/rpc/src/rpc_subscriptions.rs#L276-L322).
- [Terminal signature subscription removal](https://github.com/anza-xyz/agave/blob/5bb304027fe1a166b02cb26dc8c161593f416fca/rpc/src/rpc_pubsub_service.rs#L246-L264).
- [Disabled block subscriptions](https://github.com/anza-xyz/agave/blob/5bb304027fe1a166b02cb26dc8c161593f416fca/rpc/src/rpc_pubsub.rs#L560-L567).
- [Disabled vote subscriptions](https://github.com/anza-xyz/agave/blob/5bb304027fe1a166b02cb26dc8c161593f416fca/rpc/src/rpc_pubsub.rs#L602-L613).

### JSON-RPC method-call behavior

Agave registers subscriptions with method handlers, not notification handlers. The JSON-RPC library ignores ID-less calls to those methods.

- [`jsonrpc-core` method and notification dispatch](https://github.com/paritytech/jsonrpc/blob/v18.0.0/core/src/io.rs#L292-L316).
- [JSON-RPC 2.0 specification](https://www.jsonrpc.org/specification).

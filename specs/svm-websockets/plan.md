# Solana Virtual Machine WebSocket Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add SVM unary WebSocket upstreams and direct Solana subscription proxying without changing EVM wire behavior.

**Architecture:** Reuse the client server, upstream client, subscription accounting, and bounded writer. Add SVM method metadata and a direct subscription owner. Pin each subscription to 1 policy-selected upstream outside cache, multiplexing, consensus, retry, hedging, failover, and EVM feeds. Fan out duplicate upstream IDs and reference-count their local routes.

**Tech Stack:** Go, Gorilla WebSocket, Sonic JSON, existing eRPC selection and failsafe code, Prometheus, TypeScript, Nextra.

**Status:** Proposed design. No production implementation is included yet.

**Specification:** [feature.md](./feature.md) defines the behavioral contract and acceptance criteria.

## Global Constraints

- Add no dependency.
- Add no SVM copy of `server.webSocket` configuration.
- Add no generated Go or TypeScript configuration schema change.
- Keep WebSocket upstreams independent of `server.webSocket.enabled`.
- Send exactly 1 upstream subscribe request for each accepted client subscription.
- Do not coalesce subscribe requests through eRPC parameter matching.
- Fan out duplicate upstream IDs to every local route.
- Serialize lifecycle mutations on each upstream client.
- Send upstream unsubscribe only after the final local route leaves an upstream ID.
- Select 1 upstream once. Pin it for the subscription lifetime.
- Bypass cache, multiplexing, consensus, retry, hedging, and automatic failover for subscription calls.
- Support all 9 documented pairs and generic terminal `Subscribe` and `Unsubscribe` suffixes.
- Preserve native upstream errors and ordinary unknown unary forwarding.
- Preserve exact request-ID JSON bytes, including string, `null`, and full `u64` values.
- Never convert subscription IDs or pending response IDs through `float64`.
- Never reuse a downstream subscription ID within its connection.
- Register notification routing before acknowledgment release. Open delivery after acknowledgment write success.
- Preserve raw notification payloads, unknown members, method names, and large integers.
- Replace only `params.subscription` at the downstream boundary.
- Return `result:true` after successful unsubscribe. Never return `result:false`.
- Return `-32602` with `Invalid subscription id.` for unknown or malformed local IDs.
- Apply commitment only to the 5 documented commitment-bearing methods.
- Close affected clients with code `1013` after upstream loss or downstream overload.
- Do not resubscribe automatically. Solana has no replay or resume contract.
- Keep upstream callbacks free of downstream network input, output, and queue waits.
- Reuse metric names and label sets. Add no architecture label.
- Do not update SVM finality trackers from notifications.
- Preserve every existing EVM WebSocket test and wire response.
- Follow `AGENTS.md` documentation and testing rules.
- Do not hand-edit generated `.llms.txt` files.

---

## 1. Verified integration map

The current code provides these integration points:

| Concern | Current code | Plan boundary |
|---|---|---|
| SVM client creation | `clients/registry.go:154-174` rejects WebSocket schemes | Add `ws` and `wss` beside HTTP |
| Upstream correlation | `clients/ws_json_rpc_client.go:243-400` rewrites request IDs | Keep multiplexing and remove numeric `float64` decoding |
| Upstream subscriptions | `clients/ws_json_rpc_client.go:114-164` stores EVM string IDs | Add codec and method metadata |
| Notification routing | `clients/ws_json_rpc_client.go:618-696` accepts only `eth_subscription` | Route handler sets by raw ID and epoch |
| Upstream selection | `erpc/networks.go:2388-2450` returns policy order | Select once and store 1 upstream |
| Direct upstream call | `upstream/upstream.go:626-1092` applies per-upstream controls | Use `UpstreamPoliciesTimeoutOnly` |
| Shared accounting | `erpc/subscription_manager.go:28-39` owns global counts | Reuse the same registry and counters |
| Connection registry | `indexer/adapters/wsclient/adapter.go:18-133` owns local subscriptions | Add numeric ID and SVM metadata |
| Client dispatch | `erpc/ws_server.go:324-483` recognizes only EVM pairs | Add SVM classification and preflight |
| HTTP rejection | `erpc/http_server.go:697-706` recognizes only EVM pairs | Reject SVM pairs before forwarding |
| Response identity | `erpc/networks.go:4801-4825` restores raw request IDs | Apply equivalent SVM response stamping |
| Notification writer | `erpc/ws_server.go:695-742` builds EVM envelopes | Add an SVM raw-frame delivery adapter |
| Queue | `erpc/ws_writer.go:155-191` can wait 10 seconds | Add a nonblocking offer for upstream callbacks |
| Cleanup | `erpc/ws_server.go:649-681` drains subscriptions and writer | Reuse the same teardown order |
| Metrics | `telemetry/metrics.go:132-182` defines shared families | Bind SVM values without label changes |

`Upstream.Forward` still runs its executor. `UpstreamPoliciesTimeoutOnly` limits that executor to its timeout. The direct path must not call `Network.Forward`. That function includes cache, multiplexing, selection sweeps, and network failsafe policies.

## 2. File and interface map

### 2.1 Production and test files

| File | Change | Responsibility |
|---|---|---|
| `architecture/svm/subscriptions.go` | Create | Known pairs, generic classification, commitment, local IDs, terminal detection |
| `architecture/svm/subscriptions_test.go` | Create | Protocol metadata and parameter unit tests |
| `clients/registry.go` | Modify | SVM `ws` and `wss` construction |
| `clients/registry_test.go` | Modify | Scheme, proxy-pool, and client-type tests |
| `clients/ws_json_rpc_client.go` | Modify | Raw correlation, numeric IDs, handler-set fanout, orphan metadata, disconnect hooks |
| `clients/ws_json_rpc_client_test.go` | Modify | Transport codec, race, reconnect, and orphan tests |
| `indexer/adapters/wsupstream/adapter.go` | Modify | Adapt the existing EVM handler to the transport notification type |
| `indexer/adapters/wsclient/adapter.go` | Modify | Numeric downstream IDs and SVM subscription metadata |
| `indexer/adapters/wsclient/params.go` | Modify | SVM metric family metadata helpers |
| `indexer/adapters/wsclient/adapter_test.go` | Modify | Uniqueness, exhaustion, and shared-limit tests |
| `erpc/subscription_manager.go` | Modify | Shared accounting and architecture classifiers |
| `erpc/svm_subscription_manager.go` | Create | Direct subscribe, unsubscribe, pinning, mapping, and cleanup |
| `erpc/svm_subscription_manager_test.go` | Create | Response identity and direct-owner unit tests |
| `erpc/networks.go` | Modify | Single policy-selected upstream helper |
| `erpc/ws_server.go` | Modify | SVM dispatch, response identity, cost metadata, batch preflight |
| `erpc/ws_writer.go` | Modify | Nonblocking offer and terminal-frame preservation |
| `erpc/ws_svm_notifier.go` | Create | Acknowledgment gate and raw notification rewrite |
| `erpc/ws_svm_notifier_test.go` | Create | Gate, raw payload, order, and queue unit tests |
| `erpc/ws_server_svm_test.go` | Create | Mock SVM upstream and full server integration tests |
| `erpc/ws_server_hardening_test.go` | Modify | Security, transport rejection, and error-shape tests |
| `erpc/http_server_test.go` | Modify | HTTP subscription rejection tests |
| `erpc/ws_conn_limiter_test.go` | Modify | Shared accounting tests |
| `erpc/ws_svm_metrics_test.go` | Create | Metric labels, counts, and cost-frame tests |
| `telemetry/metrics.go` | Modify | Architecture-neutral help text |
| `typescript/config/src/__tests__/svm.test-d.ts` | Modify | Compile-time SVM `wss` coverage |
| `docs/pages/operation/websocket.mdx` | Modify | Shared transport and SVM subscription contract |
| `docs/pages/operation/url.mdx` | Modify | SVM client connection example |
| `docs/pages/operation/directives.mdx` | Modify | Selection and cost-frame behavior |
| `docs/pages/operation/production.mdx` | Modify | Backpressure, disconnect, and shutdown behavior |
| `docs/pages/config/projects/upstreams.mdx` | Modify | SVM WebSocket endpoint support |
| `docs/pages/config/server.mdx` | Modify | Shared WebSocket schema reference |
| `docs/pages/reference/metrics.mdx` | Modify | SVM kinds and metric applicability |

No task touches more than 5 files. Existing EVM test files remain unchanged.

### 2.2 Official method metadata

Use these entries in `architecture/svm/subscriptions.go`:

| Family | Subscribe | Unsubscribe | Notification | Commitment index | Metric kind | Terminal rule |
|---|---|---|---|---:|---|---|
| `account` | `accountSubscribe` | `accountUnsubscribe` | `accountNotification` | 1 | `account` | None |
| `program` | `programSubscribe` | `programUnsubscribe` | `programNotification` | 1 | `program` | None |
| `logs` | `logsSubscribe` | `logsUnsubscribe` | `logsNotification` | 1 | `logs` | None |
| `signature` | `signatureSubscribe` | `signatureUnsubscribe` | `signatureNotification` | 1 | `signature` | `result.value` object containing `err` |
| `block` | `blockSubscribe` | `blockUnsubscribe` | `blockNotification` | 1 | `block` | None |
| `slot` | `slotSubscribe` | `slotUnsubscribe` | `slotNotification` | -1 | `slot` | None |
| `slotsUpdates` | `slotsUpdatesSubscribe` | `slotsUpdatesUnsubscribe` | `slotsUpdatesNotification` | -1 | `slotsUpdates` | None |
| `root` | `rootSubscribe` | `rootUnsubscribe` | `rootNotification` | -1 | `root` | None |
| `vote` | `voteSubscribe` | `voteUnsubscribe` | `voteNotification` | -1 | `vote` | None |

Generic methods use the actual stem as the cost subscription value. They use `custom` as the metric kind. They receive no commitment or terminal metadata.

### 2.3 Cross-task interfaces

These signatures are new. Existing signatures remain unchanged unless a task explicitly changes them.

```go
// architecture/svm/subscriptions.go
type SubscriptionOperation uint8

const (
    SubscriptionOperationNone SubscriptionOperation = iota
    SubscriptionOperationSubscribe
    SubscriptionOperationUnsubscribe
)

type SubscriptionDescriptor struct {
    Family                 string
    MetricKind             string
    SubscribeMethod        string
    UnsubscribeMethod      string
    NotificationMethod     string
    CommitmentOptionsIndex int
    ClampProcessedDefault  bool
    TerminalSignature      bool
    Known                  bool
}

func DescribeSubscriptionMethod(method string) (SubscriptionOperation, SubscriptionDescriptor, bool)
func ValidateSubscriptionRequestID(ctx context.Context, nq *common.NormalizedRequest) error
func InjectSubscriptionCommitment(n common.Network, nq *common.NormalizedRequest, d SubscriptionDescriptor) error
func ParseLocalSubscriptionID(jr *common.JsonRpcRequest) (uint64, error)
func IsTerminalSignatureNotification(d SubscriptionDescriptor, params json.RawMessage) bool
```

```go
// clients/ws_json_rpc_client.go
type WsSubscriptionIDCodec uint8

const (
    WsSubscriptionIDString WsSubscriptionIDCodec = iota
    WsSubscriptionIDUint64
)

type WsSubscriptionNotification struct {
    SubscriptionID string
    Method         string
    Params         json.RawMessage
    Raw            json.RawMessage
}

type WsSubscription struct {
    Handler            func(WsSubscriptionNotification)
    OnEvent            func(WsSubscriptionNotification)
    OwnerID            string
    ID                 string
    Epoch              uint64
    IDCodec            WsSubscriptionIDCodec
    UnsubscribeMethod  string
    NotificationMethod string
    TerminalCheck      func(json.RawMessage) bool
}

func (c *WsJsonRpcClient) UnsubscribeOrphanMethod(subID, method string, epoch uint64, codec WsSubscriptionIDCodec)
func (c *WsJsonRpcClient) UnregisterSubscriptionHandlerOwner(ownerID, subID string, epoch uint64) (remaining int)
func (c *WsJsonRpcClient) SubscriptionHandlerCount(subID string, epoch uint64) int
func (c *WsJsonRpcClient) RemoveOnDisconnect(id string)
```

`WsSubscription.ID` stores the native EVM string or canonical SVM decimal string. `OwnerID` identifies 1 local route within that upstream ID's handler set. `Epoch` scopes the set to 1 upstream connection. `Raw` contains the complete upstream notification frame. `OnEvent` runs once per resolved upstream frame before local fanout. Keep `UnregisterSubscriptionHandler` as the EVM wrapper.

```go
// indexer/adapters/wsclient/adapter.go
func (a *Adapter) AddSvmSubscription(
    networkID, family, metricKind, subscribeMethod, notificationMethod string,
    maxSubs int,
    stop func(),
) (Subscription, error)
```

`Subscription` gains these fields without changing existing EVM fields:

```go
ClientSubIDJSON  json.RawMessage
Family           string
MetricKind       string
SubscribeMethod  string
NotificationMethod string
```

`ClientSubID` remains the internal string map key. SVM uses decimal digits. `ClientSubIDJSON` contains JSON string bytes for EVM and JSON number bytes for SVM.

```go
// erpc/networks.go
func (n *Network) selectDirectUpstream(
    ctx context.Context,
    req *common.NormalizedRequest,
    pairedMethod string,
) (common.Upstream, error)
```

```go
// erpc/ws_svm_notifier.go
func newSvmDelivery(wsc *WsConnection, sub wsclient.Subscription) *svmDelivery
func (d *svmDelivery) Offer(n clients.WsSubscriptionNotification, terminal bool) (<-chan error, error)
func (d *svmDelivery) Open() error
func (wsc *WsConnection) stopAsync(code int, reason string)
```

`Offer` never blocks. A terminal notification returns a buffered completion channel. Other notifications return a nil completion channel.

```go
// erpc/subscription_manager.go
func IsSubscriptionMethodForArchitecture(arch common.NetworkArchitecture, method string) bool
func IsSubscribeMethodForArchitecture(arch common.NetworkArchitecture, method string) bool
```

Keep `IsSubscriptionMethod` and `IsSubscribeMethod` as EVM wrappers. Existing callers retain their behavior until their task updates them.

## 3. Dependency-ordered tasks

### Task 1: SVM subscription protocol metadata

**Files:**
- Create: `architecture/svm/subscriptions.go`
- Test: `architecture/svm/subscriptions_test.go`

**Interfaces:**
- Consumes: `common.JsonRpcRequest.ParamsLiteral`, `common.JsonRpcRequest.ResponseID`, `projectPreForward_negativeId`, `common.Network.Config`.
- Produces: Every `architecture/svm/subscriptions.go` signature from section 2.3.

**Acceptance criteria:**
- The table contains all 9 official pairs.
- Generic suffix classification preserves unknown parameters and derives expected pairs.
- Exactly 5 known methods accept commitment injection.
- Local ID parsing accepts the full `u64` range and rejects every other shape.
- Only an injected `processed` block default becomes `confirmed`.
- Terminal detection excludes `"receivedSignature"`.

- [ ] **Step 1: Write failing classification and commitment tests**

Use this complete table from section 2.2:

```go
func TestDescribeSubscriptionMethod(t *testing.T) {
    tests := []struct {
        method, family, pair, notification string
        commitmentIndex int
    }{
        {"accountSubscribe", "account", "accountUnsubscribe", "accountNotification", 1},
        {"programSubscribe", "program", "programUnsubscribe", "programNotification", 1},
        {"logsSubscribe", "logs", "logsUnsubscribe", "logsNotification", 1},
        {"signatureSubscribe", "signature", "signatureUnsubscribe", "signatureNotification", 1},
        {"blockSubscribe", "block", "blockUnsubscribe", "blockNotification", 1},
        {"slotSubscribe", "slot", "slotUnsubscribe", "slotNotification", -1},
        {"slotsUpdatesSubscribe", "slotsUpdates", "slotsUpdatesUnsubscribe", "slotsUpdatesNotification", -1},
        {"rootSubscribe", "root", "rootUnsubscribe", "rootNotification", -1},
        {"voteSubscribe", "vote", "voteUnsubscribe", "voteNotification", -1},
    }
    for _, tt := range tests {
        op, d, ok := DescribeSubscriptionMethod(tt.method)
        require.True(t, ok)
        require.Equal(t, SubscriptionOperationSubscribe, op)
        require.Equal(t, tt.family, d.Family)
        require.Equal(t, tt.pair, d.UnsubscribeMethod)
        require.Equal(t, tt.notification, d.NotificationMethod)
        require.Equal(t, tt.commitmentIndex, d.CommitmentOptionsIndex)
    }

    op, d, ok := DescribeSubscriptionMethod("vendorFeedSubscribe")
    require.True(t, ok)
    require.Equal(t, SubscriptionOperationSubscribe, op)
    require.Equal(t, "vendorFeedUnsubscribe", d.UnsubscribeMethod)
    require.Equal(t, "vendorFeed", d.Family)
    require.Equal(t, "custom", d.MetricKind)
    require.False(t, d.Known)

    _, _, ok = DescribeSubscriptionMethod("getSlot")
    require.False(t, ok)
}
```

Add commitment cases for omitted, explicit, malformed, and missing required parameters. Add block cases for injected and explicit `processed`. Assert that generic params remain byte-equivalent after normalization.

- [ ] **Step 2: Write failing identifier and terminal tests**

```go
func TestParseLocalSubscriptionID(t *testing.T) {
    valid := []string{"0", "9007199254740993", "18446744073709551615"}
    for _, id := range valid {
        jr := common.NewJsonRpcRequest("accountUnsubscribe", nil)
        require.NoError(t, common.SonicCfg.Unmarshal(
            []byte(`{"jsonrpc":"2.0","id":1,"method":"accountUnsubscribe","params":[`+id+`]}`), jr,
        ))
        got, err := ParseLocalSubscriptionID(jr)
        require.NoError(t, err)
        require.Equal(t, id, strconv.FormatUint(got, 10))
    }

    invalid := []string{`-1`, `1.5`, `"1"`, `null`, `18446744073709551616`]
    for _, id := range invalid {
        jr := common.NewJsonRpcRequest("accountUnsubscribe", nil)
        require.NoError(t, common.SonicCfg.Unmarshal(
            []byte(`{"jsonrpc":"2.0","id":1,"method":"accountUnsubscribe","params":[`+id+`]}`), jr,
        ))
        _, err := ParseLocalSubscriptionID(jr)
        require.Error(t, err)
        require.Contains(t, err.Error(), "Invalid subscription id.")
    }
}
```

Test absent params and extra params. Test terminal `{"result":{"value":{"err":null}}}` and nonterminal `{"result":{"value":"receivedSignature"}}`.

- [ ] **Step 3: Run the tests and confirm the failure**

Run: `go test ./architecture/svm -run 'Test(DescribeSubscriptionMethod|ParseLocalSubscriptionID|InjectSubscriptionCommitment|IsTerminalSignatureNotification)'`

Expected: FAIL because `subscriptions.go` does not exist.

- [ ] **Step 4: Implement the metadata and raw-parameter functions**

Use the complete table from section 2.2. Implement generic classification with `strings.HasSuffix` and case-sensitive matching. Keep unknown unary methods outside the subscription path.

Use this error constructor for invalid local subscription IDs:

```go
func InvalidSubscriptionID() error {
    return common.NewErrJsonRpcExceptionInternal(
        0,
        common.JsonRpcErrorInvalidArgument,
        "Invalid subscription id.",
        nil,
        nil,
    )
}
```

Decode `ParamsLiteral()` into `[]json.RawMessage` when it exists. Otherwise, marshal `Params` into raw messages. Require exactly 1 parameter. Parse digits with `strconv.ParseUint(..., 10, 64)`.

Call `projectPreForward_negativeId` from `ValidateSubscriptionRequestID`. Return its parse error before any upstream selection.

Implement commitment injection with the existing `common.JsonRpcRequest` lock and `InvalidateCacheHash`. Do not call unary `networkPreForward_injectCommitment`. Its method table excludes subscriptions.

- [ ] **Step 5: Run the focused tests**

Run: `go test ./architecture/svm -run 'Test(DescribeSubscriptionMethod|ParseLocalSubscriptionID|InjectSubscriptionCommitment|IsTerminalSignatureNotification)'`

Expected: PASS.

- [ ] **Step 6: Run the complete SVM package tests**

Run: `go test ./architecture/svm`

Expected: PASS with no unary regression.

- [ ] **Step 7: Commit**

```bash
git add architecture/svm/subscriptions.go architecture/svm/subscriptions_test.go
git commit -m "feat: define SVM websocket subscription protocol"
```

### Task 2: Enable SVM WebSocket upstream clients

**Files:**
- Modify: `clients/registry.go:154-174`
- Modify: `clients/registry_test.go`
- Create: `erpc/ws_server_svm_test.go`

**Interfaces:**
- Consumes: `NewWsJsonRpcClient` at `clients/ws_json_rpc_client.go:166-212` and `setupTestERPCServer` at `erpc/ws_server_test.go:243-310`.
- Produces: SVM client creation and the shared mock helpers `svmWsConfig` and `mockSvmWsUpstream`.

**Acceptance criteria:**
- `ws` and `wss` create `ClientTypeWsJsonRpc` for SVM.
- HTTP behavior remains unchanged.
- Proxy pools fail explicitly for SVM WebSocket endpoints.
- Unary calls work through the upstream WebSocket with the client server disabled.
- Existing `jsonRpc.headers` reach the mock handshake.

- [ ] **Step 1: Write failing registry tests**

Add cases for `ws`, `wss`, HTTP, and proxy-pool rejection. Follow the existing registry fake-upstream pattern. Assert `GetType()` rather than a concrete client constructor.

```go
func TestClientRegistrySvmWebSocket(t *testing.T) {
    for _, scheme := range []string{"ws", "wss"} {
        t.Run(scheme, func(t *testing.T) {
            ctx, cancel := context.WithCancel(context.Background())
            defer cancel()
            logger := zerolog.Nop()
            registry := NewClientRegistry(&logger, "test-project", nil, nil)
            ups := common.NewFakeUpstream("svm-ws")
            ups.Config().Type = common.UpstreamTypeSvm
            ups.Config().Endpoint = scheme + "://127.0.0.1:1"
            client, err := registry.CreateClient(ctx, ups)
            require.NoError(t, err)
            require.Equal(t, ClientTypeWsJsonRpc, client.GetType())
        })
    }
}
```

Reuse `common.NewFakeUpstream` and `NewClientRegistry`. The initial dial can fail. The constructor still returns a client with background reconnect.

- [ ] **Step 2: Create the SVM mock and unary test harness**

Define this raw request shape in `erpc/ws_server_svm_test.go`:

```go
type svmMockRequest struct {
    ID     json.RawMessage `json:"id"`
    Method string          `json:"method"`
    Params json.RawMessage `json:"params"`
}

func mockSvmWsUpstream(t *testing.T, custom func(*websocket.Conn, svmMockRequest)) *httptest.Server {
    t.Helper()
    return mockWsUpstream(t, func(conn *websocket.Conn) {
        for {
            _, raw, err := conn.ReadMessage()
            if err != nil {
                return
            }
            var req svmMockRequest
            if json.Unmarshal(raw, &req) != nil {
                continue
            }
            switch req.Method {
            case "getGenesisHash":
                mockWriteJSON(conn, map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": "4sGjMW1s1HzCYy5K7i2K2Xr4Q4y5h7L9KxQh1j7j4h5F"})
            case "getSlot", "getMaxShredInsertSlot":
                mockWriteJSON(conn, map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": 1})
            case "getBlocksWithLimit":
                mockWriteJSON(conn, map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": []int{0}})
            default:
                if custom != nil {
                    custom(conn, req)
                } else {
                    mockWriteJSON(conn, map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": nil})
                }
            }
        }
    })
}
```

Define `svmWsConfig(endpoint string, clientServerEnabled bool)` with network `svm:mainnet-beta`, 1 SVM upstream, and `CheckGenesisHash:false`. Set `Server.WebSocket.Enabled` explicitly. Define `dialSvmWs` for `/test_ws/svm/mainnet-beta`.

- [ ] **Step 3: Write the failing unary test**

Configure `clientServerEnabled=false`. Send an HTTP `getSlot` request to the eRPC listener. Assert the WebSocket upstream returns result `1`. Start a second config with the client server enabled. Send `getSlot` through the client WebSocket. Assert result `1` and exact request ID `"unary-1"`.

- [ ] **Step 4: Run tests and confirm the failure**

Run: `go test ./clients ./erpc -run 'Test(ClientRegistrySvm|WebSocket_SvmUnary)'`

Expected: FAIL because the registry reports only HTTP support.

- [ ] **Step 5: Add SVM WebSocket construction**

Mirror the EVM `ws` and `wss` branch at `clients/registry.go:113-131`. Pass `manager.evmExtractor`, which is the architecture-aware composite extractor. Reject a non-nil proxy pool before dialing. Preserve the existing client cache.

- [ ] **Step 6: Run focused and regression tests**

Run: `go test ./clients ./erpc -run 'Test(ClientRegistry|WebSocket_SvmUnary|WebSocket_UpstreamClient)'`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add clients/registry.go clients/registry_test.go erpc/ws_server_svm_test.go
git commit -m "feat: enable SVM websocket upstream clients"
```

### Task 3: Generalize upstream subscription transport

**Files:**
- Modify: `clients/ws_json_rpc_client.go`
- Modify: `clients/ws_json_rpc_client_test.go`
- Modify: `indexer/adapters/wsupstream/adapter.go`

**Interfaces:**
- Consumes: SVM descriptors from Task 1 only through caller-supplied metadata. The client must not import `architecture/svm`.
- Produces: `WsSubscriptionIDCodec`, `WsSubscriptionNotification`, extended `WsSubscription`, and `UnsubscribeOrphanMethod`.

**Acceptance criteria:**
- Pending response correlation never decodes numbers through `float64`.
- Upstream ID `0` and `18446744073709551615` route correctly.
- Notifications retain their complete raw frame.
- Route registration still precedes subscribe response release.
- Duplicate upstream IDs retain every owner and fan out each notification once per owner.
- The route-level event hook runs once per upstream frame before owner fanout.
- Late success cleanup preserves other owners and uses the stored actual unsubscribe method and codec.
- Existing EVM string IDs and `eth_unsubscribe` behavior remain unchanged.

- [ ] **Step 1: Write failing raw correlation tests**

Use a response ID above `2^53` for the internal wire ID. Force `wireIDCounter` near that value in a client unit test. Assert that the pending caller receives its response.

```go
func TestWsJsonRpcClientCorrelationAboveInt53(t *testing.T) {
    requests := make(chan wsTestRequest, 16)
    client := newTestWsClient(t, newSubscribeServer(t, 0, requests))
    client.wireIDCounter.Store(9007199254740992)
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    require.NoError(t, sendSubscriptionRPC(ctx, client, nil, "eth_chainId", `[]`))
    require.Equal(t, "9007199254740993", string((<-requests).ID))
}
```

Reuse `newSubscribeServer`, `wsTestRequest`, `newTestWsClient`, and `sendSubscriptionRPC`. Keep raw request-ID restoration assertions separate from internal correlation assertions.

- [ ] **Step 2: Write failing numeric subscription tests**

Cover upstream IDs `0`, `9007199254740993`, and `18446744073709551615`. Return each ID as raw JSON. Send a notification immediately after its response. Assert handler receipt before `SendRequest` returns. Assert `notification.Raw` contains unknown top-level members and exact large integers.

Return the same ID for 2 subscriptions with different owner IDs. Send 1 upstream notification. Assert each handler receives exactly 1 frame. Assert the route-level event hook runs exactly once. Remove 1 owner and assert the other owner remains registered. Repeat with an abandoned duplicate subscribe. Assert no upstream unsubscribe occurs while another owner remains.

Add late-abandon cleanup cases without another owner. Assert the outbound method is `accountUnsubscribe` or a generic actual method. Assert the params contain the exact numeric upstream ID.

- [ ] **Step 3: Run tests and confirm the failure**

Run: `go test ./clients -run 'TestWsJsonRpcClient(CorrelationAboveInt53|Svm|Orphan)'`

Expected: FAIL because notification IDs use strings and correlation uses `float64`.

- [ ] **Step 4: Replace response ID decoding**

Change `wsMessage.ID` to `json.RawMessage`. Correlate only the internally generated unsigned decimal IDs. Use `strconv.ParseUint` on trimmed raw bytes. Do not unmarshal the ID into `interface{}`.

```go
func normalizeIDKey(raw json.RawMessage) (string, error) {
    text := string(bytes.TrimSpace(raw))
    id, err := strconv.ParseUint(text, 10, 64)
    if err != nil {
        return "", err
    }
    return strconv.FormatUint(id, 10), nil
}
```

Treat an absent ID as a notification. Treat `null` as a non-response for routing. Log malformed or unknown responses without failing unrelated pending calls.

- [ ] **Step 5: Add generic method ownership and codec-based routing**

Update `SendRequest` before pending registration. A context subscription with a non-nil handler owns subscribe. A context subscription with a nil handler owns unsubscribe. Reject an unowned terminal `Subscribe` or `Unsubscribe` method before writing. Keep ordinary methods outside this rule.

```go
sub, _ := ctx.Value(wsSubscriptionCtxKey{}).(*WsSubscription)
if sub != nil {
    if sub.Handler != nil {
        pending.sub = sub
    } else {
        unsubscribe = sub
    }
} else if method == "eth_subscribe" || method == "eth_unsubscribe" ||
    (c.upstream.Config() != nil && c.upstream.Config().Type == common.UpstreamTypeSvm &&
        (strings.HasSuffix(method, "Subscribe") || strings.HasSuffix(method, "Unsubscribe"))) {
    return nil, common.NewErrUpstreamRequestSkipped(
        fmt.Errorf("%s requires an upstream subscription owner", method),
        c.upstream.Id(),
    )
}
```

Keep the existing EVM error text for unowned EVM calls. Store this handler metadata per upstream route:

```go
type wsSubscriptionRoute struct {
    handlers           map[string]func(WsSubscriptionNotification)
    onEvent            func(WsSubscriptionNotification)
    unsubscribeMethod  string
    notificationMethod string
    codec              WsSubscriptionIDCodec
    terminalCheck      func(json.RawMessage) bool
    terminal           atomic.Bool
}
```

Parse subscribe results from `json.RawMessage`. String codec accepts a nonempty JSON string. `u64` codec accepts every raw integer from `0` through `18446744073709551615`. Store canonical decimal digits in `WsSubscription.ID`.

Replace `subHandlers` with `map[string]*wsSubscriptionRoute`. Insert `OwnerID` into the route's handler set without replacing another owner. Copy the handler set before callbacks. Release the route lock before every nonblocking callback.

Parse notification `params.subscription` as raw JSON. Resolve string or decimal keys without `float64`. Route by current connection epoch and ID. Preserve the first route's `OnEvent` hook during duplicate attachment. Run that hook once before terminal handling and owner fanout. Drop later events after a terminal route. Run `terminalCheck` before handler copying. Mark a terminal route before any handler runs. Call every copied handler exactly once, including every handler for the terminal frame. Set `WsSubscriptionNotification.SubscriptionID` to the canonical route key. Pass the complete frame in `Raw`. Release the route lock before the event hook and owner callbacks.

- [ ] **Step 6: Generalize orphan cleanup and adapt EVM**

Keep `UnsubscribeOrphan` as an EVM wrapper:

```go
func (c *WsJsonRpcClient) UnsubscribeOrphan(subID string, epoch uint64) {
    c.UnsubscribeOrphanMethod(subID, "eth_unsubscribe", epoch, WsSubscriptionIDString)
}
```

Marshal SVM params with `[]uint64{id}` after decimal parsing. Marshal EVM params with `[]string{id}`. Use known notification metadata for orphan routing. Drop an unknown generic orphan route without inventing a method. Late subscribe success always has method metadata. Trigger cleanup only when no local owner remains. A duplicate late success with another owner needs no upstream unsubscribe.

Update `indexer/adapters/wsupstream/adapter.go` to read `notification.Params` from the new notification type.

- [ ] **Step 7: Run transport and EVM adapter tests**

Run: `go test ./clients ./indexer/adapters/wsupstream`

Expected: PASS.

- [ ] **Step 8: Run the race checkpoint**

Run: `go test -race ./clients -run 'TestWsJsonRpcClient'`

Expected: PASS with no race report.

- [ ] **Step 9: Commit**

```bash
git add clients/ws_json_rpc_client.go clients/ws_json_rpc_client_test.go indexer/adapters/wsupstream/adapter.go
git commit -m "feat: generalize upstream websocket subscription routing"
```

### Task 4: Add shared downstream IDs and metadata

**Files:**
- Modify: `indexer/adapters/wsclient/adapter.go`
- Modify: `indexer/adapters/wsclient/params.go`
- Modify: `indexer/adapters/wsclient/adapter_test.go`
- Modify: `erpc/subscription_manager.go`
- Create: `erpc/svm_subscription_manager_test.go`

**Interfaces:**
- Consumes: SVM family and metric values from Task 1.
- Produces: `Adapter.AddSvmSubscription` and the additional `Subscription` fields from section 2.3.

**Acceptance criteria:**
- Each connection allocates IDs from `0` without reuse.
- ID exhaustion fails before upstream mutation.
- SVM and EVM subscriptions share per-connection and instance accounting.
- SVM responses restore exact string, `null`, and full-`u64` request IDs.
- Existing EVM ID generation and response behavior remain unchanged.

- [ ] **Step 1: Write failing ID allocation tests**

Use package `wsclient` so the test can set exhaustion state directly:

```go
func TestAdapterSvmIDsNeverRepeat(t *testing.T) {
    a := New()
    first, err := a.AddSvmSubscription("svm:mainnet-beta", "account", "account", "accountSubscribe", "accountNotification", 2, func() {})
    require.NoError(t, err)
    require.Equal(t, "0", first.ClientSubID)
    require.JSONEq(t, "0", string(first.ClientSubIDJSON))

    _, existed := a.RemoveSubscription(first.ClientSubID)
    require.True(t, existed)
    second, err := a.AddSvmSubscription("svm:mainnet-beta", "slot", "slot", "slotSubscribe", "slotNotification", 2, func() {})
    require.NoError(t, err)
    require.Equal(t, "1", second.ClientSubID)
}
```

Add a test that sets the next ID to `math.MaxUint64`. Assert successful allocation of the maximum value. Assert the next allocation returns an exhaustion error and does not change the registry.

- [ ] **Step 2: Write failing response identity tests**

Create `erpc/svm_subscription_manager_test.go`. Cover raw IDs `"client"`, `null`, `9007199254740993`, and `18446744073709551615`.

```go
func TestNewSvmJsonRpcResultResponsePreservesRequestID(t *testing.T) {
    for _, id := range []string{`"client"`, `null`, `9007199254740993`, `18446744073709551615`} {
        nq := common.NewNormalizedRequest([]byte(`{"jsonrpc":"2.0","id":`+id+`,"method":"accountSubscribe","params":[]}`))
        jr, err := nq.JsonRpcRequest()
        require.NoError(t, err)
        resp := newSvmJsonRpcResultResponse(nq, jr, []byte(`0`))
        body, err := resp.MarshalJSON()
        require.NoError(t, err)
        require.Contains(t, string(body), `"id":`+id)
    }
}
```

- [ ] **Step 3: Run tests and confirm the failure**

Run: `go test ./indexer/adapters/wsclient ./erpc -run 'Test(AdapterSvm|NewSvmJsonRpcResultResponse)'`

Expected: FAIL because `AddSvmSubscription` and `newSvmJsonRpcResultResponse` do not exist.

- [ ] **Step 4: Extend the shared adapter**

Keep the existing `AddSubscription` signature. Implement it through shared internal registration logic. Add monotonic SVM allocation under the adapter mutex:

```go
if a.svmIDsExhausted {
    return Subscription{}, ErrSubscriptionIDExhausted
}
id := a.nextSvmID
if id == math.MaxUint64 {
    a.svmIDsExhausted = true
} else {
    a.nextSvmID++
}
```

Define `ErrSubscriptionIDExhausted` beside the existing adapter errors. Store `ClientSubIDJSON` as `strconv.AppendUint(nil, id, 10)`. Store quoted JSON bytes for EVM in the shared constructor.

Add family, metric kind, subscribe method, and notification method fields. Put raw ID formatting and metadata construction helpers in `params.go`. Keep `GenerateClientSubID` unchanged. Existing EVM paths leave the new fields empty.

- [ ] **Step 5: Add an SVM result-response helper**

Add this helper in `erpc/subscription_manager.go` without changing the EVM helper:

```go
func newSvmJsonRpcResultResponse(nq *common.NormalizedRequest, jrReq *common.JsonRpcRequest, result []byte) *common.NormalizedResponse {
    jrr := &common.JsonRpcResponse{}
    if raw := jrReq.IDRawBytes(); len(raw) > 0 {
        _ = jrr.SetIDBytes(raw)
    } else {
        _ = jrr.SetID(jrReq.ResponseID(true))
    }
    jrr.SetResult(result)
    return common.NewNormalizedResponse().WithRequest(nq).WithJsonRpcResponse(jrr)
}
```

Use this helper only for SVM subscribe and unsubscribe responses in Task 6.

- [ ] **Step 6: Run adapter and EVM subscription tests**

Run: `go test ./indexer/adapters/wsclient ./erpc -run 'Test(Adapter|WebSocket_Subscriptions|WebSocket_ReconnectAfterUnsubscribe)'`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add indexer/adapters/wsclient/adapter.go indexer/adapters/wsclient/params.go indexer/adapters/wsclient/adapter_test.go erpc/subscription_manager.go erpc/svm_subscription_manager_test.go
git commit -m "feat: add shared SVM subscription identities"
```

### Task 5: Add gated nonblocking SVM delivery

**Files:**
- Modify: `erpc/ws_writer.go`
- Modify: `erpc/ws_server.go`
- Create: `erpc/ws_svm_notifier.go`
- Create: `erpc/ws_svm_notifier_test.go`

**Interfaces:**
- Consumes: `WsSubscriptionNotification` from Task 3 and SVM `Subscription` metadata from Task 4.
- Produces: `WsConnection.tryQueue`, `WsConnection.stopAsync`, `svmDelivery.Offer`, and `svmDelivery.Open` from section 2.3.

**Acceptance criteria:**
- Every notification follows its subscribe acknowledgment.
- The callback never waits for queue space and never writes a socket.
- The downstream frame changes only `params.subscription`.
- Unknown top-level and params members survive.
- Full queues and exhausted budgets close only the affected client with `1013`.
- The writer can preserve a marked terminal frame after local removal.

- [ ] **Step 1: Write failing raw-envelope tests**

Build a raw upstream frame with custom `jsonrpc`, method, top-level member, params member, direct result, and large integers. Use this assertion after `Offer` and `Open`:

```go
func TestSvmDeliveryPreservesRawEnvelope(t *testing.T) {
    wsc, sub, readFrame := newTestSvmDeliveryConnection(t)
    d := newSvmDelivery(wsc, sub)
    raw := json.RawMessage(`{"jsonrpc":"2.0","method":"rootNotification","vendorTop":9007199254740993,"params":{"result":18446744073709551615,"subscription":42,"vendorParam":{"keep":true}}}`)
    _, err := d.Offer(clients.WsSubscriptionNotification{SubscriptionID: "42", Method: "rootNotification", Params: json.RawMessage(`{"result":18446744073709551615,"subscription":42,"vendorParam":{"keep":true}}`), Raw: raw}, false)
    require.NoError(t, err)
    requireNoFrameYet(t, readFrame)
    require.NoError(t, d.Open())
    frame := readFrame()
    require.JSONEq(t, `{"jsonrpc":"2.0","method":"rootNotification","vendorTop":9007199254740993,"params":{"result":18446744073709551615,"subscription":0,"vendorParam":{"keep":true}}}`, string(frame))
}
```

Define these helpers in `erpc/ws_svm_notifier_test.go`:

```go
func newTestSvmDeliveryConnection(t *testing.T) (*WsConnection, wsclient.Subscription, func() []byte)
func requireNoFrameYet(t *testing.T, readFrame func() []byte)
```

Use a real Gorilla client and server pair. Reuse `mockWriteMus` for test writes. Assert raw numeric strings before using `JSONEq`. Go's default JSON decoder cannot prove precision by itself.

Add cases for bare root numbers and direct slot, slot update, and vote objects. Add context-bearing account, program, logs, signature, and block objects. Add a block notification whose `value.err` contains `UnsupportedTransactionVersion`. Assert no top-level error appears.

- [ ] **Step 2: Write failing acknowledgment and backpressure tests**

Send the upstream notification before `Open`. Assert the client receives no frame before `Open`. Assert the acknowledgment write completes before the notification frame.

Create a blocked writer with `wsOutCap` messages and no active consumer. Call `Offer` from a goroutine. Require immediate return with `errWsQueueFull`. Assert close code `1013` and reason `subscription backpressure; reconnect and resubscribe`.

Repeat with available channel capacity and an exhausted weighted budget. Assert no individual notification disappears without the connection close.

- [ ] **Step 3: Run tests and confirm the failure**

Run: `go test ./erpc -run 'TestSvmDelivery'`

Expected: FAIL because `newSvmDelivery` and `tryQueue` do not exist.

- [ ] **Step 4: Add the nonblocking writer offer**

Extract the existing weight calculation into `wsOutWeight(size int) int64`. Add this operation without changing the blocking `queue` used by EVM and responses:

```go
var errWsQueueFull = errors.New("websocket send queue full")

func (wsc *WsConnection) tryQueue(m wsOut) error {
    if wsc.closed.Load() {
        releaseMessage(m.msg)
        return errWsConnClosed
    }
    m.weight = wsOutWeight(m.msg.Len())
    if !wsc.w.budget.TryAcquire(m.weight) {
        releaseMessage(m.msg)
        wsc.stopAsync(websocket.CloseTryAgainLater, "subscription backpressure; reconnect and resubscribe")
        return errWsQueueFull
    }
    select {
    case wsc.w.out <- m:
        return nil
    default:
        wsc.w.budget.Release(m.weight)
        releaseMessage(m.msg)
        wsc.stopAsync(websocket.CloseTryAgainLater, "subscription backpressure; reconnect and resubscribe")
        return errWsQueueFull
    }
}
```

Add the existing `w.ended` check before acquisition. Release message buffers exactly once on every path. Do not spawn a goroutine to wait for capacity.

Add `stopAsync` for upstream callbacks. It returns after scheduling `wsc.stop` in a new goroutine. Add a separate `asyncStopOnce sync.Once` field to coalesce concurrent queue failures. Keep `stopOnce` for `wsc.stop` itself. The callback never calls `SetReadDeadline` or writes a downstream frame.

```go
func (wsc *WsConnection) stopAsync(code int, reason string) {
    wsc.asyncStopOnce.Do(func() {
        go wsc.stop(code, reason, 0)
    })
}
```

Add `preserveOnRemove bool` to `wsOut`. Change the writer skip condition to honor it:

```go
case m.note != nil && m.note.sub.Removed() && !m.preserveOnRemove:
```

Keep all EVM calls with `preserveOnRemove=false`.

- [ ] **Step 5: Implement the acknowledgment gate and raw rewrite**

`svmDelivery` owns a mutex, an open flag, pending frames, and their weighted size. Bound pending frames by `wsOutCap` and `wsOutBytes`. Use the same limits before acknowledgment and after acknowledgment.

Rewrite raw messages with `map[string]json.RawMessage` at both levels:

```go
var outer map[string]json.RawMessage
if err := common.SonicCfg.Unmarshal(n.Raw, &outer); err != nil {
    return nil, err
}
var params map[string]json.RawMessage
if err := common.SonicCfg.Unmarshal(outer["params"], &params); err != nil {
    return nil, err
}
params["subscription"] = d.sub.ClientSubIDJSON
encodedParams, err := common.SonicCfg.Marshal(params)
if err != nil {
    return nil, err
}
outer["params"] = encodedParams
frame, err := common.SonicCfg.Marshal(outer)
```

Do not assign `outer["jsonrpc"]` or `outer["method"]`. Do not decode `params.result`. Verify `n.SubscriptionID` against the callback binding before replacement. The transport sets this field from the raw notification ID. This avoids a race with subscribe response processing. Treat every `Offer` error as a connection failure. Do not log and skip the notification.

Create the cost block with `X-ERPC-Methods=<actual notification method>` and `X-ERPC-Subscription=<family>`. Include the existing call, billable, network ID, and network alias values. Resolve the network alias in `newSvmDelivery` before the upstream subscribe call. `Offer` must not call `project.GetNetwork`. Task 9 adds bounded caching.

- [ ] **Step 6: Implement Open and terminal completion handles**

`Open` runs only after acknowledgment write success. It marks delivery open and offers pending frames in original order. A terminal `Offer` allocates a 1-element completion channel and stores it in `wsOut.done`. Return that channel to the subscription owner.

Do not wait on the channel inside `Offer`. Do not mark the subscription removed inside the upstream callback. Task 7 owns terminal cleanup after write completion.

- [ ] **Step 7: Run focused and existing writer tests**

Run: `go test ./erpc -run 'Test(SvmDelivery|WebSocket_WriterOrderAndBackpressure)'`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add erpc/ws_writer.go erpc/ws_server.go erpc/ws_svm_notifier.go erpc/ws_svm_notifier_test.go
git commit -m "feat: add nonblocking SVM notification delivery"
```

### Task 6: Implement the direct SVM subscription owner

**Files:**
- Create: `erpc/svm_subscription_manager.go`
- Modify: `erpc/networks.go`
- Modify: `erpc/subscription_manager.go`
- Modify: `erpc/ws_server.go`
- Modify: `erpc/ws_server_svm_test.go`

**Interfaces:**
- Consumes: Tasks 1 through 5, `Network.selectionOrder`, `Network.preferUpstreams`, `filterMethodEligible`, `Upstream.Forward`, and `common.RequestDirectives.Clone`.
- Produces: `Network.selectDirectUpstream`, `SubscriptionManager.subscribeSvm`, `SubscriptionManager.unsubscribeSvm`, `SubscriptionManager.finishSvmSubscription`, and pinned `svmSubscriptionState`.

**Acceptance criteria:**
- All 9 pairs complete subscribe, notification, and unsubscribe flows.
- Generic pairs and unknown notification methods pass through.
- Duplicate client params still send separate upstream subscribe requests.
- Duplicate upstream IDs fan out to independent local routes.
- Non-final unsubscribe preserves the upstream stream and other local routes.
- The selected upstream remains pinned after policy order changes.
- No subscription call enters cache, multiplexing, consensus, retry, hedge, or failover.
- Upstream ID `0` and values above `2^53` map correctly.
- Only final unsubscribe methods reach the pinned upstream.

- [ ] **Step 1: Write failing single-selection unit tests**

Add selection cases to `erpc/ws_server_svm_test.go`. Use 2 fake upstreams with different policy ranks. Assert only the first receives `req.Upstreams()`. Assert `use-upstream` can select the second. Assert paired-method exclusion removes an otherwise eligible upstream.

Cover the no-upstream error and preserve the policy snapshot after a later reorder.

- [ ] **Step 2: Implement single policy-selected upstream resolution**

Add `selectDirectUpstream` in `erpc/networks.go`:

```go
func (n *Network) selectDirectUpstream(ctx context.Context, req *common.NormalizedRequest, pairedMethod string) (common.Upstream, error) {
    req.ApplyDirectiveDefaults(n.cfg.DirectiveDefaults)
    method, err := req.Method()
    if err != nil {
        return nil, err
    }
    candidates := n.selectionOrder(ctx, req, method)
    candidates = n.preferUpstreams(ctx, req, method, candidates, 0, false)
    if d := req.Directives(); d != nil && d.UseUpstream != "" {
        targeted := make([]common.Upstream, 0, len(candidates))
        for _, u := range candidates {
            if match, matchErr := common.UpstreamMatchesSelector(d.UseUpstream, u); matchErr == nil && match {
                targeted = append(targeted, u)
            }
        }
        candidates = targeted
    }
    candidates, _ = filterMethodEligible(candidates, method)
    candidates, _ = filterMethodEligible(candidates, pairedMethod)
    wsCandidates := make([]common.Upstream, 0, len(candidates))
    for _, u := range candidates {
        concrete, ok := u.(*upstream.Upstream)
        if ok && concrete.Client != nil && concrete.Client.GetType() == clients.ClientTypeWsJsonRpc {
            wsCandidates = append(wsCandidates, u)
        }
    }
    candidates = wsCandidates
    if len(candidates) == 0 {
        return nil, common.NewErrNoUpstreamsFound(n.projectId, n.networkId)
    }
    selected := candidates[0]
    req.SetUpstreams([]common.Upstream{selected})
    return selected, nil
}
```

Import `clients` in `erpc/networks.go`. Allocate the WebSocket candidate slice. Do not mutate a selection-policy backing array. In `subscribeSvm`, assert `selected.(*upstream.Upstream).Client.(*clients.WsJsonRpcClient)` after this filter.

- [ ] **Step 3: Write failing end-to-end subscription tests**

Extend `mockSvmWsUpstream` to record every lifecycle request. Return a different numeric upstream ID for each subscribe. Send an immediate notification after each response.

Use this test matrix:

```go
var svmSubscriptionCases = []struct {
    family, subscribe, unsubscribe, notification string
    params, result string
}{
    {"account", "accountSubscribe", "accountUnsubscribe", "accountNotification", `["11111111111111111111111111111111",{"encoding":"base64"}]`, `{"context":{"slot":1},"value":{"lamports":9007199254740993}}`},
    {"program", "programSubscribe", "programUnsubscribe", "programNotification", `["11111111111111111111111111111111",{"filters":[]}]`, `{"context":{"slot":1},"value":{"pubkey":"11111111111111111111111111111111","account":{}}}`},
    {"logs", "logsSubscribe", "logsUnsubscribe", "logsNotification", `["all"]`, `{"context":{"slot":1},"value":{"signature":"sig","err":null,"logs":[]}}`},
    {"signature", "signatureSubscribe", "signatureUnsubscribe", "signatureNotification", `["sig",{"enableReceivedNotification":false}]`, `{"context":{"slot":1},"value":{"err":null}}`},
    {"block", "blockSubscribe", "blockUnsubscribe", "blockNotification", `["all",{"transactionDetails":"none"}]`, `{"context":{"slot":1},"value":{"slot":1,"block":null,"err":null}}`},
    {"slot", "slotSubscribe", "slotUnsubscribe", "slotNotification", `[]`, `{"slot":1,"parent":0,"root":0}`},
    {"slotsUpdates", "slotsUpdatesSubscribe", "slotsUpdatesUnsubscribe", "slotsUpdatesNotification", `[]`, `{"type":"frozen","slot":1,"timestamp":2}`},
    {"root", "rootSubscribe", "rootUnsubscribe", "rootNotification", `[]`, `18446744073709551615`},
    {"vote", "voteSubscribe", "voteUnsubscribe", "voteNotification", `[]`, `{"votePubkey":"key","slots":[1],"hash":"hash","timestamp":null,"signature":"sig"}`},
}
```

For each row, assert numeric downstream ID `0` and exact request ID. Assert notification method, exact result, actual unsubscribe method, and `result:true`. Keep terminal signature cleanup assertions for Task 7. This task tests explicit unsubscribe for every row.

Add generic `vendorFeedSubscribe` and `otherUnsubscribe` coverage. Return native `-32601` for `blockSubscribe` in a separate test. Assert no other method becomes disabled.

Return the same upstream ID for 2 identical account subscribes. Assert 2 upstream subscribe requests. Send 1 upstream notification and assert 1 downstream frame per client ID. Unsubscribe the first local ID and assert no upstream unsubscribe. Unsubscribe the final local ID and assert exactly 1 upstream unsubscribe.

- [ ] **Step 4: Run tests and confirm the failure**

Run: `go test ./erpc -run 'TestWebSocket_Svm(Subscription|DirectSelection|Generic)'`

Expected: FAIL because SVM dispatch and the direct owner do not exist.

- [ ] **Step 5: Implement request cloning and pinned state**

Define this state in `erpc/svm_subscription_manager.go`:

```go
type svmSubscriptionState struct {
    key               string
    ownerID           string
    upstream          common.Upstream
    route             *svmUpstreamRoute
    unsubscribeMethod string
    delivery          *svmDelivery
    terminal          atomic.Bool
    cleanupOnce       sync.Once
}

type svmUpstreamClientState struct {
    lifecycleMu sync.Mutex
    routes      map[string]*svmUpstreamRoute
}

type svmUpstreamRoute struct {
    client      *clients.WsJsonRpcClient
    upstreamID  string
    epoch       uint64
    subscribers map[string]*svmSubscriptionState
    terminal    atomic.Bool
}
```

Add `svmSubs sync.Map` and `svmUpstreamClients sync.Map` to `SubscriptionManager`. Key local entries with `wsc.id + "/" + localID`. Key upstream clients by their `*clients.WsJsonRpcClient`. Key routes by epoch and upstream ID. Keep all local counts in the existing adapter and `activeSubs`.

The adapter `stop` closure must only close `svmDelivery` to new offers. It must not call `finishSvmSubscription`. A recursive call through `RemoveSubscription` would deadlock `cleanupOnce`.

Clone the parsed request with `jrReq.Clone()` and `common.NewNormalizedRequestFromJsonRpcRequest`. Copy network, user, agent name, client IP, forward headers, and cloned directives. Set `UpstreamPoliciesTimeoutOnly` on the clone. This preserves request-ID bytes and client directives.

For unsubscribe, replace cloned params with `[]interface{}{upstreamUint64}`. Do not reuse the downstream params and do not read them through `float64`.

- [ ] **Step 6: Implement subscribe without network forwarding**

Use this order in `subscribeSvm`:

1. Resolve the network and shared connection adapter.
2. Validate negative and absent request IDs before capacity reservation.
3. Apply known commitment metadata.
4. Acquire existing project and network permits once.
5. Reserve the global slot and add the SVM adapter entry.
6. Select 1 upstream with the expected unsubscribe method.
7. Create `svmDelivery` and the pinned local state.
8. Lock the selected upstream client's `lifecycleMu`.
9. Call `selected.Forward` once with the transport subscription context and local `OwnerID`.
10. Parse and store the upstream ID and epoch.
11. Attach the local state to the route's subscriber set without replacing another state.
12. Reject attachment to a terminal route and clean up only the new local state.
13. Unlock `lifecycleMu` before returning.
14. Replace the response result with the downstream ID and stamp the request ID.
15. Return `delivery.Open` as the post-write callback.

Use `clients.WithWsSubscription` with `WsSubscriptionIDUint64`, actual expected unsubscribe method, known notification metadata, and `OwnerID`. Store the client and epoch for Task 7 callback registration. Release `lifecycleMu` with `defer`. Never hold it during downstream writes.

On every error, unregister only the new transport owner, release adapter state, and decrement `activeSubs` once. Send orphan unsubscribe only when no other owner remains and the epoch is usable. Do not acknowledge an invalid or missing upstream ID. Release the upstream response after constructing the local SVM response.

- [ ] **Step 7: Implement unsubscribe without reselection**

Parse the local ID through `ParseLocalSubscriptionID`. Resolve state only within `wsc.id`. Return `svm.InvalidSubscriptionID()` when absent. ID-less unsubscribe returns no response and no mutation.

Lock the upstream client's `lifecycleMu`. If another local subscriber remains, remove only this owner and return `result:true` without an upstream call. Keep the other subscriber's route and delivery open.

For the final local subscriber, clone the request with the actual client unsubscribe method and upstream numeric ID. Call `state.upstream.Forward` once with the original pinned epoch. Return native upstream errors unchanged. Convert upstream `result:false` into `svm.InvalidSubscriptionID()`. Build success through `newSvmJsonRpcResultResponse` with `result:true`. Release the upstream response and `lifecycleMu`.

Implement `finishSvmSubscription` with `cleanupOnce` in this task. Remove only the local owner and release its shared capacity through that helper. Delete the upstream route only after its final local owner leaves. Task 7 adds disconnect callback removal and terminal completion to the same helper.

Update `CleanupConnection` to partition drained entries. Decrement EVM entries in 1 bulk operation. Call `finishSvmSubscription` for each SVM entry. The adapter drain already called each nonrecursive stop closure. This prevents a second bulk decrement for SVM entries.

- [ ] **Step 8: Add architecture-aware server dispatch**

In `erpc/ws_server.go`, classify with `wsc.architecture`. Route EVM to the existing `Subscribe` and `Unsubscribe`. Route SVM to `subscribeSvm` and `unsubscribeSvm`. Leave ordinary methods on `project.Forward`.

Reject ID-less SVM lifecycle calls before `subscribeSvm` or `unsubscribeSvm` selects an upstream. Return the existing notification flag so no response body appears.

- [ ] **Step 9: Run the complete SVM integration slice**

Run: `go test ./erpc -run 'TestWebSocket_Svm'`

Expected: PASS.

Run: `go test ./erpc -run 'TestWebSocket_(Subscriptions|UpstreamClient|ReconnectAfterUnsubscribe)'`

Expected: PASS with unchanged EVM behavior.

- [ ] **Step 10: Commit**

```bash
git add erpc/svm_subscription_manager.go erpc/networks.go erpc/subscription_manager.go erpc/ws_server.go erpc/ws_server_svm_test.go
git commit -m "feat: proxy direct SVM websocket subscriptions"
```

### Task 7: Complete disconnect and terminal cleanup

**Files:**
- Modify: `erpc/svm_subscription_manager.go`
- Modify: `erpc/svm_subscription_manager_test.go`
- Modify: `clients/ws_json_rpc_client.go`
- Modify: `clients/ws_json_rpc_client_test.go`
- Modify: `erpc/ws_server_svm_test.go`

**Interfaces:**
- Consumes: `svmDelivery.Offer` completion channels, `WsConnection.stop`, and pinned state from Tasks 5 and 6.
- Produces: `WsJsonRpcClient.RemoveOnDisconnect` and terminal completion through the existing `SubscriptionManager.finishSvmSubscription`.

**Acceptance criteria:**
- Upstream loss closes every affected downstream with code `1013` and the specified reason.
- Reconnect never revives old subscriptions.
- Terminal signature delivery completes before local removal and count release.
- `"receivedSignature"` remains active.
- Client disconnect and explicit unsubscribe release all resources exactly once.
- Orphan cleanup uses the actual method and numeric ID only on the same epoch.

- [ ] **Step 1: Write failing disconnect tests**

Create 2 clients with identical account subscriptions. Return the same upstream ID for both subscribe requests. Assert 1 upstream notification produces 2 downstream frames. Unsubscribe the first client and assert no upstream unsubscribe. Assert the second client still receives notifications. Unsubscribe the second client and assert exactly 1 upstream unsubscribe.

Use a second test with 2 active clients. Drop the upstream socket without a close frame. Assert both clients receive code `1013`. Assert reason `upstream subscription disconnected; reconnect and resubscribe`.

Restore the upstream listener. Assert no subscribe request occurs until a client reconnects and sends a new subscribe. Assert unary calls can use the reconnected client.

- [ ] **Step 2: Write failing terminal and cleanup-race tests**

Send `"receivedSignature"`. Assert the subscription remains active and its counts remain unchanged. Send terminal `{"err":null}` next. Assert the client receives both frames in order. Assert local route removal and count release occur only after the terminal socket write.

Race explicit unsubscribe, terminal notification, client disconnect, and upstream disconnect over 100 iterations. Assert every counter returns to its initial value. Assert no panic, double decrement, or stale route remains.

- [ ] **Step 3: Run tests and confirm the failure**

Run: `go test ./clients ./erpc -run 'Test(WebSocket_Svm.*(Disconnect|Terminal|Cleanup)|WsJsonRpcClient.*Disconnect)'`

Expected: FAIL because disconnect removal and terminal completion cleanup are incomplete.

- [ ] **Step 4: Add removable disconnect callbacks**

Implement `RemoveOnDisconnect` by deleting the keyed callback under `onDisconnectMu`. Do not replace it with a nil callback. Register 1 callback per pinned subscription before the subscribe call. Remove it during every cleanup path.

The callback must only call:

```go
wsc.stopAsync(
    websocket.CloseTryAgainLater,
    "upstream subscription disconnected; reconnect and resubscribe",
)
```

Do not send upstream requests or wait in this callback. Do not register a reconnect callback for SVM subscriptions.

- [ ] **Step 5: Complete idempotent cleanup**

Extend `finishSvmSubscription(state *svmSubscriptionState, unsubscribeUpstream bool)` from Task 6. Keep `cleanupOnce` as the only cleanup gate:

1. Lock the upstream client's `lifecycleMu` when route membership can change.
2. Remove the local adapter entry.
3. Delete only this owner from `svmSubs` and the route's subscriber set.
4. Decrement `activeSubs` once for this local subscription.
5. Remove this owner's disconnect callback.
6. Unregister only this upstream handler owner on the same epoch.
7. Delete the route and release its active gauge after the final owner leaves.
8. Send orphan unsubscribe only for a final owner on a usable epoch.
9. Release the lock before downstream waits or socket writes.

Call the helper for explicit unsubscribe, client drain, server shutdown, terminal completion, and failed subscribe. Concurrent callers must observe the same completed state.

- [ ] **Step 6: Complete terminal delivery before removal**

Give the transport route a `terminalCheck` only for known signature subscriptions. The transport marks its route terminal before fanout. It drops every later frame. The manager also marks its route terminal in each copied handler. Task 6 rejects new attachment to that route.

In each local notification handler, compute terminal status only from known signature metadata. Call `Offer` for every copied local handler. If `Offer` returns a completion channel, mark the local state terminal and start 1 goroutine:

```go
go func() {
    if done == nil {
        return
    }
    if err := <-done; err == nil {
        sm.finishSvmSubscription(state, false)
    }
}()
```

On write failure, connection teardown performs cleanup. Do not send upstream unsubscribe after natural completion. The writer's `preserveOnRemove` flag protects each queued terminal frame during concurrent cleanup. Delete the shared upstream route after its final local write completes.

- [ ] **Step 7: Run lifecycle and race checkpoints**

Run: `go test ./clients ./erpc -run 'Test(WebSocket_Svm|WsJsonRpcClient)'`

Expected: PASS.

Run: `go test -race ./clients ./erpc -run 'Test(WebSocket_Svm.*(Disconnect|Terminal|Cleanup)|WsJsonRpcClient.*Disconnect)'`

Expected: PASS with no race report.

- [ ] **Step 8: Commit**

```bash
git add erpc/svm_subscription_manager.go erpc/svm_subscription_manager_test.go clients/ws_json_rpc_client.go clients/ws_json_rpc_client_test.go erpc/ws_server_svm_test.go
git commit -m "feat: complete SVM subscription lifecycle cleanup"
```

### Task 8: Harden HTTP, batch, and local error handling

**Files:**
- Modify: `erpc/ws_server.go`
- Modify: `erpc/http_server.go`
- Modify: `erpc/subscription_manager.go`
- Modify: `erpc/http_server_test.go`
- Modify: `erpc/ws_server_hardening_test.go`

**Interfaces:**
- Consumes: `svm.BatchParseError`, `DescribeSubscriptionMethod`, and direct lifecycle methods from Tasks 1, 6, and 7.
- Produces: `IsSubscriptionMethodForArchitecture`, `IsSubscribeMethodForArchitecture`, and SVM batch preflight.

**Acceptance criteria:**
- Every subscription item is identified before any batch item executes.
- Subscription items in batches and over HTTP cause no upstream mutation.
- Ordinary batch items retain existing behavior and response order.
- Negative SVM IDs return the whole-batch parse error before any item executes.
- ID-less lifecycle calls produce no response and no mutation.
- Local capacity errors use `-32000` for SVM.
- Project filter errors retain exact SVM request IDs.

- [ ] **Step 1: Write failing HTTP rejection tests**

Test every known pair and `vendorFeedSubscribe` over HTTP. Include single requests and batch items. Count upstream requests. Assert `-32601`, exact request ID, and zero upstream subscription calls.

Include an ordinary `getSlot` item in the batch. Assert it retains normal forwarding behavior. Include ID-less lifecycle items. Assert no response entries for those items.

- [ ] **Step 2: Write failing WebSocket preflight tests**

Use a batch containing an ordinary call, known subscribe, generic unsubscribe, and ID-less subscribe. Block the ordinary upstream response until all preflight checks finish. Assert no lifecycle request reaches the upstream.

Return responses in input order. Omit the ID-less item. Return `-32601` for subscription items with IDs.

Add a batch with 1 negative request ID. Assert 1 `-32700` response with `id:null` and zero ordinary or lifecycle upstream calls.

- [ ] **Step 3: Write failing local error tests**

Exercise per-connection and instance capacity on SVM. Assert `-32000`, not `-32005`. Return a native upstream `-32005` in a separate case and assert it remains unchanged.

Reject a method through project filters with request ID `null`. Assert the response contains `id:null`. Repeat with a full-`u64` request ID and assert every digit.

- [ ] **Step 4: Run tests and confirm the failure**

Run: `go test ./erpc -run 'Test(HttpServer_.*Svm.*Subscription|WebSocket_Svm.*(Batch|Capacity|Filter))'`

Expected: FAIL because HTTP classification and WebSocket preflight remain EVM-only.

- [ ] **Step 5: Add architecture classifiers and HTTP rejection**

Implement the new classifier functions in `erpc/subscription_manager.go`:

```go
func IsSubscriptionMethodForArchitecture(arch common.NetworkArchitecture, method string) bool {
    if arch == common.ArchitectureSvm {
        _, _, ok := svm.DescribeSubscriptionMethod(method)
        return ok
    }
    return IsSubscriptionMethod(method)
}
```

Implement the subscribe variant with the operation check. Keep the old functions as EVM wrappers. Replace the HTTP check at `erpc/http_server.go:702` with the architecture-aware function.

Fix `buildUnsupportedMethodResponse` at `erpc/ws_server.go:486-500`. Call `jrr.ResponseID(wsc.architecture == common.ArchitectureSvm)`.

- [ ] **Step 6: Add WebSocket batch preflight**

After `svm.BatchParseError(requests)` succeeds, scan every raw item before launching any goroutine:

```go
subscriptionItems := make([]bool, len(requests))
if wsc.architecture == common.ArchitectureSvm {
    for i, raw := range requests {
        nq := common.NewNormalizedRequest(raw)
        method, err := nq.Method()
        subscriptionItems[i] = err == nil && IsSubscriptionMethodForArchitecture(wsc.architecture, method)
    }
}
```

Call `svm.BatchParseError` before this scan. Keep the existing empty-batch behavior. Process subscription items synchronously through `handleRequest` before launching ordinary items:

```go
for i, reqBody := range requests {
    if subscriptionItems[i] {
        responses[i], notifications[i], _ = wsc.handleRequest(ctx, reqBody, startedAt, true)
        continue
    }
    wg.Add(1)
    go func(index int, reqRaw json.RawMessage) {
        defer wg.Done()
        responses[index], notifications[index], _ = wsc.handleRequest(ctx, reqRaw, startedAt, true)
    }(i, reqBody)
}
```

`handleRequest` builds the local unsupported response. It also preserves ID-less behavior. Ordinary items retain concurrent execution and input response order.

- [ ] **Step 7: Correct SVM local capacity mapping**

Add `common.ErrCodeSubscriptionLimitExceeded` to the SVM local-admission set at `erpc/http_server.go:2067-2074`. Keep native upstream `-32005` unchanged. The same error body path serves HTTP and WebSocket.

- [ ] **Step 8: Run ingress and regression tests**

Run: `go test ./erpc -run 'Test(HttpServer_|WebSocket_Svm|WebSocket_MethodFiltering)'`

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add erpc/ws_server.go erpc/http_server.go erpc/subscription_manager.go erpc/http_server_test.go erpc/ws_server_hardening_test.go
git commit -m "fix: isolate SVM subscriptions from unary transports"
```

### Task 9: Instrument SVM metrics and cost frames

**Files:**
- Modify: `erpc/svm_subscription_manager.go`
- Modify: `erpc/ws_server.go`
- Modify: `telemetry/metrics.go`
- Create: `erpc/ws_svm_metrics_test.go`

**Interfaces:**
- Consumes: `Subscription.Family`, `Subscription.MetricKind`, actual notification methods, and cleanup completion from earlier tasks.
- Produces: Bounded SVM metric bindings and metadata-keyed notification cost caching.

**Acceptance criteria:**
- Existing metric names and label sets remain unchanged.
- Known kinds match feature section 16. Generic kinds equal `custom`.
- Upstream active gauges increment once per unique upstream ID and decrement after its final local route.
- Upstream event counters increment once per received frame before fanout.
- Downstream counters increment only after successful socket writes.
- Cost frames carry the actual notification method and family or generic stem.
- SVM emits no EVM feed metric and updates no finality tracker.

- [ ] **Step 1: Write failing metric tests**

Use unique project and network labels. Do not call `t.Parallel()` in these tests. Cover every known kind and 1 generic `custom` kind.

```go
func TestWebSocketSvmNotificationMetrics(t *testing.T) {
    upstreamConns := make(chan *websocket.Conn, 1)
    upstream := mockSvmWsUpstream(t, func(conn *websocket.Conn, req svmMockRequest) {
        if req.Method == "accountSubscribe" {
            mockWriteJSON(conn, map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": 42})
            upstreamConns <- conn
        }
    })
    defer upstream.Close()
    cfg := svmWsConfig("ws"+strings.TrimPrefix(upstream.URL, "http"), true)
    cfg.Projects[0].Id = "test_svm_metrics"
    addr, cleanup := setupTestERPCServer(t, cfg)
    defer cleanup()
    conn, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/test_svm_metrics/svm/mainnet-beta", nil)
    require.NoError(t, err)
    defer conn.Close()
    sendAndReceive(t, conn, `{"jsonrpc":"2.0","id":1,"method":"accountSubscribe","params":["11111111111111111111111111111111"]}`)
    upstreamConn := <-upstreamConns
    mockWriteJSON(upstreamConn, map[string]interface{}{
        "jsonrpc": "2.0", "method": "accountNotification",
        "params": map[string]interface{}{"subscription": 42, "result": map[string]interface{}{"lamports": 9007199254740993}},
    })
    require.Eventually(t, func() bool {
        return testutil.ToFloat64(telemetry.MetricWebsocketNotificationsSentTotal.WithLabelValues(
            "test_svm_metrics", "svm:mainnet-beta", "account",
        )) == 1
    }, 2*time.Second, 10*time.Millisecond)
}
```

Use raw counters and byte counts from the mock fixture. Assert active upstream subscriptions return to 0 after unsubscribe, terminal completion, client disconnect, and upstream disconnect.

- [ ] **Step 2: Write failing cost-frame tests**

Enable cost frames on the upgrade request. Read frames with `readCostFrame`. Assert:

```text
X-ERPC-Calls: 1
X-ERPC-Billable: 1
X-ERPC-Methods: accountNotification
X-ERPC-Subscription: account
```

For `vendorFeedSubscribe` and `unknownNotification`, assert `X-ERPC-Methods: unknownNotification` and `X-ERPC-Subscription: vendorFeed`. Assert the EVM cost-frame golden tests remain unchanged.

- [ ] **Step 3: Run tests and confirm the failure**

Run: `go test ./erpc -run 'TestWebSocketSvm(NotificationMetrics|CostFrames|UpstreamMetrics)'`

Expected: FAIL because direct subscriptions have no metric bindings.

- [ ] **Step 4: Bind direct upstream metrics**

Extend `svmUpstreamRoute` with bound handles:

```go
live       prometheus.Gauge
events     *telemetry.BoundCounter
eventBytes *telemetry.BoundCounter
```

Create metric handles with fixed labels before the subscribe call. Capture the event handles in the transport route's `OnEvent` hook. Attach those handles to the manager route after its response. Immediate notifications can arrive before response processing. The hook still counts those events without waiting for manager attachment. Use labels `project`, `network`, `upstream`, and `kind`. Increment `live` only for a new upstream ID. A duplicate ID preserves the first transport route's event hook and manager route handles. Increment event counters once in `OnEvent` before local fanout. Use `len(notification.Raw)` for bytes.

Decrement `live` when the final local route leaves the upstream ID. Do not decrement during a non-final unsubscribe, callback receipt, or single-client queue failure.

- [ ] **Step 5: Bind downstream metrics by successful writes**

Initialize `wsNotifier` with `sub.MetricKind` for SVM. Keep `sub.Kind.String()` for EVM. Increment `MetricWebsocketNotificationsSentTotal` and `MetricWebsocketNotificationBytesTotal` only in `writeOut` after `writeFrame` succeeds. This matches existing EVM accounting.

Do not add an architecture label. The network label already identifies SVM.

- [ ] **Step 6: Replace fixed cost caching with bounded metadata caching**

Replace the EVM-only array with method-metadata cache keys. Cache known EVM and known SVM entries by subscribe and notification method. Do not cache unknown notification method names. This prevents unbounded vendor-method cache growth.

Use the actual notification method for `X-ERPC-Methods`. Use `sub.Family` for `X-ERPC-Subscription`. Generic descriptors already carry the subscribe stem.

- [ ] **Step 7: Update metric help text and verify exclusions**

Change `MetricUpstreamSubscriptionsActive` help text to include direct SVM subscriptions. Change event help text to avoid a universal deduplication promise. Keep EVM-only head-stall help unchanged.

Assert no SVM test increases `erpc_upstream_websocket_heads_stall_reconnects_total` or `erpc_subscription_dedup_dropped_total`. Assert the mock poller's processed, confirmed, and finalized views remain unchanged after notifications.

- [ ] **Step 8: Run metric and EVM regression tests**

Run: `go test ./erpc -run 'Test(WebSocketSvm|WebSocket_.*Cost|WebSocket_SubscriptionDedup)'`

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add erpc/svm_subscription_manager.go erpc/ws_server.go telemetry/metrics.go erpc/ws_svm_metrics_test.go
git commit -m "feat: observe direct SVM websocket subscriptions"
```

### Task 10: Verify shared controls and EVM compatibility

**Files:**
- Modify: `erpc/ws_server_svm_test.go`
- Modify: `erpc/ws_server_hardening_test.go`
- Modify: `erpc/ws_conn_limiter_test.go`

**Interfaces:**
- Consumes: All production paths from Tasks 1 through 9 and existing authentication, directives, filters, and rate-limit harnesses.
- Produces: The complete SVM security and compatibility regression matrix.

**Acceptance criteria:**
- Authentication and method-scoped authorization cover both lifecycle methods.
- Project and network permits apply once per lifecycle call and never per notification.
- `use-upstream` and network defaults survive direct forwarding.
- Shared limits release after every cleanup path.
- Existing EVM test files remain unchanged and pass.

- [ ] **Step 1: Add authentication and rate-limit tests**

Reuse the existing SVM authentication rate-limit fixture at `erpc/ws_server_hardening_test.go:621-678`. Add subscribe and unsubscribe success cases. Count project and network permits. Assert 1 permit for subscribe and 1 for unsubscribe. Send several notifications and assert no additional permits.

Add method-scoped authorization rejection for subscribe and unsubscribe. Assert exact SVM request IDs and zero upstream mutation.

- [ ] **Step 2: Add directives and upstream-filter tests**

Configure 2 mock upstreams. Send `X-ERPC-Use-Upstream` on the upgrade request. Assert only the selected upstream receives subscribe and unsubscribe. Change policy order after subscribe. Assert cancellation still uses the pinned upstream.

Configure `allowMethods` and `ignoreMethods` for the expected pair. Assert an upstream that lacks the expected unsubscribe method cannot open a subscription. Call a different generic unsubscribe stem. Assert the actual method receives its own filter check.

- [ ] **Step 3: Add shared-capacity tests**

Create 2 SVM subscriptions when the per-connection cap is 2. Assert the third SVM subscription fails. Remove 1 subscription and assert a new SVM subscription can use the released slot.

Use separate EVM and SVM connections for the instance cap. Each connection binds 1 architecture. Assert `erpc_websocket_rejected_total{reason="sub_global"}` increments only for global rejection. Race cleanup paths and assert exact final counts.

- [ ] **Step 4: Run the focused control matrix**

Run: `go test ./erpc -run 'Test(WebSocket_Svm|WebSocket_.*(Auth|RateLimit|MethodFiltering)|TestWsConnectionLimiter)'`

Expected: PASS.

- [ ] **Step 5: Run every existing WebSocket test**

Run: `go test ./erpc -run '^TestWebSocket_' -count 1`

Expected: PASS. Confirm `erpc/ws_server_test.go`, `erpc/ws_newheads_test.go`, and other EVM-only test files have no diff.

- [ ] **Step 6: Run the race detector across the changed packages**

Run: `go test -race ./architecture/svm ./clients ./indexer/adapters/wsclient ./indexer/adapters/wsupstream ./erpc`

Expected: PASS with no race report.

- [ ] **Step 7: Commit**

```bash
git add erpc/ws_server_svm_test.go erpc/ws_server_hardening_test.go erpc/ws_conn_limiter_test.go
git commit -m "test: verify SVM websocket controls and compatibility"
```

### Task 11: Update TypeScript coverage and primary operator docs

**Files:**
- Modify: `typescript/config/src/__tests__/svm.test-d.ts`
- Modify: `docs/pages/operation/websocket.mdx`
- Modify: `docs/pages/operation/url.mdx`
- Modify: `docs/pages/operation/directives.mdx`
- Modify: `docs/pages/config/projects/upstreams.mdx`

**Interfaces:**
- Consumes: Feature sections 5, 6, 12, 13, 14, 16, and 17.
- Produces: Public SVM `wss` configuration coverage and primary operator guidance.

**Acceptance criteria:**
- The existing TypeScript schema accepts the SVM `wss` example without generated changes.
- The operation page separates shared transport, EVM feeds, and SVM direct proxying.
- The docs state exact IDs, commitment, errors, disconnects, backpressure, and cost values.
- The upstream page no longer limits WebSocket subscriptions to EVM.

- [ ] **Step 1: Add the TypeScript `wss` case**

Add a third upstream to `_realisticSvmConfig`:

```ts
{
  id: "solana-wss",
  type: "svm",
  endpoint: "wss://rpc.example.com/ws",
  svm: { cluster: "mainnet-beta", checkGenesisHash: false },
},
```

Run: `pnpm --filter @erpc-cloud/config test`

Expected: PASS without edits to `typescript/config/src/generated.ts` or `typescript/config/src/index.ts`.

- [ ] **Step 2: Rewrite the WebSocket page by responsibility**

Keep the visible body promise-focused. Put exact implementation details in `<AISection>`. Use these sections:

1. Shared client transport.
2. Ordinary unary JSON-RPC.
3. EVM subscriptions and existing feed promises.
4. SVM direct subscriptions.
5. Configuration and security.
6. Observability and cost frames.

Document all 9 pairs in a table. Include generic suffix behavior and native unsupported responses. State that each client subscription sends 1 upstream subscribe request. Explain Agave duplicate IDs, downstream fanout, and final-owner upstream cancellation.

- [ ] **Step 3: Document exact SVM behavior in AISection**

Include the config schema with exact existing defaults and a permalink to `common/defaults.go`. Add numbered edge cases for:

- Numeric ID `0`, full `u64`, and no downstream reuse.
- String, `null`, full-`u64`, negative, and absent request IDs.
- HTTP and batch rejection before mutation.
- Raw notification shapes and unknown fields.
- Commitment injection and block clamping.
- Actual unsubscribe methods and `Invalid subscription id.`.
- Terminal signature completion.
- Close code `1013` and client-driven resubscription.
- Queue limits and close code `1013`.

List exact metric names and labels. Do not add an architecture label. Reference the page's own `.llms.txt` URL in prompt examples. Do not hand-edit generated files.

- [ ] **Step 4: Update URL, directives, and upstream configuration docs**

Add `ws://localhost:4000/main/svm/mainnet-beta` to the URL page. Explain `use-upstream` pinning during subscribe and unsubscribe. Document actual notification method and family values in cost frames.

Update `upstreams[*].endpoint` at `docs/pages/config/projects/upstreams.mdx:219-221`. State that SVM supports `http`, `https`, `ws`, and `wss`. State that WebSocket upstreams work with the client server disabled. Preserve proxy-pool rejection and the single-endpoint constraint.

- [ ] **Step 5: Build TypeScript packages and docs**

Run: `pnpm -r run build`

Expected: PASS.

Run: `pnpm --dir docs build`

Expected: PASS. The build script generates `.llms.txt` outputs. Do not edit them manually.

- [ ] **Step 6: Commit**

```bash
git add typescript/config/src/__tests__/svm.test-d.ts docs/pages/operation/websocket.mdx docs/pages/operation/url.mdx docs/pages/operation/directives.mdx docs/pages/config/projects/upstreams.mdx
git commit -m "docs: describe SVM websocket configuration and behavior"
```

### Task 12: Complete production and observability docs

**Files:**
- Modify: `docs/pages/operation/production.mdx`
- Modify: `docs/pages/config/server.mdx`
- Modify: `docs/pages/reference/metrics.mdx`

**Interfaces:**
- Consumes: Feature sections 15, 16, and 17.
- Produces: Production close behavior, shared config reference, and SVM metric reference.

**Acceptance criteria:**
- Operators can distinguish server shutdown `1001` from upstream loss and overload `1013`.
- The server page describes every shared `server.webSocket` control or links a complete reference.
- The metrics page lists every known kind and `custom` without new labels.

- [ ] **Step 1: Update production behavior**

Document close code `1013` for upstream subscription loss and downstream overload. Use the exact upstream-loss reason from feature section 14. Explain possible notification loss and mandatory client resubscription. Keep existing server shutdown close code `1001`.

Document the 256-message queue and 64 KiB weighted budget. State that the weighted budget does not cap 1 outbound frame.

- [ ] **Step 2: Complete the shared server reference**

Add or link every `server.webSocket` field from `common/config.go:187-210`. Include defaults from `common/defaults.go:929-954`. State that these limits combine EVM and SVM subscriptions. Add no SVM-specific fields.

- [ ] **Step 3: Update the metrics reference**

Document SVM usage for the metrics in feature section 16. List `account`, `program`, `logs`, `signature`, `block`, `slot`, `slotsUpdates`, `root`, `vote`, and `custom`. Mark head-stall and deduplication metrics as EVM-only.

- [ ] **Step 4: Build docs and inspect generated scope**

Run: `pnpm --dir docs build`

Expected: PASS.

Run: `git diff --exit-code -- common/config.go typescript/config/src/generated.ts`

Expected: PASS with no generated schema diff.

- [ ] **Step 5: Commit**

```bash
git add docs/pages/operation/production.mdx docs/pages/config/server.mdx docs/pages/reference/metrics.mdx
git commit -m "docs: cover SVM websocket operations and metrics"
```

---

## 4. Checkpoints and risk controls

### 4.1 Review checkpoints

| Checkpoint | Required evidence | Stop condition |
|---|---|---|
| After Task 3 | Raw correlation and immediate-notification tests pass with race detection | Any `float64` ID conversion or EVM transport regression |
| After Task 7 | All 9 pairs, generic extension, disconnect, terminal, and cleanup tests pass | Notification before acknowledgment, double cleanup, or silent resubscribe |
| After Task 10 | Shared controls and every existing WebSocket test pass | EVM wire diff, bypassed limits, or race report |
| After Task 12 | TypeScript and docs builds pass without schema changes | Generated schema diff or universal EVM promises applied to SVM |

### 4.2 Risk register

| Risk | Control |
|---|---|
| Immediate notification overtakes acknowledgment | Transport registers first. `svmDelivery` gates until acknowledgment write success. |
| Numeric precision loss | Raw JSON IDs, `uint64`, `strconv.ParseUint`, and full-range fixtures. |
| Duplicate subscription mutation | Direct `Upstream.Forward` with `UpstreamPoliciesTimeoutOnly` and a single-element upstream list. |
| Agave reuses an upstream ID | Handler-set fanout, per-client lifecycle serialization, and reference-counted final unsubscribe. |
| Terminal event affects only part of a route | Transport-level terminal marking before fanout and per-client write completion handles. |
| Hidden automatic failover | No call to `Network.Forward` for lifecycle methods. Pin `common.Upstream` in subscription state. |
| Upstream callback stalls shared socket | Nonblocking `tryQueue`, bounded pre-open queue, and close code `1013`. |
| Terminal frame disappears during cleanup | `preserveOnRemove` and cleanup after the terminal write completion. |
| Counter leaks during races | Shared adapter and 1 `cleanupOnce` for every path. Race tests cover 100 iterations. |
| Generic methods become a closed registry | Suffix classification, raw params, raw frames, and native upstream errors. |
| Vendor notification names grow caches | Cache only known metadata. Compute unknown cost frames without caching. |
| Shared upstream reconnect revives stale routes | Epoch-scoped handlers, disconnect callbacks, and no SVM reconnect callback. |
| SVM capacity uses Solana `NodeUnhealthy` code | Add the local subscription-limit error to SVM `-32000` remapping. |
| Documentation overpromises integrity | Separate EVM feed sections from SVM direct proxy sections. |

## 5. Final verification gate

Run every command from the repository root after Task 12.

- [ ] **Step 1: Format all changed source files**

Run: `make fmt`

Expected: PASS with no formatting diff.

- [ ] **Step 2: Build the server and TypeScript packages**

Run: `make build`

Expected: PASS.

Run: `pnpm -r run build`

Expected: PASS.

- [ ] **Step 3: Run focused and race tests**

Run: `go test ./architecture/svm ./clients ./indexer/adapters/wsclient ./indexer/adapters/wsupstream ./erpc`

Expected: PASS.

Run: `go test -race ./clients ./indexer/adapters/wsclient ./erpc`

Expected: PASS with no race report.

- [ ] **Step 4: Run repository test suites**

Run: `make test-fast`

Expected: PASS.

Run: `make test`

Expected: PASS with the full race-enabled suite.

- [ ] **Step 5: Build documentation and verify schema scope**

Run: `pnpm --dir docs build`

Expected: PASS.

Run: `git diff --exit-code -- common/config.go typescript/config/src/generated.ts`

Expected: PASS with no generated schema change.

Run: `git status --short`

Expected: Only intended source, test, spec, and documentation changes appear. Remove untracked test logs and build outputs created by these commands. Do not remove pre-existing files.

## 6. Manual end-to-end smoke test with a mock SVM upstream

This smoke test uses only the repository's existing Go and Node tools. It creates no repository file.

- [ ] **Step 1: Create the mock upstream outside the repository**

Write `/tmp/svm-ws-mock.go`:

```go
package main

import (
    "encoding/json"
    "net/http"

    "github.com/gorilla/websocket"
)

type request struct {
    ID     json.RawMessage `json:"id"`
    Method string          `json:"method"`
}

func main() {
    upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
    handler := func(w http.ResponseWriter, r *http.Request) {
        conn, err := upgrader.Upgrade(w, r, nil)
        if err != nil {
            return
        }
        defer conn.Close()
        for {
            var req request
            if err := conn.ReadJSON(&req); err != nil {
                return
            }
            println(req.Method)
            response := map[string]interface{}{"jsonrpc": "2.0", "id": req.ID}
            switch req.Method {
            case "getGenesisHash":
                response["result"] = "4sGjMW1s1HzCYy5K7i2K2Xr4Q4y5h7L9KxQh1j7j4h5F"
            case "getSlot", "getMaxShredInsertSlot":
                response["result"] = 1
            case "getBlocksWithLimit":
                response["result"] = []int{0}
            case "accountSubscribe":
                response["result"] = 42
                if err := conn.WriteJSON(response); err != nil {
                    return
                }
                notification := json.RawMessage(`{"jsonrpc":"2.0","method":"accountNotification","vendorTop":9007199254740993,"params":{"result":{"context":{"slot":1},"value":{"lamports":9007199254740993}},"subscription":42,"vendorParam":true}}`)
                if err := conn.WriteMessage(websocket.TextMessage, notification); err != nil {
                    return
                }
                continue
            case "accountUnsubscribe":
                response["result"] = true
            default:
                response["result"] = nil
            }
            if err := conn.WriteJSON(response); err != nil {
                return
            }
        }
    }
    _ = http.ListenAndServe("127.0.0.1:18999", http.HandlerFunc(handler))
}
```

Run from the repository root:

```bash
go run /tmp/svm-ws-mock.go
```

Keep this process in terminal 1.

- [ ] **Step 2: Create and validate the eRPC config**

Write `/tmp/erpc-svm-ws.yaml`:

```yaml
logLevel: debug
server:
  listenV4: true
  httpPortV4: 14000
  webSocket:
    enabled: true
projects:
  - id: main
    upstreams:
      - id: mock-svm
        type: svm
        endpoint: ws://127.0.0.1:18999
        svm:
          cluster: mainnet-beta
          checkGenesisHash: false
    networks:
      - architecture: svm
        svm:
          cluster: mainnet-beta
          commitment: confirmed
```

Run:

```bash
go run ./cmd/erpc/main.go --config /tmp/erpc-svm-ws.yaml validate
go run ./cmd/erpc/main.go --config /tmp/erpc-svm-ws.yaml
```

Keep the server in terminal 2.

- [ ] **Step 3: Run the raw-ID subscription client**

Check Node WebSocket support:

```bash
node -p 'typeof WebSocket'
```

Expected output: `function`.

Write `/tmp/svm-ws-client.mjs`:

```javascript
const ws = new WebSocket("ws://127.0.0.1:14000/main/svm/mainnet-beta");
let stage = 0;

ws.onopen = () => {
  ws.send('{"jsonrpc":"2.0","id":18446744073709551615,"method":"accountSubscribe","params":["11111111111111111111111111111111",{}]}');
};

ws.onmessage = (event) => {
  const text = String(event.data);
  console.log(text);
  if (stage === 0) {
    if (!text.includes('"id":18446744073709551615') || !text.includes('"result":0')) {
      throw new Error("invalid subscribe acknowledgment");
    }
    stage = 1;
    return;
  }
  if (stage === 1) {
    if (!text.includes('"method":"accountNotification"') ||
        !text.includes('"subscription":0') ||
        !text.includes('9007199254740993') ||
        !text.includes('"vendorTop":9007199254740993') ||
        !text.includes('"vendorParam":true')) {
      throw new Error("notification was not preserved");
    }
    stage = 2;
    ws.send('{"jsonrpc":"2.0","id":"stop","method":"accountUnsubscribe","params":[0]}');
    return;
  }
  if (!text.includes('"id":"stop"') || !text.includes('"result":true')) {
    throw new Error("unsubscribe failed");
  }
  ws.close();
};
```

Run in terminal 3:

```bash
node /tmp/svm-ws-client.mjs
```

Expected order:

1. Subscribe acknowledgment with full request ID and result `0`.
2. `accountNotification` with subscription `0` and intact large integers.
3. Unsubscribe response with ID `"stop"` and result `true`.

- [ ] **Step 4: Verify upstream disconnect behavior**

Run this client in terminal 3:

```bash
node --input-type=module -e '
const ws = new WebSocket("ws://127.0.0.1:14000/main/svm/mainnet-beta");
ws.onopen = () => ws.send(JSON.stringify({jsonrpc:"2.0",id:2,method:"accountSubscribe",params:["11111111111111111111111111111111"]}));
ws.onmessage = event => console.log(String(event.data));
ws.onclose = event => {
  console.log(event.code, event.reason);
  if (event.code !== 1013 || event.reason !== "upstream subscription disconnected; reconnect and resubscribe") process.exit(1);
  process.exit(0);
};
'
```

Stop the mock process in terminal 1. Assert the client prints code `1013` and the exact reason.

Restart the mock. Assert no automatic `accountSubscribe` appears in its log. Run `/tmp/svm-ws-client.mjs` again. Assert the new connection completes its full lifecycle.

- [ ] **Step 5: Stop and clean the smoke environment**

Stop eRPC and the mock. Remove temporary files:

```bash
rm /tmp/svm-ws-mock.go /tmp/svm-ws-client.mjs /tmp/erpc-svm-ws.yaml
```

Run `git status --short`. Confirm the smoke test created no repository file.

## 7. Acceptance coverage map

| Feature acceptance criteria | Tasks and final evidence |
|---|---|
| AC1–AC5 | Tasks 1, 2, 3, and 6 |
| AC6–AC8 | Task 6 direct-owner and single-selection tests |
| AC9–AC13 | Tasks 1, 3, 4, 6, and 8 |
| AC14 | Task 8 HTTP and batch preflight tests |
| AC15–AC17 | Tasks 5 and 6 raw-delivery tests |
| AC18–AC20 | Tasks 6 and 8 unsubscribe and error tests |
| AC21–AC23 | Task 1 commitment and generic-parameter tests |
| AC24–AC25 | Task 7 terminal lifecycle tests |
| AC26–AC30 | Tasks 5 and 7 disconnect and backpressure tests |
| AC31–AC32 | Tasks 7 and 10 cleanup and shared-control tests |
| AC33–AC34 | Task 9 metrics, cost frames, and exclusion tests |
| AC35 | Task 10 unchanged EVM regression suite |
| AC36–AC37 | Tasks 11 and 12 TypeScript, docs, and schema checks |
| AC38 | Final verification gate |

## 8. Assumptions

- `feature.md` remains the behavioral source of truth.
- Generic suffix matching is case-sensitive.
- Generic unsubscribe stems need not match the original subscribe stem.
- The upstream has 1 endpoint for unary calls and subscriptions.
- Separate provider HTTP and WebSocket URLs require separate upstream entries.
- Native upstream responses determine method availability.
- Clients own reconnect, resubscribe, and state reconciliation.
- Delivery order matters only within each upstream subscription.
- Public live-endpoint tests remain optional and are not release gates.

## 9. Official references

- [Solana WebSocket method index](https://solana.com/docs/rpc/websocket).
- [Solana `signatureSubscribe`](https://solana.com/docs/rpc/websocket/signaturesubscribe).
- [Solana `blockSubscribe`](https://solana.com/docs/rpc/websocket/blocksubscribe).
- [Solana `accountUnsubscribe`](https://solana.com/docs/rpc/websocket/accountunsubscribe).
- [Agave `u64` subscription IDs](https://github.com/anza-xyz/agave/blob/5bb304027fe1a166b02cb26dc8c161593f416fca/rpc/src/rpc_subscription_tracker.rs#L29-L42).
- [Agave duplicate-parameter ID sharing](https://github.com/anza-xyz/agave/blob/5bb304027fe1a166b02cb26dc8c161593f416fca/rpc/src/rpc_subscription_tracker.rs#L185-L275).
- [Agave connection-scoped token replacement](https://github.com/anza-xyz/agave/blob/5bb304027fe1a166b02cb26dc8c161593f416fca/rpc/src/rpc_pubsub.rs#L379-L420).
- [Agave unknown unsubscribe errors](https://github.com/anza-xyz/agave/blob/5bb304027fe1a166b02cb26dc8c161593f416fca/rpc/src/rpc_pubsub.rs#L410-L420).
- [Agave notification serialization](https://github.com/anza-xyz/agave/blob/5bb304027fe1a166b02cb26dc8c161593f416fca/rpc/src/rpc_subscriptions.rs#L276-L322).
- [`jsonrpc-core` ID-less method-call behavior](https://github.com/paritytech/jsonrpc/blob/v18.0.0/core/src/io.rs#L292-L316).
- [JSON-RPC 2.0 specification](https://www.jsonrpc.org/specification).

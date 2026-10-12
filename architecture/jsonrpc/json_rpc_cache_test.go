package jsonrpc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/erpc/erpc/common"
	"github.com/erpc/erpc/telemetry"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

// Histogram metrics are nil until SetHistogramBuckets initializes them (same
// initialization the svm cache tests need before any cache call).
func init() {
	_ = telemetry.SetHistogramBuckets("0.05,0.5,5,30")
}

// ristrettoSettleDelay lets the async Ristretto admission buffer drain so a
// subsequent Get sees the value that was just Set (mirrors the svm cache test).
const ristrettoSettleDelay = 50 * time.Millisecond

// fakeTipNetwork extends the finality test's fakeNetwork with the tip accessor
// the cache buckets by (erpc.Network gains JsonRpcHighestTip in the same
// change set; the cache only type-asserts, so this standalone fake is enough).
type fakeTipNetwork struct {
	fakeNetwork
	tip int64
}

func (f *fakeTipNetwork) JsonRpcHighestTip(context.Context) int64 { return f.tip }

func newTipNetwork(cfg *common.NetworkConfig, tip int64) *fakeTipNetwork {
	return &fakeTipNetwork{fakeNetwork: fakeNetwork{cfg: cfg}, tip: tip}
}

func newTestCache(t *testing.T, policies ...*common.CachePolicyConfig) *GenericJsonRpcCache {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	if len(policies) == 0 {
		policies = []*common.CachePolicyConfig{
			{
				Connector: "mem",
				Network:   "*",
				Method:    "*",
				Finality:  common.DataFinalityStateUnknown,
			},
		}
	}

	cfg := &common.CacheConfig{
		Connectors: []*common.ConnectorConfig{
			{
				Id:     "mem",
				Driver: common.DriverMemory,
				Memory: &common.MemoryConnectorConfig{MaxItems: 1000, MaxTotalSize: "1MB"},
			},
		},
		Policies: policies,
	}

	c, err := NewGenericJsonRpcCache(ctx, &log.Logger, cfg)
	require.NoError(t, err)
	return c
}

func cacheReq(t *testing.T, network common.Network, body string) *common.NormalizedRequest {
	t.Helper()
	req := common.NewNormalizedRequest([]byte(body))
	if network != nil {
		req.SetNetwork(network)
	}
	return req
}

func cacheResp(req *common.NormalizedRequest, result interface{}) *common.NormalizedResponse {
	jrr, err := common.NewJsonRpcResponse(1, result, nil)
	if err != nil {
		panic(err)
	}
	return common.NewNormalizedResponse().WithRequest(req).WithJsonRpcResponse(jrr)
}

func TestGenericCache_SetThenGet_RoundTrips(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	ctx := context.Background()

	net := newTipNetwork(&common.NetworkConfig{Architecture: common.ArchitectureJsonRpc}, 0)
	body := `{"jsonrpc":"2.0","id":1,"method":"getQuote","params":["USDC"]}`
	req := cacheReq(t, net, body)
	require.NoError(t, c.Set(ctx, req, cacheResp(req, "0.99")))
	time.Sleep(ristrettoSettleDelay)

	req2 := cacheReq(t, net, body)
	got, err := c.Get(ctx, req2)
	require.NoError(t, err)
	require.NotNil(t, got, "identical request must hit the cache")
	jrr, err := got.JsonRpcResponse()
	require.NoError(t, err)
	require.Contains(t, string(jrr.GetResultBytes()), "0.99")
}

func TestGenericCache_Get_MissesForDifferentParams(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	ctx := context.Background()
	net := newTipNetwork(&common.NetworkConfig{Architecture: common.ArchitectureJsonRpc}, 0)

	reqA := cacheReq(t, net, `{"jsonrpc":"2.0","id":1,"method":"getQuote","params":["USDC"]}`)
	require.NoError(t, c.Set(ctx, reqA, cacheResp(reqA, "usdc-price")))
	time.Sleep(ristrettoSettleDelay)

	reqB := cacheReq(t, net, `{"jsonrpc":"2.0","id":2,"method":"getQuote","params":["DAI"]}`)
	got, err := c.Get(ctx, reqB)
	require.NoError(t, err)
	require.Nil(t, got, "different params must not reuse a cached entry")
}

func TestGenericCache_Set_SkipsResponsesWithJsonRpcError(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	ctx := context.Background()
	net := newTipNetwork(&common.NetworkConfig{Architecture: common.ArchitectureJsonRpc}, 0)

	body := `{"jsonrpc":"2.0","id":1,"method":"getQuote","params":["USDC"]}`
	req := cacheReq(t, net, body)
	rpcErr := common.NewErrJsonRpcExceptionExternal(-32000, "upstream boom", "")
	jrr, err := common.NewJsonRpcResponse(1, nil, rpcErr)
	require.NoError(t, err)
	resp := common.NewNormalizedResponse().WithRequest(req).WithJsonRpcResponse(jrr)

	require.NoError(t, c.Set(ctx, req, resp))
	time.Sleep(ristrettoSettleDelay)

	got, err := c.Get(ctx, cacheReq(t, net, body))
	require.NoError(t, err)
	require.Nil(t, got, "error responses must never be cached")
}

func TestGenericCache_IsObjectNull(t *testing.T) {
	t.Parallel()
	var nilC *GenericJsonRpcCache
	require.True(t, nilC.IsObjectNull(), "nil cache must report null")

	emptyCfg := &common.CacheConfig{
		Connectors: []*common.ConnectorConfig{
			{Id: "mem", Driver: common.DriverMemory, Memory: &common.MemoryConnectorConfig{MaxItems: 10, MaxTotalSize: "1KB"}},
		},
	}
	c, err := NewGenericJsonRpcCache(context.Background(), &log.Logger, emptyCfg)
	require.NoError(t, err)
	require.True(t, c.IsObjectNull(), "cache without policies reports null")
}

// ---- key building -----------------------------------------------------------

func TestGenericCache_GenerateKeys_EmptyRefWithoutBucket(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	net := newTipNetwork(&common.NetworkConfig{Architecture: common.ArchitectureJsonRpc}, 105)
	req := cacheReq(t, net, `{"jsonrpc":"2.0","id":1,"method":"getQuote","params":[]}`)

	rpcReq, err := req.JsonRpcRequest(context.Background())
	require.NoError(t, err)
	groupKey, requestKey, err := c.generateKeys(context.Background(), req, rpcReq)
	require.NoError(t, err)

	require.Equal(t, "jsonrpc:test:", groupKey, "no cacheTipBucket → empty ref segment")
	require.True(t, strings.HasPrefix(requestKey, "getQuote:"), "range key is method:paramsHash")
}

func TestGenericCache_GenerateKeys_TipBucketRef(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	cfg := &common.NetworkConfig{
		Architecture: common.ArchitectureJsonRpc,
		Methods: &common.MethodsConfig{Definitions: map[string]*common.CacheMethodConfig{
			"getLatestLedger": {Realtime: true, CacheTipBucket: 10},
		}},
	}
	net := newTipNetwork(cfg, 105)
	req := cacheReq(t, net, `{"jsonrpc":"2.0","id":1,"method":"getLatestLedger","params":[]}`)

	rpcReq, err := req.JsonRpcRequest(context.Background())
	require.NoError(t, err)
	groupKey, _, err := c.generateKeys(context.Background(), req, rpcReq)
	require.NoError(t, err)
	require.Equal(t, "jsonrpc:test:10", groupKey, "tip 105 / bucket 10 → bucket ref 10")
}

func TestGenericCache_GenerateKeys_CacheKeySuffixHonored(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	cfg := &common.NetworkConfig{
		Architecture:   common.ArchitectureJsonRpc,
		CacheKeySuffix: "blue",
		Methods: &common.MethodsConfig{Definitions: map[string]*common.CacheMethodConfig{
			"getLatestLedger": {Realtime: true, CacheTipBucket: 10},
		}},
	}
	net := newTipNetwork(cfg, 105)
	req := cacheReq(t, net, `{"jsonrpc":"2.0","id":1,"method":"getLatestLedger","params":[]}`)

	rpcReq, err := req.JsonRpcRequest(context.Background())
	require.NoError(t, err)
	groupKey, _, err := c.generateKeys(context.Background(), req, rpcReq)
	require.NoError(t, err)
	require.Equal(t, "jsonrpc:test:blue:10", groupKey)
}

func TestGenericCache_GenerateKeys_NoTipAvailable_PlainKey(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	cfg := &common.NetworkConfig{
		Architecture: common.ArchitectureJsonRpc,
		Methods: &common.MethodsConfig{Definitions: map[string]*common.CacheMethodConfig{
			"getLatestLedger": {Realtime: true, CacheTipBucket: 10},
		}},
	}
	// tip 0 (not yet polled) → no bucket, plain key — a frozen bucket must never
	// silently cache forever.
	net := newTipNetwork(cfg, 0)
	req := cacheReq(t, net, `{"jsonrpc":"2.0","id":1,"method":"getLatestLedger","params":[]}`)

	rpcReq, err := req.JsonRpcRequest(context.Background())
	require.NoError(t, err)
	groupKey, _, err := c.generateKeys(context.Background(), req, rpcReq)
	require.NoError(t, err)
	require.Equal(t, "jsonrpc:test:", groupKey)
}

// Bucket behavior end to end: requests within the same bucket share a key,
// crossing a bucket boundary misses.
func TestGenericCache_TipBucket_RollsForwardWithTip(t *testing.T) {
	t.Parallel()
	c := newTestCache(t, &common.CachePolicyConfig{
		Connector: "mem",
		Network:   "*",
		Method:    "*",
		Finality:  common.DataFinalityStateRealtime,
	})
	ctx := context.Background()
	cfg := &common.NetworkConfig{
		Architecture: common.ArchitectureJsonRpc,
		Methods: &common.MethodsConfig{Definitions: map[string]*common.CacheMethodConfig{
			"getLatestLedger": {Realtime: true, CacheTipBucket: 10},
		}},
	}
	net := newTipNetwork(cfg, 105)
	body := `{"jsonrpc":"2.0","id":1,"method":"getLatestLedger","params":[]}`

	req := cacheReq(t, net, body)
	require.NoError(t, c.Set(ctx, req, cacheResp(req, "ledger-105")))
	time.Sleep(ristrettoSettleDelay)

	// Same bucket (tip 108 → bucket 10): hit.
	net.tip = 108
	got, err := c.Get(ctx, cacheReq(t, net, body))
	require.NoError(t, err)
	require.NotNil(t, got, "same bucket must hit")

	// Different bucket (tip 205 → bucket 20): miss.
	net.tip = 205
	got, err = c.Get(ctx, cacheReq(t, net, body))
	require.NoError(t, err)
	require.Nil(t, got, "a new bucket must not see the old bucket's entry")
}

// ---- finality-aware policy matching -----------------------------------------

func cachePolicy(finality common.DataFinalityState) *common.CachePolicyConfig {
	return &common.CachePolicyConfig{
		Connector: "mem",
		Network:   "*",
		Method:    "*",
		Finality:  finality,
	}
}

func TestGenericCache_PolicyMatching_ByMethodFinalityDeclaration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	methods := &common.MethodsConfig{Definitions: map[string]*common.CacheMethodConfig{
		"getHealth":        {Realtime: true},
		"getTransaction":   {Finalized: true},
		"getLatestLedger":  {Unfinalized: true},
		"undeclaredMethod": {},
	}}
	cfg := &common.NetworkConfig{Architecture: common.ArchitectureJsonRpc, Methods: methods}

	cases := []struct {
		name       string
		policy     common.DataFinalityState
		method     string
		wantCached bool
	}{
		{"unknown policy caches undeclared method (v1 traffic)", common.DataFinalityStateUnknown, "undeclaredMethod", true},
		{"unknown policy does not cache finalized-declared method", common.DataFinalityStateUnknown, "getTransaction", false},
		{"finalized policy caches finalized-declared method", common.DataFinalityStateFinalized, "getTransaction", true},
		{"finalized policy does not cache realtime method", common.DataFinalityStateFinalized, "getHealth", false},
		{"realtime policy caches realtime-declared method", common.DataFinalityStateRealtime, "getHealth", true},
		{"realtime policy does not cache undeclared method", common.DataFinalityStateRealtime, "undeclaredMethod", false},
		{"unfinalized policy caches unfinalized-declared method", common.DataFinalityStateUnfinalized, "getLatestLedger", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newTestCache(t, cachePolicy(tc.policy))
			net := newTipNetwork(cfg, 0)
			body := `{"jsonrpc":"2.0","id":1,"method":"` + tc.method + `","params":["x"]}`

			req := cacheReq(t, net, body)
			require.NoError(t, c.Set(ctx, req, cacheResp(req, "v")))
			time.Sleep(ristrettoSettleDelay)

			got, err := c.Get(ctx, cacheReq(t, net, body))
			require.NoError(t, err)
			if tc.wantCached {
				require.NotNil(t, got, "%s should be cached under a %s policy", tc.method, tc.policy)
			} else {
				require.Nil(t, got, "%s must NOT be cached under a %s policy", tc.method, tc.policy)
			}
		})
	}
}

func TestGenericCache_PolicyMatching_FinalityFromParamResolved(t *testing.T) {
	t.Parallel()
	c := newTestCache(t, cachePolicy(common.DataFinalityStateFinalized))
	ctx := context.Background()

	idx := 0
	cfg := &common.NetworkConfig{
		Architecture: common.ArchitectureJsonRpc,
		Methods: &common.MethodsConfig{Definitions: map[string]*common.CacheMethodConfig{
			"starknet_getBlockWithTxs": {
				FinalityFromParam: &common.FinalityFromParamConfig{
					Index:   &idx,
					Map:     map[string]string{"l1_accepted": "finalized", "latest": "realtime"},
					Default: "unfinalized",
				},
			},
		}},
	}
	net := newTipNetwork(cfg, 0)
	body := `{"jsonrpc":"2.0","id":1,"method":"starknet_getBlockWithTxs","params":["l1_accepted"]}`

	req := cacheReq(t, net, body)
	require.NoError(t, c.Set(ctx, req, cacheResp(req, "immutable-block")))
	time.Sleep(ristrettoSettleDelay)

	got, err := c.Get(ctx, cacheReq(t, net, body))
	require.NoError(t, err)
	require.NotNil(t, got, "finalityFromParam must resolve per-request so the finalized policy matches")
}

package erpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/erpc/erpc/architecture/jsonrpc"
	"github.com/erpc/erpc/common"
	"github.com/erpc/erpc/util"
	"github.com/h2non/gock"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

// jsonrpcE2EHarness spins up a real eRPC instance with one jsonrpc network
// ("svc" slug) fronting two fake JSON-RPC upstreams.
type jsonrpcE2EHarness struct {
	baseURL string
	post    func(t *testing.T, method, params string) map[string]any
	cleanup func()
	hits    map[string]*int64
}

func newJsonrpcE2E(t *testing.T, networkMut func(nw *common.NetworkConfig), database *common.DatabaseConfig) *jsonrpcE2EHarness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	cfg := &common.Config{
		Server:       &common.ServerConfig{ListenV4: util.BoolPtr(true)},
		HealthCheck:  &common.HealthCheckConfig{},
		Metrics:      &common.MetricsConfig{},
		RateLimiters: &common.RateLimiterConfig{},
		Projects: []*common.ProjectConfig{{
			Id: "test_project",
			Networks: []*common.NetworkConfig{{
				Architecture: common.ArchitectureJsonRpc,
				Jsonrpc:      &common.JsonrpcNetworkConfig{NetworkId: "svc"},
				Failsafe: []*common.FailsafeConfig{{
					MatchMethod: "*",
					Timeout:     &common.TimeoutPolicyConfig{Duration: &common.AdaptiveDuration{Base: common.Duration(5 * time.Second)}},
					Retry:       &common.RetryPolicyConfig{MaxAttempts: 2},
				}},
			}},
			Upstreams: []*common.UpstreamConfig{
				{Id: "ups-a", Type: common.UpstreamTypeJsonRpc, Jsonrpc: &common.JsonrpcUpstreamConfig{NetworkId: "svc"}, Endpoint: "http://ups-a.localhost", Routing: &common.UpstreamRoutingConfig{Probe: common.ProbeModeOff}},
				{Id: "ups-b", Type: common.UpstreamTypeJsonRpc, Jsonrpc: &common.JsonrpcUpstreamConfig{NetworkId: "svc"}, Endpoint: "http://ups-b.localhost", Routing: &common.UpstreamRoutingConfig{Probe: common.ProbeModeOff}},
			},
		}},
		Database: database,
	}
	if networkMut != nil {
		networkMut(cfg.Projects[0].Networks[0])
	}
	require.NoError(t, cfg.SetDefaults(nil))

	logger := log.Logger
	var jsonRpcCache *jsonrpc.GenericJsonRpcCache
	if database != nil && database.JsonRpcCache != nil {
		var err error
		jsonRpcCache, err = jsonrpc.NewGenericJsonRpcCache(ctx, &logger, database.JsonRpcCache)
		require.NoError(t, err)
	}

	instance, err := NewERPC(ctx, &logger, nil, nil, nil, jsonRpcCache, cfg)
	require.NoError(t, err)
	instance.Bootstrap(ctx)

	httpServer, err := NewHttpServer(ctx, &logger, cfg.Server, cfg.HealthCheck, cfg.Admin, cfg.Indexer, instance)
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = httpServer.serverV4.Serve(listener) }()

	require.Eventually(t, func() bool {
		prj, err := instance.GetProject("test_project")
		if err != nil {
			return false
		}
		return len(prj.upstreamsRegistry.GetAllUpstreams()) == 2
	}, 10*time.Second, 50*time.Millisecond)

	h := &jsonrpcE2EHarness{
		baseURL: fmt.Sprintf("http://%s", listener.Addr().String()),
		hits:    map[string]*int64{},
	}
	h.post = func(t *testing.T, method, params string) map[string]any {
		t.Helper()
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":%s}`, method, params)
		resp, err := http.Post(h.baseURL+"/test_project/jsonrpc/svc", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		return out
	}
	h.cleanup = func() {
		_ = httpServer.serverV4.Shutdown(context.Background())
		cancel()
		util.ResetGock()
	}
	return h
}

// mockUpstream counts hits per upstream id and replies with the given static
// JSON body. The filter guards on hostname: gock evaluates filters broadly
// (an empty Post("") path matches any path), so an unconditional true would
// hijack unrelated requests — including the test client's own calls to the
// local eRPC server.
func mockUpstream(id, host, body string, hits *int64) {
	gock.New("http://" + host).Post("").Persist().
		Filter(func(r *http.Request) bool {
			if r.URL.Hostname() != host {
				return false
			}
			_ = util.SafeReadBody(r)
			atomic.AddInt64(hits, 1)
			return true
		}).
		Reply(200).
		JSON([]byte(body))
}

// repeatUntilCached re-issues an identical request until one is served
// without new upstream traffic (i.e. the async cache write has landed and the
// read hits). Production cache writes are fire-and-forget goroutines, so a
// fixed sleep is inherently racy; polling the observable behavior is not.
func repeatUntilCached(t *testing.T, h *jsonrpcE2EHarness, method, params string, hits ...*int64) map[string]any {
	t.Helper()
	total := func() int64 {
		var n int64
		for _, c := range hits {
			n += atomic.LoadInt64(c)
		}
		return n
	}
	var out map[string]any
	require.Eventually(t, func() bool {
		before := total()
		out = h.post(t, method, params)
		return total() == before
	}, 10*time.Second, 100*time.Millisecond, "repeat request never served from cache")
	return out
}

func TestJsonRpcNetwork_EndToEnd(t *testing.T) {
	util.ResetGock()
	defer util.ResetGock()

	t.Run("proxies and load-balances across upstreams", func(t *testing.T) {
		var a, b int64
		mockUpstream("ups-a", "ups-a.localhost", `{"jsonrpc":"2.0","id":1,"result":"from-a"}`, &a)
		mockUpstream("ups-b", "ups-b.localhost", `{"jsonrpc":"2.0","id":1,"result":"from-b"}`, &b)
		h := newJsonrpcE2E(t, nil, nil)
		defer h.cleanup()

		out := h.post(t, "getThing", `[]`)
		require.Nil(t, out["error"])
		require.Contains(t, []any{"from-a", "from-b"}, out["result"])
		require.Equal(t, int64(1), a+b, "exactly one upstream should serve the request")
	})

	t.Run("unknown error code retried on second upstream", func(t *testing.T) {
		var a, b int64
		mockUpstream("ups-a", "ups-a.localhost", `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"backend blew up"}}`, &a)
		mockUpstream("ups-b", "ups-b.localhost", `{"jsonrpc":"2.0","id":1,"result":"recovered"}`, &b)
		h := newJsonrpcE2E(t, nil, nil)
		defer h.cleanup()

		out := h.post(t, "getThing", `[]`)
		require.Nil(t, out["error"])
		require.Equal(t, "recovered", out["result"])
		require.Equal(t, int64(1), a)
		require.Equal(t, int64(1), b)
	})

	t.Run("spec code -32602 not retried", func(t *testing.T) {
		var a, b int64
		mockUpstream("ups-a", "ups-a.localhost", `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"invalid params"}}`, &a)
		mockUpstream("ups-b", "ups-b.localhost", `{"jsonrpc":"2.0","id":1,"result":"should-not-serve"}`, &b)
		h := newJsonrpcE2E(t, nil, nil)
		defer h.cleanup()

		out := h.post(t, "getThing", `[]`)
		errObj, ok := out["error"].(map[string]any)
		require.True(t, ok, "client error must surface: %v", out)
		require.Equal(t, float64(-32602), errObj["code"])
		require.Equal(t, int64(1), a, "spec-code errors are client-side: no retry")
		require.Equal(t, int64(0), b)
	})

	t.Run("ttl cache with finality unknown dedups repeat requests", func(t *testing.T) {
		var a, b int64
		mockUpstream("ups-a", "ups-a.localhost", `{"jsonrpc":"2.0","id":1,"result":"cached-thing"}`, &a)
		mockUpstream("ups-b", "ups-b.localhost", `{"jsonrpc":"2.0","id":1,"result":"cached-thing"}`, &b)
		h := newJsonrpcE2E(t, nil, &common.DatabaseConfig{
			JsonRpcCache: &common.CacheConfig{
				Connectors: []*common.ConnectorConfig{{Id: "memory", Driver: common.DriverMemory, Memory: &common.MemoryConnectorConfig{MaxItems: 1000, MaxTotalSize: "100MB"}}},
				Policies: []*common.CachePolicyConfig{{
					Network:   "jsonrpc:svc",
					Method:    "getThing",
					Finality:  common.DataFinalityStateUnknown,
					Connector: "memory",
					TTL:       &common.BlockTimeAdaptiveDuration{Fallback: common.Duration(60 * time.Second)},
				}},
			},
		})
		defer h.cleanup()

		out1 := h.post(t, "getThing", `[]`)
		require.Equal(t, "cached-thing", out1["result"])
		out2 := repeatUntilCached(t, h, "getThing", `[]`, &a, &b)
		require.Equal(t, "cached-thing", out2["result"])
	})

	t.Run("result-embedded NOT_FOUND never cached and retried", func(t *testing.T) {
		var a, b int64
		// Stellar-style: NOT_FOUND arrives as a 200 result, not an error.
		mockUpstream("ups-a", "ups-a.localhost", `{"jsonrpc":"2.0","id":1,"result":{"status":"NOT_FOUND","ledger":0,"createdAt":"0"}}`, &a)
		mockUpstream("ups-b", "ups-b.localhost", `{"jsonrpc":"2.0","id":1,"result":{"status":"SUCCESS","ledger":123}}`, &b)
		h := newJsonrpcE2E(t, func(nw *common.NetworkConfig) {
			nw.Methods = &common.MethodsConfig{Definitions: map[string]*common.CacheMethodConfig{
				"getTransaction": {
					Finalized: true, // safe ONLY with the classification below
					ResultClassifications: []*common.JsonRpcClassificationRuleConfig{{
						Match:      &common.JsonRpcClassificationMatchConfig{ResultPath: "$.result.status", In: []interface{}{"NOT_FOUND"}},
						ClassifyAs: "missingData",
					}},
				},
			}}
		}, &common.DatabaseConfig{
			JsonRpcCache: &common.CacheConfig{
				Connectors: []*common.ConnectorConfig{{Id: "memory", Driver: common.DriverMemory, Memory: &common.MemoryConnectorConfig{MaxItems: 1000, MaxTotalSize: "100MB"}}},
				Policies: []*common.CachePolicyConfig{{
					Network:   "jsonrpc:svc",
					Method:    "getTransaction",
					Finality:  common.DataFinalityStateFinalized,
					Connector: "memory",
					TTL:       &common.BlockTimeAdaptiveDuration{Fallback: common.Duration(60 * time.Second)},
				}},
			},
		})
		defer h.cleanup()

		out := h.post(t, "getTransaction", `["some-hash"]`)
		res, ok := out["result"].(map[string]any)
		require.True(t, ok, "expected the SUCCESS result after retry past NOT_FOUND: %v", out)
		require.Equal(t, "SUCCESS", res["status"])
		require.Equal(t, int64(1), a)
		require.Equal(t, int64(1), b)

		// The SUCCESS result is finalized-declared → cached; a repeat request
		// must not hit any upstream again (repeatUntilCached only returns once a
		// repeat is served with zero new upstream traffic).
		out2 := repeatUntilCached(t, h, "getTransaction", `["some-hash"]`, &a, &b)
		res2, ok := out2["result"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "SUCCESS", res2["status"])
	})

	t.Run("finalityFromParam scopes policy by request param", func(t *testing.T) {
		var aFinal, aRealtime, bFinal int64
		// NEAR-style: same method name, finality carried by param.
		gock.New("http://ups-a.localhost").Post("").Persist().
			Filter(func(r *http.Request) bool {
				if r.URL.Hostname() != "ups-a.localhost" {
					return false
				}
				body := util.SafeReadBody(r)
				if strings.Contains(body, `"final"`) {
					atomic.AddInt64(&aFinal, 1)
				} else {
					atomic.AddInt64(&aRealtime, 1)
				}
				return true
			}).
			Reply(200).
			JSON([]byte(`{"jsonrpc":"2.0","id":1,"result":{"header":{"height":100}}}`))
		mockUpstream("ups-b", "ups-b.localhost", `{"jsonrpc":"2.0","id":1,"result":{"header":{"height":100}}}`, &bFinal)
		h := newJsonrpcE2E(t, func(nw *common.NetworkConfig) {
			nw.Methods = &common.MethodsConfig{Definitions: map[string]*common.CacheMethodConfig{
				"block": {FinalityFromParam: &common.FinalityFromParamConfig{
					Key:     "finality",
					Map:     map[string]string{"final": "finalized", "optimistic": "realtime"},
					Default: "realtime",
				}},
			}}
		}, &common.DatabaseConfig{
			JsonRpcCache: &common.CacheConfig{
				Connectors: []*common.ConnectorConfig{{Id: "memory", Driver: common.DriverMemory, Memory: &common.MemoryConnectorConfig{MaxItems: 1000, MaxTotalSize: "100MB"}}},
				Policies: []*common.CachePolicyConfig{{
					Network:   "jsonrpc:svc",
					Method:    "block",
					Finality:  common.DataFinalityStateFinalized,
					Connector: "memory",
					TTL:       &common.BlockTimeAdaptiveDuration{Fallback: common.Duration(60 * time.Second)},
				}},
			},
		})
		defer h.cleanup()

		finalParams := `[{"finality":"final"}]`
		rtParams := `[{"finality":"optimistic"}]`

		// finality=final resolves to the finalized policy → cached.
		h.post(t, "block", finalParams)
		repeatUntilCached(t, h, "block", finalParams, &aFinal, &bFinal)
		require.Equal(t, int64(1), aFinal, "finality=final resolves finalized → exactly one upstream fetch before caching")

		// finality=optimistic resolves realtime → the finalized policy never
		// matches, so every request reaches an upstream.
		before := atomic.LoadInt64(&aRealtime) + atomic.LoadInt64(&bFinal)
		h.post(t, "block", rtParams)
		h.post(t, "block", rtParams)
		after := atomic.LoadInt64(&aRealtime) + atomic.LoadInt64(&bFinal)
		require.Equal(t, before+2, after, "finality=optimistic resolves realtime → finalized policy does not match, no caching")
	})
}

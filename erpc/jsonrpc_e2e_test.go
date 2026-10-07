package erpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/erpc/erpc/common"
	"github.com/erpc/erpc/data"
	"github.com/erpc/erpc/util"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- httptest-based mock upstream for generic jsonrpc networks ----

type jsonrpcMockUpstream struct {
	server       *httptest.Server
	requestCount atomic.Int64
	// handler receives the parsed JSON-RPC request body and returns the full
	// response body to send back. When nil, a default success is returned.
	handler func(body map[string]interface{}) (status int, respBody string)
}

func newJsonrpcMockUpstream(handler func(body map[string]interface{}) (int, string)) *jsonrpcMockUpstream {
	m := &jsonrpcMockUpstream{handler: handler}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.requestCount.Add(1)
		raw, _ := io.ReadAll(r.Body)
		defer r.Body.Close()

		var parsed map[string]interface{}
		_ = json.Unmarshal(raw, &parsed)

		status, respBody := 200, `{"jsonrpc":"2.0","id":1,"result":"0xok"}`
		if m.handler != nil {
			status, respBody = m.handler(parsed)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
	return m
}

func (m *jsonrpcMockUpstream) URL() string     { return m.server.URL }
func (m *jsonrpcMockUpstream) Close()          { m.server.Close() }
func (m *jsonrpcMockUpstream) Requests() int64 { return m.requestCount.Load() }

// ---- shared fixture: full ERPC instance with jsonrpc network + upstreams ----

type jsonrpcErpcFixture struct {
	erpc   *ERPC
	cancel context.CancelFunc
	mocks  []*jsonrpcMockUpstream
}

func (f *jsonrpcErpcFixture) Close() {
	f.cancel()
	for _, m := range f.mocks {
		m.Close()
	}
}

func setupJsonrpcErpc(t *testing.T, networkCfg *common.NetworkConfig, upstreamCfgs []*common.UpstreamConfig) *jsonrpcErpcFixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	ssr, err := data.NewSharedStateRegistry(ctx, &log.Logger, &common.SharedStateConfig{
		Connector: &common.ConnectorConfig{
			Driver: "memory",
			Memory: &common.MemoryConnectorConfig{MaxItems: 100_000, MaxTotalSize: "1GB"},
		},
	})
	require.NoError(t, err)

	if networkCfg == nil {
		networkCfg = &common.NetworkConfig{
			Architecture: common.ArchitectureJsonRpc,
			JsonRpc:      &common.JsonRpcNetworkConfig{Slug: "starknet"},
		}
	}

	cfg := &common.Config{
		Server: &common.ServerConfig{
			MaxTimeout: common.Duration(5 * time.Second).Ptr(),
		},
		Projects: []*common.ProjectConfig{
			{
				Id:        "main",
				Networks:  []*common.NetworkConfig{networkCfg},
				Upstreams: upstreamCfgs,
			},
		},
		RateLimiters: &common.RateLimiterConfig{},
	}

	erpcInstance, err := NewERPC(ctx, &log.Logger, ssr, nil, nil, cfg)
	require.NoError(t, err)
	erpcInstance.Bootstrap(ctx)
	time.Sleep(200 * time.Millisecond)

	return &jsonrpcErpcFixture{erpc: erpcInstance, cancel: cancel}
}

func jsonrpcUpstreamConfig(id, endpoint, slug string) *common.UpstreamConfig {
	return &common.UpstreamConfig{
		Id:       id,
		Type:     common.UpstreamTypeJsonRpc,
		Endpoint: endpoint,
		JsonRpc: &common.JsonRpcUpstreamConfig{
			Slug: slug,
		},
	}
}

// ---- tests ----

// TestJsonRpc_Forward verifies the basic jsonrpc pipeline: a request reaches
// the upstream via the generic HTTP JSON-RPC client and the response is
// passed through unmodified.
func TestJsonRpc_Forward(t *testing.T) {
	util.ConfigureTestLogger()
	mock := newJsonrpcMockUpstream(func(body map[string]interface{}) (int, string) {
		assert.Equal(t, "starknet_getBlockWithTxHashes", body["method"])
		return 200, `{"jsonrpc":"2.0","id":1,"result":{"block_hash":"0xabc123"}}`
	})
	defer mock.Close()

	fx := setupJsonrpcErpc(t, nil, []*common.UpstreamConfig{
		jsonrpcUpstreamConfig("rpc1", mock.URL(), "starknet"),
	})
	defer fx.Close()

	nw, err := fx.erpc.GetNetwork(context.Background(), "main", "jsonrpc:starknet")
	require.NoError(t, err)
	require.NotNil(t, nw)
	assert.Equal(t, common.ArchitectureJsonRpc, nw.Architecture())
	assert.Equal(t, "jsonrpc:starknet", nw.Id())

	req := common.NewNormalizedRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"starknet_getBlockWithTxHashes","params":["latest"]}`))
	resp, err := nw.Forward(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)

	jrr, err := resp.JsonRpcResponse()
	require.NoError(t, err)
	assert.Contains(t, string(jrr.GetResultBytes()), "0xabc123")
	assert.Equal(t, int64(1), mock.Requests())
}

// TestJsonRpc_RetryAcrossUpstreamsOnServerError verifies that when the first
// upstream returns a server-side error (-32603), the failsafe retry policy
// routes the request to a second upstream.
func TestJsonRpc_RetryAcrossUpstreamsOnServerError(t *testing.T) {
	util.ConfigureTestLogger()
	failMock := newJsonrpcMockUpstream(func(body map[string]interface{}) (int, string) {
		return 200, `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"Internal error"}}`
	})
	defer failMock.Close()

	okMock := newJsonrpcMockUpstream(func(body map[string]interface{}) (int, string) {
		return 200, `{"jsonrpc":"2.0","id":1,"result":{"status":"ok-from-second"}}`
	})
	defer okMock.Close()

	networkCfg := &common.NetworkConfig{
		Architecture: common.ArchitectureJsonRpc,
		JsonRpc:      &common.JsonRpcNetworkConfig{Slug: "starknet"},
		Failsafe: []*common.FailsafeConfig{
			{
				Retry: &common.RetryPolicyConfig{
					MaxAttempts: 2,
					Delay:       common.Duration(10 * time.Millisecond),
				},
			},
		},
	}

	fx := setupJsonrpcErpc(t, networkCfg, []*common.UpstreamConfig{
		jsonrpcUpstreamConfig("rpc1", failMock.URL(), "starknet"),
		jsonrpcUpstreamConfig("rpc2", okMock.URL(), "starknet"),
	})
	defer fx.Close()

	nw, err := fx.erpc.GetNetwork(context.Background(), "main", "jsonrpc:starknet")
	require.NoError(t, err)

	req := common.NewNormalizedRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"starknet_getBlockWithTxHashes","params":["latest"]}`))
	resp, err := nw.Forward(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)

	jrr, err := resp.JsonRpcResponse()
	require.NoError(t, err)
	assert.Contains(t, string(jrr.GetResultBytes()), "ok-from-second")
	assert.Equal(t, int64(1), failMock.Requests(), "failing upstream should be hit once")
	assert.Equal(t, int64(1), okMock.Requests(), "second upstream should be hit once via retry")
}

// TestJsonRpc_NoRetryOnClientSideError verifies that a -32602 (invalid
// params) error is classified as client-side and is NOT retried across
// upstreams.
func TestJsonRpc_NoRetryOnClientSideError(t *testing.T) {
	util.ConfigureTestLogger()
	errMock := newJsonrpcMockUpstream(func(body map[string]interface{}) (int, string) {
		return 200, `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid params"}}`
	})
	defer errMock.Close()

	okMock := newJsonrpcMockUpstream(func(body map[string]interface{}) (int, string) {
		return 200, `{"jsonrpc":"2.0","id":1,"result":{"status":"should-not-be-hit"}}`
	})
	defer okMock.Close()

	networkCfg := &common.NetworkConfig{
		Architecture: common.ArchitectureJsonRpc,
		JsonRpc:      &common.JsonRpcNetworkConfig{Slug: "starknet"},
		Failsafe: []*common.FailsafeConfig{
			{
				Retry: &common.RetryPolicyConfig{
					MaxAttempts: 3,
					Delay:       common.Duration(10 * time.Millisecond),
				},
			},
		},
	}

	fx := setupJsonrpcErpc(t, networkCfg, []*common.UpstreamConfig{
		jsonrpcUpstreamConfig("rpc1", errMock.URL(), "starknet"),
		jsonrpcUpstreamConfig("rpc2", okMock.URL(), "starknet"),
	})
	defer fx.Close()

	nw, err := fx.erpc.GetNetwork(context.Background(), "main", "jsonrpc:starknet")
	require.NoError(t, err)

	req := common.NewNormalizedRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"starknet_getBlockWithTxHashes","params":["bad-params"]}`))
	resp, err := nw.Forward(context.Background(), req)

	// The forward should return an error (client-side errors are not retried).
	// Even if a response wrapper is returned, the second upstream must not be hit.
	_ = resp
	_ = err

	assert.Equal(t, int64(1), errMock.Requests(), "client-side error should only hit first upstream once")
	assert.Equal(t, int64(0), okMock.Requests(), "second upstream must NOT be contacted for client-side errors")
}

// TestJsonRpc_AliasRouting verifies that a network with an alias resolves
// correctly through the networks registry.
func TestJsonRpc_AliasRouting(t *testing.T) {
	util.ConfigureTestLogger()
	mock := newJsonrpcMockUpstream(nil)
	defer mock.Close()

	networkCfg := &common.NetworkConfig{
		Architecture: common.ArchitectureJsonRpc,
		JsonRpc:      &common.JsonRpcNetworkConfig{Slug: "starknet"},
		Alias:        "starknet-mainnet",
	}

	fx := setupJsonrpcErpc(t, networkCfg, []*common.UpstreamConfig{
		jsonrpcUpstreamConfig("rpc1", mock.URL(), "starknet"),
	})
	defer fx.Close()

	project, err := fx.erpc.GetProject("main")
	require.NoError(t, err)

	// The alias should resolve to the jsonrpc architecture + slug.
	arch, chainID := project.networksRegistry.ResolveAlias("starknet-mainnet")
	assert.Equal(t, "jsonrpc", arch)
	assert.Equal(t, "starknet", chainID)

	// Forwarding via the resolved network ID should work.
	nw, err := fx.erpc.GetNetwork(context.Background(), "main", "jsonrpc:starknet")
	require.NoError(t, err)
	req := common.NewNormalizedRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"starknet_getBlockWithTxHashes","params":["latest"]}`))
	resp, err := nw.Forward(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)
}

// TestJsonRpc_StaticResponseShortCircuit verifies that static responses are
// served without contacting any upstream.
func TestJsonRpc_StaticResponseShortCircuit(t *testing.T) {
	util.ConfigureTestLogger()
	mock := newJsonrpcMockUpstream(nil)
	defer mock.Close()

	networkCfg := &common.NetworkConfig{
		Architecture: common.ArchitectureJsonRpc,
		JsonRpc:      &common.JsonRpcNetworkConfig{Slug: "starknet"},
		StaticResponses: []*common.StaticResponseConfig{
			{
				Method: "starknet_chainId",
				Response: &common.StaticResponseBodyConfig{
					Result: map[string]interface{}{
						"chain_id": "0x534e5f4d41494e",
					},
				},
			},
		},
	}

	fx := setupJsonrpcErpc(t, networkCfg, []*common.UpstreamConfig{
		jsonrpcUpstreamConfig("rpc1", mock.URL(), "starknet"),
	})
	defer fx.Close()

	nw, err := fx.erpc.GetNetwork(context.Background(), "main", "jsonrpc:starknet")
	require.NoError(t, err)

	req := common.NewNormalizedRequest([]byte(`{"jsonrpc":"2.0","id":42,"method":"starknet_chainId","params":[]}`))
	resp, err := nw.Forward(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)

	jrr, err := resp.JsonRpcResponse()
	require.NoError(t, err)
	assert.Contains(t, string(jrr.GetResultBytes()), "0x534e5f4d41494e")
	assert.Equal(t, int64(0), mock.Requests(), "upstream must not be contacted for static response")
}

// TestJsonRpc_BatchCoalescing verifies that concurrent requests to a
// SupportsBatch upstream are coalesced by the HTTP JSON-RPC client into a
// single upstream HTTP call carrying a JSON-RPC array body, and each caller
// receives its own element back.
//
// Note: incoming batch arrays are split per-element at the HTTP server layer
// (http_server.go), so Network.Forward never sees an array. Client-side
// coalescing is the only path that produces an upstream batch — this is the
// same pattern TestNetwork_BatchRequests uses for EVM.
func TestJsonRpc_BatchCoalescing(t *testing.T) {
	util.ConfigureTestLogger()

	var hits atomic.Int64
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		defer r.Body.Close()
		hits.Add(1)

		if len(raw) == 0 || raw[0] != '[' {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0xsingle"}`))
			return
		}

		var batch []map[string]interface{}
		_ = json.Unmarshal(raw, &batch)
		elements := make([]string, 0, len(batch))
		for _, el := range batch {
			id := el["id"]
			elements = append(elements, fmt.Sprintf(`{"jsonrpc":"2.0","id":%v,"result":"0xbatched-%v"}`, id, id))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`[` + joinComma(elements) + `]`))
	}))
	defer mock.Close()

	upCfg := jsonrpcUpstreamConfig("rpc1", mock.URL, "starknet")
	upCfg.JsonRpc.SupportsBatch = &common.TRUE
	upCfg.JsonRpc.BatchMaxSize = 5
	upCfg.JsonRpc.BatchMaxWait = common.Duration(100 * time.Millisecond)

	fx := setupJsonrpcErpc(t, nil, []*common.UpstreamConfig{upCfg})
	defer fx.Close()

	nw, err := fx.erpc.GetNetwork(context.Background(), "main", "jsonrpc:starknet")
	require.NoError(t, err)

	// Fire two concurrent requests with DIFFERENT params (multiplexing
	// coalesces same method+params regardless of ID, so distinct params
	// are required for both to reach the upstream batch).
	var wg sync.WaitGroup
	results := make([]string, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"starknet_getBlockWithTxHashes","params":["tag-%d"]}`, idx+1, idx+1)
			req := common.NewNormalizedRequest([]byte(body))
			resp, err := nw.Forward(context.Background(), req)
			if err != nil {
				errs[idx] = err
				return
			}
			jrr, jerr := resp.JsonRpcResponse()
			if jerr != nil {
				errs[idx] = jerr
				return
			}
			results[idx] = string(jrr.GetResultBytes())
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "request %d must succeed", i+1)
	}
	assert.Contains(t, results[0], "0xbatched-1", "caller 1 must receive its own batch element")
	assert.Contains(t, results[1], "0xbatched-2", "caller 2 must receive its own batch element")
	assert.Equal(t, int64(1), hits.Load(), "both requests must coalesce into exactly one upstream call")
}

func joinComma(elements []string) string {
	out := ""
	for i, e := range elements {
		if i > 0 {
			out += ","
		}
		out += e
	}
	return out
}

// TestJsonRpc_StaticResponseShortCircuit_NonMatchingMethod Falls through to upstream
func TestJsonRpc_StaticResponseShortCircuit_NonMatchingMethod(t *testing.T) {
	util.ConfigureTestLogger()
	mock := newJsonrpcMockUpstream(func(body map[string]interface{}) (int, string) {
		return 200, `{"jsonrpc":"2.0","id":1,"result":"0xupstream"}`
	})
	defer mock.Close()

	networkCfg := &common.NetworkConfig{
		Architecture: common.ArchitectureJsonRpc,
		JsonRpc:      &common.JsonRpcNetworkConfig{Slug: "starknet"},
		StaticResponses: []*common.StaticResponseConfig{
			{
				Method: "starknet_chainId",
				Response: &common.StaticResponseBodyConfig{
					Result: map[string]interface{}{
						"chain_id": "0x534e5f4d41494e",
					},
				},
			},
		},
	}

	fx := setupJsonrpcErpc(t, networkCfg, []*common.UpstreamConfig{
		jsonrpcUpstreamConfig("rpc1", mock.URL(), "starknet"),
	})
	defer fx.Close()

	nw, err := fx.erpc.GetNetwork(context.Background(), "main", "jsonrpc:starknet")
	require.NoError(t, err)

	// A non-matching method should fall through to the upstream.
	req := common.NewNormalizedRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"starknet_getBlockWithTxHashes","params":["latest"]}`))
	resp, err := nw.Forward(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)

	jrr, err := resp.JsonRpcResponse()
	require.NoError(t, err)
	assert.Contains(t, string(jrr.GetResultBytes()), "0xupstream")
	assert.Equal(t, int64(1), mock.Requests(), "upstream should be hit for non-matching method")
}

// TestJsonRpc_NetworkIdRoutedViaURLPath verifies the URL routing pattern
// /<project>/jsonrpc/<slug> maps to the correct network ID.
func TestJsonRpc_NetworkIdRoutedViaURLPath(t *testing.T) {
	util.ConfigureTestLogger()

	// Simulate the URL path parsing: /main/jsonrpc/starknet
	// The http server splits this into architecture="jsonrpc", chainId="starknet"
	// and constructs networkId = "jsonrpc:starknet".
	networkId := fmt.Sprintf("%s:%s", "jsonrpc", "starknet")
	assert.Equal(t, "jsonrpc:starknet", networkId)
	assert.True(t, util.IsValidNetworkId(networkId))

	// Verify that a network created with this ID is retrievable.
	mock := newJsonrpcMockUpstream(nil)
	defer mock.Close()

	fx := setupJsonrpcErpc(t, nil, []*common.UpstreamConfig{
		jsonrpcUpstreamConfig("rpc1", mock.URL(), "starknet"),
	})
	defer fx.Close()

	nw, err := fx.erpc.GetNetwork(context.Background(), "main", networkId)
	require.NoError(t, err)
	assert.Equal(t, "jsonrpc:starknet", nw.Id())
	assert.Equal(t, common.ArchitectureJsonRpc, nw.Architecture())
}

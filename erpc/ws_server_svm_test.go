package erpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/erpc/erpc/common"
	"github.com/erpc/erpc/util"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

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
				mockWriteJSON(conn, map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": "5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2N9d"})
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

func svmWsConfig(endpoint string, clientServerEnabled bool) *common.Config {
	return &common.Config{
		Server: &common.ServerConfig{
			ListenV4:  util.BoolPtr(true),
			WebSocket: &common.WebSocketServerConfig{Enabled: util.BoolPtr(clientServerEnabled)},
		},
		Projects: []*common.ProjectConfig{
			{
				Id: "test_ws",
				Networks: []*common.NetworkConfig{
					{
						Architecture: common.ArchitectureSvm,
						Svm:          &common.SvmNetworkConfig{Cluster: "mainnet-beta", Commitment: "confirmed"},
					},
				},
				Upstreams: []*common.UpstreamConfig{
					{
						Id:       "svm-ws",
						Type:     common.UpstreamTypeSvm,
						Endpoint: endpoint,
						Svm:      &common.SvmUpstreamConfig{Cluster: "mainnet-beta", CheckGenesisHash: false},
					},
				},
			},
		},
		RateLimiters: &common.RateLimiterConfig{},
	}
}

func dialSvmWs(t *testing.T, addr string) *websocket.Conn {
	t.Helper()
	wsURL := fmt.Sprintf("ws://%s/test_ws/svm/mainnet-beta", addr)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err, "SVM WebSocket dial should succeed")
	return conn
}

func TestWebSocket_SvmUnary(t *testing.T) {
	upstream := mockSvmWsUpstream(t, nil)
	defer upstream.Close()

	t.Run("http", func(t *testing.T) {
		cfg := svmWsConfig("ws"+strings.TrimPrefix(upstream.URL, "http"), false)
		addr, cleanup := setupTestERPCServer(t, cfg)
		defer cleanup()

		body := []byte(`{"jsonrpc":"2.0","id":1,"method":"getSlot","params":[]}`)
		resp, err := http.Post("http://"+addr+"/test_ws/svm/mainnet-beta", "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		out, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		var result map[string]interface{}
		require.NoError(t, json.Unmarshal(out, &result))
		require.Equal(t, float64(1), result["result"])
	})

	t.Run("websocket", func(t *testing.T) {
		cfg := svmWsConfig("ws"+strings.TrimPrefix(upstream.URL, "http"), true)
		addr, cleanup := setupTestERPCServer(t, cfg)
		defer cleanup()

		conn := dialSvmWs(t, addr)
		defer conn.Close()

		got := sendAndReceive(t, conn, `{"jsonrpc":"2.0","id":"unary-1","method":"getSlot","params":[]}`)
		require.Equal(t, "unary-1", got["id"])
		require.Equal(t, float64(1), got["result"])
	})
}

var svmSubscriptionCases = []struct {
	family, subscribe, unsubscribe, notification string
	params, result                               string
}{
	{"account", "accountSubscribe", "accountUnsubscribe", "accountNotification", `["11111111111111111111111111111111",{"encoding":"base64"}]`, `{"context":{"slot":1},"value":{"lamports":9007199254740993}}`},
	{"program", "programSubscribe", "programUnsubscribe", "programNotification", `["11111111111111111111111111111111",{"filters":[]}]`, `{"context":{"slot":1},"value":{"pubkey":"11111111111111111111111111111111","account":{}}}`},
	{"logs", "logsSubscribe", "logsUnsubscribe", "logsNotification", `["all"]`, `{"context":{"slot":1},"value":{"signature":"sig","err":null,"logs":[]}}`},
	{"signature", "signatureSubscribe", "signatureUnsubscribe", "signatureNotification", `["sig",{"enableReceivedNotification":false}]`, `{"context":{"slot":1},"value":"receivedSignature"}`},
	{"block", "blockSubscribe", "blockUnsubscribe", "blockNotification", `["all",{"transactionDetails":"none"}]`, `{"context":{"slot":1},"value":{"slot":1,"block":null,"err":null}}`},
	{"slot", "slotSubscribe", "slotUnsubscribe", "slotNotification", `[]`, `{"slot":1,"parent":0,"root":0}`},
	{"slotsUpdates", "slotsUpdatesSubscribe", "slotsUpdatesUnsubscribe", "slotsUpdatesNotification", `[]`, `{"type":"frozen","slot":1,"timestamp":2}`},
	{"root", "rootSubscribe", "rootUnsubscribe", "rootNotification", `[]`, `18446744073709551615`},
	{"vote", "voteSubscribe", "voteUnsubscribe", "voteNotification", `[]`, `{"votePubkey":"key","slots":[1],"hash":"hash","timestamp":null,"signature":"sig"}`},
}

func TestWebSocket_SvmSubscription(t *testing.T) {
	for _, tc := range svmSubscriptionCases {
		t.Run(tc.family, func(t *testing.T) {
			upstream := mockSvmWsUpstream(t, func(conn *websocket.Conn, req svmMockRequest) {
				switch req.Method {
				case tc.subscribe:
					mockWriteJSON(conn, map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": 42})
					mockWriteJSON(conn, map[string]interface{}{
						"jsonrpc": "2.0",
						"method":  tc.notification,
						"params":  map[string]interface{}{"subscription": 42, "result": json.RawMessage(tc.result)},
					})
				case tc.unsubscribe:
					mockWriteJSON(conn, map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": true})
				default:
					mockWriteJSON(conn, map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": nil})
				}
			})
			defer upstream.Close()

			cfg := svmWsConfig("ws"+strings.TrimPrefix(upstream.URL, "http"), true)
			addr, cleanup := setupTestERPCServer(t, cfg)
			defer cleanup()

			conn := dialSvmWs(t, addr)
			defer conn.Close()

			subReq := fmt.Sprintf(`{"jsonrpc":"2.0","id":"sub-1","method":"%s","params":%s}`, tc.subscribe, tc.params)
			subResp := sendAndReceive(t, conn, subReq)
			require.Equal(t, "sub-1", subResp["id"])
			require.Equal(t, float64(0), subResp["result"])

			_, raw, err := conn.ReadMessage()
			require.NoError(t, err)
			var notif map[string]interface{}
			require.NoError(t, json.Unmarshal(raw, &notif))
			require.Equal(t, tc.notification, notif["method"])
			params := notif["params"].(map[string]interface{})
			require.Equal(t, float64(0), params["subscription"])

			unsubReq := fmt.Sprintf(`{"jsonrpc":"2.0","id":"sub-2","method":"%s","params":[0]}`, tc.unsubscribe)
			unsubResp := sendAndReceive(t, conn, unsubReq)
			require.Equal(t, "sub-2", unsubResp["id"])
			require.Equal(t, true, unsubResp["result"])
		})
	}
}

func TestWebSocket_SvmDuplicateUpstreamIDFanout(t *testing.T) {
	var subscribeCount atomic.Int32
	var unsubscribeCount atomic.Int32
	upstreamConns := make(chan *websocket.Conn, 1)
	upstream := mockSvmWsUpstream(t, func(conn *websocket.Conn, req svmMockRequest) {
		switch req.Method {
		case "accountSubscribe":
			subscribeCount.Add(1)
			select {
			case upstreamConns <- conn:
			default:
			}
			mockWriteJSON(conn, map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": 42})
		case "accountUnsubscribe":
			unsubscribeCount.Add(1)
			mockWriteJSON(conn, map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": true})
		default:
			mockWriteJSON(conn, map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": nil})
		}
	})
	defer upstream.Close()

	cfg := svmWsConfig("ws"+strings.TrimPrefix(upstream.URL, "http"), true)
	addr, cleanup := setupTestERPCServer(t, cfg)
	defer cleanup()

	conn := dialSvmWs(t, addr)
	defer conn.Close()

	subReq := `{"jsonrpc":"2.0","id":"sub-1","method":"accountSubscribe","params":["11111111111111111111111111111111",{"encoding":"base64"}]}`
	subResp1 := sendAndReceive(t, conn, subReq)
	require.Equal(t, "sub-1", subResp1["id"])
	require.Equal(t, float64(0), subResp1["result"])

	subResp2 := sendAndReceive(t, conn, subReq)
	require.Equal(t, "sub-1", subResp2["id"])
	require.Equal(t, float64(1), subResp2["result"])

	require.EqualValues(t, 2, subscribeCount.Load())

	var upConn *websocket.Conn
	select {
	case upConn = <-upstreamConns:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream connection not captured")
	}
	require.NoError(t, mockWriteJSON(upConn, map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "accountNotification",
		"params":  map[string]interface{}{"subscription": 42, "result": map[string]interface{}{"context": map[string]interface{}{"slot": 1}, "value": map[string]interface{}{"lamports": 9007199254740993}}},
	}))

	for i := 0; i < 2; i++ {
		_, raw, err := conn.ReadMessage()
		require.NoError(t, err)
		var notif map[string]interface{}
		require.NoError(t, json.Unmarshal(raw, &notif))
		require.Equal(t, "accountNotification", notif["method"])
	}

	unsubReq := `{"jsonrpc":"2.0","id":"sub-2","method":"accountUnsubscribe","params":[0]}`
	unsubResp1 := sendAndReceive(t, conn, unsubReq)
	require.Equal(t, "sub-2", unsubResp1["id"])
	require.Equal(t, true, unsubResp1["result"])
	require.EqualValues(t, 0, unsubscribeCount.Load())

	unsubReq2 := `{"jsonrpc":"2.0","id":"sub-3","method":"accountUnsubscribe","params":[1]}`
	unsubResp2 := sendAndReceive(t, conn, unsubReq2)
	require.Equal(t, "sub-3", unsubResp2["id"])
	require.Equal(t, true, unsubResp2["result"])
	require.EqualValues(t, 1, unsubscribeCount.Load())
}

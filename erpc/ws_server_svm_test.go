package erpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

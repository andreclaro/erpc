package erpc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/erpc/erpc/telemetry"
	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestWebSocketSvmNotificationMetrics(t *testing.T) {
	upstream := mockSvmWsUpstream(t, func(conn *websocket.Conn, req svmMockRequest) {
		if req.Method == "accountSubscribe" {
			mockWriteJSON(conn, map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": 42})
			mockWriteJSON(conn, map[string]interface{}{
				"jsonrpc": "2.0",
				"method":  "accountNotification",
				"params":  map[string]interface{}{"subscription": 42, "result": map[string]interface{}{"context": map[string]interface{}{"slot": 1}, "value": map[string]interface{}{"lamports": 9007199254740993}}},
			})
		}
	})
	defer upstream.Close()

	cfg := svmWsConfig("ws"+strings.TrimPrefix(upstream.URL, "http"), true)
	addr, cleanup := setupTestERPCServer(t, cfg)
	defer cleanup()

	conn := dialSvmWs(t, addr)
	defer conn.Close()

	downLabels := []string{"test_ws", "svm:mainnet-beta", "account"}
	upLabels := []string{"test_ws", "svm:mainnet-beta", "svm-ws", "account"}

	beforeSent := testutil.ToFloat64(telemetry.MetricWebsocketNotificationsSentTotal.WithLabelValues(downLabels...))
	beforeBytes := testutil.ToFloat64(telemetry.MetricWebsocketNotificationBytesTotal.WithLabelValues(downLabels...))
	beforeEvents := testutil.ToFloat64(telemetry.MetricUpstreamSubscriptionEventsTotal.WithLabelValues(upLabels...))
	beforeEventBytes := testutil.ToFloat64(telemetry.MetricUpstreamSubscriptionEventBytesTotal.WithLabelValues(upLabels...))
	beforeActive := testutil.ToFloat64(telemetry.MetricUpstreamSubscriptionsActive.WithLabelValues(upLabels...))

	subReq := `{"jsonrpc":"2.0","id":"sub-1","method":"accountSubscribe","params":["11111111111111111111111111111111",{"encoding":"base64"}]}`
	subResp := sendAndReceive(t, conn, subReq)
	require.Equal(t, "sub-1", subResp["id"])

	_, raw, err := conn.ReadMessage()
	require.NoError(t, err)
	var notif map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &notif))

	require.Equal(t, beforeSent+1, testutil.ToFloat64(telemetry.MetricWebsocketNotificationsSentTotal.WithLabelValues(downLabels...)))
	require.Greater(t, testutil.ToFloat64(telemetry.MetricWebsocketNotificationBytesTotal.WithLabelValues(downLabels...)), beforeBytes)
	require.Equal(t, beforeEvents+1, testutil.ToFloat64(telemetry.MetricUpstreamSubscriptionEventsTotal.WithLabelValues(upLabels...)))
	require.Greater(t, testutil.ToFloat64(telemetry.MetricUpstreamSubscriptionEventBytesTotal.WithLabelValues(upLabels...)), beforeEventBytes)
	require.Equal(t, beforeActive+1, testutil.ToFloat64(telemetry.MetricUpstreamSubscriptionsActive.WithLabelValues(upLabels...)))
}

package jsonrpc

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/erpc/erpc/common"
	"github.com/erpc/erpc/data"
	"github.com/erpc/erpc/health"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

// ---- scripted upstream ------------------------------------------------------

type scriptedResponse struct {
	result []byte // raw JSON-RPC result member bytes
	errJr  *common.ErrJsonRpcExceptionExternal
}

// scriptedUpstream returns canned responses keyed by request method and
// records every call's params (the thenCall test asserts the resolve value is
// passed through as the sole param).
type scriptedUpstream struct {
	id string
	mu sync.Mutex
	// responses keyed by method name
	responses map[string]scriptedResponse
	calls     map[string][][]interface{}
	cordons   []string
	uncordons []string
}

func newScriptedUpstream() *scriptedUpstream {
	return &scriptedUpstream{
		id:        "scripted",
		responses: map[string]scriptedResponse{},
		calls:     map[string][][]interface{}{},
	}
}

func (s *scriptedUpstream) scriptResult(method string, result []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responses[method] = scriptedResponse{result: result}
}

func (s *scriptedUpstream) scriptError(method string, code int, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responses[method] = scriptedResponse{errJr: common.NewErrJsonRpcExceptionExternal(code, msg, "")}
}

func (s *scriptedUpstream) scriptMissing(method string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.responses, method)
}

func (s *scriptedUpstream) Forward(_ context.Context, req *common.NormalizedRequest, _, _ bool) (*common.NormalizedResponse, error) {
	var probe struct {
		Method string        `json:"method"`
		Params []interface{} `json:"params"`
	}
	if err := json.Unmarshal(req.Body(), &probe); err != nil {
		return nil, fmt.Errorf("scriptedUpstream: unparsable probe body: %w", err)
	}

	s.mu.Lock()
	s.calls[probe.Method] = append(s.calls[probe.Method], probe.Params)
	scripted, ok := s.responses[probe.Method]
	s.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("scriptedUpstream: no scripted response for %q", probe.Method)
	}
	var jrr *common.JsonRpcResponse
	var err error
	if scripted.errJr != nil {
		jrr, err = common.NewJsonRpcResponse(1, nil, scripted.errJr)
	} else {
		jrr, err = common.NewJsonRpcResponseFromBytes(nil, scripted.result, nil)
	}
	if err != nil {
		return nil, err
	}
	return common.NewNormalizedResponse().WithRequest(req).WithJsonRpcResponse(jrr), nil
}

func (s *scriptedUpstream) callParams(method string) [][]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]interface{}(nil), s.calls[method]...)
}

func (s *scriptedUpstream) cordonReasons() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.cordons...)
}

func (s *scriptedUpstream) uncordonReasons() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.uncordons...)
}

func (s *scriptedUpstream) Id() string           { return s.id }
func (s *scriptedUpstream) VendorName() string   { return "" }
func (s *scriptedUpstream) NetworkId() string    { return "jsonrpc:test" }
func (s *scriptedUpstream) NetworkLabel() string { return "" }
func (s *scriptedUpstream) Config() *common.UpstreamConfig {
	return &common.UpstreamConfig{Id: s.id, Type: common.UpstreamTypeJsonRpc, Endpoint: "http://x"}
}
func (s *scriptedUpstream) Logger() *zerolog.Logger       { l := zerolog.Nop(); return &l }
func (s *scriptedUpstream) Vendor() common.Vendor         { return nil }
func (s *scriptedUpstream) Tracker() common.HealthTracker { return nil }
func (s *scriptedUpstream) ShouldHandleMethod(string) (bool, error) {
	return true, nil
}
func (s *scriptedUpstream) Cordon(_, reason string) {
	s.mu.Lock()
	s.cordons = append(s.cordons, reason)
	s.mu.Unlock()
}
func (s *scriptedUpstream) Uncordon(_, reason string) {
	s.mu.Lock()
	s.uncordons = append(s.uncordons, reason)
	s.mu.Unlock()
}
func (s *scriptedUpstream) IgnoreMethod(string) {}

// ---- poller construction (no background loop: tests drive Poll directly) ----

func newTestPoller(t *testing.T, up common.Upstream) *JsonRpcStatePoller {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := &common.SharedStateConfig{
		Connector: &common.ConnectorConfig{
			Driver: common.DriverMemory,
			Memory: &common.MemoryConnectorConfig{MaxItems: 1000, MaxTotalSize: "1MB"},
		},
		LockMaxWait:     common.Duration(50 * time.Millisecond),
		UpdateMaxWait:   common.Duration(50 * time.Millisecond),
		FallbackTimeout: common.Duration(1 * time.Second),
		LockTtl:         common.Duration(2 * time.Second),
	}
	cfg.SetDefaults("test")
	ssr, err := data.NewSharedStateRegistry(ctx, &log.Logger, cfg)
	require.NoError(t, err)

	return NewJsonRpcStatePoller(
		"test", ctx, &log.Logger, up,
		health.NewTracker(&log.Logger, "test", time.Minute),
		ssr,
	)
}

func tipOnlyCfg(tip *common.JsonRpcStateProbeConfig) *common.JsonRpcStatePollerConfig {
	return &common.JsonRpcStatePollerConfig{Tip: tip}
}

// ---- encoding parsing (unit table + Poll-level skip behavior) ---------------

func TestParseCounterValue_EncodingTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		raw      interface{}
		encoding string
		want     int64
		wantErr  bool
	}{
		{"number: json number", float64(12345), "number", 12345, false},
		{"number rejects strings", "12345", "number", 0, true},
		{"string: decimal digits", "12345", "string", 12345, false},
		{"string rejects hex", "0x2a2b", "string", 0, true},
		{"string rejects numbers", float64(123), "string", 0, true},
		{"hex: 0x-prefixed", "0x2a2b", "hex", 0x2a2b, false},
		{"hex: 0X-prefixed", "0X2A2B", "hex", 0x2a2b, false},
		{"hex rejects plain decimal", "12345", "hex", 0, true},
		{"auto: json number", float64(12345), "", 12345, false},
		{"auto: explicit", float64(12345), "auto", 12345, false},
		{"auto: decimal string", "12345", "auto", 12345, false},
		{"auto: hex string", "0x2a2b", "auto", 0x2a2b, false},
		{"auto: junk string", "not-a-number", "auto", 0, true},
		{"auto: empty string", "", "auto", 0, true},
		{"auto: bool", true, "auto", 0, true},
		{"auto: nil", nil, "auto", 0, true},
		{"unknown encoding degrades to auto", "12345", "weird", 12345, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseCounterValue(tc.raw, tc.encoding)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestParseRangeStart_Table(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"32570-8812345", 32570, true},
		{"32570-8812345,99000000-100000000", 32570, true}, // disjoint ranges: first start wins
		{"empty", 0, false},                               // unsynced XRPL node
		{"a-b,c-d", 0, false},                             // non-numeric start is unknown, never 0
		{"", 0, false},
		{"garbage", 0, false},
		{"-5-10", 0, false}, // leading dash is not a range start
	}
	for _, tc := range cases {
		got, ok := parseRangeStart(tc.in)
		require.Equal(t, tc.ok, ok, "parseRangeStart(%q) ok", tc.in)
		require.Equal(t, tc.want, got, "parseRangeStart(%q) value", tc.in)
	}
}

func TestPoller_Poll_UpdatesTipAndFinalized(t *testing.T) {
	t.Parallel()
	up := newScriptedUpstream()
	up.scriptResult("status", []byte(`{"sync_info":{"latest_block_height":12345}}`))
	p := newTestPoller(t, up)
	require.NoError(t, p.Configure(tipOnlyCfg(&common.JsonRpcStateProbeConfig{
		Method: "status", Path: "$.result.sync_info.latest_block_height",
	})))

	require.NoError(t, p.Poll(context.Background()))
	require.Equal(t, int64(12345), p.LatestTip())
	require.True(t, p.IsHealthy())
	require.Empty(t, up.cordonReasons())
}

func TestPoller_Poll_UnparseableTipValue_SkipsTickAndCordons(t *testing.T) {
	t.Parallel()
	up := newScriptedUpstream()
	up.scriptResult("status", []byte(`{"sync_info":{"latest_block_height":"not-a-number"}}`))
	p := newTestPoller(t, up)
	require.NoError(t, p.Configure(tipOnlyCfg(&common.JsonRpcStateProbeConfig{
		Method: "status", Path: "$.result.sync_info.latest_block_height",
	})))

	require.NoError(t, p.Poll(context.Background()))
	require.Equal(t, int64(0), p.LatestTip(), "unparseable sample must be a skipped tick, not a stored value")
	require.False(t, p.IsHealthy(), "tip parse failure with no other signal is an unhealthy tick")
	require.Len(t, up.cordonReasons(), 1, "unhealthy tick must cordon the upstream")

	// Recovery: next tick parses again → uncordoned, edge-triggered.
	up.scriptResult("status", []byte(`{"sync_info":{"latest_block_height":12346}}`))
	require.NoError(t, p.Poll(context.Background()))
	require.Equal(t, int64(12346), p.LatestTip())
	require.True(t, p.IsHealthy())
	require.Len(t, up.cordonReasons(), 1, "cordon must be edge-triggered, not re-issued per tick")
	require.Len(t, up.uncordonReasons(), 1)
}

func TestPoller_Poll_ProbeTransportError_Cordons(t *testing.T) {
	t.Parallel()
	up := newScriptedUpstream()
	// No scripted response: the probe forward errors.
	p := newTestPoller(t, up)
	require.NoError(t, p.Configure(tipOnlyCfg(&common.JsonRpcStateProbeConfig{
		Method: "status", Path: "$.result.sync_info.latest_block_height",
	})))

	require.NoError(t, p.Poll(context.Background()))
	require.False(t, p.IsHealthy())
	require.Len(t, up.cordonReasons(), 1)
}

func TestPoller_Poll_ProbeJsonRpcError_Cordons(t *testing.T) {
	t.Parallel()
	up := newScriptedUpstream()
	up.scriptError("status", -32603, "internal")
	p := newTestPoller(t, up)
	require.NoError(t, p.Configure(tipOnlyCfg(&common.JsonRpcStateProbeConfig{
		Method: "status", Path: "$.result.sync_info.latest_block_height",
	})))

	require.NoError(t, p.Poll(context.Background()))
	require.False(t, p.IsHealthy(), "a JSON-RPC error from the tip probe is a failed probe")
	require.Len(t, up.cordonReasons(), 1)
}

func TestPoller_Poll_WithoutConfigure_NeverPolls(t *testing.T) {
	t.Parallel()
	up := newScriptedUpstream()
	p := newTestPoller(t, up)
	require.NoError(t, p.Poll(context.Background()))
	require.Empty(t, up.callParams("status"), "no probe config → no probe calls (v1 no-poller property)")
	require.True(t, p.IsHealthy())
	require.False(t, p.IsObjectNull())
}

// ---- encodings over the wire ------------------------------------------------

func TestPoller_Poll_EncodingsOverWire(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		result   string
		encoding string
		want     int64
	}{
		{"starknet plain number", `12345`, "", 12345},
		{"cometbft decimal string", `"12345"`, "string", 12345},
		{"substrate hex string", `"0x2a2b"`, "hex", 0x2a2b},
		{"auto across number", `12345`, "auto", 12345},
		{"auto across decimal string", `"12345"`, "auto", 12345},
		{"auto across hex string", `"0x2a2b"`, "auto", 0x2a2b},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			up := newScriptedUpstream()
			up.scriptResult("tip", []byte(tc.result))
			p := newTestPoller(t, up)
			require.NoError(t, p.Configure(tipOnlyCfg(&common.JsonRpcStateProbeConfig{
				Method: "tip", Path: "$.result", Encoding: tc.encoding,
			})))
			require.NoError(t, p.Poll(context.Background()))
			require.Equal(t, tc.want, p.LatestTip())
			require.True(t, p.IsHealthy())
		})
	}
}

// ---- floor ------------------------------------------------------------------

func TestPoller_Poll_XrplFloor_RangeStart(t *testing.T) {
	t.Parallel()
	up := newScriptedUpstream()
	up.scriptResult("server_info", []byte(`{"info":{"complete_ledgers":"32570-8812345","validated_ledger":{"seq":8812345}}}`))
	p := newTestPoller(t, up)
	require.NoError(t, p.Configure(&common.JsonRpcStatePollerConfig{
		Tip:   &common.JsonRpcStateProbeConfig{Method: "server_info", Path: "$.result.info.validated_ledger.seq"},
		Floor: &common.JsonRpcStateProbeConfig{Method: "server_info", Path: "$.result.info.complete_ledgers", RangeStart: true},
	}))

	require.NoError(t, p.Poll(context.Background()))
	require.Equal(t, int64(8812345), p.LatestTip())
	require.Equal(t, int64(32570), p.floorTip.Load())
	require.True(t, p.IsHealthy())
}

func TestPoller_Poll_FloorEmpty_UnknownNotZero(t *testing.T) {
	t.Parallel()
	up := newScriptedUpstream()
	up.scriptResult("server_info", []byte(`{"info":{"complete_ledgers":"empty","validated_ledger":{"seq":8812345}}}`))
	p := newTestPoller(t, up)
	require.NoError(t, p.Configure(&common.JsonRpcStatePollerConfig{
		Tip:   &common.JsonRpcStateProbeConfig{Method: "server_info", Path: "$.result.info.validated_ledger.seq"},
		Floor: &common.JsonRpcStateProbeConfig{Method: "server_info", Path: "$.result.info.complete_ledgers", RangeStart: true},
	}))

	require.NoError(t, p.Poll(context.Background()))
	require.Equal(t, int64(0), p.floorTip.Load(), "\"empty\" must mean floor unknown, never floor 0")
	require.True(t, p.IsHealthy(), "floor failure alone must NOT cordon (failure isolation)")
	require.Empty(t, up.cordonReasons())
}

// ---- unhealthyWhen ----------------------------------------------------------

func TestPoller_Poll_UnhealthyWhenMatch_Cordons(t *testing.T) {
	t.Parallel()
	up := newScriptedUpstream()
	up.scriptResult("status", []byte(`{"sync_info":{"latest_block_height":12345,"catching_up":true}}`))
	p := newTestPoller(t, up)
	require.NoError(t, p.Configure(tipOnlyCfg(&common.JsonRpcStateProbeConfig{
		Method: "status", Path: "$.result.sync_info.latest_block_height",
		UnhealthyWhen: &common.JsonRpcValueMatcherConfig{
			Path:   "$.result.sync_info.catching_up",
			Equals: "true",
		},
	})))

	require.NoError(t, p.Poll(context.Background()))
	require.Equal(t, int64(12345), p.LatestTip(), "value still updates; the matcher only gates health")
	require.False(t, p.IsHealthy())
	require.Len(t, up.cordonReasons(), 1)
}

func TestPoller_Poll_UnhealthyWhenNoMatch_StaysHealthy(t *testing.T) {
	t.Parallel()
	up := newScriptedUpstream()
	up.scriptResult("status", []byte(`{"sync_info":{"latest_block_height":12345,"catching_up":false}}`))
	p := newTestPoller(t, up)
	require.NoError(t, p.Configure(tipOnlyCfg(&common.JsonRpcStateProbeConfig{
		Method: "status", Path: "$.result.sync_info.latest_block_height",
		UnhealthyWhen: &common.JsonRpcValueMatcherConfig{
			Path:   "$.result.sync_info.catching_up",
			Equals: "true",
		},
	})))

	require.NoError(t, p.Poll(context.Background()))
	require.True(t, p.IsHealthy())
	require.Empty(t, up.cordonReasons())
}

// Health probes with content-free success bodies (CometBFT `health` → {}) are
// healthy on a clean transport answer alone.
func TestPoller_Poll_ContentFreeHealthProbe_HealthyOnCleanAnswer(t *testing.T) {
	t.Parallel()
	up := newScriptedUpstream()
	up.scriptResult("status", []byte(`{"sync_info":{"latest_block_height":12345}}`))
	up.scriptResult("health", []byte(`{}`))
	p := newTestPoller(t, up)
	require.NoError(t, p.Configure(&common.JsonRpcStatePollerConfig{
		Tip:    &common.JsonRpcStateProbeConfig{Method: "status", Path: "$.result.sync_info.latest_block_height"},
		Health: &common.JsonRpcStateProbeConfig{Method: "health"},
	}))

	require.NoError(t, p.Poll(context.Background()))
	require.True(t, p.IsHealthy())
	require.Equal(t, int64(12345), p.LatestTip())
}

func TestPoller_Poll_HealthProbeJsonRpcError_Cordons(t *testing.T) {
	t.Parallel()
	up := newScriptedUpstream()
	up.scriptResult("status", []byte(`{"sync_info":{"latest_block_height":12345}}`))
	up.scriptError("health", -32603, "boom")
	p := newTestPoller(t, up)
	require.NoError(t, p.Configure(&common.JsonRpcStatePollerConfig{
		Tip:    &common.JsonRpcStateProbeConfig{Method: "status", Path: "$.result.sync_info.latest_block_height"},
		Health: &common.JsonRpcStateProbeConfig{Method: "health"},
	}))

	require.NoError(t, p.Poll(context.Background()))
	require.False(t, p.IsHealthy(), "a JSON-RPC error from the health probe marks the tick unhealthy")
	require.Len(t, up.cordonReasons(), 1)
}

// ---- thenCall two-hop -------------------------------------------------------

func TestPoller_Poll_ThenCallTwoHop_ResolvesFinalizedTip(t *testing.T) {
	t.Parallel()
	up := newScriptedUpstream()
	up.scriptResult("chain_getHeader", []byte(`{"number":"0x2a2b"}`))
	up.scriptResult("chain_getFinalizedHead", []byte(`"0xabcd1234hash"`))
	up.scriptResult("status", []byte(`{"sync_info":{"latest_block_height":10800}}`))
	p := newTestPoller(t, up)
	require.NoError(t, p.Configure(&common.JsonRpcStatePollerConfig{
		Tip: &common.JsonRpcStateProbeConfig{Method: "status", Path: "$.result.sync_info.latest_block_height"},
		FinalizedTip: &common.JsonRpcStateProbeConfig{
			Method: "chain_getFinalizedHead",
			Path:   "$.result",
			ThenCall: &common.JsonRpcThenCallConfig{
				Method:           "chain_getHeader",
				PassValueAsParam: true,
				Path:             "$.result.number",
				Encoding:         "hex",
			},
		},
	}))

	require.NoError(t, p.Poll(context.Background()))
	require.Equal(t, int64(10800), p.LatestTip())
	require.Equal(t, int64(0x2a2b), p.FinalizedTip())

	// The first hop's hash became the second hop's sole param.
	headerCalls := up.callParams("chain_getHeader")
	require.Len(t, headerCalls, 1)
	require.Equal(t, []interface{}{"0xabcd1234hash"}, headerCalls[0])
	require.True(t, p.IsHealthy())
}

func TestPoller_Poll_FailedThenCall_IsolatesFinalizedCounter(t *testing.T) {
	t.Parallel()
	up := newScriptedUpstream()
	up.scriptResult("chain_getFinalizedHead", []byte(`"0xabcd1234hash"`))
	up.scriptError("chain_getHeader", -32603, "header unavailable")
	up.scriptResult("status", []byte(`{"sync_info":{"latest_block_height":10800}}`))
	p := newTestPoller(t, up)
	require.NoError(t, p.Configure(&common.JsonRpcStatePollerConfig{
		Tip: &common.JsonRpcStateProbeConfig{Method: "status", Path: "$.result.sync_info.latest_block_height"},
		FinalizedTip: &common.JsonRpcStateProbeConfig{
			Method: "chain_getFinalizedHead",
			Path:   "$.result",
			ThenCall: &common.JsonRpcThenCallConfig{
				Method:           "chain_getHeader",
				PassValueAsParam: true,
				Path:             "$.result.number",
				Encoding:         "hex",
			},
		},
	}))

	require.NoError(t, p.Poll(context.Background()))
	require.Equal(t, int64(0), p.FinalizedTip(), "failed resolve hop invalidates only the finalized counter")
	require.Equal(t, int64(10800), p.LatestTip(), "tip still updates (§9.6 failure isolation)")
	require.True(t, p.IsHealthy(), "finalized-tip failure alone must NOT cordon")
	require.Empty(t, up.cordonReasons())
}

func TestPoller_Poll_FailedFinalizedProbe_IsolatesFinalizedCounter(t *testing.T) {
	t.Parallel()
	up := newScriptedUpstream()
	// No finalizedTip scripted at all: the probe transport fails.
	up.scriptResult("status", []byte(`{"sync_info":{"latest_block_height":10800}}`))
	p := newTestPoller(t, up)
	require.NoError(t, p.Configure(&common.JsonRpcStatePollerConfig{
		Tip:          &common.JsonRpcStateProbeConfig{Method: "status", Path: "$.result.sync_info.latest_block_height"},
		FinalizedTip: &common.JsonRpcStateProbeConfig{Method: "chain_getFinalizedHead", Path: "$.result"},
	}))

	require.NoError(t, p.Poll(context.Background()))
	require.Equal(t, int64(10800), p.LatestTip())
	require.Equal(t, int64(0), p.FinalizedTip())
	require.True(t, p.IsHealthy())
	require.Empty(t, up.cordonReasons())
}

// ---- interface surface ------------------------------------------------------

func TestPoller_SatisfiesCommonInterface(t *testing.T) {
	t.Parallel()
	var p common.JsonRpcStatePoller = newTestPoller(t, newScriptedUpstream())
	require.False(t, p.IsObjectNull())
	require.True(t, p.IsHealthy(), "cold poller reads healthy: not-yet-observed is not unhealthy")
}

// Configure validates the minimal probe shape (defense in depth — config
// validation is the primary gate).
func TestPoller_Configure_RequiresTipProbe(t *testing.T) {
	t.Parallel()
	p := newTestPoller(t, newScriptedUpstream())
	require.Error(t, p.Configure(nil))
	require.Error(t, p.Configure(&common.JsonRpcStatePollerConfig{}))
	require.NoError(t, p.Configure(tipOnlyCfg(&common.JsonRpcStateProbeConfig{Method: "status", Path: "$.result"})))
}

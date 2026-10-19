package jsonrpc

import (
	"context"
	"fmt"
	"testing"

	"github.com/erpc/erpc/common"
	"github.com/rs/zerolog"
)

// fakeNetwork satisfies common.Network with only what the finality resolver
// touches: Config() (method declarations) and the identity accessors.
type fakeNetwork struct {
	cfg *common.NetworkConfig
}

func (f *fakeNetwork) Id() string                               { return "jsonrpc:test" }
func (f *fakeNetwork) Label() string                            { return "" }
func (f *fakeNetwork) ProjectId() string                        { return "test" }
func (f *fakeNetwork) Architecture() common.NetworkArchitecture { return common.ArchitectureJsonRpc }
func (f *fakeNetwork) Config() *common.NetworkConfig            { return f.cfg }
func (f *fakeNetwork) Logger() *zerolog.Logger                  { l := zerolog.Nop(); return &l }
func (f *fakeNetwork) GetMethodMetrics(string) common.TrackedMetrics {
	return nil
}
func (f *fakeNetwork) Forward(context.Context, *common.NormalizedRequest) (*common.NormalizedResponse, error) {
	return nil, nil
}
func (f *fakeNetwork) GetFinality(ctx context.Context, req *common.NormalizedRequest, resp *common.NormalizedResponse) common.DataFinalityState {
	return GetFinality(ctx, f, req, resp)
}

func finalityReq(method, paramsJson string) *common.NormalizedRequest {
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":%s}`, method, paramsJson)
	return common.NewNormalizedRequest([]byte(body))
}

func nearNetwork() *fakeNetwork {
	return &fakeNetwork{cfg: &common.NetworkConfig{
		Architecture: common.ArchitectureJsonRpc,
		Methods: &common.MethodsConfig{Definitions: map[string]*common.CacheMethodConfig{
			"block": {
				FinalityFromParam: &common.FinalityFromParamConfig{
					Key: "finality",
					Map: map[string]string{
						"final":      "finalized",
						"near-final": "unfinalized",
						"optimistic": "realtime",
					},
					Default: "realtime",
				},
			},
		}},
	}}
}

func TestFinality_StaticUnfinalized(t *testing.T) {
	t.Parallel()
	net := &fakeNetwork{cfg: &common.NetworkConfig{
		Architecture: common.ArchitectureJsonRpc,
		Methods: &common.MethodsConfig{Definitions: map[string]*common.CacheMethodConfig{
			"chain_getBlock": {Unfinalized: true},
		}},
	}}
	if got := GetFinality(context.Background(), net, finalityReq("chain_getBlock", `[]`), nil); got != common.DataFinalityStateUnfinalized {
		t.Fatalf("chain_getBlock: expected Unfinalized, got %v", got)
	}
}

func TestFinality_KeyForm_NearMap(t *testing.T) {
	t.Parallel()
	net := nearNetwork()
	cases := []struct {
		params string
		want   common.DataFinalityState
	}{
		{`[{"finality":"final"}]`, common.DataFinalityStateFinalized},
		{`[{"finality":"near-final"}]`, common.DataFinalityStateUnfinalized},
		{`[{"finality":"optimistic"}]`, common.DataFinalityStateRealtime},
		// Absent param → default.
		{`[{"block_id":123}]`, common.DataFinalityStateRealtime},
		{`[]`, common.DataFinalityStateRealtime},
		// Unmapped value → default.
		{`[{"finality":"doomslug"}]`, common.DataFinalityStateRealtime},
		// Non-string value → default.
		{`[{"finality":42}]`, common.DataFinalityStateRealtime},
		// First string value wins; the key may sit behind other members.
		{`[{"request_type":"view_access_key","finality":"final","account_id":"x.near"}]`, common.DataFinalityStateFinalized},
	}
	for _, tc := range cases {
		got := GetFinality(context.Background(), net, finalityReq("block", tc.params), nil)
		if got != tc.want {
			t.Errorf("params %s: got %v, want %v", tc.params, got, tc.want)
		}
	}
}

func TestFinality_KeyForm_CaseInsensitiveFallback(t *testing.T) {
	t.Parallel()
	net := nearNetwork()
	// Chains vary in accepted casing; exact match is preferred, then a
	// case-insensitive fold finds the same table entry.
	if got := GetFinality(context.Background(), net, finalityReq("block", `[{"finality":"FINAL"}]`), nil); got != common.DataFinalityStateFinalized {
		t.Fatalf("FINAL: got %v, want Finalized", got)
	}
	if got := GetFinality(context.Background(), net, finalityReq("block", `[{"finality":" Final "}]`), nil); got != common.DataFinalityStateFinalized {
		t.Fatalf("' Final ': got %v, want Finalized", got)
	}
}

func starknetNetwork() *fakeNetwork {
	idx := 0
	return &fakeNetwork{cfg: &common.NetworkConfig{
		Architecture: common.ArchitectureJsonRpc,
		Methods: &common.MethodsConfig{Definitions: map[string]*common.CacheMethodConfig{
			"starknet_getBlockWithTxs": {
				FinalityFromParam: &common.FinalityFromParamConfig{
					Index: &idx,
					Map: map[string]string{
						"l1_accepted":   "finalized",
						"latest":        "realtime",
						"pre_confirmed": "realtime",
					},
					Default: "unfinalized",
				},
			},
		}},
	}}
}

func TestFinality_IndexForm_StarknetTags(t *testing.T) {
	t.Parallel()
	net := starknetNetwork()
	cases := []struct {
		params string
		want   common.DataFinalityState
	}{
		{`["l1_accepted"]`, common.DataFinalityStateFinalized},
		{`["latest"]`, common.DataFinalityStateRealtime},
		{`["pre_confirmed"]`, common.DataFinalityStateRealtime},
		// Object block id ({block_number}/{block_hash}) is non-string → default.
		{`[{"block_number":123}]`, common.DataFinalityStateUnfinalized},
		// Absent (index past end) → default.
		{`[]`, common.DataFinalityStateUnfinalized},
		// Unmapped tag → default.
		{`["pending"]`, common.DataFinalityStateUnfinalized},
	}
	for _, tc := range cases {
		got := GetFinality(context.Background(), net, finalityReq("starknet_getBlockWithTxs", tc.params), nil)
		if got != tc.want {
			t.Errorf("params %s: got %v, want %v", tc.params, got, tc.want)
		}
	}
}

// XRPL params is an array with ONE object at index 0: the Key form finds the
// tag inside it, while the Index form would extract the whole object (and land
// on the default) — the v2 §9.4 warning, verified both ways.
func TestFinality_XrplKeyVersusIndex(t *testing.T) {
	t.Parallel()
	keyNet := &fakeNetwork{cfg: &common.NetworkConfig{
		Architecture: common.ArchitectureJsonRpc,
		Methods: &common.MethodsConfig{Definitions: map[string]*common.CacheMethodConfig{
			"ledger": {
				FinalityFromParam: &common.FinalityFromParamConfig{
					Key:     "ledger_index",
					Map:     map[string]string{"validated": "finalized", "current": "realtime", "closed": "realtime"},
					Default: "realtime",
				},
			},
		}},
	}}
	if got := GetFinality(context.Background(), keyNet, finalityReq("ledger", `[{"ledger_index":"validated"}]`), nil); got != common.DataFinalityStateFinalized {
		t.Fatalf("key form validated: got %v, want Finalized", got)
	}
	if got := GetFinality(context.Background(), keyNet, finalityReq("ledger", `[{"ledger_index":123}]`), nil); got != common.DataFinalityStateRealtime {
		t.Fatalf("numeric ledger_index: got %v, want Realtime (default)", got)
	}

	idx := 0
	indexNet := &fakeNetwork{cfg: &common.NetworkConfig{
		Architecture: common.ArchitectureJsonRpc,
		Methods: &common.MethodsConfig{Definitions: map[string]*common.CacheMethodConfig{
			"ledger": {
				FinalityFromParam: &common.FinalityFromParamConfig{
					Index:   &idx,
					Map:     map[string]string{"validated": "finalized"},
					Default: "realtime",
				},
			},
		}},
	}}
	if got := GetFinality(context.Background(), indexNet, finalityReq("ledger", `[{"ledger_index":"validated"}]`), nil); got != common.DataFinalityStateRealtime {
		t.Fatalf("index form over object param: got %v, want Realtime (default)", got)
	}
}

func TestFinality_NoDeclaration_IsUnknown(t *testing.T) {
	t.Parallel()
	net := &fakeNetwork{cfg: &common.NetworkConfig{Architecture: common.ArchitectureJsonRpc}}
	if got := GetFinality(context.Background(), net, finalityReq("getHealth", `[]`), nil); got != common.DataFinalityStateUnknown {
		t.Fatalf("undeclared method: got %v, want Unknown", got)
	}
	if got := GetFinality(context.Background(), net, nil, nil); got != common.DataFinalityStateUnknown {
		t.Fatalf("nil request: got %v, want Unknown", got)
	}
}

package jsonrpc

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/erpc/erpc/common"
	"github.com/rs/zerolog"
)

// stubUpstream satisfies common.Upstream plus the defensive NetworkConfig()
// assertion the extractor makes. Only Config / Id / NetworkConfig carry
// meaningful values; everything else is a no-op (mirrors svm's stubSvm).
type stubUpstream struct {
	id string
	nc *common.NetworkConfig
}

func (s *stubUpstream) Id() string           { return s.id }
func (s *stubUpstream) VendorName() string   { return "" }
func (s *stubUpstream) NetworkId() string    { return "jsonrpc:test" }
func (s *stubUpstream) NetworkLabel() string { return "" }
func (s *stubUpstream) Config() *common.UpstreamConfig {
	return &common.UpstreamConfig{Id: s.id, Type: common.UpstreamTypeJsonRpc}
}
func (s *stubUpstream) NetworkConfig() *common.NetworkConfig { return s.nc }
func (s *stubUpstream) Logger() *zerolog.Logger              { l := zerolog.Nop(); return &l }
func (s *stubUpstream) Vendor() common.Vendor                { return nil }
func (s *stubUpstream) Tracker() common.HealthTracker        { return nil }
func (s *stubUpstream) Forward(context.Context, *common.NormalizedRequest, bool, bool) (*common.NormalizedResponse, error) {
	return nil, nil
}
func (s *stubUpstream) ShouldHandleMethod(string) (bool, error) { return true, nil }
func (s *stubUpstream) Cordon(string, string)                   {}
func (s *stubUpstream) Uncordon(string, string)                 {}
func (s *stubUpstream) IgnoreMethod(string)                     {}

func newStub(nc *common.NetworkConfig) *stubUpstream {
	return &stubUpstream{id: "jsonrpc-stub", nc: nc}
}

func jsonRpcNetworkCfg(jsonrpc *common.JsonrpcNetworkConfig, methods map[string]*common.CacheMethodConfig) *common.NetworkConfig {
	nc := &common.NetworkConfig{
		Architecture: common.ArchitectureJsonRpc,
		Jsonrpc:      jsonrpc,
	}
	if methods != nil {
		nc.Methods = &common.MethodsConfig{Definitions: methods}
	}
	return nc
}

// ---- guard + spec codes (v1 §6) ---------------------------------------------

func TestExtract_NonJsonRpcUpstream_IsNoOp(t *testing.T) {
	t.Parallel()
	e := NewJsonRpcErrorExtractor()
	r := &http.Response{StatusCode: 200, Header: http.Header{}}

	evmStub := &evmTypedStub{}
	if got := e.Extract(r, nil, common.MustNewJsonRpcResponse(1, nil, common.NewErrJsonRpcExceptionExternal(-32000, "server error", "")), evmStub); got != nil {
		t.Fatalf("expected nil for non-jsonrpc upstream, got %v", got)
	}
	if got := e.Extract(r, nil, nil, nil); got != nil {
		t.Fatalf("expected nil for nil upstream, got %v", got)
	}
}

// evmTypedStub carries an EVM upstream type so the guard's Type check is what
// declines it.
type evmTypedStub struct{ stubUpstream }

func (s *evmTypedStub) Config() *common.UpstreamConfig {
	return &common.UpstreamConfig{Id: "evm-stub", Type: common.UpstreamTypeEvm}
}

func TestExtract_SpecCodes_AreClientSideNonRetryable(t *testing.T) {
	t.Parallel()
	for _, code := range []int{-32700, -32600, -32601, -32602} {
		err := extractCode(t, code, "spec violation", newStub(nil))
		if !common.HasErrorCode(err, common.ErrCodeEndpointClientSideException) {
			t.Fatalf("code %d: expected ErrEndpointClientSideException, got %T: %v", code, err, err)
		}
		if common.IsRetryableTowardNetwork(err) {
			t.Fatalf("code %d: spec codes must be non-retryable toward network", code)
		}
		if got := wireCodeOf(t, err); got != common.JsonRpcErrorNumber(code) {
			t.Fatalf("code %d: wire code must stay native, got %v", code, got)
		}
	}
}

func TestExtract_UnknownCode_FallsThroughToGeneric(t *testing.T) {
	t.Parallel()
	// -32000 is not ours: the generic server-side fallthrough classifies it
	// retryable (v1 §6). The extractor must return nil so that path runs.
	if got := extractCode(t, -32000, "vendor bucket", newStub(nil)); got != nil {
		t.Fatalf("expected nil for unknown code, got %T: %v", got, got)
	}
	// -32603 (internal error) is likewise not one of the four spec codes.
	if got := extractCode(t, -32603, "internal", newStub(nil)); got != nil {
		t.Fatalf("expected nil for -32603, got %T: %v", got, got)
	}
}

// ---- configured network errorRules (v2 §4.3) --------------------------------

func nearNetworkCfg() *common.NetworkConfig {
	return jsonRpcNetworkCfg(&common.JsonrpcNetworkConfig{
		NetworkId: "near",
		ErrorRules: []*common.JsonRpcClassificationRuleConfig{
			{
				Match: &common.JsonRpcClassificationMatchConfig{
					ErrorPath: "$.error.data.cause.name",
					In:        []interface{}{"UNKNOWN_BLOCK", "GARBAGE_COLLECTED_BLOCK"},
				},
				ClassifyAs: "missingData",
			},
		},
	}, nil)
}

func TestExtract_NearVerrorCauseName_MissingDataRetryableNotPermanent(t *testing.T) {
	t.Parallel()
	jrErr := &common.ErrJsonRpcExceptionExternal{
		Code:    -32000,
		Message: "Server error",
		Data: map[string]interface{}{
			"cause": map[string]interface{}{"name": "UNKNOWN_BLOCK", "info": map[string]interface{}{"block_height": 123}},
		},
	}
	err := extractWith(t, 200, jrErr, nil, newStub(nearNetworkCfg()))
	if !common.HasErrorCode(err, common.ErrCodeEndpointMissingData) {
		t.Fatalf("expected ErrEndpointMissingData, got %T: %v", err, err)
	}
	if !common.IsRetryableTowardNetwork(err) {
		t.Fatal("missingData must stay retryable: an archival peer can have the data")
	}
	if common.IsPermanentlyMissingData(err) {
		t.Fatal("missingData must NEVER be permanent: a NOT_FOUND can be transient")
	}
	// The native -32000 reaches the caller; the routing verdict lives in the
	// outer class, not the wire code.
	if got := wireCodeOf(t, err); got != common.JsonRpcErrorNumber(-32000) {
		t.Fatalf("wire code must preserve native -32000, got %v", got)
	}
	// The structured cause rides through verbatim in details["data"].
	if got := dataOf(t, err); got == nil {
		t.Fatal("details[data] must carry the upstream's structured cause")
	}
}

func TestExtract_SubtreeRelativeErrorPath_AlsoMatches(t *testing.T) {
	t.Parallel()
	// Operators write both styles; the walker accepts subtree-relative paths
	// ($.cause.name against the error object) as well as whole-response ones.
	nc := jsonRpcNetworkCfg(&common.JsonrpcNetworkConfig{
		ErrorRules: []*common.JsonRpcClassificationRuleConfig{
			{
				Match:      &common.JsonRpcClassificationMatchConfig{ErrorPath: "$.data.cause.name", Equals: "UNKNOWN_BLOCK"},
				ClassifyAs: "missingData",
			},
		},
	}, nil)
	jrErr := &common.ErrJsonRpcExceptionExternal{
		Code: -32000, Message: "Server error",
		Data: map[string]interface{}{"cause": map[string]interface{}{"name": "UNKNOWN_BLOCK"}},
	}
	err := extractWith(t, 200, jrErr, nil, newStub(nc))
	if !common.HasErrorCode(err, common.ErrCodeEndpointMissingData) {
		t.Fatalf("expected ErrEndpointMissingData for subtree-relative path, got %T: %v", err, err)
	}
}

func starknetNetworkCfg() *common.NetworkConfig {
	return jsonRpcNetworkCfg(&common.JsonrpcNetworkConfig{
		ErrorRules: []*common.JsonRpcClassificationRuleConfig{
			{
				// Starknet custom codes are small positive ints (24 = BlockNotFound);
				// the numeric code must stringify to "24" for the comparison.
				Match:      &common.JsonRpcClassificationMatchConfig{ErrorPath: "$.error.code", Equals: "24"},
				ClassifyAs: "missingData",
			},
		},
	}, nil)
}

func TestExtract_StarknetNumericCode_StringifiedEquals(t *testing.T) {
	t.Parallel()
	err := extractWith(t, 200, common.NewErrJsonRpcExceptionExternal(24, "Block not found", ""), nil, newStub(starknetNetworkCfg()))
	if !common.HasErrorCode(err, common.ErrCodeEndpointMissingData) {
		t.Fatalf("expected ErrEndpointMissingData, got %T: %v", err, err)
	}
	if got := wireCodeOf(t, err); got != common.JsonRpcErrorNumber(24) {
		t.Fatalf("wire code must preserve native 24, got %v", got)
	}
}

func xrplNetworkCfg() *common.NetworkConfig {
	return jsonRpcNetworkCfg(&common.JsonrpcNetworkConfig{
		ErrorRules: []*common.JsonRpcClassificationRuleConfig{
			{
				Match:    &common.JsonRpcClassificationMatchConfig{ResultPath: "$.result.status", Equals: "error"},
				CodePath: "$.result.error",
				CodeMap: map[string]string{
					"txnNotFound": "missingData",
					"lgrNotFound": "missingData",
					"tooBusy":     "serverError",
				},
				Default: "serverError",
			},
		},
	}, nil)
}

func xrplResult(t *testing.T, status, code string) *common.JsonRpcResponse {
	t.Helper()
	jrr, err := common.NewJsonRpcResponse(1, map[string]interface{}{
		"status": status,
		"error":  code,
	}, nil)
	if err != nil {
		t.Fatalf("build xrpl result: %v", err)
	}
	return jrr
}

func TestExtract_XrplTwoStage_CodeMapPicksClass(t *testing.T) {
	t.Parallel()
	e := NewJsonRpcErrorExtractor()
	stub := newStub(xrplNetworkCfg())
	resp := &http.Response{StatusCode: 200, Header: http.Header{}}

	err := e.Extract(resp, nil, xrplResult(t, "error", "txnNotFound"), stub)
	if !common.HasErrorCode(err, common.ErrCodeEndpointMissingData) {
		t.Fatalf("txnNotFound: expected ErrEndpointMissingData, got %T: %v", err, err)
	}
	if !common.IsRetryableTowardNetwork(err) {
		t.Fatal("txnNotFound must be retryable toward network")
	}

	err = e.Extract(resp, nil, xrplResult(t, "error", "tooBusy"), stub)
	if !common.HasErrorCode(err, common.ErrCodeEndpointServerSideException) {
		t.Fatalf("tooBusy: expected ErrEndpointServerSideException, got %T: %v", err, err)
	}
	if !common.IsRetryableTowardNetwork(err) {
		t.Fatal("tooBusy must be retryable (server-side)")
	}

	// Unmapped code falls to the rule default.
	err = e.Extract(resp, nil, xrplResult(t, "error", "mysteryCode"), stub)
	if !common.HasErrorCode(err, common.ErrCodeEndpointServerSideException) {
		t.Fatalf("unmapped code: expected default serverError, got %T: %v", err, err)
	}

	// Success envelope passes through untouched.
	if got := e.Extract(resp, nil, xrplResult(t, "success", ""), stub); got != nil {
		t.Fatalf("success result: expected nil, got %T: %v", got, got)
	}
}

// ---- per-method resultClassifications (v2 §4.3) -----------------------------

func stellarNetworkCfg() *common.NetworkConfig {
	return jsonRpcNetworkCfg(nil, map[string]*common.CacheMethodConfig{
		"getTransaction": {
			ResultClassifications: []*common.JsonRpcClassificationRuleConfig{
				{
					Match:      &common.JsonRpcClassificationMatchConfig{ResultPath: "$.result.status", In: []interface{}{"NOT_FOUND"}},
					ClassifyAs: "missingData",
				},
			},
		},
	})
}

func stellarResult(t *testing.T, status string) *common.JsonRpcResponse {
	t.Helper()
	jrr, err := common.NewJsonRpcResponse(1, map[string]interface{}{
		"status":       status,
		"latestLedger": float64(1234),
		"oldestLedger": float64(100),
		"ledger":       float64(1200),
		"createdAt":    "2026-01-01T00:00:00Z",
	}, nil)
	if err != nil {
		t.Fatalf("build stellar result: %v", err)
	}
	return jrr
}

func nrWithMethod(t *testing.T, method string) *common.NormalizedResponse {
	t.Helper()
	req := common.NewNormalizedRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":["abc"]}`))
	return common.NewNormalizedResponse().WithRequest(req)
}

func TestExtract_StellarPerMethod_NotFoundIsMissingData(t *testing.T) {
	t.Parallel()
	e := NewJsonRpcErrorExtractor()
	stub := newStub(stellarNetworkCfg())
	resp := &http.Response{StatusCode: 200, Header: http.Header{}}

	err := e.Extract(resp, nrWithMethod(t, "getTransaction"), stellarResult(t, "NOT_FOUND"), stub)
	if !common.HasErrorCode(err, common.ErrCodeEndpointMissingData) {
		t.Fatalf("NOT_FOUND: expected ErrEndpointMissingData, got %T: %v", err, err)
	}
	if !common.IsRetryableTowardNetwork(err) {
		t.Fatal("NOT_FOUND must be retryable (pending or pruned — another node may have it)")
	}
	if common.IsPermanentlyMissingData(err) {
		t.Fatal("NOT_FOUND must never be permanent: the tx may still be pending")
	}

	// FAILED is a final, cacheable result — the rule must not touch it.
	if got := e.Extract(resp, nrWithMethod(t, "getTransaction"), stellarResult(t, "FAILED"), stub); got != nil {
		t.Fatalf("FAILED: expected nil (cacheable success shape), got %T: %v", got, got)
	}
	// SUCCESS likewise.
	if got := e.Extract(resp, nrWithMethod(t, "getTransaction"), stellarResult(t, "SUCCESS"), stub); got != nil {
		t.Fatalf("SUCCESS: expected nil, got %T: %v", got, got)
	}
}

func TestExtract_PerMethodRule_NotAppliedToOtherMethods(t *testing.T) {
	t.Parallel()
	e := NewJsonRpcErrorExtractor()
	stub := newStub(stellarNetworkCfg())
	resp := &http.Response{StatusCode: 200, Header: http.Header{}}

	// Same NOT_FOUND shape on an undeclared method: no rule applies.
	if got := e.Extract(resp, nrWithMethod(t, "getTransactions"), stellarResult(t, "NOT_FOUND"), stub); got != nil {
		t.Fatalf("undeclared method: expected nil, got %T: %v", got, got)
	}
}

// CometBFT-style health probe chains surface gaps as null/missing members;
// IsNull must treat missing keys as null.
func TestExtract_IsNull_MissingKeyMatches(t *testing.T) {
	t.Parallel()
	nc := jsonRpcNetworkCfg(nil, map[string]*common.CacheMethodConfig{
		"getBlock": {
			ResultClassifications: []*common.JsonRpcClassificationRuleConfig{
				{
					Match:      &common.JsonRpcClassificationMatchConfig{ResultPath: "$.result.block", IsNull: true},
					ClassifyAs: "missingData",
				},
			},
		},
	})
	e := NewJsonRpcErrorExtractor()
	stub := newStub(nc)
	resp := &http.Response{StatusCode: 200, Header: http.Header{}}

	// result carries no "block" member → IsNull matches.
	jrrEmpty, err := common.NewJsonRpcResponse(1, map[string]interface{}{"block_id": float64(7)}, nil)
	if err != nil {
		t.Fatalf("build result: %v", err)
	}
	err = e.Extract(resp, nrWithMethod(t, "getBlock"), jrrEmpty, stub)
	if !common.HasErrorCode(err, common.ErrCodeEndpointMissingData) {
		t.Fatalf("missing block member: expected ErrEndpointMissingData, got %T: %v", err, err)
	}

	// Explicit JSON null matches too.
	jrrNull, err := common.NewJsonRpcResponse(1, map[string]interface{}{"block": nil}, nil)
	if err != nil {
		t.Fatalf("build result: %v", err)
	}
	err = e.Extract(resp, nrWithMethod(t, "getBlock"), jrrNull, stub)
	if !common.HasErrorCode(err, common.ErrCodeEndpointMissingData) {
		t.Fatalf("null block: expected ErrEndpointMissingData, got %T: %v", err, err)
	}

	// A present block does NOT match IsNull.
	jrrBlock, err := common.NewJsonRpcResponse(1, map[string]interface{}{"block": map[string]interface{}{"height": float64(7)}}, nil)
	if err != nil {
		t.Fatalf("build result: %v", err)
	}
	if got := e.Extract(resp, nrWithMethod(t, "getBlock"), jrrBlock, stub); got != nil {
		t.Fatalf("present block: expected nil, got %T: %v", got, got)
	}
}

// ---- rule ordering ----------------------------------------------------------

func TestExtract_FirstMatchingRuleWins(t *testing.T) {
	t.Parallel()
	nc := jsonRpcNetworkCfg(&common.JsonrpcNetworkConfig{
		ErrorRules: []*common.JsonRpcClassificationRuleConfig{
			{
				Match:      &common.JsonRpcClassificationMatchConfig{ErrorPath: "$.error.code", Equals: "-32000"},
				ClassifyAs: "missingData",
			},
			{
				// Would also match, but must never be reached.
				Match:      &common.JsonRpcClassificationMatchConfig{ErrorPath: "$.error.code", Equals: "-32000"},
				ClassifyAs: "serverError",
			},
		},
	}, nil)
	err := extractWith(t, 200, common.NewErrJsonRpcExceptionExternal(-32000, "handler error", ""), nil, newStub(nc))
	if !common.HasErrorCode(err, common.ErrCodeEndpointMissingData) {
		t.Fatalf("first rule should win, got %T: %v", err, err)
	}
}

func TestExtract_NoRuleMatch_FallsThrough(t *testing.T) {
	t.Parallel()
	// A rule list that matches nothing must not swallow the fallthrough.
	nc := jsonRpcNetworkCfg(&common.JsonrpcNetworkConfig{
		ErrorRules: []*common.JsonRpcClassificationRuleConfig{
			{
				Match:      &common.JsonRpcClassificationMatchConfig{ErrorPath: "$.error.data.cause.name", In: []interface{}{"SOME_OTHER_CAUSE"}},
				ClassifyAs: "missingData",
			},
		},
	}, nil)
	jrErr := &common.ErrJsonRpcExceptionExternal{
		Code: -32000, Message: "Server error",
		Data: map[string]interface{}{"cause": map[string]interface{}{"name": "UNKNOWN_BLOCK"}},
	}
	// -32000 is not a spec code → nil (generic server-side fallthrough).
	if got := extractWith(t, 200, jrErr, nil, newStub(nc)); got != nil {
		t.Fatalf("expected nil when no rule matches, got %T: %v", got, got)
	}
}

// Matched rule with a codeMap but an unmapped code and NO default is a
// no-match: the next rule (here: none) gets consulted — hand-built configs
// only, since validation enforces default-when-codeMap.
func TestExtract_CodeMapWithoutDefault_FallsToNextRule(t *testing.T) {
	t.Parallel()
	nc := jsonRpcNetworkCfg(&common.JsonrpcNetworkConfig{
		ErrorRules: []*common.JsonRpcClassificationRuleConfig{
			{
				Match:    &common.JsonRpcClassificationMatchConfig{ResultPath: "$.result.status", Equals: "error"},
				CodePath: "$.result.error",
				CodeMap:  map[string]string{"txnNotFound": "missingData"},
				// no Default
			},
		},
	}, nil)
	e := NewJsonRpcErrorExtractor()
	resp := &http.Response{StatusCode: 200, Header: http.Header{}}
	if got := e.Extract(resp, nil, xrplResult(t, "error", "slowDown"), newStub(nc)); got != nil {
		t.Fatalf("expected nil for unmapped code without default, got %T: %v", got, got)
	}
}

// Configured rules only apply on jsonrpc-architecture networks; a jsonrpc-type
// upstream wired to a non-jsonrpc network config (defensive) keeps v1 behavior.
func TestExtract_ConfiguredRules_SkipNonJsonRpcNetwork(t *testing.T) {
	t.Parallel()
	nc := &common.NetworkConfig{
		Architecture: common.ArchitectureEvm,
		Jsonrpc: &common.JsonrpcNetworkConfig{
			ErrorRules: []*common.JsonRpcClassificationRuleConfig{
				{Match: &common.JsonRpcClassificationMatchConfig{ErrorPath: "$.error.code", Equals: "24"}, ClassifyAs: "missingData"},
			},
		},
	}
	if got := extractWith(t, 200, common.NewErrJsonRpcExceptionExternal(24, "Block not found", ""), nil, newStub(nc)); got != nil {
		t.Fatalf("expected nil on non-jsonrpc network, got %T: %v", got, got)
	}
}

// ---- helpers ----------------------------------------------------------------

func extractCode(t *testing.T, code int, msg string, stub *stubUpstream) error {
	t.Helper()
	return extractWith(t, 200, common.NewErrJsonRpcExceptionExternal(code, msg, ""), nil, stub)
}

func extractWith(t *testing.T, status int, jrErr *common.ErrJsonRpcExceptionExternal, result interface{}, stub *stubUpstream) error {
	t.Helper()
	e := NewJsonRpcErrorExtractor()
	r := &http.Response{StatusCode: status, Header: http.Header{}}
	return e.Extract(r, nil, common.MustNewJsonRpcResponse(1, result, jrErr), stub)
}

func wireCodeOf(t *testing.T, err error) common.JsonRpcErrorNumber {
	t.Helper()
	var jre *common.ErrJsonRpcExceptionInternal
	if !errors.As(err, &jre) {
		t.Fatalf("expected ErrJsonRpcExceptionInternal in chain, got %T: %v", err, err)
	}
	return jre.NormalizedCode()
}

func dataOf(t *testing.T, err error) interface{} {
	t.Helper()
	var jre *common.ErrJsonRpcExceptionInternal
	if !errors.As(err, &jre) {
		t.Fatalf("expected ErrJsonRpcExceptionInternal in chain, got %T: %v", err, err)
	}
	return jre.Details["data"]
}

// detailsOf asserts the standard extractor detail keys are present and the
// upstream data member round-trips — the SVM normalizer's contract, kept here.
func TestExtract_DetailsCarryStatusHeadersUpstreamAndData(t *testing.T) {
	t.Parallel()
	jrErr := &common.ErrJsonRpcExceptionExternal{
		Code: -32000, Message: "err",
		Data: map[string]interface{}{"cause": map[string]interface{}{"name": "UNKNOWN_BLOCK"}},
	}
	err := extractWith(t, 502, jrErr, nil, newStub(nearNetworkCfg()))
	var jre *common.ErrJsonRpcExceptionInternal
	if !errors.As(err, &jre) {
		t.Fatalf("expected ErrJsonRpcExceptionInternal in chain, got %T", err)
	}
	if jre.Details["statusCode"] != 502 {
		t.Errorf("statusCode detail = %v, want 502", jre.Details["statusCode"])
	}
	if _, ok := jre.Details["headers"].(map[string]interface{}); !ok {
		t.Errorf("headers detail missing or wrong type: %#v", jre.Details["headers"])
	}
	if jre.Details["upstreamId"] != "jsonrpc-stub" {
		t.Errorf("upstreamId detail = %v, want jsonrpc-stub", jre.Details["upstreamId"])
	}
	want := map[string]interface{}{"cause": map[string]interface{}{"name": "UNKNOWN_BLOCK"}}
	if !reflect.DeepEqual(jre.Details["data"], want) {
		t.Errorf("data detail = %#v, want %#v", jre.Details["data"], want)
	}
}

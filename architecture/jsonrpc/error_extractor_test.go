package jsonrpc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/erpc/erpc/common"
	"github.com/rs/zerolog"
)

// The generic extractor's entire contract: JSON-RPC spec codes
// -32600/-32601/-32602 (and any failing HTTP 4xx) are client-side and
// non-retryable; everything else is server-side and retryable; HTTP verdicts
// outrank the body. -32700 is special: a parse complaint on a 2xx means the
// upstream emitted unparseable bytes (eRPC serializes requests itself), so it
// is server-side and retryable; only a failing HTTP status next to a
// synthesized -32700 defers to the status.

func extract(t *testing.T, code int, msg string, status int) error {
	t.Helper()
	return extractWith(t, common.NewErrJsonRpcExceptionExternal(code, msg, ""), status)
}

func extractWith(t *testing.T, jrErr *common.ErrJsonRpcExceptionExternal, status int) error {
	t.Helper()
	e := NewJsonRpcErrorExtractor()
	r := &http.Response{StatusCode: status, Header: http.Header{}}
	return e.Extract(r, nil, common.MustNewJsonRpcResponse(1, nil, jrErr), newJsonRpcStub())
}

func wireCodeOf(t *testing.T, err error) common.JsonRpcErrorNumber {
	t.Helper()
	var jre *common.ErrJsonRpcExceptionInternal
	if !errors.As(err, &jre) {
		t.Fatalf("expected ErrJsonRpcExceptionInternal in chain, got %T: %v", err, err)
	}
	return jre.NormalizedCode()
}

func TestExtract_SpecClientErrorCodes_AreNonRetryableClientSide(t *testing.T) {
	t.Parallel()
	for _, code := range []int{
		-32600, // Invalid Request
		-32601, // Method not found
		-32602, // Invalid params
	} {
		t.Run(fmt.Sprintf("%d", code), func(t *testing.T) {
			err := extract(t, code, "client fault", 200)
			if !common.HasErrorCode(err, common.ErrCodeEndpointClientSideException) {
				t.Fatalf("expected ErrEndpointClientSideException, got %T: %v", err, err)
			}
			if common.IsRetryableTowardNetwork(err) {
				t.Fatal("spec client error must be non-retryable toward network")
			}
			if got := wireCodeOf(t, err); got != common.JsonRpcErrorNumber(code) {
				t.Errorf("wire code %d, want original %d", got, code)
			}
		})
	}
}

// A -32700 arriving on a 2xx is synthesized by eRPC's own parse layer: the
// upstream returned bytes that are not a JSON-RPC envelope. eRPC serializes
// outbound requests itself, so the caller's JSON cannot be at fault — this is
// an upstream fault and must stay retryable, with the raw code passing
// through to the client.
func TestExtract_ParseErrorOn2xx_IsRetryableServerSide(t *testing.T) {
	t.Parallel()
	err := extract(t, -32700, "cannot parse json-rpc response: invalid char", 200)
	if !common.HasErrorCode(err, common.ErrCodeEndpointServerSideException) {
		t.Fatalf("expected ErrEndpointServerSideException, got %T: %v", err, err)
	}
	if !common.IsRetryableTowardNetwork(err) {
		t.Fatal("synthesized parse error on 2xx must be retryable toward network")
	}
	if got := wireCodeOf(t, err); got != common.JsonRpcErrorNumber(-32700) {
		t.Errorf("wire code %d, want original -32700", got)
	}
}

func TestExtract_InternalError_IsRetryableServerSide(t *testing.T) {
	t.Parallel()
	err := extract(t, -32603, "Internal error", 200)
	if !common.HasErrorCode(err, common.ErrCodeEndpointServerSideException) {
		t.Fatalf("expected ErrEndpointServerSideException, got %T: %v", err, err)
	}
	if !common.IsRetryableTowardNetwork(err) {
		t.Fatal("-32603 must be retryable across upstreams")
	}
}

func TestExtract_AppDefinedServerCodes_AreRetryableServerSide(t *testing.T) {
	t.Parallel()
	// Starknet/Stellar/NEAR style server-defined error spaces.
	for _, code := range []int{-32000, -32001, -32050, 1, 100} {
		t.Run(fmt.Sprintf("%d", code), func(t *testing.T) {
			err := extract(t, code, "server fault", 200)
			if !common.HasErrorCode(err, common.ErrCodeEndpointServerSideException) {
				t.Fatalf("expected ErrEndpointServerSideException, got %T: %v", err, err)
			}
			if !common.IsRetryableTowardNetwork(err) {
				t.Fatal("application-defined server error must be retryable")
			}
			if got := wireCodeOf(t, err); got != common.JsonRpcErrorNumber(code) {
				t.Errorf("wire code %d, want original %d", got, code)
			}
		})
	}
}

func TestExtract_HTTP401_403_BecomeUnauthorized(t *testing.T) {
	t.Parallel()
	for _, status := range []int{401, 403} {
		err := extract(t, -32603, "whatever the body says", status)
		if !common.HasErrorCode(err, common.ErrCodeEndpointUnauthorized) {
			t.Fatalf("HTTP %d: expected ErrEndpointUnauthorized, got %T: %v", status, err, err)
		}
	}
}

func TestExtract_HTTP429_BareStatus_BecomesCapacityExceeded(t *testing.T) {
	t.Parallel()
	err := extractNoJr(t, 429)
	if !common.HasErrorCode(err, common.ErrCodeEndpointCapacityExceeded) {
		t.Fatalf("expected ErrEndpointCapacityExceeded, got %T: %v", err, err)
	}
}

// A real JSON-RPC error body outranks the HTTP status (svm semantics): the
// structured -32603 verdict wins over the bare 429, and stays retryable.
func TestExtract_HTTP429_RealErrorBody_BodyWins(t *testing.T) {
	t.Parallel()
	err := extract(t, -32603, "slow down", 429)
	if !common.HasErrorCode(err, common.ErrCodeEndpointServerSideException) {
		t.Fatalf("expected ErrEndpointServerSideException, got %T: %v", err, err)
	}
	if !common.IsRetryableTowardNetwork(err) {
		t.Fatal("real -32603 body on 429 must stay retryable")
	}
}

func TestExtract_HTTP5xx_BareStatus_BecomeRetryableServerSide(t *testing.T) {
	t.Parallel()
	for _, status := range []int{500, 502, 503} {
		err := extractNoJr(t, status)
		if !common.HasErrorCode(err, common.ErrCodeEndpointServerSideException) {
			t.Fatalf("HTTP %d: expected ErrEndpointServerSideException, got %T: %v", status, err, err)
		}
		if !common.IsRetryableTowardNetwork(err) {
			t.Fatalf("HTTP %d must stay retryable", status)
		}
	}
}

func TestExtract_HTTPOther4xx_BareStatus_BecomeNonRetryableClientSide(t *testing.T) {
	t.Parallel()
	for _, status := range []int{400, 404, 422} {
		err := extractNoJr(t, status)
		if !common.HasErrorCode(err, common.ErrCodeEndpointClientSideException) {
			t.Fatalf("HTTP %d: expected ErrEndpointClientSideException, got %T: %v", status, err, err)
		}
		if common.IsRetryableTowardNetwork(err) {
			t.Fatalf("HTTP %d must be non-retryable", status)
		}
	}
}

// A plaintext/HTML error page next to a failing HTTP status synthesizes a
// -32700 parse error upstream; the status must outrank it, otherwise every
// CDN 5xx would be condemned as a non-retryable client fault.
func TestExtract_SynthesizedParseError_ClassifiedFromFailingStatus(t *testing.T) {
	t.Parallel()
	synthesized := func() *common.ErrJsonRpcExceptionExternal {
		return common.NewErrJsonRpcExceptionExternal(
			int(common.JsonRpcErrorParseException),
			"cannot parse json-rpc response: invalid char", "")
	}
	for _, tc := range []struct {
		name      string
		status    int
		wantCode  common.ErrorCode
		retryable bool
	}{
		{"429 keeps capacity verdict", 429, common.ErrCodeEndpointCapacityExceeded, true},
		{"503 is an upstream failure", 503, common.ErrCodeEndpointServerSideException, true},
		{"400 is a request problem", 400, common.ErrCodeEndpointClientSideException, false},
		// No failing status to defer to: the -32700 speaks for itself.
		{"200 keeps the parse-error passthrough", 200, common.ErrCodeEndpointServerSideException, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := extractWith(t, synthesized(), tc.status)
			if !common.HasErrorCode(err, tc.wantCode) {
				t.Fatalf("HTTP %d + synthesized -32700: expected %s, got %T: %v",
					tc.status, tc.wantCode, err, err)
			}
			if got := common.IsRetryableTowardNetwork(err); got != tc.retryable {
				t.Errorf("HTTP %d + synthesized -32700: retryable=%v, want %v", tc.status, got, tc.retryable)
			}
		})
	}
}

func TestExtract_NoErrorResponse_NilBody_ReturnsNil(t *testing.T) {
	t.Parallel()
	e := NewJsonRpcErrorExtractor()
	r := &http.Response{StatusCode: 200, Header: http.Header{}}
	if got := e.Extract(r, nil, nil, newJsonRpcStub()); got != nil {
		t.Fatalf("expected nil for a clean 2xx with no error object, got %v", got)
	}
}

func TestExtract_NonJsonRpcUpstream_IsNoOp(t *testing.T) {
	t.Parallel()
	e := NewJsonRpcErrorExtractor()
	r := &http.Response{StatusCode: 500, Header: http.Header{}}
	stub := &stubJsonRpc{id: "evm-stub", typ: common.UpstreamTypeEvm}
	if got := e.Extract(r, nil, nil, stub); got != nil {
		t.Fatalf("expected nil for non-jsonrpc upstream, got %v", got)
	}
}

func TestExtract_NilUpstream_IsNoOp(t *testing.T) {
	t.Parallel()
	e := NewJsonRpcErrorExtractor()
	r := &http.Response{StatusCode: 500, Header: http.Header{}}
	if got := e.Extract(r, nil, nil, nil); got != nil {
		t.Fatalf("expected nil for nil upstream, got %v", got)
	}
}

// ---- helpers ---------------------------------------------------------------

type stubJsonRpc struct {
	id  string
	typ common.UpstreamType
}

func extractNoJr(t *testing.T, status int) error {
	t.Helper()
	e := NewJsonRpcErrorExtractor()
	r := &http.Response{StatusCode: status, Header: http.Header{}}
	return e.Extract(r, nil, nil, newJsonRpcStub())
}

func newJsonRpcStub() common.Upstream {
	return &stubJsonRpc{id: "jsonrpc-stub", typ: common.UpstreamTypeJsonRpc}
}

func (s *stubJsonRpc) Id() string           { return s.id }
func (s *stubJsonRpc) VendorName() string   { return "" }
func (s *stubJsonRpc) NetworkId() string    { return "jsonrpc:stub" }
func (s *stubJsonRpc) NetworkLabel() string { return "" }
func (s *stubJsonRpc) Config() *common.UpstreamConfig {
	return &common.UpstreamConfig{Id: s.id, Type: s.typ}
}
func (s *stubJsonRpc) Logger() *zerolog.Logger { l := zerolog.Nop(); return &l }
func (s *stubJsonRpc) Vendor() common.Vendor   { return nil }
func (s *stubJsonRpc) Tracker() common.HealthTracker {
	return nil
}
func (s *stubJsonRpc) Forward(_ context.Context, _ *common.NormalizedRequest, _, _ bool) (*common.NormalizedResponse, error) {
	return nil, nil
}
func (s *stubJsonRpc) ShouldHandleMethod(string) (bool, error) { return true, nil }
func (s *stubJsonRpc) Cordon(string, string)                   {}
func (s *stubJsonRpc) Uncordon(string, string)                 {}
func (s *stubJsonRpc) IgnoreMethod(string)                     {}

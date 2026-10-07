// Package jsonrpc implements the generic JSON-RPC 2.0 architecture (issue
// #1203): any plain JSON-RPC 2.0 endpoint (Starknet, Stellar RPC, NEAR, …)
// gets eRPC's transport-grade features with zero protocol logic. There is no
// state poller, no feature probing, no request/response normalization, and no
// architecture-level cache wiring — this handler's hooks are deliberately
// no-ops and the error extractor is the only architecture-specific behavior.
package jsonrpc

import (
	"fmt"
	"net/http"

	"github.com/erpc/erpc/common"
	"github.com/rs/zerolog"
)

type JsonRpcArchitecture struct{}

type JsonRpcErrorExtractor struct{}

func NewJsonRpcErrorExtractor() *JsonRpcErrorExtractor {
	return &JsonRpcErrorExtractor{}
}

// JSON-RPC 2.0 spec error codes. eRPC's common package only names a subset of
// them (-32600/-32602/-32603/-32700); the full client-side quartet is
// defined here so the classification reads directly off the spec.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// Extract classifies upstream failures for generic JSON-RPC 2.0 endpoints.
//
// JSON-RPC 2.0 spec error codes -32600/-32601/-32602 are client-side and
// non-retryable: the request itself is malformed, the method does not exist,
// or the params are wrong — every upstream answers identically, so retrying
// only multiplies the failure. Everything else (-32603 and all
// application-defined codes such as Starknet's server -32xxx spaces) is
// server-side and retryable.
//
// Two hard-won deferences sit on top of the raw codes (mirroring the svm
// extractor):
//
//   - HTTP status outranks the body only when the body carries no usable
//     JSON-RPC information. The parse layer synthesizes a -32700 error object
//     for any unparseable body, so a plaintext/HTML 429 or 5xx from a CDN
//     arrives here looking exactly like a JSON-RPC parse error; a well-formed
//     error object, however, is deliberate information from the real provider
//     and its code outranks the bare status (same deference evm applies).
//   - A -32700 on a 2xx is never the caller's fault either: eRPC serializes
//     the outbound request itself, so a parse complaint at this layer means
//     the UPSTREAM emitted bytes eRPC could not parse. It stays server-side
//     and retryable, and the raw code passes through to the client untouched.
//     (Documented deviation from the issue's literal "-32700 → client-side":
//     at this layer a -32700 is overwhelmingly the parse layer's own
//     synthesis and cannot be distinguished from an upstream-sent one; see
//     specs/jsonrpc-architecture/SPEC.md.)
func (e *JsonRpcErrorExtractor) Extract(r *http.Response, nr *common.NormalizedResponse, jr *common.JsonRpcResponse, ups common.Upstream) error {
	if ups == nil || ups.Config() == nil {
		return nil
	}
	if ups.Config().Type != common.UpstreamTypeJsonRpc {
		// Only classify errors for jsonrpc-architecture upstreams; never
		// interfere with evm/svm upstreams sharing the composite extractor.
		return nil
	}

	lg := ups.Logger()
	if lg == nil {
		l := zerolog.Nop()
		lg = &l
	}

	details := map[string]interface{}{
		"statusCode": r.StatusCode,
		"headers":    r.Header,
	}

	// Authentication failures first — the auth verdict outranks whatever the
	// body says. A 401/403 paired with a random error page must not be
	// classified from the body.
	if r.StatusCode == http.StatusUnauthorized || r.StatusCode == http.StatusForbidden {
		msg := fmt.Sprintf("jsonrpc upstream auth failure (HTTP %d)", r.StatusCode)
		if jr != nil && jr.Error != nil && jr.Error.Message != "" {
			msg = jr.Error.Message
		}
		return common.NewErrEndpointUnauthorized(
			common.NewErrJsonRpcExceptionInternal(0, common.JsonRpcErrorUnauthorized, msg, nil, details),
		)
	}

	// A failing HTTP status outranks the body ONLY when the body carries no
	// usable JSON-RPC information. The parse layer synthesizes a -32700 error
	// object for any unparseable body, so a plaintext/HTML 429 or 5xx from a
	// CDN arrives shaped like a parse error and must be classified from the
	// status, not condemned as a client fault. A well-formed error object,
	// however, is deliberate information from the real provider (the CDN
	// passed the request through) — its code outranks the bare status, which
	// is the same body-over-status deference evm applies.
	unparseable := jr != nil && jr.Error != nil && jr.Error.Code == codeParseError && r.StatusCode >= 400
	if jr == nil || jr.Error == nil || unparseable {
		switch {
		case r.StatusCode == http.StatusTooManyRequests:
			return common.NewErrEndpointCapacityExceeded(
				common.NewErrJsonRpcExceptionInternal(0, common.JsonRpcErrorCapacityExceeded,
					fmt.Sprintf("jsonrpc upstream rate limited (HTTP %d)", r.StatusCode),
					nil, details),
			)
		case r.StatusCode >= 500:
			return common.NewErrEndpointServerSideException(
				common.NewErrJsonRpcExceptionInternal(0, common.JsonRpcErrorServerSideException,
					fmt.Sprintf("jsonrpc upstream http failure %d", r.StatusCode),
					nil, details),
				details, r.StatusCode,
			)
		case r.StatusCode >= 400:
			// Other 4xx: request/config problem — do not retry across upstreams.
			wrapped := common.NewErrJsonRpcExceptionInternal(0, common.JsonRpcErrorClientSideException,
				fmt.Sprintf("jsonrpc upstream http failure %d", r.StatusCode),
				nil, details)
			return common.NewErrEndpointClientSideException(wrapped).WithRetryableTowardNetwork(false)
		default:
			// Clean 2xx with no error object: nothing to classify.
			return nil
		}
	}

	code := jr.Error.Code
	msg := jr.Error.Message

	// HTTP 2xx with a genuine JSON-RPC error object: classify by code. The
	// original numeric code passes through verbatim (as the normalized code)
	// so chain-native clients (starknet.js, soroban rpc, near-api-js) keep
	// dispatching on their own error numbers.
	switch code {
	case codeInvalidRequest, codeMethodNotFound, codeInvalidParams:
		// The request itself is the problem; every upstream answers
		// identically. WithRetryableTowardNetwork(false) scopes the opt-out
		// to this verdict only.
		wrapped := common.NewErrJsonRpcExceptionInternal(code, common.JsonRpcErrorNumber(code), msg, nil, details)
		return common.NewErrEndpointClientSideException(wrapped).WithRetryableTowardNetwork(false)

	case codeParseError:
		// Synthesized by the parse layer (or an upstream complaining about
		// bytes eRPC serialized): the upstream emitted something that is not a
		// JSON-RPC envelope. That is an upstream fault — retryable, raw code
		// passes through to the client.
		return common.NewErrEndpointServerSideException(
			common.NewErrJsonRpcExceptionInternal(code, common.JsonRpcErrorNumber(code), msg, nil, details),
			details, r.StatusCode,
		)

	default:
		// -32603 Internal error and all application-defined error codes:
		// another upstream may succeed, so the request fails over.
		return common.NewErrEndpointServerSideException(
			common.NewErrJsonRpcExceptionInternal(code, common.JsonRpcErrorNumber(code), msg, nil, details),
			details, r.StatusCode,
		)
	}
}

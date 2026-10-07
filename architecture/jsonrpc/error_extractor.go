package jsonrpc

import (
	"fmt"
	"net/http"

	"github.com/erpc/erpc/common"
	"github.com/erpc/erpc/util"
)

// Generic JSON-RPC 2.0 error codes (§5.1 of the spec). These four — and only
// these four — are classified as non-retryable client-side errors: the request
// itself is malformed, the method/params are unacceptable, and resending it
// unchanged to any upstream will fail the same way. Everything else
// (incl. -32603 internal error and every application-defined server error)
// is treated as retryable server-side so the proxy can fail over.
const (
	codeParseError     = -32700 // JSON-RPC 2.0 spec
	codeInvalidRequest = -32600 // JSON-RPC 2.0 spec
	codeMethodNotFound = -32601 // JSON-RPC 2.0 spec
	codeInvalidParams  = -32602 // JSON-RPC 2.0 spec
)

type JsonRpcErrorExtractor struct{}

func NewJsonRpcErrorExtractor() *JsonRpcErrorExtractor {
	return &JsonRpcErrorExtractor{}
}

// Extract normalizes errors from a type: jsonrpc upstream. Guarded on the
// upstream type so the composite extractor's registry iteration never
// misfires on evm/svm upstreams.
func (e *JsonRpcErrorExtractor) Extract(
	resp *http.Response,
	nr *common.NormalizedResponse,
	jr *common.JsonRpcResponse,
	upstream common.Upstream,
) error {
	if upstream == nil || upstream.Config() == nil || upstream.Config().Type != common.UpstreamTypeJsonRpc {
		// Not a jsonrpc upstream — let the composite extractor fall through.
		return nil
	}
	if resp == nil {
		return nil
	}

	details := map[string]interface{}{
		"statusCode": resp.StatusCode,
		"headers":    util.ExtractUsefulHeaders(resp),
	}

	code := 0
	msg := ""
	if jr != nil && jr.Error != nil {
		code = jr.Error.Code
		msg = jr.Error.Message
		if d := jr.Error.Data; d != nil {
			if s, isStr := d.(string); !isStr || s != "" {
				details["data"] = d
			}
		}
	}

	// HTTP auth failures outrank the JSON-RPC body: a 401/403 is a verdict
	// about the credential, not the call.
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		authMsg := msg
		if authMsg == "" {
			authMsg = fmt.Sprintf("jsonrpc upstream unauthorized (HTTP %d)", resp.StatusCode)
		}
		return common.NewErrEndpointUnauthorized(
			common.NewErrJsonRpcExceptionInternal(code, common.JsonRpcErrorNumber(code), authMsg, nil, details),
		)
	}

	// A synthesized -32700 next to a failing HTTP status means the body told
	// us nothing (CDN/nginx plaintext 429s, HTML error pages). Classify from
	// the status — the only trustworthy signal left — instead of condemning
	// the request as a non-retryable parse error.
	unparseableBody := code == codeParseError && resp.StatusCode >= 400
	if jr == nil || jr.Error == nil || unparseableBody {
		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			return common.NewErrEndpointCapacityExceeded(
				common.NewErrJsonRpcExceptionInternal(code, common.JsonRpcErrorCapacityExceeded, msg, nil, details),
			)
		case resp.StatusCode >= 500:
			if msg == "" {
				msg = fmt.Sprintf("jsonrpc upstream HTTP %d", resp.StatusCode)
			}
			return common.NewErrEndpointServerSideException(
				common.NewErrJsonRpcExceptionInternal(code, common.JsonRpcErrorServerSideException, msg, nil, details),
				details,
				resp.StatusCode,
			)
		case resp.StatusCode >= 400:
			if msg == "" {
				msg = fmt.Sprintf("jsonrpc upstream rejected the request (HTTP %d)", resp.StatusCode)
			}
			return common.NewErrEndpointClientSideException(
				common.NewErrJsonRpcExceptionInternal(code, common.JsonRpcErrorClientSideException, msg, nil, details),
			)
		default:
			// 2xx with no JSON-RPC error object — nothing to extract.
			return nil
		}
	}

	// The upstream sent a genuine JSON-RPC error object. The routing verdict
	// lives in the outer StandardError class; the numeric code passes through
	// verbatim so chain-native clients (starknet.js, soroban rpc, near-api-js)
	// keep dispatching on their own error numbers.
	switch code {
	case codeParseError, codeInvalidRequest, codeMethodNotFound, codeInvalidParams:
		return common.NewErrEndpointClientSideException(
			common.NewErrJsonRpcExceptionInternal(code, common.JsonRpcErrorNumber(code), msg, nil, details),
		)
	default:
		// -32603 internal error and all application-defined codes: retryable.
		return common.NewErrEndpointServerSideException(
			common.NewErrJsonRpcExceptionInternal(code, common.JsonRpcErrorNumber(code), msg, nil, details),
			details,
			resp.StatusCode,
		)
	}
}

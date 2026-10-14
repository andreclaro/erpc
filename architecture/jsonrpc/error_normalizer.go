package jsonrpc

import (
	"net/http"

	"github.com/erpc/erpc/common"
	"github.com/erpc/erpc/util"
)

// JSON-RPC 2.0 spec error codes. These four are the caller's fault by
// definition — no upstream will accept the request, so retrying is pure waste.
// Everything else falls through to the generic server-side classification in
// clients/http_json_rpc_client.go (retryable), per specs/generic-jsonrpc/feature.md §6.
const (
	jsonRpcCodeParseError     = -32700
	jsonRpcCodeInvalidRequest = -32600
	jsonRpcCodeMethodNotFound = -32601
	jsonRpcCodeInvalidParams  = -32602
)

// Classification classes from JsonRpcClassificationRuleConfig. Only these three
// map onto the error taxonomy; anything else a config might carry is treated as
// no-match so the v1 fallthrough governs.
const (
	classificationMissingData = "missingData"
	classificationClientError = "clientError"
	classificationServerError = "serverError"
)

// JsonRpcErrorExtractor translates generic JSON-RPC upstream errors into eRPC's
// StandardError taxonomy. It intentionally no-ops on non-jsonrpc upstreams so a
// mixed project can share a single composite extractor.
//
// The extractor carries NO protocol knowledge (design razor): beyond the four
// JSON-RPC 2.0 spec codes — which are caller-fault by definition — every
// classification comes from operator-declared config (network errorRules and
// per-method resultClassifications), evaluated in order, first match wins.
type JsonRpcErrorExtractor struct{}

func NewJsonRpcErrorExtractor() *JsonRpcErrorExtractor {
	return &JsonRpcErrorExtractor{}
}

func (e *JsonRpcErrorExtractor) Extract(
	resp *http.Response,
	nr *common.NormalizedResponse,
	jr *common.JsonRpcResponse,
	upstream common.Upstream,
) error {
	if upstream == nil || upstream.Config() == nil || upstream.Config().Type != common.UpstreamTypeJsonRpc {
		// Not a generic jsonrpc upstream — let the composite extractor fall through.
		return nil
	}
	if resp == nil {
		return nil
	}

	// Extract details up front — reused by every branch below. Never embed
	// request params; the details key on codes/paths/status only (spec §11 /
	// feature-v2 §4.3 sanitation note).
	details := map[string]interface{}{
		"statusCode": resp.StatusCode,
		"headers":    util.ExtractUsefulHeaders(resp),
		"upstreamId": upstream.Id(),
	}

	code := 0
	msg := ""
	if jr != nil && jr.Error != nil {
		code = jr.Error.Code
		msg = jr.Error.Message

		// Carry the upstream's "data" member through verbatim, exactly like the
		// SVM normalizer: structured causes (NEAR's verror object) live there and
		// callers read them. An empty string is a "no data" placeholder, not a
		// payload.
		if d := jr.Error.Data; d != nil {
			if s, isStr := d.(string); !isStr || s != "" {
				details["data"] = d
			}
		}
	}

	// Configured classification (feature-v2 §4.3): network errorRules first
	// (they cover error envelopes AND result-embedded shapes), then per-method
	// resultClassifications (result-embedded only — those shapes live in success
	// envelopes, so they apply only when there is no JSON-RPC error). Network
	// rules need the jsonrpc config block; per-method rules live under
	// methods.definitions and apply on any jsonrpc-architecture network.
	if nc, ok := upstream.(interface{ NetworkConfig() *common.NetworkConfig }); ok {
		if networkCfg := nc.NetworkConfig(); networkCfg != nil &&
			networkCfg.Architecture == common.ArchitectureJsonRpc {

			respRoot := buildResponseRoot(jr)
			var resultSub, errSub interface{}
			if r, exists := respRoot["result"]; exists {
				resultSub = r
			}
			if er, exists := respRoot["error"]; exists {
				errSub = er
			}

			if jsonrpcCfg := networkCfg.Jsonrpc; jsonrpcCfg != nil {
				if class, subCode, matched := classifyConfiguredRules(jsonrpcCfg.ErrorRules, respRoot, resultSub, errSub); matched {
					return configuredClassError(class, subCode, code, msg, resp, details, upstream)
				}
			}

			if jr == nil || jr.Error == nil {
				if method := responseRequestMethod(nr); method != "" {
					if mc := networkCfg.Methods.FindMethodConfig(method); mc != nil {
						if class, subCode, matched := classifyConfiguredRules(mc.ResultClassifications, respRoot, resultSub, errSub); matched {
							return configuredClassError(class, subCode, code, msg, resp, details, upstream)
						}
					}
				}
			}
		}
	}

	// JSON-RPC 2.0 spec codes: caller-fault, never retryable toward the network.
	if jr == nil || jr.Error == nil {
		return nil
	}
	switch code {
	case jsonRpcCodeParseError, jsonRpcCodeInvalidRequest, jsonRpcCodeMethodNotFound, jsonRpcCodeInvalidParams:
		wrapped := common.NewErrJsonRpcExceptionInternal(code, jsonRpcWireCode(code, common.JsonRpcErrorClientSideException), msg, nil, details)
		return common.NewErrEndpointClientSideException(wrapped).WithRetryableTowardNetwork(false)
	}

	// Unknown code — not ours. The generic server-side fallthrough classifies it
	// retryable (v1 §6); a NOT_FOUND can be transient and must be retryable.
	return nil
}

// classifyConfiguredRules evaluates an ordered rule list, first match wins.
// A matched rule with a codeMap extracts the sub-code via codePath and maps it
// to a class, falling back to the rule default; when that yields no known class
// (empty sub-code, unmapped code, no default) the rule is treated as no-match
// and the next rule is consulted. Validation enforces default-when-codeMap, so
// the fall-through exists only for hand-built configs.
func classifyConfiguredRules(rules []*common.JsonRpcClassificationRuleConfig, respRoot map[string]interface{}, resultSub, errSub interface{}) (class string, subCode string, matched bool) {
	for _, rule := range rules {
		if rule == nil || rule.Match == nil {
			continue
		}
		if !matchClassificationMatch(rule.Match, respRoot, resultSub, errSub) {
			continue
		}
		if len(rule.CodeMap) > 0 {
			if rule.CodePath != "" {
				if v, ok := walkDotPath(respRoot, rule.CodePath); ok {
					subCode, _ = stringifyMatchValue(v)
				}
			}
			if subCode != "" {
				if c, exists := rule.CodeMap[subCode]; exists && isKnownClass(c) {
					return c, subCode, true
				}
			}
			if isKnownClass(rule.Default) {
				return rule.Default, subCode, true
			}
			continue
		}
		if isKnownClass(rule.ClassifyAs) {
			return rule.ClassifyAs, "", true
		}
	}
	return "", "", false
}

func isKnownClass(class string) bool {
	switch class {
	case classificationMissingData, classificationClientError, classificationServerError:
		return true
	}
	return false
}

// configuredClassError maps a matched classification class onto the error
// taxonomy. The upstream's native code stays on the wire whenever there is one
// (generic chains' clients dispatch on vendor codes, same invariant as the SVM
// normalizer); result-embedded shapes have no JSON-RPC code, so the class
// constant becomes the wire code.
func configuredClassError(class, subCode string, code int, msg string, resp *http.Response, details map[string]interface{}, upstream common.Upstream) error {
	if msg == "" && subCode != "" {
		msg = subCode
	}
	switch class {
	case classificationMissingData:
		// Retryable missing-data: another upstream can genuinely have this data
		// (archival vs pruned node). NEVER permanent — a NOT_FOUND can be a
		// pending transaction, so a time-delayed re-fetch is meaningful.
		return common.NewErrEndpointMissingData(
			common.NewErrJsonRpcExceptionInternal(code, jsonRpcWireCode(code, common.JsonRpcErrorMissingData), msg, nil, details),
			upstream,
		)
	case classificationClientError:
		wrapped := common.NewErrJsonRpcExceptionInternal(code, jsonRpcWireCode(code, common.JsonRpcErrorClientSideException), msg, nil, details)
		return common.NewErrEndpointClientSideException(wrapped).WithRetryableTowardNetwork(false)
	case classificationServerError:
		return common.NewErrEndpointServerSideException(
			common.NewErrJsonRpcExceptionInternal(code, jsonRpcWireCode(code, common.JsonRpcErrorServerSideException), msg, nil, details),
			details, resp.StatusCode,
		)
	}
	return nil
}

// jsonRpcWireCode preserves the upstream's native code on the wire; the
// fallback applies when the response carried no numeric code (result-embedded
// shapes) and exists because NewErrJsonRpcExceptionInternal drops a zero
// normalized code, which would put "code": 0 on the wire.
func jsonRpcWireCode(originalCode int, fallback common.JsonRpcErrorNumber) common.JsonRpcErrorNumber {
	if originalCode == 0 {
		return fallback
	}
	return common.JsonRpcErrorNumber(originalCode)
}

// responseRequestMethod resolves the request method from the response's
// attached request. Nil-safe at every step: without a resolvable method the
// per-method classification is simply skipped.
func responseRequestMethod(nr *common.NormalizedResponse) string {
	if nr == nil {
		return ""
	}
	req := nr.Request()
	if req == nil {
		return ""
	}
	method, _ := req.Method()
	return method
}

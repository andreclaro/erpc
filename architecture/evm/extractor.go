package evm

import (
	"net/http"

	"github.com/erpc/erpc/common"
)

// JsonRpcErrorExtractor implements common.JsonRpcErrorExtractor for EVM
// by delegating to ExtractJsonRpcError.
type JsonRpcErrorExtractor struct{}

func NewJsonRpcErrorExtractor() common.JsonRpcErrorExtractor { return &JsonRpcErrorExtractor{} }

func (e *JsonRpcErrorExtractor) Extract(resp *http.Response, nr *common.NormalizedResponse, jr *common.JsonRpcResponse, upstream common.Upstream) error {
	// The composite runs extractors in sorted order (evm before svm); without this
	// guard the EVM extractor would claim SVM JSON-RPC codes first and drop the SVM
	// taxonomy (incl. the sendTransaction non-retryable-toward-network guard).
	// Empty Type is treated as EVM: bootstrap defaults apply UpstreamTypeEvm, but
	// tests and exotic direct-config callers may skip that pass — historically the
	// EVM extractor owned those upstreams, so it keeps them.
	if upstream != nil && upstream.Config() != nil {
		t := upstream.Config().Type
		if t != "" && t != common.UpstreamTypeEvm {
			return nil
		}
	}
	return ExtractJsonRpcError(resp, nr, jr, upstream)
}

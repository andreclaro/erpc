package jsonrpc

import (
	"context"

	"github.com/erpc/erpc/common"
)

func init() {
	common.RegisterArchitecture(common.ArchitectureJsonRpc, NewJsonRpcArchitectureHandler())
}

// JsonRpcArchitectureHandler is the registry hook that exposes the generic
// JSON-RPC architecture to the generic pipeline. The architecture carries no
// protocol logic (see specs/generic-jsonrpc/feature.md): every pre/post-forward
// hook is a pass-through, and the only behavior lives in the error extractor
// (configured error-shape classification + JSON-RPC 2.0 spec codes).
type JsonRpcArchitectureHandler struct{}

func NewJsonRpcArchitectureHandler() *JsonRpcArchitectureHandler {
	return &JsonRpcArchitectureHandler{}
}

func (h *JsonRpcArchitectureHandler) HandleProjectPreForward(ctx context.Context, network common.Network, req *common.NormalizedRequest) (bool, *common.NormalizedResponse, error) {
	return false, nil, nil
}

func (h *JsonRpcArchitectureHandler) HandleNetworkPreForward(ctx context.Context, network common.Network, upstreams []common.Upstream, req *common.NormalizedRequest) (bool, *common.NormalizedResponse, error) {
	return false, nil, nil
}

func (h *JsonRpcArchitectureHandler) HandleNetworkPostForward(ctx context.Context, network common.Network, req *common.NormalizedRequest, resp *common.NormalizedResponse, err error) (*common.NormalizedResponse, error) {
	return resp, err
}

func (h *JsonRpcArchitectureHandler) HandleUpstreamPreForward(ctx context.Context, network common.Network, upstream common.Upstream, req *common.NormalizedRequest, skipCacheRead bool) (bool, *common.NormalizedResponse, error) {
	return false, nil, nil
}

func (h *JsonRpcArchitectureHandler) HandleUpstreamPostForward(ctx context.Context, network common.Network, upstream common.Upstream, req *common.NormalizedRequest, resp *common.NormalizedResponse, err error, skipCacheRead bool) (*common.NormalizedResponse, error) {
	return resp, err
}

func (h *JsonRpcArchitectureHandler) NewJsonRpcErrorExtractor() common.JsonRpcErrorExtractor {
	return NewJsonRpcErrorExtractor()
}

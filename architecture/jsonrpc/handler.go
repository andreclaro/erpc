package jsonrpc

import (
	"context"

	"github.com/erpc/erpc/common"
)

func init() {
	common.RegisterArchitecture(common.ArchitectureJsonRpc, &JsonRpcArchitectureHandler{})
}

// JsonRpcArchitectureHandler is the minimal ArchitectureHandler: the generic
// jsonrpc architecture carries NO protocol logic, so every hook is a
// pass-through. Its only real payload is the error extractor (see
// error_extractor.go) and the registration itself, which puts "jsonrpc" on
// the architecture registry so config parsing, routing, and the pipeline
// accept jsonrpc networks.
type JsonRpcArchitectureHandler struct{}

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

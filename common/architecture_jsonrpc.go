package common

import (
	"context"
)

const (
	UpstreamTypeJsonRpc UpstreamType = "jsonrpc"
)

// JsonRpcUpstream narrows common.Upstream to the surface the generic
// JSON-RPC architecture needs to reach into per-upstream state (currently
// just the state poller). Parallels the SvmUpstream / EvmUpstream pattern.
type JsonRpcUpstream interface {
	Upstream
	JsonRpcStatePoller() JsonRpcStatePoller
}

// JsonRpcStatePoller is the per-upstream tip/floor/health tracker for generic
// JSON-RPC networks. It exists only when the network's jsonrpc.statePoller
// config block is present; the concrete type lives in architecture/jsonrpc.
type JsonRpcStatePoller interface {
	Bootstrap(ctx context.Context) error
	IsObjectNull() bool

	// Configure applies the network-level probe configuration and starts the
	// poll loop on first invocation. Wired from JsonrpcNetworkConfig.StatePoller
	// via upstream.SetNetworkConfig.
	Configure(cfg *JsonRpcStatePollerConfig) error

	Poll(ctx context.Context) error

	LatestTip() int64
	FinalizedTip() int64
	IsHealthy() bool
}

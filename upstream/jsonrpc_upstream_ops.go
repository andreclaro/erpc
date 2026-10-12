package upstream

import "github.com/erpc/erpc/common"

// JsonRpcStatePoller exposes the per-upstream generic JSON-RPC tip tracker
// for hooks, caches, and tests. Nil unless the upstream has type jsonrpc.
func (u *Upstream) JsonRpcStatePoller() common.JsonRpcStatePoller {
	return u.jsonRpcStatePoller
}

// NetworkConfig returns the network config this upstream was registered
// against (via SetNetworkConfig), or nil if not associated yet. The generic
// JSON-RPC extractor uses it to reach error-classification rules and method
// declarations without a direct network reference. The returned config must
// be treated as read-only.
func (u *Upstream) NetworkConfig() *common.NetworkConfig {
	return u.networkCfg.Load()
}

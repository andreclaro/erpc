package common

// UpstreamTypeJsonRpc marks an upstream that serves a generic JSON-RPC 2.0
// endpoint through the jsonrpc architecture. Unlike evm/svm there is NO
// feature probing or state poller: the upstream is bound to its network
// purely by config (jsonRpc.slug -> network jsonrpc:<slug>).
const (
	UpstreamTypeJsonRpc UpstreamType = "jsonrpc"
)

// JsonRpcNetworkConfig holds the per-network settings for the generic
// jsonrpc architecture. The slug is the entire network identity — it feeds
// the jsonrpc:<slug> network ID and the /<project>/jsonrpc/<slug> route.
// There is deliberately no chainId/cluster/finality machinery here: the
// jsonrpc architecture carries no protocol logic.
type JsonRpcNetworkConfig struct {
	Slug string `yaml:"slug" json:"slug" tstype:"string"`
}

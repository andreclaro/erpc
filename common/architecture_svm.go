package common

import "context"

const (
	UpstreamTypeSvm UpstreamType = "svm"
)

// SlotSharedVariable mirrors the EVM SharedStateVariable pattern for int64 slot numbers.
// It lets the state poller publish slot progress to a shared-state backend (memory or Redis)
// while keeping callers synchronous via the cached local value.
type SlotSharedVariable interface {
	GetValue() int64
	TryUpdate(ctx context.Context, newValue int64) int64
	OnValue(callback func(int64))
	OnLargeRollback(callback func(currentVal, newVal int64))
}

// SvmUpstream narrows common.Upstream to the surface SVM hooks need to reach
// into per-upstream state (currently just the state poller). Keeping this a
// tiny sub-interface avoids committing the full Upstream struct to common/ and
// parallels the existing EvmUpstream pattern.
type SvmUpstream interface {
	Upstream
	SvmStatePoller() SvmStatePoller
}

// SvmStatePoller is the per-upstream slot/health tracker. Concrete type lives in
// architecture/svm to avoid pulling the full implementation into common.
type SvmStatePoller interface {
	Bootstrap(ctx context.Context) error
	IsObjectNull() bool

	Poll(ctx context.Context) error

	LatestSlot() int64
	FinalizedSlot() int64
	MaxShredInsertSlotLag() int64
	IsHealthy() bool

	SuggestLatestSlot(slot int64)
	SuggestFinalizedSlot(slot int64)
}

// MaxShredInsertSlotLagThreshold is the cutoff beyond which an upstream is treated
// as degraded for shred-insert lag. Solana nodes with high `latest - maxShredInsertSlot`
// are receiving shreds but not processing them; their reads go stale silently.
const MaxShredInsertSlotLagThreshold int64 = 100

// knownSvmClusters maps cluster name → immutable genesis hash.
// Genesis hashes are the hash of block 0 and never change, so we can validate
// upstream cluster membership at bootstrap without an RPC call.
//
// Onboarding a new cluster:
//   - Obtain the genesis hash once via `curl -X POST -d '{"method":"getGenesisHash"}'`
//     against any trusted node of that cluster.
//   - Add the (cluster, hash) row below.
//
// Do NOT add a cluster with an empty hash — that would cost an RPC per upstream
// bootstrap (svmVerifyGenesisHash would call getGenesisHash) without any
// comparison happening. Better to leave the cluster absent; operators can still
// run it via CheckGenesisHash:true, which opts in to runtime fetch+compare
// against another of their own upstreams' responses.
//
// Non-Solana SVM-compatible chains (Fogo, Eclipse, custom forks) are expected
// to be added here as they're onboarded for eRPC Phase 2+ support.
var knownSvmClusters = map[string]string{
	"mainnet-beta": "5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2N9d",
	"devnet":       "EtWTRABZaYq6iMfeYKouRu166VU2xqa1wcaWoxPkrZBG",
	"testnet":      "4uhcVJyU9pJkvQyS88uRDiswHXSCkY3zQawwpjk2NsNY",
}

// IsValidSvmCluster returns true when the cluster name is a recognized SVM network.
// Unknown clusters (e.g. localnet, fogo-mainnet) must set CheckGenesisHash:true
// on the upstream config to opt in to runtime validation.
func IsValidSvmCluster(cluster string) bool {
	_, ok := knownSvmClusters[cluster]
	return ok
}

// KnownGenesisHash returns the hardcoded genesis hash for a cluster, or "" if unknown.
// Callers can tell "unknown cluster" (ok=false, skip check) from "known but empty"
// (ok=true, use the returned hash).
func KnownGenesisHash(cluster string) (string, bool) {
	h, ok := knownSvmClusters[cluster]
	return h, ok
}

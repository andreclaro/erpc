package svm

import (
	"context"
	"strings"

	"github.com/erpc/erpc/common"
)

// neverCacheMethods returns realtime finality — forcing the cache layer to skip
// these entirely. These are either transient (latest blockhash, priority fees)
// or mutating (sendTransaction, requestAirdrop).
var neverCacheMethods = map[string]bool{
	"getLatestBlockhash":          true,
	"getRecentBlockhash":          true,
	"getFeeForMessage":            true,
	"sendTransaction":             true,
	"sendRawTransaction":          true,
	"simulateTransaction":         true,
	"getSignatureStatuses":        true,
	"getVoteAccounts":             true,
	"getLeaderSchedule":           true,
	"getEpochInfo":                true,
	"getEpochSchedule":            true,
	"getSlotLeaders":              true,
	"getRecentPerformanceSamples": true,
	"getRecentPrioritizationFees": true,
	"requestAirdrop":              true,
}

// alwaysFinalizedMethods return finalized data by their nature — regardless of
// the request's commitment param, the response is safe to treat as final.
// getBlock/getTransaction in particular only return confirmed/finalized rollups.
var alwaysFinalizedMethods = map[string]bool{
	"getBlock":                true,
	"getTransaction":          true,
	"getInflationReward":      true,
	"getBlocks":               true,
	"getBlockTime":            true,
	"getSignaturesForAddress": true,
}

// GetFinality resolves the finality of an SVM request/response pair. It is
// intentionally a free function (not a method on SvmArchitectureHandler) so
// erpc/networks.go can call it without taking a dependency on the handler's
// concrete type. Priority:
//
//  1. neverCacheMethods       → realtime (no cache)
//  2. alwaysFinalizedMethods  → finalized
//  3. Explicit commitment param in request → maps directly
//  4. Default (no commitment) → unfinalized (safe default; TTL kicks in)
func GetFinality(ctx context.Context, network common.Network, req *common.NormalizedRequest, _ *common.NormalizedResponse) common.DataFinalityState {
	if req == nil {
		return common.DataFinalityStateUnknown
	}

	method, _ := req.Method()
	if method == "" {
		return common.DataFinalityStateUnknown
	}

	if neverCacheMethods[method] {
		return common.DataFinalityStateRealtime
	}
	if alwaysFinalizedMethods[method] {
		return common.DataFinalityStateFinalized
	}

	switch extractCommitment(ctx, req) {
	case "finalized":
		return common.DataFinalityStateFinalized
	case "confirmed", "processed":
		return common.DataFinalityStateUnfinalized
	}

	// Fall back to the network's default commitment if one is configured.
	if cfg := network.Config(); cfg != nil && cfg.Svm != nil {
		switch strings.ToLower(cfg.Svm.Commitment) {
		case "finalized":
			return common.DataFinalityStateFinalized
		case "confirmed", "processed":
			return common.DataFinalityStateUnfinalized
		}
	}

	return common.DataFinalityStateUnfinalized
}

// extractCommitment pulls a commitment string out of the request params. Solana
// methods typically accept an options object as the final param, shaped
// {commitment: "confirmed", ...}. We scan any map-typed param, not just the last
// one, because vendor forks occasionally place config objects earlier.
func extractCommitment(ctx context.Context, req *common.NormalizedRequest) string {
	jrq, err := req.JsonRpcRequest(ctx)
	if err != nil || jrq == nil {
		return ""
	}
	jrq.RLock()
	defer jrq.RUnlock()
	for _, p := range jrq.Params {
		m, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		if v, ok := m["commitment"].(string); ok && v != "" {
			return strings.ToLower(v)
		}
	}
	return ""
}

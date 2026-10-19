package jsonrpc

import (
	"context"
	"strings"

	"github.com/erpc/erpc/common"
)

// GetFinality resolves the finality of a generic JSON-RPC request. It is
// intentionally a free function (not a method on JsonRpcArchitectureHandler) so
// erpc/networks.go can call it without depending on the handler's concrete
// type — mirroring svm.GetFinality.
//
// erpc/networks.go evaluates the method config's static Finalized/Realtime
// flags BEFORE dispatching here (that check is architecture-agnostic and
// unchanged). The same flags are repeated below so direct callers of this
// function (tests, cache paths driving off standalone Network fakes) resolve
// identically; the duplication is behavior-preserving because both checks read
// the same immutable config. This function then handles the jsonrpc-specific
// declarations:
//
//   - cfg.Finalized          → DataFinalityStateFinalized (immutable per params)
//   - cfg.Realtime           → DataFinalityStateRealtime (moving head)
//   - cfg.Unfinalized        → DataFinalityStateUnfinalized (pinned but
//     fork-droppable until the chain's finality catches up)
//   - cfg.FinalityFromParam  → finality carried by a request param (NEAR's
//     "finality" key, Starknet's positional block tag, XRPL's ledger_index)
//   - neither (or no method declaration) → DataFinalityStateUnknown, the v1
//     answer for everything on a generic network
//
// Nothing here inspects chain identity: the operator's map IS the assertion
// about the chain's finality model (feature-v2 §4.2).
func GetFinality(ctx context.Context, network common.Network, req *common.NormalizedRequest, _ *common.NormalizedResponse) common.DataFinalityState {
	if req == nil {
		return common.DataFinalityStateUnknown
	}
	method, _ := req.Method()
	if method == "" {
		return common.DataFinalityStateUnknown
	}

	var cfg *common.CacheMethodConfig
	if network != nil {
		if nc := network.Config(); nc != nil {
			cfg = nc.Methods.FindMethodConfig(method)
		}
	}
	if cfg == nil {
		return common.DataFinalityStateUnknown
	}
	// Mirrors the config-driven section of erpc/networks.go (which runs before
	// architecture dispatch and therefore also governs the production path).
	if cfg.Finalized {
		return common.DataFinalityStateFinalized
	}
	if cfg.Realtime {
		return common.DataFinalityStateRealtime
	}
	if cfg.Unfinalized {
		return common.DataFinalityStateUnfinalized
	}
	if ffp := cfg.FinalityFromParam; ffp != nil {
		return resolveFinalityFromParam(ctx, req, ffp)
	}
	return common.DataFinalityStateUnknown
}

// resolveFinalityFromParam extracts the finality-carrying param from the
// request and maps it through the operator's table. Key form scans every
// object param for the key (first string value wins — mirrors how svm's
// resolveCommitment scans params); Index form takes the positional param.
// Absent param, non-string value (e.g. Starknet's {block_number} object or
// XRPL's single-object params array at index 0), or an unmapped value all
// land on the configured default — spec requires the default explicitly so
// there is no silent unknown.
func resolveFinalityFromParam(ctx context.Context, req *common.NormalizedRequest, ffp *common.FinalityFromParamConfig) common.DataFinalityState {
	rpcReq, err := req.JsonRpcRequest(ctx)
	if err != nil || rpcReq == nil {
		return parseDataFinalityState(ffp.Default)
	}
	rpcReq.RLock()
	defer rpcReq.RUnlock()

	var raw interface{}
	found := false
	switch {
	case ffp.Key != "":
		for _, p := range rpcReq.Params {
			m, ok := p.(map[string]interface{})
			if !ok {
				continue
			}
			if v, ok := m[ffp.Key]; ok {
				if s, isStr := v.(string); isStr && s != "" {
					raw, found = s, true
					break
				}
				// Present but non-string: this is the declared key, so the shape
				// is the operator's problem — stop scanning and take the default.
				found = false
				break
			}
		}
	case ffp.Index != nil:
		idx := *ffp.Index
		if idx >= 0 && idx < len(rpcReq.Params) {
			raw, found = rpcReq.Params[idx], true
		}
	}
	if !found {
		return parseDataFinalityState(ffp.Default)
	}
	s, ok := raw.(string)
	if !ok {
		return parseDataFinalityState(ffp.Default)
	}
	return mapFinalityValue(ffp, s)
}

// mapFinalityValue prefers an exact map-key match, then the lowercased/trimmed
// value, then a case-insensitive key fold — chains vary in the casing they
// accept and operators copy-paste tables.
func mapFinalityValue(ffp *common.FinalityFromParamConfig, value string) common.DataFinalityState {
	if f, ok := ffp.Map[value]; ok {
		return parseDataFinalityState(f)
	}
	norm := strings.ToLower(strings.TrimSpace(value))
	if f, ok := ffp.Map[norm]; ok {
		return parseDataFinalityState(f)
	}
	for k, f := range ffp.Map {
		if strings.EqualFold(strings.TrimSpace(k), norm) {
			return parseDataFinalityState(f)
		}
	}
	return parseDataFinalityState(ffp.Default)
}

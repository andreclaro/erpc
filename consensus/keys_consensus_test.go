package consensus

import (
	"context"
	"testing"

	"github.com/erpc/erpc/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeGetBlockResponse builds a NormalizedResponse for SVM getBlock with the
// supplied top-level fields. transactions may be any JSON-serializable value.
func makeGetBlockResponse(t *testing.T, blockhash, prevHash string, parentSlot, blockHeight int64, transactions any) *common.NormalizedResponse {
	t.Helper()
	result := map[string]any{
		"blockhash":         blockhash,
		"previousBlockhash": prevHash,
		"parentSlot":        parentSlot,
		"blockHeight":       blockHeight,
		"blockTime":         1700000000,
		"transactions":      transactions,
		"rewards":           []any{},
	}
	jrr, err := common.NewJsonRpcResponse(1, result, nil)
	require.NoError(t, err)
	req := common.NewNormalizedRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"getBlock","params":[100]}`))
	return common.NewNormalizedResponse().WithRequest(req).WithJsonRpcResponse(jrr)
}

// TestConsensusKey_Blockhash_GroupsByBlockhashOnly verifies that two getBlock
// responses with the same blockhash but different transaction payloads are
// treated as agreeing when key=blockhash.
func TestConsensusKey_Blockhash_GroupsByBlockhashOnly(t *testing.T) {
	ctx := context.WithValue(context.Background(), common.RequestContextKey,
		common.NewNormalizedRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"getBlock","params":[100]}`)))

	cfg := &config{key: ConsensusKeyBlockhash}
	r1 := &execResult{Result: makeGetBlockResponse(t, "bh1", "ph1", 99, 95, []any{map[string]any{"tx": "a"}})}
	r2 := &execResult{Result: makeGetBlockResponse(t, "bh1", "ph1", 99, 95, []any{map[string]any{"tx": "b"}})}

	classifyAndHashResponse(r1, ctx, cfg)
	classifyAndHashResponse(r2, ctx, cfg)

	assert.Equal(t, "svm:bh1", r1.CachedHash)
	assert.Equal(t, r1.CachedHash, r2.CachedHash)
}

// TestConsensusKey_BlockhashWithLink_GroupsByLinkFields verifies that the key
// includes previousBlockhash, parentSlot, and blockHeight.
func TestConsensusKey_BlockhashWithLink_GroupsByLinkFields(t *testing.T) {
	ctx := context.WithValue(context.Background(), common.RequestContextKey,
		common.NewNormalizedRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"getBlock","params":[100]}`)))

	cfg := &config{key: ConsensusKeyBlockhashWithLink}
	r1 := &execResult{Result: makeGetBlockResponse(t, "bh1", "ph1", 99, 95, []any{})}
	r2 := &execResult{Result: makeGetBlockResponse(t, "bh1", "ph1", 99, 95, []any{})}
	r3 := &execResult{Result: makeGetBlockResponse(t, "bh1", "ph2", 99, 95, []any{})}

	classifyAndHashResponse(r1, ctx, cfg)
	classifyAndHashResponse(r2, ctx, cfg)
	classifyAndHashResponse(r3, ctx, cfg)

	assert.Equal(t, "svm:bh1:ph1:99:95", r1.CachedHash)
	assert.Equal(t, r1.CachedHash, r2.CachedHash)
	assert.NotEqual(t, r1.CachedHash, r3.CachedHash)
}

// TestConsensusKey_Canonical_GroupsByFullContent verifies that canonical
// hashing still differentiates responses with different transactions.
func TestConsensusKey_Canonical_GroupsByFullContent(t *testing.T) {
	ctx := context.WithValue(context.Background(), common.RequestContextKey,
		common.NewNormalizedRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"getBlock","params":[100]}`)))

	cfg := &config{key: ConsensusKeyCanonical}
	r1 := &execResult{Result: makeGetBlockResponse(t, "bh1", "ph1", 99, 95, []any{map[string]any{"tx": "a"}})}
	r2 := &execResult{Result: makeGetBlockResponse(t, "bh1", "ph1", 99, 95, []any{map[string]any{"tx": "b"}})}

	classifyAndHashResponse(r1, ctx, cfg)
	classifyAndHashResponse(r2, ctx, cfg)

	assert.NotEqual(t, r1.CachedHash, r2.CachedHash)
}

// TestConsensusKey_UnknownMethodFallsBackToCanonical verifies that a method
// without a registered extractor falls back to canonical hashing.
func TestConsensusKey_UnknownMethodFallsBackToCanonical(t *testing.T) {
	ctx := context.WithValue(context.Background(), common.RequestContextKey,
		common.NewNormalizedRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"getSlot","params":[]}`)))

	cfg := &config{key: ConsensusKeyBlockhash}
	jrr, err := common.NewJsonRpcResponse(1, 12345, nil)
	require.NoError(t, err)
	req := common.NewNormalizedRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"getSlot","params":[]}`))
	r := &execResult{Result: common.NewNormalizedResponse().WithRequest(req).WithJsonRpcResponse(jrr)}

	classifyAndHashResponse(r, ctx, cfg)

	// Should not be the raw slot value; should be a canonical hash.
	assert.NotEqual(t, "", r.CachedHash)
	assert.NotEqual(t, "svm:", r.CachedHash)
}

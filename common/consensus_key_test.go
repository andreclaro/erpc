package common_test

import (
	"context"
	"testing"

	_ "github.com/erpc/erpc/architecture/svm"
	"github.com/erpc/erpc/common"
	"github.com/stretchr/testify/require"
)

func TestConsensusKey_SVM_GetBlock_Extractor(t *testing.T) {
	parentHash := make([]byte, 32)
	parentHash[0] = 3
	blockhash := make([]byte, 32)
	blockhash[0] = 7

	block := map[string]any{
		"blockhash":         "2vJ8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8",
		"previousBlockhash": "1uH7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7",
		"parentSlot":        4999999,
		"blockHeight":       4999995,
		"blockTime":         1700000000,
		"transactions":      []any{},
		"rewards":           []any{},
	}
	raw, err := common.SonicCfg.Marshal(block)
	require.NoError(t, err)

	jrr, err := common.NewJsonRpcResponseFromBytes([]byte("1"), raw, nil)
	require.NoError(t, err)

	fn, ok := common.GetConsensusKeyExtractor(common.ConsensusKeyBlockhashWithLink, "getBlock")
	require.True(t, ok)

	hash, extracted, err := fn(context.Background(), jrr, "getBlock")
	require.NoError(t, err)
	require.True(t, extracted)
	require.NotEmpty(t, hash)
	t.Logf("blockhashWithLink hash: %s", hash)

	fn2, ok := common.GetConsensusKeyExtractor(common.ConsensusKeyBlockhash, "getBlock")
	require.True(t, ok)
	hash2, extracted2, err := fn2(context.Background(), jrr, "getBlock")
	require.NoError(t, err)
	require.True(t, extracted2)
	require.NotEmpty(t, hash2)
	t.Logf("blockhash hash: %s", hash2)
}

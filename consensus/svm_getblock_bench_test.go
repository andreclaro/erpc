package consensus

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	_ "github.com/erpc/erpc/architecture/svm"
	"github.com/erpc/erpc/common"
	"github.com/erpc/erpc/util"
	"github.com/rs/zerolog"
)

func init() {
	util.ConfigureTestLogger()
}

// benchSvmGetBlock returns a getBlock result with n transactions.
func benchSvmGetBlock(n int) []byte {
	parentHash := make([]byte, 32)
	parentHash[0] = 3
	blockhash := make([]byte, 32)
	blockhash[0] = 7

	txs := make([]any, n)
	for i := range txs {
		txs[i] = map[string]any{
			"transaction": map[string]any{
				"signatures": []string{
					"5vK8Wg6JKQJk8Q4Q6ZQ6ZQ6ZQ6ZQ6ZQ6ZQ6ZQ6ZQ6ZQ6ZQ6ZQ6ZQ6ZQ6ZQ6ZQ6ZQ6ZQ6ZQ6ZQ6ZQ6ZQ6ZQ6ZQ6ZQ6ZQ6ZQ6ZQ6ZQ6ZQ",
				},
				"message": map[string]any{
					"accountKeys": []string{
						"7nY7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7",
						"8oZ8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8Z8",
					},
					"header": map[string]any{
						"numRequiredSignatures":       1,
						"numReadonlySignedAccounts":   0,
						"numReadonlyUnsignedAccounts": 1,
					},
					"recentBlockhash": "9pA9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9A9",
					"instructions": []any{
						map[string]any{
							"programIdIndex": 1,
							"accounts":       []int{0},
							"data":           "3gK1",
						},
					},
				},
			},
			"meta": map[string]any{
				"err":                  nil,
				"fee":                  5000,
				"preBalances":          []int64{1000000000, 0},
				"postBalances":         []int64{999995000, 0},
				"preTokenBalances":     []any{},
				"postTokenBalances":    []any{},
				"logMessages":          []string{"Program 11111111111111111111111111111111 invoke [1]", "Program 11111111111111111111111111111111 success"},
				"rewards":              nil,
				"loadedAddresses":      map[string]any{"writable": []any{}, "readonly": []any{}},
				"computeUnitsConsumed": 150,
			},
			"version": 0,
		}
	}

	block := map[string]any{
		"blockhash":         "2vJ8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8J8",
		"previousBlockhash": "1uH7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7H7",
		"parentSlot":        4999999,
		"blockHeight":       4999995,
		"blockTime":         1700000000,
		"transactions":      txs,
		"rewards":           []any{},
	}
	raw, err := json.Marshal(block)
	if err != nil {
		panic(err)
	}
	return raw
}

func benchSvmGetBlockRequest() *common.NormalizedRequest {
	return common.NewNormalizedRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"getBlock","params":[5000000,{"commitment":"finalized","encoding":"json"}]}`))
}

func benchSvmGetBlockResponse(raw []byte) *common.NormalizedResponse {
	jrr, err := common.NewJsonRpcResponseFromBytes([]byte("1"), raw, nil)
	if err != nil {
		panic(err)
	}
	return common.NewNormalizedResponse().WithJsonRpcResponse(jrr)
}

// BenchmarkConsensus_SVM_GetBlock_Key compares end-to-end consensus latency
// for SVM getBlock under different consensus keys. All participants return the
// same response, so the measured time is the hashing and consensus machinery
// overhead, not dispute handling.
func BenchmarkConsensus_SVM_GetBlock_Key(b *testing.B) {
	for _, txs := range []int{100, 400, 1000} {
		for _, key := range []common.ConsensusKey{common.ConsensusKeyCanonical, common.ConsensusKeyBlockhash, common.ConsensusKeyBlockhashWithLink} {
			b.Run(fmt.Sprintf("tx=%d/key=%s", txs, key), func(b *testing.B) {
				logger := zerolog.New(zerolog.NewConsoleWriter())
				pol := NewConsensusPolicyBuilder().
					WithKey(key).
					WithMaxParticipants(3).
					WithAgreementThreshold(3).
					WithLogger(&logger).
					WithMaxWaitOnResult(common.NewStaticDuration(5 * time.Second)).
					Build()

				req := benchSvmGetBlockRequest()
				raw := benchSvmGetBlock(txs)

				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					ctx := context.WithValue(context.Background(), common.RequestContextKey, req)
					_, err := pol.Run(ctx, req, func(_ context.Context, _ *common.NormalizedRequest) (*common.NormalizedResponse, error) {
						return benchSvmGetBlockResponse(raw), nil
					})
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

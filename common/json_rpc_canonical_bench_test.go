package common

import (
	"encoding/json"
	"fmt"
	"testing"
)

// benchSvmGetBlock returns a getBlock result with n transactions.
// Fields mirror the Solana RPC JSON encoding used in production.
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
				"err":               nil,
				"fee":               5000,
				"preBalances":       []int64{1000000000, 0},
				"postBalances":      []int64{999995000, 0},
				"preTokenBalances":  []any{},
				"postTokenBalances": []any{},
				"logMessages": []string{
					"Program 11111111111111111111111111111111 invoke [1]",
					"Program 11111111111111111111111111111111 success",
				},
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

func BenchmarkCanonicalHash_GetBlock_SVM(b *testing.B) {
	for _, n := range []int{20, 100, 500, 1000} {
		b.Run(fmt.Sprintf("tx=%d", n), func(b *testing.B) {
			raw := benchSvmGetBlock(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r := &JsonRpcResponse{result: raw}
				_, err := r.CanonicalHash()
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkCanonicalHash_GetBlock_SVM_WithIgnored(b *testing.B) {
	ignore := []string{"transactions.*.meta.logMessages", "transactions.*.meta.loadedAddresses"}
	for _, n := range []int{20, 100, 500, 1000} {
		b.Run(fmt.Sprintf("tx=%d", n), func(b *testing.B) {
			raw := benchSvmGetBlock(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r := &JsonRpcResponse{result: raw}
				_, err := r.CanonicalHashWithIgnoredFields(ignore)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

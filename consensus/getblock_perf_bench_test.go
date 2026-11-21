package consensus

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"runtime/pprof"
	"testing"

	"github.com/erpc/erpc/common"
	"github.com/erpc/erpc/telemetry"
	"github.com/erpc/erpc/util"
	"github.com/rs/zerolog"
)

// generateEvmLargeBlockResult returns a JSON object for eth_getBlockByNumber
// (full=true) with txCount synthetic transactions.
func generateEvmLargeBlockResult(txCount int) interface{} {
	transactions := make([]map[string]interface{}, txCount)
	for i := 0; i < txCount; i++ {
		transactions[i] = map[string]interface{}{
			"hash":             fmt.Sprintf("0x%064x", i),
			"nonce":            fmt.Sprintf("0x%x", i),
			"blockHash":        "0x0000000000000000000000000000000000000000000000000000000000000001",
			"blockNumber":      "0x11118888",
			"transactionIndex": fmt.Sprintf("0x%x", i),
			"from":             fmt.Sprintf("0x%040x", rand.Int63()),
			"to":               fmt.Sprintf("0x%040x", rand.Int63()),
			"value":            "0x1",
			"gas":              "0x5208",
			"gasPrice":         "0x1",
			"input":            "0x",
			"v":                "0x1c",
			"r":                fmt.Sprintf("0x%064x", rand.Int63()),
			"s":                fmt.Sprintf("0x%064x", rand.Int63()),
		}
	}
	return map[string]interface{}{
		"number":           "0x11118888",
		"hash":             "0x0000000000000000000000000000000000000000000000000000000000000001",
		"parentHash":       "0x0000000000000000000000000000000000000000000000000000000000000000",
		"nonce":            "0x00000000000000000000",
		"sha3Uncles":       "0x0000000000000000000000000000000000000000000000000000000000000000",
		"logsBloom":        "0x" + fmt.Sprintf("%0512x", 0),
		"transactionsRoot": "0x0000000000000000000000000000000000000000000000000000000000000000",
		"stateRoot":        "0x0000000000000000000000000000000000000000000000000000000000000000",
		"receiptsRoot":     "0x0000000000000000000000000000000000000000000000000000000000000000",
		"miner":            "0x0000000000000000000000000000000000000000",
		"difficulty":       "0x0",
		"totalDifficulty":  "0x0",
		"extraData":        "0x",
		"size":             "0x1000",
		"gasLimit":         "0x1c9c380",
		"gasUsed":          "0x5208",
		"timestamp":        "0x6702a8f0",
		"transactions":     transactions,
		"uncles":           []string{},
	}
}

// generateSvmLargeBlockResult returns a JSON object for Solana getBlock
// (encoding=json, transactionDetails=full) with txCount synthetic
// transactions.
func generateSvmLargeBlockResult(txCount int) interface{} {
	transactions := make([]map[string]interface{}, txCount)
	for i := 0; i < txCount; i++ {
		accounts := make([]string, 8)
		for j := range accounts {
			accounts[j] = svmBase58Key()
		}
		instructions := make([]map[string]interface{}, 4)
		for j := range instructions {
			accounts := make([]int, 4)
			for k := range accounts {
				accounts[k] = rand.Intn(len(accounts))
			}
			instructions[j] = map[string]interface{}{
				"programIdIndex": rand.Intn(len(accounts)),
				"accounts":       accounts,
				"data":           svmBase58Data(50),
			}
		}
		transactions[i] = map[string]interface{}{
			"transaction": map[string]interface{}{
				"signatures": []string{svmBase58Sig(), svmBase58Sig()},
				"message": map[string]interface{}{
					"accountKeys": accounts,
					"header": map[string]interface{}{
						"numRequiredSignatures":       2,
						"numReadonlySignedAccounts":   0,
						"numReadonlyUnsignedAccounts": 6,
					},
					"recentBlockhash": svmBase58Key(),
					"instructions":    instructions,
				},
			},
			"meta": map[string]interface{}{
				"err":    nil,
				"fee":    5000,
				"status": map[string]interface{}{"Ok": nil},
			},
			"version": "legacy",
		}
	}
	return map[string]interface{}{
		"blockhash":         svmBase58Key(),
		"previousBlockhash": svmBase58Key(),
		"parentSlot":        123456789,
		"blockTime":         1698000000,
		"blockHeight":       123456000,
		"transactions":      transactions,
		"rewards":           []interface{}{},
	}
}

// svmBase58Key returns a synthetic 32-byte base58 key.
func svmBase58Key() string {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	b := make([]byte, 44)
	for i := range b {
		b[i] = alphabet[rand.Intn(len(alphabet))]
	}
	return string(b)
}

// svmBase58Sig returns a synthetic 64-byte base58 signature.
func svmBase58Sig() string {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	b := make([]byte, 88)
	for i := range b {
		b[i] = alphabet[rand.Intn(len(alphabet))]
	}
	return string(b)
}

// svmBase58Data returns a synthetic base58 data string.
func svmBase58Data(n int) string {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[rand.Intn(len(alphabet))]
	}
	return string(b)
}

// largeBlockResponse builds a NormalizedResponse carrying a block with
// txCount transactions.
func largeBlockResponse(method string, txCount int) *common.NormalizedResponse {
	var result interface{}
	switch method {
	case "eth_getBlockByNumber":
		result = generateEvmLargeBlockResult(txCount)
	case "getBlock":
		result = generateSvmLargeBlockResult(txCount)
	default:
		panic("unknown method: " + method)
	}
	jrpc, err := common.NewJsonRpcResponse(1, result, nil)
	if err != nil {
		panic(err)
	}
	return common.NewNormalizedResponse().WithJsonRpcResponse(jrpc)
}

// requestBodyForMethod returns a request body for the target method.
func requestBodyForMethod(method string) []byte {
	switch method {
	case "eth_getBlockByNumber":
		return []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["latest",true]}`)
	case "getBlock":
		return []byte(`{"jsonrpc":"2.0","id":1,"method":"getBlock","params":[123456789,{"encoding":"json","transactionDetails":"full","maxSupportedTransactionVersion":0}]}`)
	default:
		panic("unknown method: " + method)
	}
}

// runConsensusGetBlockBench runs one consensus round with n participants,
// all returning identical large blocks, and agreementThreshold=3.
func runConsensusGetBlockBench(b *testing.B, method string, participants int, txCount int, key ConsensusKey) {
	lg := zerolog.Nop()
	cfg := &config{
		maxParticipants:         participants,
		agreementThreshold:      3,
		disputeBehavior:         common.ConsensusDisputeBehaviorReturnError,
		lowParticipantsBehavior: common.ConsensusLowParticipantsBehaviorReturnError,
		key:                     key,
	}
	ex := &executor{consensusPolicy: &consensusPolicy{logger: &lg, config: cfg}}

	body := requestBodyForMethod(method)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		req := common.NewNormalizedRequest(body)
		ctx := context.WithValue(context.Background(), common.RequestContextKey, req)
		_, _ = ex.Run(ctx, req, func(_ context.Context, _ *common.NormalizedRequest) (*common.NormalizedResponse, error) {
			// Each participant gets its own response object, matching real
			// upstream fan-out. Pointer identity determines the winner group;
			// identical content hashes into one consensus group.
			return largeBlockResponse(method, txCount), nil
		})
	}
	b.StopTimer()
}

// BenchmarkConsensus_GetBlock_5up_1000tx measures consensus latency for a
// 5-upstream round on a 1000-transaction EVM block.
func BenchmarkConsensus_GetBlock_5up_1000tx(b *testing.B) {
	util.ConfigureTestLogger()
	_ = telemetry.SetHistogramBuckets("")
	runConsensusGetBlockBench(b, "eth_getBlockByNumber", 5, 1000, ConsensusKeyCanonical)
}

// BenchmarkConsensus_GetBlock_5up_1000tx_Profile captures a CPU profile of
// the same EVM scenario to /tmp/erpc_consensus_getblock_cpu.prof.
func BenchmarkConsensus_GetBlock_5up_1000tx_Profile(b *testing.B) {
	util.ConfigureTestLogger()
	_ = telemetry.SetHistogramBuckets("")

	f, err := os.Create("/tmp/erpc_consensus_getblock_cpu.prof")
	if err != nil {
		b.Fatalf("create profile: %v", err)
	}
	defer f.Close()
	if err := pprof.StartCPUProfile(f); err != nil {
		b.Fatalf("start cpu profile: %v", err)
	}
	defer pprof.StopCPUProfile()

	runConsensusGetBlockBench(b, "eth_getBlockByNumber", 5, 1000, ConsensusKeyCanonical)
}

// BenchmarkConsensus_GetBlock_SVM_5up_1000tx measures consensus latency for
// a 5-upstream round on a 1000-transaction Solana block.
func BenchmarkConsensus_GetBlock_SVM_5up_1000tx(b *testing.B) {
	util.ConfigureTestLogger()
	_ = telemetry.SetHistogramBuckets("")
	runConsensusGetBlockBench(b, "getBlock", 5, 1000, ConsensusKeyCanonical)
}

// BenchmarkConsensus_GetBlock_SVM_5up_1000tx_Profile captures a CPU profile
// of the SVM scenario to /tmp/erpc_consensus_getblock_svm_cpu.prof.
func BenchmarkConsensus_GetBlock_SVM_5up_1000tx_Profile(b *testing.B) {
	util.ConfigureTestLogger()
	_ = telemetry.SetHistogramBuckets("")

	f, err := os.Create("/tmp/erpc_consensus_getblock_svm_cpu.prof")
	if err != nil {
		b.Fatalf("create profile: %v", err)
	}
	defer f.Close()
	if err := pprof.StartCPUProfile(f); err != nil {
		b.Fatalf("start cpu profile: %v", err)
	}
	defer pprof.StopCPUProfile()

	runConsensusGetBlockBench(b, "getBlock", 5, 1000, ConsensusKeyCanonical)
}

// BenchmarkConsensus_GetBlock_SVM_5up_1000tx_Blockhash measures consensus
// latency when comparing only the Solana blockhash.
func BenchmarkConsensus_GetBlock_SVM_5up_1000tx_Blockhash(b *testing.B) {
	util.ConfigureTestLogger()
	_ = telemetry.SetHistogramBuckets("")
	runConsensusGetBlockBench(b, "getBlock", 5, 1000, ConsensusKeyBlockhash)
}

// BenchmarkConsensus_GetBlock_SVM_5up_1000tx_BlockhashWithLink measures
// consensus latency when comparing blockhash plus chain-link fields.
func BenchmarkConsensus_GetBlock_SVM_5up_1000tx_BlockhashWithLink(b *testing.B) {
	util.ConfigureTestLogger()
	_ = telemetry.SetHistogramBuckets("")
	runConsensusGetBlockBench(b, "getBlock", 5, 1000, ConsensusKeyBlockhashWithLink)
}

// BenchmarkConsensus_GetBlock_SVM_5up_400tx_BlockhashWithLink measures
// consensus latency for an average-mainnet-size Solana block.
func BenchmarkConsensus_GetBlock_SVM_5up_400tx_BlockhashWithLink(b *testing.B) {
	util.ConfigureTestLogger()
	_ = telemetry.SetHistogramBuckets("")
	runConsensusGetBlockBench(b, "getBlock", 5, 400, ConsensusKeyBlockhashWithLink)
}

// BenchmarkConsensus_GetBlock_5up_100tx is a smaller EVM block baseline.
func BenchmarkConsensus_GetBlock_5up_100tx(b *testing.B) {
	util.ConfigureTestLogger()
	_ = telemetry.SetHistogramBuckets("")
	runConsensusGetBlockBench(b, "eth_getBlockByNumber", 5, 100, ConsensusKeyCanonical)
}

// BenchmarkConsensus_GetBlock_5up_5000tx is a very large EVM block stress test.
func BenchmarkConsensus_GetBlock_5up_5000tx(b *testing.B) {
	util.ConfigureTestLogger()
	_ = telemetry.SetHistogramBuckets("")
	runConsensusGetBlockBench(b, "eth_getBlockByNumber", 5, 5000, ConsensusKeyCanonical)
}

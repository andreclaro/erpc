package integrity

// Benchmarks for the production hot paths of the integrity layer. Fixtures
// are package-level and self-verifying: the bench block is run through
// Validate once at init and panics if any check rejects it, so a benchmark
// can never silently measure a truncated failure path after a code change.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"

	"github.com/erpc/erpc/common"
)

// ---------- t-free fixture builders (panics instead of require) ----------

func benchKeys(n int) []keyFixture {
	out := make([]keyFixture, n)
	for i := range out {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			panic(err)
		}
		out[i] = keyFixture{pub: pub, priv: priv, b58: base58Encode(pub)}
	}
	return out
}

// benchTxJSON mirrors buildTx's output: a real ed25519-signed encoding:"json"
// transaction object with one instruction.
func benchTxJSON(keys []keyFixture, signerCount int, recent []byte) map[string]any {
	signed := tsSignedMessage(false, byte(signerCount), 0, byte(len(keys)-signerCount), rawKeys(keys), recent, standardInstr(), nil)
	sigs := make([]string, signerCount)
	for i := 0; i < signerCount; i++ {
		sigs[i] = base58Encode(ed25519.Sign(keys[i].priv, signed))
	}
	keyStrs := make([]string, len(keys))
	for i, k := range keys {
		keyStrs[i] = k.b58
	}
	return map[string]any{
		"signatures": sigs,
		"message": map[string]any{
			"accountKeys": keyStrs,
			"header": map[string]any{
				"numRequiredSignatures":       signerCount,
				"numReadonlySignedAccounts":   0,
				"numReadonlyUnsignedAccounts": len(keys) - signerCount,
			},
			"recentBlockhash": base58Encode(recent),
			"instructions": []any{
				map[string]any{
					"programIdIndex": 1,
					"accounts":       []int{0},
					"data":           base58Encode([]byte{1, 2, 3}),
				},
			},
		},
	}
}

const (
	benchSlot    = int64(5_000_000)
	benchTxCount = 20
)

// buildBenchBlock returns a getBlock result that passes every applicable
// check, plus the request params. Twenty one-signer transactions: exercises
// sigUniqueness and 20 real ed25519 verifications per Validate.
func buildBenchBlock() (resultJSON, paramsJSON string) {
	parentHash := make([]byte, 32)
	parentHash[0] = 3
	blockhash := make([]byte, 32)
	blockhash[0] = 7
	recent := make([]byte, 32)
	recent[0] = 9

	txs := make([]any, benchTxCount)
	for i := range txs {
		txs[i] = benchTxJSON(benchKeys(3), 1, recent)
	}
	block := map[string]any{
		"blockhash":         base58Encode(blockhash),
		"previousBlockhash": base58Encode(parentHash),
		"parentSlot":        benchSlot - 1,
		"blockHeight":       benchSlot - 5,
		"blockTime":         1700000000,
		"transactions":      txs,
		"rewards":           []any{},
	}
	raw, err := json.Marshal(block)
	if err != nil {
		panic(err)
	}
	return string(raw), `[5000000,{"commitment":"finalized","encoding":"json"}]`
}

// buildBenchVerifyTx returns a parsedTx with n real signatures over a v0
// wire message, sanity-verified once so the benchmark measures only the
// verify path.
func buildBenchVerifyTx(n int) *parsedTx {
	keys := benchKeys(n + 2)
	recent := make([]byte, 32)
	recent[0] = 9
	msg := parsedMessage{
		Version:               1,
		NumRequiredSignatures: n,
		NumReadonlyUnsigned:   2,
		AccountKeys:           make([]string, len(keys)),
		RecentBlockhash:       base58Encode(recent),
		Instructions:          []parsedInstr{{ProgramIDIndex: 1, Accounts: []byte{0}, Data: []byte{1, 2, 3}}},
		Lookups:               []parsedLookup{{AccountKey: keys[n].b58, WritableIndexes: []byte{0}, ReadonlyIndexes: []byte{1}}},
	}
	for i, k := range keys {
		msg.AccountKeys[i] = k.b58
	}
	wire, err := messageWireBytes(&msg)
	if err != nil {
		panic(err)
	}
	tx := &parsedTx{
		Signatures: make([]string, n),
		Message:    msg,
		HasMessage: true,
	}
	for i := 0; i < n; i++ {
		tx.Signatures[i] = base58Encode(ed25519.Sign(keys[i].priv, wire))
	}
	if err := verifyTxSignatures(tx); err != nil {
		panic("bench fixture must verify: " + err.Error())
	}
	return tx
}

// ---------- fixtures (initialized in dependency order) ----------

var (
	benchBlockResult, benchBlockParams = buildBenchBlock()
	benchCorroborated                  = CheckSetForLevel(LevelCorroborated)
	benchSigOnly                       = CheckSet{}.Enable("svm.auth.signatureVerify", nil)

	// Sanity: the block must pass every applicable check — a benchmark must
	// never measure a truncated failure path.
	benchGuard = func() int {
		res := benchValidate(benchBlockResult, benchBlockParams, benchCorroborated)
		if res.Err != nil {
			panic("bench block rejected by " + res.RejectedCheckID + ": " + res.RejectedReason)
		}
		return len(res.Outcomes)
	}()

	benchVerify1 = buildBenchVerifyTx(1)
	benchVerify5 = buildBenchVerifyTx(5)
)

func benchValidate(resultJSON, paramsJSON string, cs CheckSet) Result {
	req := common.NewNormalizedRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"getBlock","params":` + paramsJSON + `}`))
	jrr := common.MustNewJsonRpcResponseFromBytes([]byte("1"), []byte(resultJSON), nil)
	rs := common.NewNormalizedResponse().WithRequest(req).WithJsonRpcResponse(jrr)
	var params []any
	if err := common.SonicCfg.Unmarshal([]byte(paramsJSON), &params); err != nil {
		panic(err)
	}
	return Validate(context.Background(), Input{
		Method:   "getBlock",
		Upstream: common.NewFakeUpstream("bench"),
		Response: rs,
		Checks:   cs,
		Params:   params,
		Reorg:    rejectAll,
	})
}

// ---------- benchmarks ----------

func BenchmarkBase58(b *testing.B) {
	h32 := base58Encode(make([]byte, 32))
	h64 := base58Encode(make([]byte, 64))
	b.Run("decodeLen32", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := base58DecodeLen(h32, 32); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("decodeLen64", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := base58DecodeLen(h64, 64); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("encode32", func(b *testing.B) {
		in := make([]byte, 32)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = base58Encode(in)
		}
	})
}

func BenchmarkMessageWireBytes(b *testing.B) {
	recent := base58Encode(make([]byte, 32))
	key := base58Encode(make([]byte, 32))
	m := &parsedMessage{
		Version:               1,
		NumRequiredSignatures: 2,
		AccountKeys:           []string{key, key},
		RecentBlockhash:       recent,
		Instructions:          []parsedInstr{{ProgramIDIndex: 1, Accounts: []byte{0}, Data: []byte{1, 2, 3}}},
		Lookups:               []parsedLookup{{AccountKey: key, WritableIndexes: []byte{0}, ReadonlyIndexes: []byte{1}}},
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := messageWireBytes(m); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkVerifyTxSignatures(b *testing.B) {
	b.Run("signers=1", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if err := verifyTxSignatures(benchVerify1); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("signers=5", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if err := verifyTxSignatures(benchVerify5); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkChainState(b *testing.B) {
	base := NewChainState()
	for s := int64(0); s < 1000; s++ {
		base.Observe(entry(s, byte(s)))
	}
	b.Run("observe", func(b *testing.B) {
		cs := NewChainState()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			cs.Observe(entry(int64(i), byte(i)))
		}
	})
	b.Run("parentHit", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, ok := base.Parent(500); !ok {
				b.Fatal("expected hit")
			}
		}
	})
}

// BenchmarkDecodedGetBlock isolates the parse layer: one fresh Decoded per
// iteration, exactly as the engine sees one response per request.
func BenchmarkDecodedGetBlock(b *testing.B) {
	raw := []byte(benchBlockResult)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		d := newDecoded("getblock", raw)
		d.reqParams = []any{float64(benchSlot)}
		if _, err := d.Block(); err != nil {
			b.Fatal(err)
		}
		if _, err := d.BlockTxs(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkValidate is the end-to-end pipeline including response
// construction — the per-response cost a deployment actually pays.
func BenchmarkValidate(b *testing.B) {
	b.Run("getBlock/corroborated", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			res := benchValidate(benchBlockResult, benchBlockParams, benchCorroborated)
			if res.Err != nil {
				b.Fatal(res.Err)
			}
		}
	})
	b.Run("getBlock/signatureVerify", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			res := benchValidate(benchBlockResult, benchBlockParams, benchSigOnly)
			if res.Err != nil {
				b.Fatal(res.Err)
			}
		}
	})
	b.Run("getSlot/corroborated", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			res := benchValidate(`950`, `[]`, benchCorroborated)
			if res.Err != nil {
				b.Fatal(res.Err)
			}
		}
	})
}

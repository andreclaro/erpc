package integrity

// Unit tests for the SVM integrity engine. The fixture builder below
// implements the Solana wire format INDEPENDENTLY (its own shortvec, its own
// message layout) so a bug in the production serializer (message.go) fails the
// ed25519 round-trip instead of mirroring itself.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/erpc/erpc/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- independent wire-format builder ----------

func tsShortvec(n int) []byte {
	var out []byte
	for {
		b := byte(n & 0x7f)
		n >>= 7
		if n == 0 {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}

type tsInstr struct {
	programIDIndex byte
	accounts       []byte
	data           []byte
}

type tsLookup struct {
	key                []byte
	writable, readonly []byte
}

// tsSignedMessage builds the exact byte sequence Solana transaction signatures
// are produced over (v0 includes the 0x80 version prefix).
func tsSignedMessage(v0 bool, numReq, roSigned, roUnsigned byte, keys [][]byte, blockhash []byte, instrs []tsInstr, lookups []tsLookup) []byte {
	var b []byte
	if v0 {
		b = append(b, 0x80)
	}
	b = append(b, numReq, roSigned, roUnsigned)
	b = append(b, tsShortvec(len(keys))...)
	for _, k := range keys {
		b = append(b, k...)
	}
	b = append(b, blockhash...)
	b = append(b, tsShortvec(len(instrs))...)
	for _, ix := range instrs {
		b = append(b, ix.programIDIndex)
		b = append(b, tsShortvec(len(ix.accounts))...)
		b = append(b, ix.accounts...)
		b = append(b, tsShortvec(len(ix.data))...)
		b = append(b, ix.data...)
	}
	if v0 {
		b = append(b, tsShortvec(len(lookups))...)
		for _, lk := range lookups {
			b = append(b, lk.key...)
			b = append(b, tsShortvec(len(lk.writable))...)
			b = append(b, lk.writable...)
			b = append(b, tsShortvec(len(lk.readonly))...)
			b = append(b, lk.readonly...)
		}
	}
	return b
}

type keyFixture struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
	b58  string
}

func makeKeyFixtures(t *testing.T, n int) []keyFixture {
	t.Helper()
	out := make([]keyFixture, n)
	for i := range out {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		out[i] = keyFixture{pub: pub, priv: priv, b58: base58Encode(pub)}
	}
	return out
}

func rawKeys(keys []keyFixture) [][]byte {
	out := make([][]byte, len(keys))
	for i, k := range keys {
		out[i] = k.pub
	}
	return out
}

// txFixture is one verifiable transaction plus its RPC JSON form.
type txFixture struct {
	keys      []keyFixture
	signers   int
	blockhash []byte
	json      map[string]any // the tx element of a getBlock transactions array
	signed    []byte
}

// buildTx signs an independently-built wire message and returns the RPC-JSON
// (encoding:"json") transaction object. signerCount keys starting at index 0
// sign; the rest are non-signers.
func buildTx(t *testing.T, v0 bool, keys []keyFixture, signerCount int, blockhash []byte, instrs []tsInstr, lookups []tsLookup) txFixture {
	t.Helper()
	rawKeys := rawKeys(keys)
	signed := tsSignedMessage(v0, byte(signerCount), 0, byte(len(keys)-signerCount), rawKeys, blockhash, instrs, lookups)
	sigs := make([]string, signerCount)
	for i := 0; i < signerCount; i++ {
		sigs[i] = base58Encode(ed25519.Sign(keys[i].priv, signed))
	}
	keyStrs := make([]string, len(keys))
	for i, k := range keys {
		keyStrs[i] = k.b58
	}
	ixJSON := make([]map[string]any, len(instrs))
	for i, ix := range instrs {
		accts := make([]int, len(ix.accounts))
		for j, a := range ix.accounts {
			accts[j] = int(a)
		}
		ixJSON[i] = map[string]any{
			"programIdIndex": int(ix.programIDIndex),
			"accounts":       accts,
			"data":           base58Encode(ix.data),
		}
	}
	msg := map[string]any{
		"accountKeys":     keyStrs,
		"header":          map[string]any{"numRequiredSignatures": signerCount, "numReadonlySignedAccounts": 0, "numReadonlyUnsignedAccounts": len(keys) - signerCount},
		"recentBlockhash": base58Encode(blockhash),
		"instructions":    ixJSON,
	}
	if v0 {
		lkJSON := make([]map[string]any, len(lookups))
		for i, lk := range lookups {
			w := make([]int, len(lk.writable))
			for j, x := range lk.writable {
				w[j] = int(x)
			}
			r := make([]int, len(lk.readonly))
			for j, x := range lk.readonly {
				r[j] = int(x)
			}
			lkJSON[i] = map[string]any{"accountKey": base58Encode(lk.key), "writableIndexes": w, "readonlyIndexes": r}
		}
		msg["addressTableLookups"] = lkJSON
	}
	tx := map[string]any{"signatures": sigs, "message": msg}
	if v0 {
		tx["version"] = 0
	}
	return txFixture{keys: keys, signers: signerCount, blockhash: blockhash, json: tx, signed: signed}
}

// standardInstr is one instruction addressed to a system-program-like account
// at index 1, invoking account 0, with small data.
func standardInstr() []tsInstr {
	return []tsInstr{{programIDIndex: 1, accounts: []byte{0}, data: []byte{1, 2, 3}}}
}

// ---------- validate harness ----------

func only(id string, params map[string]string) CheckSet { return CheckSet{}.Enable(id, params) }

var rejectAll = ReorgPolicy{Finalized: BehaviorError, Unfinalized: BehaviorError}

func outcomeOf(res Result, id string) string {
	for _, oc := range res.Outcomes {
		if oc.CheckID == id {
			return oc.Outcome
		}
	}
	return ""
}

func runValidate(t *testing.T, method, paramsJSON, resultJSON string, cs CheckSet) Result {
	t.Helper()
	return runValidateObserve(t, method, paramsJSON, resultJSON, cs, false)
}

func runValidateObserve(t *testing.T, method, paramsJSON, resultJSON string, cs CheckSet, observeOnly bool) Result {
	t.Helper()
	req := common.NewNormalizedRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + paramsJSON + `}`))
	jrr := common.MustNewJsonRpcResponseFromBytes([]byte("1"), []byte(resultJSON), nil)
	rs := common.NewNormalizedResponse().WithRequest(req).WithJsonRpcResponse(jrr)
	var params []any
	require.NoError(t, common.SonicCfg.Unmarshal([]byte(paramsJSON), &params))
	return Validate(context.Background(), Input{
		Method:      method,
		Upstream:    common.NewFakeUpstream("u"),
		Response:    rs,
		Checks:      cs,
		Params:      params,
		Reorg:       rejectAll,
		ObserveOnly: observeOnly,
	})
}

// validBlockFixture returns a getBlock result for a block at slot 100 with one
// signed transaction. tamper mutates the decoded block before re-marshal.
func validBlockFixture(t *testing.T, tamper func(map[string]any)) string {
	t.Helper()
	keys := makeKeyFixtures(t, 3)
	blockhash := make([]byte, 32)
	blockhash[0] = 7
	parentHash := make([]byte, 32)
	parentHash[0] = 3
	recent := make([]byte, 32)
	recent[0] = 9
	tx := buildTx(t, false, keys, 2, recent, standardInstr(), nil)

	txs := []any{tx.json}
	block := map[string]any{
		"blockhash":         base58Encode(blockhash),
		"previousBlockhash": base58Encode(parentHash),
		"parentSlot":        95,
		"blockHeight":       90,
		"blockTime":         1700000000,
		"transactions":      txs,
		"rewards":           []any{},
	}
	if tamper != nil {
		tamper(block)
	}
	raw, err := json.Marshal(block)
	require.NoError(t, err)
	return string(raw)
}

// ---------- level membership invariant ----------

func TestLevelMembershipCoversAllChecks(t *testing.T) {
	seen := map[string]int{}
	for lvl, ids := range levelMembership {
		for _, id := range ids {
			seen[id]++
			found := false
			for _, c := range allChecks {
				if c.ID == id {
					found = true
					break
				}
			}
			assert.True(t, found, "level %s names unknown check %s", lvl, id)
		}
	}
	for _, c := range allChecks {
		assert.Equal(t, 1, seen[c.ID], "check %s must appear in exactly one levelMembership row", c.ID)
	}
	// Phase 1: the intrinsic row is populated; higher tiers land with their
	// phases but must not silently duplicate intrinsic ids.
	for _, lvl := range []Level{LevelCorroborated, LevelAuthoritative} {
		for _, id := range levelMembership[lvl] {
			assert.NotContains(t, levelMembership[LevelIntrinsic], id, "%s duplicated in %s", id, lvl)
		}
	}
}

// ---------- signature verification ----------

func TestSignatureVerify_ValidLegacyPasses(t *testing.T) {
	res := runValidate(t, "getBlock", `[100]`, validBlockFixture(t, nil), only("svm.auth.signatureVerify", nil))
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.auth.signatureVerify"))
}

func TestSignatureVerify_TamperedRecentBlockhashRejected(t *testing.T) {
	// Attacker rewrote the recentBlockhash but kept the (now invalid) signatures.
	res := runValidate(t, "getBlock", `[100]`, validBlockFixture(t, func(b map[string]any) {
		txs := b["transactions"].([]any)
		tx := txs[0].(map[string]any)
		msg := tx["message"].(map[string]any)
		other := make([]byte, 32)
		other[0] = 42
		msg["recentBlockhash"] = base58Encode(other)
	}), only("svm.auth.signatureVerify", nil))
	require.Error(t, res.Err)
	assert.True(t, common.HasErrorCode(res.Err, common.ErrCodeEndpointContentValidation))
	assert.Equal(t, "svm.auth.signatureVerify", res.RejectedCheckID)
}

func TestSignatureVerify_TamperedInstructionDataRejected(t *testing.T) {
	res := runValidate(t, "getBlock", `[100]`, validBlockFixture(t, func(b map[string]any) {
		txs := b["transactions"].([]any)
		tx := txs[0].(map[string]any)
		msg := tx["message"].(map[string]any)
		ixs := msg["instructions"].([]map[string]any)
		ixs[0]["data"] = base58Encode([]byte{9, 9, 9, 9})
	}), only("svm.auth.signatureVerify", nil))
	require.Error(t, res.Err)
}

func TestSignatureVerify_ValidV0WithLookupsPasses(t *testing.T) {
	keys := makeKeyFixtures(t, 3)
	lookupKey := make([]byte, 32)
	lookupKey[0] = 77
	recent := make([]byte, 32)
	recent[0] = 5
	tx := buildTx(t, true, keys, 1, recent, standardInstr(), []tsLookup{{key: lookupKey, writable: []byte{0}, readonly: []byte{1}}})
	blockhash := make([]byte, 32)
	blockhash[0] = 8
	parent := make([]byte, 32)
	parent[0] = 4
	block := map[string]any{
		"blockhash":         base58Encode(blockhash),
		"previousBlockhash": base58Encode(parent),
		"parentSlot":        99,
		"blockHeight":       98,
		"blockTime":         1700000000,
		"transactions":      []any{tx.json},
	}
	raw, err := json.Marshal(block)
	require.NoError(t, err)
	res := runValidate(t, "getBlock", `[100]`, string(raw), only("svm.auth.signatureVerify", nil))
	assert.NoError(t, res.Err, "v0 signed payload must include the 0x80 prefix")
	assert.Equal(t, "pass", outcomeOf(res, "svm.auth.signatureVerify"))
}

func TestSignatureVerify_TamperedV0LookupRejected(t *testing.T) {
	keys := makeKeyFixtures(t, 3)
	lookupKey := make([]byte, 32)
	lookupKey[0] = 77
	recent := make([]byte, 32)
	recent[0] = 5
	tx := buildTx(t, true, keys, 1, recent, standardInstr(), []tsLookup{{key: lookupKey, writable: []byte{0}, readonly: []byte{1}}})
	// Flip a writable index after signing.
	lks := tx.json["message"].(map[string]any)["addressTableLookups"].([]map[string]any)
	lks[0]["writableIndexes"] = []int{1}
	blockhash := make([]byte, 32)
	parent := make([]byte, 32)
	block := map[string]any{
		"blockhash":         base58Encode(blockhash),
		"previousBlockhash": base58Encode(parent),
		"parentSlot":        99,
		"blockHeight":       98,
		"blockTime":         1700000000,
		"transactions":      []any{tx.json},
	}
	raw, err := json.Marshal(block)
	require.NoError(t, err)
	res := runValidate(t, "getBlock", `[100]`, string(raw), only("svm.auth.signatureVerify", nil))
	require.Error(t, res.Err)
}

func TestSignatureVerify_JsonParsedAccountKeyObjectsPass(t *testing.T) {
	// jsonParsed encodes accountKeys as {pubkey, signer, writable, source}
	// objects; verification must accept the same material.
	res := runValidate(t, "getBlock", `[100]`, validBlockFixture(t, func(b map[string]any) {
		txs := b["transactions"].([]any)
		tx := txs[0].(map[string]any)
		msg := tx["message"].(map[string]any)
		keys := msg["accountKeys"].([]string)
		objs := make([]map[string]any, len(keys))
		for i, k := range keys {
			objs[i] = map[string]any{"pubkey": k, "signer": i < 2, "writable": i == 0, "source": "transaction"}
		}
		msg["accountKeys"] = objs
	}), only("svm.auth.signatureVerify", nil))
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.auth.signatureVerify"))
}

func TestSignatureVerify_SignaturesDetailSkips(t *testing.T) {
	// transactionDetails:"signatures" returns bare strings — no message
	// material. Must skip, never pass silently, never reject.
	keys := makeKeyFixtures(t, 1)
	res := runValidate(t, "getBlock", `[100]`, validBlockFixture(t, func(b map[string]any) {
		b["transactions"] = []any{base58Encode(ed25519.Sign(keys[0].priv, []byte("payload")))}
	}), only("svm.auth.signatureVerify", nil))
	assert.NoError(t, res.Err)
	assert.Equal(t, "skip", outcomeOf(res, "svm.auth.signatureVerify"))
}

func TestSignatureVerify_GetTransactionEnvelope(t *testing.T) {
	keys := makeKeyFixtures(t, 2)
	recent := make([]byte, 32)
	recent[0] = 2
	tx := buildTx(t, false, keys, 1, recent, standardInstr(), nil)
	env := map[string]any{"slot": 100, "transaction": tx.json, "meta": map[string]any{}}
	raw, err := json.Marshal(env)
	require.NoError(t, err)
	res := runValidate(t, "getTransaction", `["sig"]`, string(raw), only("svm.auth.signatureVerify", nil))
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.auth.signatureVerify"))
}

func TestSignatureVerify_NullResultSkips(t *testing.T) {
	// A null result (skipped slot / pruned history) is "nothing there", not
	// corruption: no error, and an explicit skip outcome (never a silent pass).
	res := runValidate(t, "getBlock", `[100]`, `null`, only("svm.auth.signatureVerify", nil))
	assert.NoError(t, res.Err)
	assert.Equal(t, "skip", outcomeOf(res, "svm.auth.signatureVerify"))
}

// ---------- tx shape ----------

func TestTxShape_HeaderBounds(t *testing.T) {
	cases := []struct {
		name   string
		tamper func(map[string]any)
	}{
		{"programIdIndex out of range", func(b map[string]any) {
			msg := b["transactions"].([]any)[0].(map[string]any)["message"].(map[string]any)
			msg["instructions"].([]map[string]any)[0]["programIdIndex"] = 99
		}},
		{"account index out of range", func(b map[string]any) {
			msg := b["transactions"].([]any)[0].(map[string]any)["message"].(map[string]any)
			msg["instructions"].([]map[string]any)[0]["accounts"] = []int{55}
		}},
		{"signature count mismatch", func(b map[string]any) {
			tx := b["transactions"].([]any)[0].(map[string]any)
			tx["signatures"] = append(tx["signatures"].([]string), tx["signatures"].([]string)[0])
		}},
		{"malformed recentBlockhash", func(b map[string]any) {
			msg := b["transactions"].([]any)[0].(map[string]any)["message"].(map[string]any)
			msg["recentBlockhash"] = "not!base58!"
		}},
		{"readonly signed exceeds signers", func(b map[string]any) {
			msg := b["transactions"].([]any)[0].(map[string]any)["message"].(map[string]any)
			hdr := msg["header"].(map[string]any)
			hdr["numReadonlySignedAccounts"] = 5
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := runValidate(t, "getBlock", `[100]`, validBlockFixture(t, tc.tamper), only("svm.struct.txShape", nil))
			require.Error(t, res.Err, tc.name)
			assert.Equal(t, "svm.struct.txShape", res.RejectedCheckID)
		})
	}
}

// ---------- signature uniqueness ----------

func TestSigUniqueness_DuplicateRejected(t *testing.T) {
	res := runValidate(t, "getBlock", `[100]`, validBlockFixture(t, func(b map[string]any) {
		txs := b["transactions"].([]any)
		// Duplicate the single transaction — same signature twice in the block.
		txs = append(txs, txs[0])
		b["transactions"] = txs
	}), only("svm.struct.sigUniqueness", nil))
	require.Error(t, res.Err)
	assert.Equal(t, "svm.struct.sigUniqueness", res.RejectedCheckID)
}

func TestSigUniqueness_DistinctPasses(t *testing.T) {
	res := runValidate(t, "getBlock", `[100]`, validBlockFixture(t, nil), only("svm.struct.sigUniqueness", nil))
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.struct.sigUniqueness"))
}

// ---------- block shape ----------

func TestBlockShape_SelfParentRejected(t *testing.T) {
	res := runValidate(t, "getBlock", `[100]`, validBlockFixture(t, func(b map[string]any) {
		b["previousBlockhash"] = b["blockhash"]
	}), only("svm.struct.blockShape", nil))
	require.Error(t, res.Err)
}

func TestBlockShape_GenesisZeroParentAllowed(t *testing.T) {
	// Slot 0 self-references an all-zeros previous hash — legal.
	zero := make([]byte, 32)
	res := runValidate(t, "getBlock", `[0]`, validBlockFixture(t, func(b map[string]any) {
		genesisHash := make([]byte, 32)
		genesisHash[0] = 1
		b["blockhash"] = base58Encode(genesisHash)
		b["previousBlockhash"] = base58Encode(zero)
		b["parentSlot"] = 0
	}), only("svm.struct.blockShape", nil))
	assert.NoError(t, res.Err)
}

func TestBlockShape_ParentSlotAboveRequestedRejected(t *testing.T) {
	res := runValidate(t, "getBlock", `[100]`, validBlockFixture(t, func(b map[string]any) {
		b["parentSlot"] = 100
	}), only("svm.struct.blockShape", nil))
	require.Error(t, res.Err)
}

func TestBlockShape_FutureBlockTimeRejected(t *testing.T) {
	res := runValidate(t, "getBlock", `[100]`, validBlockFixture(t, func(b map[string]any) {
		b["blockTime"] = 4_000_000_000 // year 2096
	}), only("svm.struct.blockShape", nil))
	require.Error(t, res.Err)
}

func TestBlockShape_MalformedHashRejected(t *testing.T) {
	res := runValidate(t, "getBlock", `[100]`, validBlockFixture(t, func(b map[string]any) {
		b["blockhash"] = "0xdeadbeef"
	}), only("svm.struct.blockShape", nil))
	require.Error(t, res.Err)
}

func TestBlockShape_ValidPasses(t *testing.T) {
	res := runValidate(t, "getBlock", `[100]`, validBlockFixture(t, nil), only("svm.struct.blockShape", nil))
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.struct.blockShape"))
}

// ---------- genesis hash ----------

func TestGenesisHash_MismatchRejected(t *testing.T) {
	expected := base58Encode(bytes32(0xaa))
	res := runValidate(t, "getGenesisHash", `[]`, `"`+base58Encode(bytes32(0xbb))+`"`,
		only("svm.auth.genesisHash", map[string]string{"expected": expected}))
	require.Error(t, res.Err)
	assert.Equal(t, "svm.auth.genesisHash", res.RejectedCheckID)
}

func TestGenesisHash_MatchPasses(t *testing.T) {
	expected := base58Encode(bytes32(0xaa))
	res := runValidate(t, "getGenesisHash", `[]`, `"`+expected+`"`,
		only("svm.auth.genesisHash", map[string]string{"expected": expected}))
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.auth.genesisHash"))
}

func TestGenesisHash_UnknownClusterSkips(t *testing.T) {
	// No expected hash bound (cluster unknown to us): never fabricate ground
	// truth — skip.
	res := runValidate(t, "getGenesisHash", `[]`, `"`+base58Encode(bytes32(0xbb))+`"`,
		only("svm.auth.genesisHash", nil))
	assert.NoError(t, res.Err)
	assert.Equal(t, "skip", outcomeOf(res, "svm.auth.genesisHash"))
}

// ---------- magnitude ----------

func TestMagnitude_CommitmentDepth(t *testing.T) {
	mk := func(n int) string {
		arr := make([]int, n)
		for i := range arr {
			arr[i] = i
		}
		raw, _ := json.Marshal(map[string]any{"commitment": arr, "totalStake": 100})
		return string(raw)
	}
	res := runValidate(t, "getBlockCommitment", `[100]`, mk(32), only("svm.shape.magnitude", nil))
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.shape.magnitude"))

	res = runValidate(t, "getBlockCommitment", `[100]`, mk(31), only("svm.shape.magnitude", nil))
	require.Error(t, res.Err)
	assert.Equal(t, "svm.shape.magnitude", res.RejectedCheckID)
}

func TestMagnitude_SigListOversize(t *testing.T) {
	list := make([]map[string]any, 1001)
	for i := range list {
		list[i] = map[string]any{"signature": fmt.Sprintf("sig%d", i), "slot": 100}
	}
	raw, _ := json.Marshal(list)
	res := runValidate(t, "getSignaturesForAddress", `["addr"]`, string(raw), only("svm.shape.magnitude", nil))
	require.Error(t, res.Err)
}

// ---------- commitment param ----------

func TestCommitmentParam_Vocabulary(t *testing.T) {
	for _, level := range []string{"processed", "confirmed", "finalized", "max", "singleGossip"} {
		res := runValidate(t, "getAccountInfo", `["key", {"commitment": "`+level+`"}]`,
			`{"context":{"slot":1},"value":null}`, only("svm.shape.commitmentParam", nil))
		assert.NoError(t, res.Err, "commitment %q must be accepted", level)
	}
	res := runValidate(t, "getAccountInfo", `["key", {"commitment": "eventually"}]`,
		`{"context":{"slot":1},"value":null}`, only("svm.shape.commitmentParam", nil))
	require.Error(t, res.Err)
}

func TestCommitmentParam_LimitBound(t *testing.T) {
	res := runValidate(t, "getSignaturesForAddress", `["addr", {"limit": 5000}]`, `[]`,
		only("svm.shape.commitmentParam", nil))
	require.Error(t, res.Err)
	res = runValidate(t, "getSignaturesForAddress", `["addr", {"limit": 100}]`, `[]`,
		only("svm.shape.commitmentParam", nil))
	assert.NoError(t, res.Err)
}

// ---------- slot encoding ----------

func TestSlotEncoding_GetBlocksStrictlyIncreasing(t *testing.T) {
	res := runValidate(t, "getBlocks", `[1, 100]`, `[10, 20, 30]`, only("svm.shape.slotEncoding", nil))
	assert.NoError(t, res.Err)
	res = runValidate(t, "getBlocks", `[1, 100]`, `[10, 10, 30]`, only("svm.shape.slotEncoding", nil))
	require.Error(t, res.Err)
	res = runValidate(t, "getBlocks", `[1, 100]`, `[30, 20]`, only("svm.shape.slotEncoding", nil))
	require.Error(t, res.Err)
}

func TestSlotEncoding_NonNumericRejected(t *testing.T) {
	res := runValidate(t, "getSlot", `[]`, `"not-a-number"`, only("svm.shape.slotEncoding", nil))
	require.Error(t, res.Err)
	res = runValidate(t, "getSlot", `[]`, `1234`, only("svm.shape.slotEncoding", nil))
	assert.NoError(t, res.Err)
}

func TestSlotEncoding_EpochInfoFields(t *testing.T) {
	ok := `{"absoluteSlot": 200, "blockHeight": 180, "slotIndex": 20, "epoch": 10}`
	res := runValidate(t, "getEpochInfo", `[]`, ok, only("svm.shape.slotEncoding", nil))
	assert.NoError(t, res.Err)
	bad := `{"absoluteSlot": 100, "blockHeight": 180, "slotIndex": 20, "epoch": 10}`
	res = runValidate(t, "getEpochInfo", `[]`, bad, only("svm.shape.slotEncoding", nil))
	require.Error(t, res.Err)
	neg := `{"absoluteSlot": -5, "blockHeight": 0, "slotIndex": 0, "epoch": 0}`
	res = runValidate(t, "getEpochInfo", `[]`, neg, only("svm.shape.slotEncoding", nil))
	require.Error(t, res.Err)
}

// ---------- level preset composition ----------

func TestCheckSetForLevel_IntrinsicSuperset(t *testing.T) {
	intr := CheckSetForLevel(LevelIntrinsic)
	require.NotEmpty(t, intr)
	for id := range intr {
		assert.True(t, intr[id].Enabled, id)
	}
	// off enables nothing.
	assert.Empty(t, CheckSetForLevel(LevelOff))
	// Every enabled intrinsic check resolves through the registry.
	for id := range intr {
		found := false
		for _, c := range allChecks {
			if c.ID == id {
				found = true
				break
			}
		}
		assert.True(t, found, "level enables unknown check %s", id)
	}
}

// ---------- observe-only ----------

func TestObserveOnly_ServesAndRecordsWouldReject(t *testing.T) {
	res := runValidateObserve(t, "getBlock", `[100]`, validBlockFixture(t, func(b map[string]any) {
		b["previousBlockhash"] = b["blockhash"]
	}), only("svm.struct.blockShape", nil), true)
	assert.NoError(t, res.Err, "observe-only must serve the response")
	assert.Equal(t, "would_reject", outcomeOf(res, "svm.struct.blockShape"))
	require.Len(t, res.Recorded, 1)
	assert.Equal(t, "would_reject", res.Recorded[0].Verdict)
	assert.Equal(t, "svm.struct.blockShape", res.Recorded[0].CheckID)
}

// ---------- chain-safety: unverifiable never rejects ----------

func TestChainSafety_UnverifiableEncodingsSkip(t *testing.T) {
	// Wire-string txs (encoding base64/base58) carry no parseable message in
	// our model — every authenticity/structural check must skip, not reject.
	raw := `{"blockhash":"` + base58Encode(bytes32(1)) + `","previousBlockhash":"` + base58Encode(bytes32(2)) +
		`","parentSlot":99,"blockHeight":98,"blockTime":1700000000,"transactions":["dGVzdA=="]}`
	cs := CheckSet{}.
		Enable("svm.auth.signatureVerify", nil).
		Enable("svm.struct.txShape", nil)
	res := runValidate(t, "getBlock", `[100]`, raw, cs)
	assert.NoError(t, res.Err)
	assert.Equal(t, "skip", outcomeOf(res, "svm.auth.signatureVerify"))
	assert.Equal(t, "skip", outcomeOf(res, "svm.struct.txShape"))
}

func bytes32(seed byte) []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = seed
	}
	return b
}

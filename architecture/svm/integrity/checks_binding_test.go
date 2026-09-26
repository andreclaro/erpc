package integrity

import (
	"testing"

	"github.com/erpc/erpc/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- helpers ----

// txEnvelopeResultJSON builds a getTransaction-style result whose
// transaction object carries the given inline signatures.
func txEnvelopeResultJSON(sigs ...string) string {
	out := `{"slot":100,"blockTime":1700000000,"transaction":{"signatures":[`
	for i, s := range sigs {
		if i > 0 {
			out += ","
		}
		out += `"` + s + `"`
	}
	return out + `],"message":{"accountKeys":[]}},"meta":{"err":null}}`
}

func blocksResult(slots ...int) string {
	out := `[`
	for i, s := range slots {
		if i > 0 {
			out += ","
		}
		out += itoa(s)
	}
	return out + `]`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func validateBinding(t *testing.T, method, paramsJSON, result string) Result {
	t.Helper()
	req := common.NewNormalizedRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + paramsJSON + `}`))
	jrr := common.MustNewJsonRpcResponseFromBytes([]byte("1"), []byte(result), nil)
	rs := common.NewNormalizedResponse().WithRequest(req).WithJsonRpcResponse(jrr)
	var params []any
	require.NoError(t, common.SonicCfg.Unmarshal([]byte(paramsJSON), &params))
	return Validate(t.Context(), Input{
		Method:   method,
		Upstream: common.NewFakeUpstream("u"),
		Response: rs,
		Checks:   CheckSetForLevel(LevelIntrinsic),
		Params:   params,
		Reorg:    DefaultReorgPolicy(),
	})
}

// ---- svm.struct.requestedSigMatch ----

func TestRequestedSigMatch_PassesMatchingSig(t *testing.T) {
	sig := "5K7R9wBf8h2mX3JvQmPzYcLdNe4fGdKzEtEWsXaF8p3qS1uVbMn6jHkC2oArGiD4tEwFyU7hN"
	res := validateBinding(t, "getTransaction",
		`["`+sig+`"]`, txEnvelopeResultJSON(sig))
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.struct.requestedSigMatch"))
}

func TestRequestedSigMatch_RejectsDifferentTx(t *testing.T) {
	res := validateBinding(t, "getTransaction",
		`["5K7R9wBf8h2mX3JvQmPzYcLdNe4fGdKzEtEWsXaF8p3qS1uVbMn6jHkC2oArGiD4tEwFyU7hN"]`,
		txEnvelopeResultJSON("3JkM8pQvR2wXzL9dYnFbThC5sEaGuK7oN4iUfHgD6tAqS1eVrBmWc"))
	require.Error(t, res.Err)
	assert.Equal(t, "svm.struct.requestedSigMatch", res.RejectedCheckID)
}

func TestRequestedSigMatch_LaterServedSignaturePasses(t *testing.T) {
	// Membership, not index-0: multi-sig transactions are legitimately
	// queried by any member's signature — signer order is a wallet detail,
	// not transaction identity (regression: only signatures[0] was compared).
	req := "5K7R9wBf8h2mX3JvQmPzYcLdNe4fGdKzEtEWsXaF8p3qS1uVbMn6jHkC2oArGiD4tEwFyU7hN"
	other := "3JkM8pQvR2wXzL9dYnFbThC5sEaGuK7oN4iUfHgD6tAqS1eVrBmWc"
	res := validateBinding(t, "getTransaction",
		`["`+req+`"]`, txEnvelopeResultJSON(other, req))
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.struct.requestedSigMatch"))
}

func TestRequestedSigMatch_SkipsNullResult(t *testing.T) {
	res := validateBinding(t, "getTransaction",
		`["5K7R9wBf8h2mX3JvQmPzYcLdNe4fGdKzEtEWsXaF8p3qS1uVbMn6jHkC2oArGiD4tEwFyU7hN"]`,
		`null`)
	assert.NoError(t, res.Err)
	assert.Equal(t, "skip", outcomeOf(res, "svm.struct.requestedSigMatch"))
}

// ---- svm.shape.blocksLimit ----

func TestBlocksLimit_PassesWithinLimit(t *testing.T) {
	res := validateBinding(t, "getBlocksWithLimit", `[100, 5]`, blocksResult(100, 101, 102))
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.shape.blocksLimit"))
}

func TestBlocksLimit_RejectsOverLimit(t *testing.T) {
	res := validateBinding(t, "getBlocksWithLimit", `[100, 2]`, blocksResult(100, 101, 102, 103))
	require.Error(t, res.Err)
	assert.Equal(t, "svm.shape.blocksLimit", res.RejectedCheckID)
}

func TestBlocksLimit_RejectsOverRange(t *testing.T) {
	res := validateBinding(t, "getBlocks", `[100, 102]`, blocksResult(100, 101, 102, 103, 104))
	require.Error(t, res.Err)
	assert.Equal(t, "svm.shape.blocksLimit", res.RejectedCheckID)
}

func TestBlocksLimit_SkipsUnboundedGetBlocks(t *testing.T) {
	// No end slot: the request is "to tip" — no deterministic bound exists.
	res := validateBinding(t, "getBlocks", `[100]`, blocksResult(100, 101, 102, 103))
	assert.NoError(t, res.Err)
	assert.Equal(t, "skip", outcomeOf(res, "svm.shape.blocksLimit"))
}

// ---- svm.struct.rewardsShape ----

func TestRewardsShape_PassesValidRewards(t *testing.T) {
	pk := base58Encode(bytes32(7))
	result := `{"blockhash":"` + base58Encode(bytes32(1)) + `","previousBlockhash":"` + base58Encode(bytes32(2)) + `","parentSlot":9,"blockHeight":100,"blockTime":1700000000,"transactions":[],"rewards":[{"pubkey":"` + pk + `","lamports":-5000,"rewardType":"rent","commission":0}]}`
	res := validateBinding(t, "getBlock", `[100]`, result)
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.struct.rewardsShape"))
}

func TestRewardsShape_RejectsBadPayee(t *testing.T) {
	result := `{"blockhash":"` + base58Encode(bytes32(1)) + `","previousBlockhash":"` + base58Encode(bytes32(2)) + `","parentSlot":9,"blockHeight":100,"blockTime":1700000000,"transactions":[],"rewards":[{"pubkey":"not!base58!","lamports":5000,"rewardType":"fee"}]}`
	res := validateBinding(t, "getBlock", `[100]`, result)
	require.Error(t, res.Err)
	assert.Equal(t, "svm.struct.rewardsShape", res.RejectedCheckID)
}

func TestRewardsShape_RejectsNonIntegerLamports(t *testing.T) {
	pk := base58Encode(bytes32(7))
	result := `{"blockhash":"` + base58Encode(bytes32(1)) + `","previousBlockhash":"` + base58Encode(bytes32(2)) + `","parentSlot":9,"blockHeight":100,"blockTime":1700000000,"transactions":[],"rewards":[{"pubkey":"` + pk + `","lamports":"lots","rewardType":"fee"}]}`
	res := validateBinding(t, "getBlock", `[100]`, result)
	require.Error(t, res.Err)
}

// ---- wiring ----

func TestBindingChecks_InIntrinsicLevel(t *testing.T) {
	cs := CheckSetForLevel(LevelIntrinsic)
	for _, id := range []string{"svm.struct.requestedSigMatch", "svm.shape.blocksLimit", "svm.struct.rewardsShape"} {
		assert.True(t, cs[id].Enabled, id)
	}
}

func TestRequestedSigMatch_HardRejectOverride(t *testing.T) {
	cs := CheckSetForLevel(LevelIntrinsic)
	enableHardReject(cs, "svm.struct.requestedSigMatch")
	res := validateBinding(t, "getTransaction",
		`["5K7R9wBf8h2mX3JvQmPzYcLdNe4fGdKzEtEWsXaF8p3qS1uVbMn6jHkC2oArGiD4tEwFyU7hN"]`,
		txEnvelopeResultJSON("3JkM8pQvR2wXzL9dYnFbThC5sEaGuK7oN4iUfHgD6tAqS1eVrBmWc"))
	require.Error(t, res.Err)
	assert.Equal(t, "svm.struct.requestedSigMatch", res.RejectedCheckID)
}

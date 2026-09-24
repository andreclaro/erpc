package integrity

import (
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/erpc/erpc/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- SPL layout fixtures ----

// validMintBytes builds an 82-byte base SPL mint: COption tag, authority,
// supply, decimals, isInitialized, COption tag, authority.
func validMintBytes(isInit byte) []byte {
	b := make([]byte, mintLen)
	b[0] = 1 // mintAuthority present
	for i := 4; i < 36; i++ {
		b[i] = byte(i)
	}
	// supply u64 at 36:44 stays 0
	b[44] = 6 // decimals
	b[45] = isInit
	// freezeAuthority COption tag at 46:50 stays 0
	return b
}

// validTokenAccountBytes builds a 165-byte base SPL token account.
func validTokenAccountBytes(state byte) []byte {
	b := make([]byte, tokenAccountMinLen)
	for i := 0; i < 32; i++ {
		b[i] = byte(i + 1)      // mint
		b[32+i] = byte(255 - i) // owner
	}
	// amount u64 at 64:72 stays 0
	b[acctStateOff] = state
	return b
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func mintAccountResult(owner string, data []byte) string {
	return fmt.Sprintf(
		`{"context":{"slot":100},"value":{"owner":%q,"lamports":1000000,"executable":false,"rentEpoch":2,"data":[%q,"base64"]}}`,
		owner, b64(data))
}

func byOwnerResult(entries ...string) string {
	out := `{"context":{"slot":100},"value":[`
	for i, e := range entries {
		if i > 0 {
			out += ","
		}
		out += e
	}
	return out + `]}`
}

func acctEntry(owner, b64data string) string {
	return fmt.Sprintf(
		`{"pubkey":"Addr1111111111111111111111111111111111111","account":{"owner":%q,"lamports":100,"executable":false,"rentEpoch":2,"data":[%q,"base64"]}}`,
		owner, b64data)
}

func validateToken(t *testing.T, method, result string, cs CheckSet) Result {
	t.Helper()
	req := common.NewNormalizedRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":[]}`))
	jrr := common.MustNewJsonRpcResponseFromBytes([]byte("1"), []byte(result), nil)
	rs := common.NewNormalizedResponse().WithRequest(req).WithJsonRpcResponse(jrr)
	return Validate(t.Context(), Input{
		Method:   method,
		Upstream: common.NewFakeUpstream("u"),
		Response: rs,
		Checks:   cs,
		Reorg:    DefaultReorgPolicy(),
	})
}

func intrinsicTokenSet(t *testing.T) CheckSet {
	t.Helper()
	return CheckSetForLevel(LevelIntrinsic)
}

// ---- svm.auth.tokenProgram ----

func TestTokenProgram_RejectsBogusOwner(t *testing.T) {
	res := validateToken(t, "getTokenAccountsByOwner",
		byOwnerResult(acctEntry("FakeTokenProgram111111111111111111111111111", b64(validTokenAccountBytes(1)))),
		intrinsicTokenSet(t))
	require.Error(t, res.Err)
	assert.Equal(t, "svm.auth.tokenProgram", res.RejectedCheckID)
}

func TestTokenProgram_RejectsBogusParsedProgram(t *testing.T) {
	entry := `{"pubkey":"Addr1111111111111111111111111111111111111","account":{"owner":"` + splTokenProgramID + `","lamports":100,"data":{"program":"fake-token-program","parsed":{"type":"account"},"space":165}}}`
	res := validateToken(t, "getTokenAccountsByOwner", byOwnerResult(entry), intrinsicTokenSet(t))
	require.Error(t, res.Err)
	assert.Equal(t, "svm.auth.tokenProgram", res.RejectedCheckID)
}

func TestTokenProgram_PassesKnownOwners(t *testing.T) {
	res := validateToken(t, "getTokenAccountsByOwner", byOwnerResult(
		acctEntry(splTokenProgramID, b64(validTokenAccountBytes(1))),
		acctEntry(token2022ProgramID, b64(validTokenAccountBytes(2))),
	), intrinsicTokenSet(t))
	assert.NoError(t, res.Err)
}

func TestTokenProgram_PassesParsedSplToken(t *testing.T) {
	entry := `{"pubkey":"Addr1111111111111111111111111111111111111","account":{"owner":"` + splTokenProgramID + `","lamports":100,"data":{"program":"spl-token-2022","parsed":{"type":"account"},"space":165}}}`
	res := validateToken(t, "getTokenAccountsByOwner", byOwnerResult(entry), intrinsicTokenSet(t))
	assert.NoError(t, res.Err)
}

func TestTokenProgram_SkipsEmptyResult(t *testing.T) {
	res := validateToken(t, "getTokenAccountsByOwner",
		`{"context":{"slot":100},"value":[]}`, intrinsicTokenSet(t))
	assert.NoError(t, res.Err)
	assert.Equal(t, "skip", outcomeOf(res, "svm.auth.tokenProgram"))
}

// ---- svm.struct.tokenMintShape ----

func TestTokenMintShape_ValidMintPasses(t *testing.T) {
	res := validateToken(t, "getAccountInfo",
		mintAccountResult(splTokenProgramID, validMintBytes(1)), intrinsicTokenSet(t))
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.struct.tokenMintShape"))
}

func TestTokenMintShape_UninitializedMintPasses(t *testing.T) {
	res := validateToken(t, "getAccountInfo",
		mintAccountResult(token2022ProgramID, validMintBytes(0)), intrinsicTokenSet(t))
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.struct.tokenMintShape"))
}

func TestTokenMintShape_RejectsGarbageIsInitialized(t *testing.T) {
	b := validMintBytes(1)
	b[45] = 7
	res := validateToken(t, "getAccountInfo",
		mintAccountResult(splTokenProgramID, b), intrinsicTokenSet(t))
	require.Error(t, res.Err)
	assert.Equal(t, "svm.struct.tokenMintShape", res.RejectedCheckID)
}

func TestTokenMintShape_RejectsBadOptionTag(t *testing.T) {
	b := validMintBytes(1)
	b[46] = 9 // freezeAuthority tag 9 — not a COption
	res := validateToken(t, "getAccountInfo",
		mintAccountResult(splTokenProgramID, b), intrinsicTokenSet(t))
	require.Error(t, res.Err)
}

func TestTokenMintShape_RejectsTamperedMintAuthorityTag(t *testing.T) {
	b := validMintBytes(1)
	b[0] = 4
	res := validateToken(t, "getAccountInfo",
		mintAccountResult(splTokenProgramID, b), intrinsicTokenSet(t))
	require.Error(t, res.Err)
}

func TestTokenMintShape_SkipsNonTokenOwner(t *testing.T) {
	// A system-owned 82-byte account is NOT a mint — the check must stay out
	// of getAccountInfo's way for arbitrary addresses.
	res := validateToken(t, "getAccountInfo",
		mintAccountResult("11111111111111111111111111111111", validMintBytes(7)),
		intrinsicTokenSet(t))
	assert.NoError(t, res.Err)
	assert.Equal(t, "skip", outcomeOf(res, "svm.struct.tokenMintShape"))
}

func TestTokenMintShape_SkipsNonCanonicalLength(t *testing.T) {
	// Token-2022 mint with extensions (or a token account) — ambiguous, skip.
	res := validateToken(t, "getAccountInfo",
		mintAccountResult(splTokenProgramID, validTokenAccountBytes(1)), intrinsicTokenSet(t))
	assert.NoError(t, res.Err)
	assert.Equal(t, "skip", outcomeOf(res, "svm.struct.tokenMintShape"))
}

// ---- svm.struct.tokenAccountShape ----

func TestTokenAccountShape_ValidAccountsPass(t *testing.T) {
	res := validateToken(t, "getTokenAccountsByOwner", byOwnerResult(
		acctEntry(splTokenProgramID, b64(validTokenAccountBytes(1))),
		acctEntry(token2022ProgramID, b64(validTokenAccountBytes(2))),
	), intrinsicTokenSet(t))
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.struct.tokenAccountShape"))
}

func TestTokenAccountShape_RejectsShortPayload(t *testing.T) {
	res := validateToken(t, "getTokenAccountsByOwner",
		byOwnerResult(acctEntry(splTokenProgramID, b64(make([]byte, 100)))),
		intrinsicTokenSet(t))
	require.Error(t, res.Err)
	assert.Equal(t, "svm.struct.tokenAccountShape", res.RejectedCheckID)
}

func TestTokenAccountShape_RejectsImpossibleState(t *testing.T) {
	res := validateToken(t, "getTokenAccountsByOwner",
		byOwnerResult(acctEntry(splTokenProgramID, b64(validTokenAccountBytes(9)))),
		intrinsicTokenSet(t))
	require.Error(t, res.Err)
}

func TestTokenAccountShape_RejectsBadDelegateTag(t *testing.T) {
	b := validTokenAccountBytes(1)
	b[acctDelegateTagOff] = 3
	res := validateToken(t, "getTokenAccountsByOwner",
		byOwnerResult(acctEntry(splTokenProgramID, b64(b))), intrinsicTokenSet(t))
	require.Error(t, res.Err)
}

func TestTokenAccountShape_SkipsParsedEntries(t *testing.T) {
	entry := `{"pubkey":"Addr1111111111111111111111111111111111111","account":{"owner":"` + splTokenProgramID + `","lamports":100,"data":{"program":"spl-token","parsed":{"type":"account"},"space":165}}}`
	res := validateToken(t, "getTokenAccountsByOwner", byOwnerResult(entry), intrinsicTokenSet(t))
	assert.NoError(t, res.Err)
	assert.Equal(t, "skip", outcomeOf(res, "svm.struct.tokenAccountShape"))
}

// ---- wiring ----

func TestTokenChecks_InIntrinsicLevelOnly(t *testing.T) {
	cs := CheckSetForLevel(LevelIntrinsic)
	for _, id := range []string{"svm.auth.tokenProgram", "svm.struct.tokenMintShape", "svm.struct.tokenAccountShape"} {
		cfg, ok := cs[id]
		require.True(t, ok, id)
		assert.True(t, cfg.Enabled, id)
	}
	// Enabling the intrinsic level must NOT silently enable corroborated
	// machinery.
	cs2 := CheckSetForLevel(LevelIntrinsic)
	assert.False(t, cs2["svm.commit.parentLink"].Enabled)
}

func TestTokenAccountShape_HardRejectOverride(t *testing.T) {
	cs := intrinsicTokenSet(t)
	enableHardReject(cs, "svm.struct.tokenAccountShape")
	res := validateToken(t, "getTokenAccountsByOwner",
		byOwnerResult(acctEntry(splTokenProgramID, b64(make([]byte, 100)))), cs)
	require.Error(t, res.Err)
	assert.Equal(t, "svm.struct.tokenAccountShape", res.RejectedCheckID)
}

// TestTokenAccountShape_NoEarlyExitOnValidFirstView: a valid first entry must
// not mask a poisoned second one.
func TestTokenAccountShape_NoEarlyExitOnValidFirstView(t *testing.T) {
	res := validateToken(t, "getTokenAccountsByOwner", byOwnerResult(
		acctEntry(splTokenProgramID, b64(validTokenAccountBytes(1))),
		acctEntry(splTokenProgramID, b64(validTokenAccountBytes(8))),
	), intrinsicTokenSet(t))
	require.Error(t, res.Err)
	assert.Equal(t, "svm.struct.tokenAccountShape", res.RejectedCheckID)
}

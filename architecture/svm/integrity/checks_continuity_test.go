package integrity

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- svm.cont.headProgression ----

func TestHeadProgression_ForwardMovePasses(t *testing.T) {
	chain := NewChainState()
	chain.NoteHead("processed", 900)
	res := validateChain(t, "getSlot", `[]`, `950`,
		corroboratedSet("svm.cont.headProgression"), chain, nil)
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.cont.headProgression"))
	// The store advanced.
	got, ok := chain.LastHead("processed")
	require.True(t, ok)
	assert.Equal(t, int64(950), got)
}

func TestHeadProgression_BackwardMoveRecords(t *testing.T) {
	chain := NewChainState()
	chain.NoteHead("processed", 950)
	res := validateChain(t, "getSlot", `[]`, `900`,
		corroboratedSet("svm.cont.headProgression"), chain, nil)
	// ReorgSensitive default: recorded, never rejected.
	assert.NoError(t, res.Err)
	require.Len(t, res.Recorded, 1)
	assert.Equal(t, "svm.cont.headProgression", res.Recorded[0].CheckID)
	// The store did NOT move backwards — the evidence stays observable.
	got, _ := chain.LastHead("processed")
	assert.Equal(t, int64(950), got)
}

func TestHeadProgression_BackwardMoveHardRejects(t *testing.T) {
	chain := NewChainState()
	chain.NoteHead("processed", 950)
	cs := corroboratedSet("svm.cont.headProgression")
	enableHardReject(cs, "svm.cont.headProgression")
	res := validateChain(t, "getSlot", `[]`, `900`, cs, chain, nil)
	require.Error(t, res.Err)
	assert.Equal(t, "svm.cont.headProgression", res.RejectedCheckID)
}

func TestHeadProgression_BucketsAreIndependent(t *testing.T) {
	chain := NewChainState()
	chain.NoteHead("finalized", 990)
	// A processed head below the finalized head is an inversion.
	res := validateChain(t, "getSlot", `[]`, `950`,
		corroboratedSet("svm.cont.headProgression"), chain, nil)
	require.Len(t, res.Recorded, 1)
	assert.Contains(t, res.Recorded[0].Reason, "inverted finality")
}

func TestHeadProgression_FinalizedBucketSkipsCrossCheck(t *testing.T) {
	chain := NewChainState()
	chain.NoteHead("processed", 950)
	// finalized naturally sits below processed — no inversion for the
	// finalized bucket itself.
	res := validateChain(t, "getSlot", `[{"commitment":"finalized"}]`, `940`,
		corroboratedSet("svm.cont.headProgression"), chain, nil)
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.cont.headProgression"))
	assert.Equal(t, int64(940), chainMustLastHead(t, chain, "finalized"))
}

func chainMustLastHead(t *testing.T, c *ChainState, bucket string) int64 {
	t.Helper()
	v, ok := c.LastHead(bucket)
	require.True(t, ok)
	return v
}

func TestHeadProgression_CommitmentStringParam(t *testing.T) {
	chain := NewChainState()
	res := validateChain(t, "getSlot", `["confirmed"]`, `940`,
		corroboratedSet("svm.cont.headProgression"), chain, nil)
	assert.NoError(t, res.Err)
	assert.Equal(t, int64(940), chainMustLastHead(t, chain, "confirmed"))
}

func TestHeadProgression_EpochInfoUsesAbsoluteSlot(t *testing.T) {
	chain := NewChainState()
	chain.NoteHead("processed", 5*432000+200)
	res := validateChain(t, "getEpochInfo", `[]`,
		epochInfoJSON(5, 300, 5*432000+300),
		corroboratedSet("svm.cont.headProgression"), chain, nil)
	assert.NoError(t, res.Err)
	assert.Equal(t, int64(5*432000+300), chainMustLastHead(t, chain, "processed"))
}

func TestHeadProgression_NoChainSkips(t *testing.T) {
	res := validateChain(t, "getSlot", `[]`, `900`,
		corroboratedSet("svm.cont.headProgression"), nil, nil)
	assert.Equal(t, "skip", outcomeOf(res, "svm.cont.headProgression"))
}

func TestHeadProgression_RejectedHeadNotCommitted(t *testing.T) {
	// A head sighting tainted by ANY enabled check must not enter the
	// progression store — the bad value would become the baseline future
	// regressions are judged against. tipBound rejects the far-future slot;
	// headProgression itself passes.
	chain := NewChainState()
	cs := corroboratedSet("svm.cont.headProgression", "svm.final.tipBound")
	enableHardReject(cs, "svm.final.tipBound")
	tr := tipFake{fakeFinalityResolver{finalizedTip: 1000}, 1000}
	res := validateChain(t, "getSlot", `[]`, `5000`, cs, chain, tr)
	require.Error(t, res.Err)
	assert.Equal(t, "svm.final.tipBound", res.RejectedCheckID)
	_, seen := chain.LastHead("processed")
	assert.False(t, seen, "a rejected head must never enter the progression store")
}

func TestHeadProgression_RecordedMismatchHeadNotCommitted(t *testing.T) {
	// Same gate for a recorded (not rejected) mismatch: recordOnly still
	// means "do not trust this response", so no state commit either.
	chain := NewChainState()
	cs := corroboratedSet("svm.cont.headProgression", "svm.final.tipBound")
	tr := tipFake{fakeFinalityResolver{finalizedTip: 1000}, 1000}
	// 5000 is unfinalized (above the 1000 finalized tip) and beyond latest+1
	// → tipBound records under the default policy; headProgression passes.
	res := validateChain(t, "getSlot", `[]`, `5000`, cs, chain, tr)
	assert.NoError(t, res.Err)
	require.Len(t, res.Recorded, 1)
	_, seen := chain.LastHead("processed")
	assert.False(t, seen, "a recorded-mismatch head must not enter the store")
	// Clean follow-up DOES commit — the gate is per-response, not a lockout.
	res = validateChain(t, "getSlot", `[]`, `900`, cs, chain, tr)
	assert.NoError(t, res.Err)
	assert.Equal(t, int64(900), chainMustLastHead(t, chain, "processed"))
}

// ---- svm.cont.minContextSlot ----

func minCtxResult(slot int64) string {
	return fmt.Sprintf(`{"context":{"slot":%d,"apiVersion":"1.0"},"value":{"lamports":1,"owner":%q,"executable":false,"rentEpoch":2}}`,
		slot, base58Encode(bytes32(9)))
}

func TestMinContextSlot_HonoredFloorPasses(t *testing.T) {
	res := validateChain(t, "getAccountInfo",
		`["addr", {"encoding":"base64","minContextSlot":900}]`, minCtxResult(950),
		corroboratedSet("svm.cont.minContextSlot"), nil, nil)
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.cont.minContextSlot"))
}

func TestMinContextSlot_StaleSnapshotViolates(t *testing.T) {
	res := validateChain(t, "getAccountInfo",
		`["addr", {"encoding":"base64","minContextSlot":900}]`, minCtxResult(850),
		corroboratedSet("svm.cont.minContextSlot"), nil, nil)
	require.Error(t, res.Err)
	assert.Equal(t, "svm.cont.minContextSlot", res.RejectedCheckID)
}

func TestMinContextSlot_NoFloorSkips(t *testing.T) {
	res := validateChain(t, "getAccountInfo", `["addr", {"encoding":"base64"}]`,
		minCtxResult(850), corroboratedSet("svm.cont.minContextSlot"), nil, nil)
	assert.Equal(t, "skip", outcomeOf(res, "svm.cont.minContextSlot"))
}

func TestMinContextSlot_NoContextSlotSkips(t *testing.T) {
	res := validateChain(t, "getAccountInfo",
		`["addr", {"encoding":"base64","minContextSlot":900}]`,
		`{"value":{"lamports":1}}`, corroboratedSet("svm.cont.minContextSlot"), nil, nil)
	assert.Equal(t, "skip", outcomeOf(res, "svm.cont.minContextSlot"))
}

// ---- svm.struct.heightVsSlot ----

func heightSlotBlock(height int64, hashSeed byte) string {
	return fmt.Sprintf(
		`{"blockhash":%q,"previousBlockhash":%q,"parentSlot":1,"blockHeight":%d,"transactions":[],"rewards":[]}`,
		base58Encode(bytes32(hashSeed)), base58Encode(bytes32(hashSeed+1)), height)
}

func TestHeightVsSlot_NormalSkipsPatternPasses(t *testing.T) {
	res := validateChain(t, "getBlock", `[900]`, heightSlotBlock(801, 7),
		corroboratedSet("svm.struct.heightVsSlot"), nil, nil)
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.struct.heightVsSlot"))
}

func TestHeightVsSlot_GenesisEqualityPasses(t *testing.T) {
	res := validateChain(t, "getBlock", `[0]`, heightSlotBlock(0, 7),
		corroboratedSet("svm.struct.heightVsSlot"), nil, nil)
	assert.NoError(t, res.Err)
}

func TestHeightVsSlot_HeightAboveSlotViolates(t *testing.T) {
	res := validateChain(t, "getBlock", `[100]`, heightSlotBlock(101, 7),
		corroboratedSet("svm.struct.heightVsSlot"), nil, nil)
	require.Error(t, res.Err)
	assert.Equal(t, "svm.struct.heightVsSlot", res.RejectedCheckID)
}

func TestHeightVsSlot_NoHeightSkips(t *testing.T) {
	res := validateChain(t, "getBlock", `[900]`,
		`{"blockhash":"x","previousBlockhash":"y","transactions":[]}`,
		corroboratedSet("svm.struct.heightVsSlot"), nil, nil)
	assert.Equal(t, "skip", outcomeOf(res, "svm.struct.heightVsSlot"))
}

// ---- wiring ----

func TestContinuityChecks_Levels(t *testing.T) {
	intrinsic := CheckSetForLevel(LevelIntrinsic)
	corroborated := CheckSetForLevel(LevelCorroborated)
	assert.True(t, intrinsic["svm.struct.heightVsSlot"].Enabled,
		"heightVsSlot is per-block structural truth — intrinsic level")
	assert.True(t, corroborated["svm.cont.headProgression"].Enabled)
	assert.True(t, corroborated["svm.cont.minContextSlot"].Enabled)
	assert.False(t, intrinsic["svm.cont.headProgression"].Enabled)
	assert.Equal(t, FamilyContinuity, checkByID(t, "svm.cont.headProgression").Family)
	assert.Equal(t, Deterministic, checkByID(t, "svm.cont.minContextSlot").Class)
	assert.Equal(t, ReorgSensitive, checkByID(t, "svm.cont.headProgression").Class)
}

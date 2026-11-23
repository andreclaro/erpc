package integrity

// Tests for the finality-consistency checks (svm.final.*): the response's
// slot claims are compared against the upstream's own poller tips. All three
// are ReorgSensitive — the suite covers pass, record-by-default, and the
// hardReject override path.

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tipFake extends the commit-test fake with a latest tip.
type tipFake struct {
	fakeFinalityResolver
	latestTip int64
}

func (f tipFake) Latest(ctx context.Context) (int64, bool) {
	if f.latestTip < 0 {
		return 0, false
	}
	return f.latestTip, true
}

func envResult(slot int64) string {
	return `{"context":{"slot":` + strconv.FormatInt(slot, 10) + `},"value":{"lamports":50,"owner":"11111111111111111111111111111111"}}`
}

func enableHardReject(cs CheckSet, id string) {
	cfg := cs[id]
	b := BehaviorError
	cfg.FailOverride = &b
	cs[id] = cfg
}

func TestFinalizedBound_SlotAboveFinalizedTipRecords(t *testing.T) {
	cs := corroboratedSet("svm.final.finalizedBound")
	tr := tipFake{fakeFinalityResolver{finalizedTip: 990}, 1000}
	res := validateChain(t, "getAccountInfo", `["addr", {"commitment":"finalized"}]`, envResult(995),
		cs, nil, tr)
	assert.NoError(t, res.Err, "reorg-sensitive default records, never rejects")
	require.Len(t, res.Recorded, 1)
	assert.Equal(t, "svm.final.finalizedBound", res.Recorded[0].CheckID)
	// The finality label comes from the request context (getAccountInfo has
	// no numeric request slot), so it reports "unknown" — the record verdict
	// is what matters.
	assert.Equal(t, "unknown", res.Recorded[0].Finality)
}

func TestFinalizedBound_HardRejectOverrideRejects(t *testing.T) {
	cs := corroboratedSet("svm.final.finalizedBound")
	enableHardReject(cs, "svm.final.finalizedBound")
	tr := tipFake{fakeFinalityResolver{finalizedTip: 990}, 1000}
	res := validateChain(t, "getAccountInfo", `["addr", {"commitment":"finalized"}]`, envResult(995),
		cs, nil, tr)
	require.Error(t, res.Err, "hardReject override must reject")
	assert.Equal(t, "svm.final.finalizedBound", res.RejectedCheckID)
}

func TestFinalizedBound_FinalizedSlotPasses(t *testing.T) {
	cs := corroboratedSet("svm.final.finalizedBound")
	tr := tipFake{fakeFinalityResolver{finalizedTip: 990}, 1000}
	res := validateChain(t, "getAccountInfo", `["addr", {"commitment":"finalized"}]`, envResult(980),
		cs, nil, tr)
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.final.finalizedBound"))
}

func TestFinalizedBound_NonFinalizedRequestSkips(t *testing.T) {
	cs := corroboratedSet("svm.final.finalizedBound")
	tr := tipFake{fakeFinalityResolver{finalizedTip: 990}, 1000}
	res := validateChain(t, "getAccountInfo", `["addr", {"commitment":"confirmed"}]`, envResult(995),
		cs, nil, tr)
	assert.NoError(t, res.Err)
	assert.Equal(t, "skip", outcomeOf(res, "svm.final.finalizedBound"))
}

func TestFinalizedBound_UnknownTipSkips(t *testing.T) {
	cs := corroboratedSet("svm.final.finalizedBound")
	tr := tipFake{fakeFinalityResolver{finalizedTip: -1}, -1}
	res := validateChain(t, "getAccountInfo", `["addr", {"commitment":"finalized"}]`, envResult(995),
		cs, nil, tr)
	assert.NoError(t, res.Err)
	assert.Equal(t, "skip", outcomeOf(res, "svm.final.finalizedBound"))
}

func TestSlotAhead_ContextSlotBeyondLatest(t *testing.T) {
	cs := corroboratedSet("svm.final.slotAhead")
	tr := tipFake{fakeFinalityResolver{finalizedTip: 990}, 1000}
	res := validateChain(t, "getAccountInfo", `["addr"]`, envResult(1500), cs, nil, tr)
	assert.NoError(t, res.Err)
	require.Len(t, res.Recorded, 1)
	assert.Equal(t, "svm.final.slotAhead", res.Recorded[0].CheckID)
}

func TestSlotAhead_WithinLatestPasses(t *testing.T) {
	cs := corroboratedSet("svm.final.slotAhead")
	tr := tipFake{fakeFinalityResolver{finalizedTip: 990}, 1000}
	// latest+1 is the natural one-slot race window — still a pass.
	res := validateChain(t, "getAccountInfo", `["addr"]`, envResult(1001), cs, nil, tr)
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.final.slotAhead"))
}

func TestSlotAhead_NoResolverSkips(t *testing.T) {
	cs := corroboratedSet("svm.final.slotAhead")
	res := validateChain(t, "getAccountInfo", `["addr"]`, envResult(1500), cs, nil,
		fakeFinalityResolver{finalizedTip: 990}) // not a TipResolver
	assert.NoError(t, res.Err)
	assert.Equal(t, "skip", outcomeOf(res, "svm.final.slotAhead"))
}

func TestTipBound_GetSlotBeyondLatest(t *testing.T) {
	cs := corroboratedSet("svm.final.tipBound")
	tr := tipFake{fakeFinalityResolver{finalizedTip: 990}, 1000}
	res := validateChain(t, "getSlot", `[]`, `1500`, cs, nil, tr)
	assert.NoError(t, res.Err)
	require.Len(t, res.Recorded, 1)
	assert.Equal(t, "svm.final.tipBound", res.Recorded[0].CheckID)
}

func TestTipBound_GetBlockHeightWithinLatestPasses(t *testing.T) {
	cs := corroboratedSet("svm.final.tipBound")
	tr := tipFake{fakeFinalityResolver{finalizedTip: 990}, 1000}
	res := validateChain(t, "getBlockHeight", `[]`, `999`, cs, nil, tr)
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.final.tipBound"))
}

func TestFinalityChecks_InCorroboratedLevelOnly(t *testing.T) {
	cs := CheckSetForLevel(LevelCorroborated)
	for _, id := range []string{"svm.final.finalizedBound", "svm.final.slotAhead", "svm.final.tipBound"} {
		assert.True(t, cs.For(id).Enabled, id)
	}
	intr := CheckSetForLevel(LevelIntrinsic)
	for _, id := range []string{"svm.final.finalizedBound", "svm.final.slotAhead", "svm.final.tipBound"} {
		assert.False(t, intr.For(id).Enabled, id)
	}
}

func TestFinalizedBound_BareGetSlotUsesFinalizedDefault(t *testing.T) {
	// A bare getSlot answers from the finalized bank by protocol default and
	// carries NO context envelope — the check must anchor on the result's
	// own slot number (Slot() fallback) and treat the claim as finalized.
	cs := corroboratedSet("svm.final.finalizedBound")
	tr := tipFake{fakeFinalityResolver{finalizedTip: 990}, 1000}
	res := validateChain(t, "getSlot", `[]`, `500`, cs, nil, tr)
	assert.NoError(t, res.Err, "slot 500 is finalized (finalized tip 990) — a bare finalized claim passes")
	assert.Equal(t, "pass", outcomeOf(res, "svm.final.finalizedBound"))

	enableHardReject(cs, "svm.final.finalizedBound")
	tr = tipFake{fakeFinalityResolver{finalizedTip: 100}, 1000}
	res = validateChain(t, "getSlot", `[]`, `500`, cs, nil, tr)
	require.Error(t, res.Err, "slot 500 is NOT finalized (tip 100) — the bare finalized claim fails")
	assert.Equal(t, "svm.final.finalizedBound", res.RejectedCheckID)
}

func TestFinalizedBound_RootAliasCountsAsFinalized(t *testing.T) {
	// "root" is Agave's legacy alias of finalized: a request carrying it
	// expects finalized data exactly like "finalized".
	cs := corroboratedSet("svm.final.finalizedBound")
	enableHardReject(cs, "svm.final.finalizedBound")
	tr := tipFake{fakeFinalityResolver{finalizedTip: 100}, 1000}
	res := validateChain(t, "getBlock", `[100, {"commitment":"root"}]`,
		`{"context":{"slot":500},"value":{"blockhash":"abc"}}`, cs, nil, tr)
	require.Error(t, res.Err, "commitment alias 'root' must be judged as finalized")
	assert.Equal(t, "svm.final.finalizedBound", res.RejectedCheckID)
}

func TestSlotAhead_BareGetSlotFallsBackToResultSlot(t *testing.T) {
	// getSlot's bare-number result has no context.slot; the check must fall
	// back to the decoded slot number instead of skipping.
	cs := corroboratedSet("svm.final.slotAhead")
	tr := tipFake{fakeFinalityResolver{finalizedTip: 990}, 1000}
	res := validateChain(t, "getSlot", `[]`, `1500`, cs, nil, tr)
	assert.NoError(t, res.Err)
	require.Len(t, res.Recorded, 1, "slot 1500 beyond latest 1000 must be caught via the result slot")
	assert.Equal(t, "svm.final.slotAhead", res.Recorded[0].CheckID)
}

package integrity

// Tests for the commitment-tier slot-chain checks: parentLink and
// heightMonotonic. These are the first ReorgSensitive checks, so the suite
// covers both verdict paths (recorded when unfinalized/unknown, rejected when
// finalized) and the index trust rule (rejected/recorded blocks never anchor).

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/erpc/erpc/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeFinalityResolver reports every slot as finalized (or unknown when fin<0).
type fakeFinalityResolver struct {
	finalizedTip int64
}

func (f fakeFinalityResolver) IsFinalized(ctx context.Context, slot int64) (bool, bool) {
	if f.finalizedTip < 0 {
		return false, false
	}
	return slot <= f.finalizedTip, true
}

func validateChain(t *testing.T, method, paramsJSON, resultJSON string, cs CheckSet, chain *ChainState, fin FinalityResolver) Result {
	t.Helper()
	req := common.NewNormalizedRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + paramsJSON + `}`))
	jrr := common.MustNewJsonRpcResponseFromBytes([]byte("1"), []byte(resultJSON), nil)
	rs := common.NewNormalizedResponse().WithRequest(req).WithJsonRpcResponse(jrr)
	var params []any
	require.NoError(t, common.SonicCfg.Unmarshal([]byte(paramsJSON), &params))
	return Validate(context.Background(), Input{
		Method:   method,
		Upstream: common.NewFakeUpstream("u"),
		Response: rs,
		Checks:   cs,
		Params:   params,
		Reorg:    DefaultReorgPolicy(),
		Chain:    chain,
		Finality: fin,
	})
}

// chainBlock builds a minimal getBlock result with the given links. height
// < 0 omits blockHeight.
func chainBlock(slot, parentSlot, height int64, hashSeed, parentHashSeed byte) string {
	h := int64(0)
	if height >= 0 {
		h = height
	}
	b := map[string]any{
		"blockhash":         base58Encode(bytes32(hashSeed)),
		"previousBlockhash": base58Encode(bytes32(parentHashSeed)),
		"parentSlot":        parentSlot,
		"blockHeight":       h,
		"blockTime":         1700000000,
		"transactions":      []any{},
	}
	if height < 0 {
		delete(b, "blockHeight")
	}
	raw, err := json.Marshal(b)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func corroboratedSet(ids ...string) CheckSet {
	cs := CheckSet{}
	for _, id := range ids {
		cs.Enable(id, nil)
	}
	return cs
}

func TestParentLink_NoParentObservedSkips(t *testing.T) {
	chain := NewChainState()
	res := validateChain(t, "getBlock", `[100]`, chainBlock(100, 99, 90, 7, 3),
		corroboratedSet("svm.commit.parentLink"), chain, fakeFinalityResolver{finalizedTip: 1000})
	assert.NoError(t, res.Err)
	assert.Equal(t, "skip", outcomeOf(res, "svm.commit.parentLink"))
}

func TestParentLink_AndHeight_CleanChainPasses(t *testing.T) {
	chain := NewChainState()
	fin := fakeFinalityResolver{finalizedTip: 1000}
	cs := corroboratedSet("svm.commit.parentLink", "svm.commit.heightMonotonic")

	// Parent at slot 99 enters the index on first verified sighting; its own
	// parent (98) is unknown, so the link checks skip — nothing unverifiable
	// is ever a violation.
	res := validateChain(t, "getBlock", `[99]`, chainBlock(99, 98, 89, 3, 1), cs, chain, fin)
	require.NoError(t, res.Err)
	assert.Equal(t, "skip", outcomeOf(res, "svm.commit.parentLink"))

	// Child links by hash and height.
	res = validateChain(t, "getBlock", `[100]`, chainBlock(100, 99, 90, 7, 3), cs, chain, fin)
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.commit.parentLink"))
	assert.Equal(t, "pass", outcomeOf(res, "svm.commit.heightMonotonic"))
}

func TestParentLink_WrongParentHash(t *testing.T) {
	chain := NewChainState()
	fin := fakeFinalityResolver{finalizedTip: 1000}
	cs := corroboratedSet("svm.commit.parentLink")

	res := validateChain(t, "getBlock", `[99]`, chainBlock(99, 98, 89, 3, 1), cs, chain, fin)
	require.NoError(t, res.Err)

	// Child claims a parent hash that is NOT the verified parent's blockhash.
	res = validateChain(t, "getBlock", `[100]`, chainBlock(100, 99, 90, 7, 200), cs, chain, fin)
	require.Error(t, res.Err, "finalized-slot data with a broken parent link must reject")
	assert.Equal(t, "svm.commit.parentLink", res.RejectedCheckID)
	assert.Equal(t, ReorgSensitive, res.RejectedClass)
	assert.Equal(t, "finalized", res.Finality)
}

func TestParentLink_UnfinalizedMismatchRecordsNotRejects(t *testing.T) {
	chain := NewChainState()
	cs := corroboratedSet("svm.commit.parentLink")

	res := validateChain(t, "getBlock", `[99]`, chainBlock(99, 98, 89, 3, 1), cs, chain, nil)
	require.NoError(t, res.Err)

	// Unknown finality (no resolver): default policy records, never rejects.
	res = validateChain(t, "getBlock", `[100]`, chainBlock(100, 99, 90, 7, 200), cs, chain, nil)
	assert.NoError(t, res.Err, "unknown finality must record, not reject")
	assert.Equal(t, "record_only", outcomeOf(res, "svm.commit.parentLink"))
	require.Len(t, res.Recorded, 1)
	assert.Equal(t, "unknown", res.Recorded[0].Finality)
}

func TestParentLink_UnfinalizedTipRecords(t *testing.T) {
	chain := NewChainState()
	cs := corroboratedSet("svm.commit.parentLink")

	res := validateChain(t, "getBlock", `[99]`, chainBlock(99, 98, 89, 3, 1), cs, chain, fakeFinalityResolver{finalizedTip: 50})
	require.NoError(t, res.Err)

	// Slot 100 is above the finalized tip (50): the mismatch may be a fork
	// race → recorded.
	res = validateChain(t, "getBlock", `[100]`, chainBlock(100, 99, 90, 7, 200), cs, chain, fakeFinalityResolver{finalizedTip: 50})
	assert.NoError(t, res.Err)
	assert.Equal(t, "record_only", outcomeOf(res, "svm.commit.parentLink"))
	assert.Equal(t, "unfinalized", res.Recorded[0].Finality)
}

func TestHeightMonotonic_OffByOne(t *testing.T) {
	chain := NewChainState()
	fin := fakeFinalityResolver{finalizedTip: 1000}
	cs := corroboratedSet("svm.commit.parentLink", "svm.commit.heightMonotonic")

	res := validateChain(t, "getBlock", `[99]`, chainBlock(99, 98, 89, 3, 1), cs, chain, fin)
	require.NoError(t, res.Err)

	// Correct hash link, wrong height (parent 89, child 91 — skipped slot in
	// between does NOT skip heights; child must be exactly parent+1).
	res = validateChain(t, "getBlock", `[100]`, chainBlock(100, 99, 91, 7, 3), cs, chain, fin)
	require.Error(t, res.Err)
	assert.Equal(t, "svm.commit.heightMonotonic", res.RejectedCheckID)
}

func TestHeightMonotonic_NullHeightsSkip(t *testing.T) {
	chain := NewChainState()
	fin := fakeFinalityResolver{finalizedTip: 1000}
	cs := corroboratedSet("svm.commit.parentLink", "svm.commit.heightMonotonic")

	res := validateChain(t, "getBlock", `[99]`, chainBlock(99, 98, -1, 3, 1), cs, chain, fin)
	require.NoError(t, res.Err)

	res = validateChain(t, "getBlock", `[100]`, chainBlock(100, 99, -1, 7, 3), cs, chain, fin)
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.commit.parentLink"))
	assert.Equal(t, "skip", outcomeOf(res, "svm.commit.heightMonotonic"))
}

func TestObservation_RecordedMismatchNotAnchored(t *testing.T) {
	chain := NewChainState()
	cs := corroboratedSet("svm.commit.parentLink")

	// Seed the parent.
	res := validateChain(t, "getBlock", `[99]`, chainBlock(99, 98, 89, 3, 1), cs, chain, nil)
	require.NoError(t, res.Err)

	// Child with broken link, unknown finality → recorded, served — but must
	// NOT enter the verified index.
	res = validateChain(t, "getBlock", `[100]`, chainBlock(100, 99, 90, 7, 200), cs, chain, nil)
	require.NoError(t, res.Err)
	_, found := chain.Parent(100)
	assert.False(t, found, "a served-with-mismatch block must never become link ground truth")
}

func TestObservation_RejectedBlockNotAnchored(t *testing.T) {
	chain := NewChainState()
	fin := fakeFinalityResolver{finalizedTip: 1000}
	cs := corroboratedSet("svm.commit.parentLink")

	res := validateChain(t, "getBlock", `[99]`, chainBlock(99, 98, 89, 3, 1), cs, chain, fin)
	require.NoError(t, res.Err)

	res = validateChain(t, "getBlock", `[100]`, chainBlock(100, 99, 90, 7, 200), cs, chain, fin)
	require.Error(t, res.Err)
	_, found := chain.Parent(100)
	assert.False(t, found, "a rejected block must never enter the index")
}

func TestFork_SameSlotKeepsFirstVerified(t *testing.T) {
	// A fork's block at an already-anchored slot must NOT replace the verified
	// entry (first-seen pinning). A canonical child still links cleanly; a
	// fork child mismatches and is judged by the link check, not the index.
	chain := NewChainState()
	fin := fakeFinalityResolver{finalizedTip: 1000}
	cs := corroboratedSet("svm.commit.parentLink", "svm.commit.heightMonotonic")

	res := validateChain(t, "getBlock", `[99]`, chainBlock(99, 98, 89, 3, 1), cs, chain, fin)
	require.NoError(t, res.Err)

	// Fork block at slot 99: parent (98) not in the index, so the link checks
	// skip and it passes — but must NOT overwrite the anchored entry.
	res = validateChain(t, "getBlock", `[99]`, chainBlock(99, 98, 40, 30, 1), cs, chain, nil)
	require.NoError(t, res.Err)
	parent, found := chain.Parent(99)
	require.True(t, found)
	assert.Equal(t, int64(89), parent.blockHeight, "first verified block at a slot is pinned")

	// Canonical child of the anchored parent links cleanly.
	res = validateChain(t, "getBlock", `[100]`, chainBlock(100, 99, 90, 7, 3), cs, chain, fin)
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.commit.parentLink"))

	// Fork child (parented on the fork block) mismatches the pinned anchor.
	res = validateChain(t, "getBlock", `[100]`, chainBlock(100, 99, 41, 8, 30), cs, chain, fin)
	require.Error(t, res.Err)
	assert.Equal(t, "svm.commit.parentLink", res.RejectedCheckID)
}

func TestLevelMembership_CorroboratedIncludesChainChecks(t *testing.T) {
	cs := CheckSetForLevel(LevelCorroborated)
	assert.True(t, cs.For("svm.commit.parentLink").Enabled)
	assert.True(t, cs.For("svm.commit.heightMonotonic").Enabled)
	// Superset: intrinsic checks stay enabled at corroborated.
	assert.True(t, cs.For("svm.struct.blockShape").Enabled)
	assert.True(t, cs.For("svm.auth.signatureVerify").Enabled)
	// Intrinsic alone does NOT include the chain checks.
	intr := CheckSetForLevel(LevelIntrinsic)
	assert.False(t, intr.For("svm.commit.parentLink").Enabled)
}

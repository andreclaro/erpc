package integrity

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// followerBlock builds a getBlock result with hash/parent links and an
// optional blockTime (nil omits the field).
func followerBlock(slot, parentSlot, height int64, hashSeed, parentSeed byte, bt *int64) string {
	ts := ""
	if bt != nil {
		ts = fmt.Sprintf(`"blockTime":%d,`, *bt)
	}
	return fmt.Sprintf(
		`{"blockhash":%q,"previousBlockhash":%q,"parentSlot":%d,"blockHeight":%d,%s"transactions":[]}`,
		base58Encode(bytes32(hashSeed)), base58Encode(bytes32(parentSeed)), parentSlot, height, ts)
}

func seedBlock(chain *ChainState, slot, parentSlot, height, bt int64, hashSeed, parentSeed byte) {
	chain.Observe(chainEntry{
		slot: slot, blockHeight: height, parentSlot: parentSlot, blockTime: bt,
		blockhash: toHash32(bytes32(hashSeed)), parentHash: toHash32(bytes32(parentSeed)),
	})
}

func epochInfoJSON(epoch, slotIndex, abs int64) string {
	return fmt.Sprintf(`{"absoluteSlot":%d,"blockHeight":%d,"slotIndex":%d,"epoch":%d}`,
		abs, abs/2, slotIndex, epoch)
}

// ---- svm.commit.chainFollower ----

func TestChainFollower_PinnedMatchPasses(t *testing.T) {
	chain := NewChainState()
	seedBlock(chain, 900, 899, 800, 1700000000, 7, 6)
	res := validateChain(t, "getBlock", `[900]`, followerBlock(900, 899, 800, 7, 6, nil),
		corroboratedSet("svm.commit.chainFollower"), chain, fakeFinalityResolver{finalizedTip: 1000})
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.commit.chainFollower"))
}

func TestChainFollower_DifferentBankhashViolates(t *testing.T) {
	chain := NewChainState()
	seedBlock(chain, 900, 899, 800, 1700000000, 7, 6)
	res := validateChain(t, "getBlock", `[900]`, followerBlock(900, 899, 800, 8, 6, nil),
		corroboratedSet("svm.commit.chainFollower"), chain, fakeFinalityResolver{finalizedTip: 1000})
	require.Error(t, res.Err)
	assert.Equal(t, "svm.commit.chainFollower", res.RejectedCheckID)
}

func TestChainFollower_DifferentParentViolates(t *testing.T) {
	chain := NewChainState()
	seedBlock(chain, 900, 899, 800, 1700000000, 7, 6)
	res := validateChain(t, "getBlock", `[900]`, followerBlock(900, 899, 800, 7, 9, nil),
		corroboratedSet("svm.commit.chainFollower"), chain, fakeFinalityResolver{finalizedTip: 1000})
	require.Error(t, res.Err)
}

func TestChainFollower_FirstSightSkips(t *testing.T) {
	chain := NewChainState()
	res := validateChain(t, "getBlock", `[950]`, followerBlock(950, 949, 850, 3, 2, nil),
		corroboratedSet("svm.commit.chainFollower"), chain, fakeFinalityResolver{finalizedTip: 1000})
	assert.NoError(t, res.Err)
	assert.Equal(t, "skip", outcomeOf(res, "svm.commit.chainFollower"))
}

// ---- svm.commit.timeWindow ----

func ptrInt64(v int64) *int64 { return &v }

func TestTimeWindow_CurrentTimePasses(t *testing.T) {
	chain := NewChainState()
	now := time.Now().Unix()
	res := validateChain(t, "getBlock", `[900]`, followerBlock(900, 899, 800, 7, 6, ptrInt64(now)),
		corroboratedSet("svm.commit.timeWindow"), chain, fakeFinalityResolver{finalizedTip: 1000})
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.commit.timeWindow"))
}

func TestTimeWindow_PreGenesisViolates(t *testing.T) {
	chain := NewChainState()
	res := validateChain(t, "getBlock", `[900]`, followerBlock(900, 899, 800, 7, 6, ptrInt64(1000000)),
		corroboratedSet("svm.commit.timeWindow"), chain, fakeFinalityResolver{finalizedTip: 1000})
	require.Error(t, res.Err)
	assert.Equal(t, "svm.commit.timeWindow", res.RejectedCheckID)
}

func TestTimeWindow_FutureViolates(t *testing.T) {
	chain := NewChainState()
	res := validateChain(t, "getBlock", `[900]`, followerBlock(900, 899, 800, 7, 6, ptrInt64(time.Now().Unix()+3600)),
		corroboratedSet("svm.commit.timeWindow"), chain, fakeFinalityResolver{finalizedTip: 1000})
	require.Error(t, res.Err)
}

func TestTimeWindow_ParentReversalViolates(t *testing.T) {
	chain := NewChainState()
	parentTime := time.Now().Unix()
	seedBlock(chain, 899, 898, 799, parentTime, 6, 5)
	// Child claims a blockTime 100s before its pinned parent (> 10s tol).
	res := validateChain(t, "getBlock", `[900]`, followerBlock(900, 899, 800, 7, 6, ptrInt64(parentTime-100)),
		corroboratedSet("svm.commit.timeWindow"), chain, fakeFinalityResolver{finalizedTip: 1000})
	require.Error(t, res.Err)
}

func TestTimeWindow_ParentTolerancePasses(t *testing.T) {
	chain := NewChainState()
	parentTime := time.Now().Unix()
	seedBlock(chain, 899, 898, 799, parentTime, 6, 5)
	res := validateChain(t, "getBlock", `[900]`, followerBlock(900, 899, 800, 7, 6, ptrInt64(parentTime-5)),
		corroboratedSet("svm.commit.timeWindow"), chain, fakeFinalityResolver{finalizedTip: 1000})
	assert.NoError(t, res.Err)
}

func TestTimeWindow_NullBlockTimeSkips(t *testing.T) {
	chain := NewChainState()
	res := validateChain(t, "getBlock", `[900]`, followerBlock(900, 899, 800, 7, 6, nil),
		corroboratedSet("svm.commit.timeWindow"), chain, fakeFinalityResolver{finalizedTip: 1000})
	assert.Equal(t, "skip", outcomeOf(res, "svm.commit.timeWindow"))
}

// ---- svm.commit.slotEpoch ----

func TestSlotEpoch_ConsistentFieldsPass(t *testing.T) {
	res := validateChain(t, "getEpochInfo", `[]`, epochInfoJSON(5, 100, 5*432000+100),
		corroboratedSet("svm.commit.slotEpoch"), nil, fakeFinalityResolver{finalizedTip: 1000})
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.commit.slotEpoch"))
}

func TestSlotEpoch_MismatchedAbsoluteSlotViolates(t *testing.T) {
	res := validateChain(t, "getEpochInfo", `[]`, epochInfoJSON(5, 100, 5*432000+101),
		corroboratedSet("svm.commit.slotEpoch"), nil, fakeFinalityResolver{finalizedTip: 1000})
	require.Error(t, res.Err)
	assert.Equal(t, "svm.commit.slotEpoch", res.RejectedCheckID)
}

func TestSlotEpoch_SlotIndexOutOfRangeViolates(t *testing.T) {
	res := validateChain(t, "getEpochInfo", `[]`, epochInfoJSON(5, 432000, 5*432000+432000),
		corroboratedSet("svm.commit.slotEpoch"), nil, fakeFinalityResolver{finalizedTip: 1000})
	require.Error(t, res.Err)
}

func TestSlotEpoch_CustomSlotsPerEpoch(t *testing.T) {
	cs := corroboratedSet("svm.commit.slotEpoch")
	cfg := cs["svm.commit.slotEpoch"]
	cfg.Params = map[string]string{"slotsPerEpoch": "10"}
	cs["svm.commit.slotEpoch"] = cfg
	res := validateChain(t, "getEpochInfo", `[]`, epochInfoJSON(3, 2, 32),
		cs, nil, fakeFinalityResolver{finalizedTip: 1000})
	assert.NoError(t, res.Err)
}

func TestSlotEpoch_MalformedSkips(t *testing.T) {
	res := validateChain(t, "getEpochInfo", `[]`, `{"epoch":"five"}`,
		corroboratedSet("svm.commit.slotEpoch"), nil, fakeFinalityResolver{finalizedTip: 1000})
	assert.Equal(t, "skip", outcomeOf(res, "svm.commit.slotEpoch"))
}

package integrity

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- helpers ----

func commitmentResult(total int64, tiers ...int64) string {
	out := `"commitment":[`
	for i, t := range tiers {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprintf("%d", t)
	}
	out += `],"totalStake":` + fmt.Sprintf("%d", total)
	return `{` + out + `}`
}

func voteAccountsResult(current, delinquent string) string {
	return fmt.Sprintf(`{"current":[%s],"delinquent":[%s]}`, current, delinquent)
}

func voteAcct(stake, lastVote int64, rootSlot any) string {
	rs := "null"
	if rootSlot != nil {
		rs = fmt.Sprintf("%d", rootSlot)
	}
	return fmt.Sprintf(`{"votePubkey":%q,"nodePubkey":%q,"activatedStake":%d,"lastVote":%d,"epochCredits":[],"rootSlot":%s}`,
		base58Encode(bytes32(1)), base58Encode(bytes32(2)), stake, lastVote, rs)
}

func validateEvidence(t *testing.T, method, result string, cs CheckSet, fin FinalityResolver) Result {
	t.Helper()
	return validateChain(t, method, `[]`, result, cs, nil, fin)
}

// ---- svm.final.commitmentQuorum ----

func TestCommitmentQuorum_ValidHistogramPasses(t *testing.T) {
	res := validateEvidence(t, "getBlockCommitment",
		commitmentResult(1000, 900, 800, 700, 600, 500, 400, 300, 200, 100, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0),
		corroboratedSet("svm.final.commitmentQuorum"), nil)
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.final.commitmentQuorum"))
}

func TestCommitmentQuorum_TierAboveTotalViolates(t *testing.T) {
	res := validateEvidence(t, "getBlockCommitment",
		commitmentResult(100, 200, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0),
		corroboratedSet("svm.final.commitmentQuorum"), nil)
	require.Error(t, res.Err)
	assert.Equal(t, "svm.final.commitmentQuorum", res.RejectedCheckID)
}

func TestCommitmentQuorum_IncreasingWithDepthViolates(t *testing.T) {
	res := validateEvidence(t, "getBlockCommitment",
		commitmentResult(1000, 100, 200, 300, 400, 500, 600, 700, 800, 900, 1000, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0),
		corroboratedSet("svm.final.commitmentQuorum"), nil)
	require.Error(t, res.Err)
}

func TestCommitmentQuorum_WrongTierCountViolates(t *testing.T) {
	res := validateEvidence(t, "getBlockCommitment",
		commitmentResult(1000, 900, 800),
		corroboratedSet("svm.final.commitmentQuorum"), nil)
	require.Error(t, res.Err)
}

func TestCommitmentQuorum_ZeroTotalStakeViolates(t *testing.T) {
	res := validateEvidence(t, "getBlockCommitment",
		commitmentResult(0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0),
		corroboratedSet("svm.final.commitmentQuorum"), nil)
	require.Error(t, res.Err)
}

func TestCommitmentQuorum_MissingTiersViolates(t *testing.T) {
	// totalStake present but the histogram absent — a real node always ships
	// both; the shape is wrong, so this rejects (Deterministic).
	res := validateEvidence(t, "getBlockCommitment", `{"totalStake":1000}`,
		corroboratedSet("svm.final.commitmentQuorum"), nil)
	require.Error(t, res.Err)
	assert.Equal(t, "svm.final.commitmentQuorum", res.RejectedCheckID)
}

func TestCommitmentQuorum_UnparseableSkips(t *testing.T) {
	res := validateEvidence(t, "getBlockCommitment", `"not-an-object"`,
		corroboratedSet("svm.final.commitmentQuorum"), nil)
	assert.Equal(t, "skip", outcomeOf(res, "svm.final.commitmentQuorum"))
}

// ---- svm.final.rootSlotSanity ----

func TestRootSlotSanity_ConsistentTablePasses(t *testing.T) {
	res := validateEvidence(t, "getVoteAccounts",
		voteAccountsResult(
			voteAcct(5000, 980, 950)+","+voteAcct(3000, 990, 990),
			voteAcct(1000, 900, 800)),
		corroboratedSet("svm.final.rootSlotSanity"), tipFake{fakeFinalityResolver{finalizedTip: 990}, 1000})
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.final.rootSlotSanity"))
}

func TestRootSlotSanity_RootAboveLastVoteRecords(t *testing.T) {
	res := validateEvidence(t, "getVoteAccounts",
		voteAccountsResult(voteAcct(5000, 980, 990), ""),
		corroboratedSet("svm.final.rootSlotSanity"), tipFake{fakeFinalityResolver{finalizedTip: 990}, 1000})
	// ReorgSensitive default: recorded, never rejected.
	assert.NoError(t, res.Err)
	require.Len(t, res.Recorded, 1)
	assert.Equal(t, "svm.final.rootSlotSanity", res.Recorded[0].CheckID)
}

func TestRootSlotSanity_RootAboveLastVoteHardRejects(t *testing.T) {
	cs := corroboratedSet("svm.final.rootSlotSanity")
	enableHardReject(cs, "svm.final.rootSlotSanity")
	res := validateEvidence(t, "getVoteAccounts",
		voteAccountsResult(voteAcct(5000, 980, 990), ""),
		cs, tipFake{fakeFinalityResolver{finalizedTip: 990}, 1000})
	require.Error(t, res.Err)
	assert.Equal(t, "svm.final.rootSlotSanity", res.RejectedCheckID)
}

func TestRootSlotSanity_FutureVotesRecordAgainstHead(t *testing.T) {
	res := validateEvidence(t, "getVoteAccounts",
		voteAccountsResult(voteAcct(5000, 2000, 900), ""),
		corroboratedSet("svm.final.rootSlotSanity"), tipFake{fakeFinalityResolver{finalizedTip: 990}, 1000})
	// ReorgSensitive default: record, never reject.
	assert.NoError(t, res.Err)
	assert.NotEmpty(t, res.Recorded)
}

func TestRootSlotSanity_NullRootSlotSkipsEntryComparison(t *testing.T) {
	res := validateEvidence(t, "getVoteAccounts",
		voteAccountsResult(voteAcct(100, 980, nil), ""),
		corroboratedSet("svm.final.rootSlotSanity"), tipFake{fakeFinalityResolver{finalizedTip: 990}, 1000})
	assert.NoError(t, res.Err)
	assert.Equal(t, "pass", outcomeOf(res, "svm.final.rootSlotSanity"))
}

func TestRootSlotSanity_UnknownHeadStillChecksEntries(t *testing.T) {
	// Without a tip the future-vote bound cannot run, but rootSlot ≤ lastVote
	// is per-entry truth and still applies — recorded even with no resolver.
	res := validateEvidence(t, "getVoteAccounts",
		voteAccountsResult(voteAcct(5000, 980, 990), ""),
		corroboratedSet("svm.final.rootSlotSanity"), nil)
	assert.NoError(t, res.Err)
	require.Len(t, res.Recorded, 1)
}

// ---- wiring ----

func checkByID(t *testing.T, id string) *Check {
	t.Helper()
	for _, c := range allChecks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("check %s not registered", id)
	return nil
}

func TestFinalityEvidenceChecks_InCorroboratedLevel(t *testing.T) {
	cs := CheckSetForLevel(LevelCorroborated)
	assert.True(t, cs["svm.final.commitmentQuorum"].Enabled)
	assert.True(t, cs["svm.final.rootSlotSanity"].Enabled)
	// Deterministic evidence checks hard-reject by class policy; the
	// head-relative vote check records (poller lag looks like future votes).
	assert.Equal(t, Deterministic, checkByID(t, "svm.final.commitmentQuorum").Class)
	assert.Equal(t, ReorgSensitive, checkByID(t, "svm.final.rootSlotSanity").Class)
}

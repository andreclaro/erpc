package consensus

import (
	"testing"

	"github.com/erpc/erpc/common"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resWithUpstream builds a non-empty execResult attributed to an upstream
// carrying the given tags, returning val as the JSON-RPC result.
func resWithUpstream(id string, tags []string, val interface{}) *execResult {
	jrpc, _ := common.NewJsonRpcResponse(1, val, nil)
	resp := common.NewNormalizedResponse()
	resp.WithJsonRpcResponse(jrpc)
	return &execResult{
		Result:   resp,
		Upstream: common.NewFakeUpstream(id, common.WithTags(tags...)),
	}
}

// groupOf assembles a responseGroup from results, treating them as a single
// agreeing group (they share a hash) of the given response type.
func groupOf(hash string, rt ResponseType, results ...*execResult) *responseGroup {
	g := &responseGroup{Hash: hash, ResponseType: rt, Count: len(results), Results: results}
	if len(results) > 0 {
		g.LargestResult = results[0].Result
		g.HasResult = true
	}
	return g
}

func mkAnalysis(cfg *config, groups ...*responseGroup) *consensusAnalysis {
	a := &consensusAnalysis{
		config: cfg,
		groups: make(map[string]*responseGroup, len(groups)),
	}
	for _, g := range groups {
		a.groups[g.Hash] = g
		a.totalParticipants += g.Count
		if g.ResponseType != ResponseTypeInfrastructureError {
			a.validParticipants += g.Count
		}
	}
	return a
}

func mkAnalysisWithParticipants(cfg *config, participants []common.Upstream, groups ...*responseGroup) *consensusAnalysis {
	a := mkAnalysis(cfg, groups...)
	a.allParticipants = participants
	return a
}

var testNopLogger = zerolog.Nop()

func newTestExecutor(cfg *config) *executor {
	return &executor{consensusPolicy: &consensusPolicy{logger: &testNopLogger, config: cfg}}
}

func TestGroupSatisfiesAgreementQuotas(t *testing.T) {
	reqs := []*common.ConsensusRequiredParticipant{
		{Tag: "type:internal", MinParticipants: 1, MinAgreement: 1},
		{Tag: "type:external", MinParticipants: 1, MinAgreement: 1},
	}

	t.Run("mixed group satisfies", func(t *testing.T) {
		g := groupOf("h", ResponseTypeNonEmpty,
			resWithUpstream("internal-1", []string{"type:internal"}, "0x1"),
			resWithUpstream("alchemy-1", []string{"type:external"}, "0x1"),
		)
		require.True(t, groupSatisfiesAgreementQuotas(g, reqs))
	})

	t.Run("all-external group fails", func(t *testing.T) {
		g := groupOf("h", ResponseTypeNonEmpty,
			resWithUpstream("alchemy-1", []string{"type:external"}, "0x1"),
			resWithUpstream("quicknode-1", []string{"type:external"}, "0x1"),
		)
		require.False(t, groupSatisfiesAgreementQuotas(g, reqs))
	})

	t.Run("no minAgreement entries is a no-op", func(t *testing.T) {
		poolOnly := []*common.ConsensusRequiredParticipant{{Tag: "type:internal", MinParticipants: 1}}
		require.False(t, anyAgreementQuota(poolOnly))
		g := groupOf("h", ResponseTypeNonEmpty,
			resWithUpstream("alchemy-1", []string{"type:external"}, "0x1"),
		)
		require.True(t, groupSatisfiesAgreementQuotas(g, poolOnly))
	})
}

func TestMinAgreement_WinnerRule(t *testing.T) {
	reqs := []*common.ConsensusRequiredParticipant{
		{Tag: "type:internal", MinParticipants: 1, MinAgreement: 1},
		{Tag: "type:external", MinParticipants: 1, MinAgreement: 1},
	}
	cfg := func() *config {
		return &config{
			maxParticipants:      3,
			agreementThreshold:   2,
			disputeBehavior:      common.ConsensusDisputeBehaviorReturnError,
			requiredParticipants: reqs,
		}
	}

	t.Run("two externals agree, internal dissents -> dispute", func(t *testing.T) {
		win := groupOf("ext", ResponseTypeNonEmpty,
			resWithUpstream("alchemy-1", []string{"type:external"}, "0x100"),
			resWithUpstream("quicknode-1", []string{"type:external"}, "0x100"),
		)
		dissent := groupOf("int", ResponseTypeNonEmpty,
			resWithUpstream("internal-1", []string{"type:internal"}, "0x999"),
		)
		a := mkAnalysis(cfg(), win, dissent)
		winner := newTestExecutor(a.config).determineWinner(&testNopLogger, a)
		require.NotNil(t, winner.Error)
		assert.True(t, common.HasErrorCode(winner.Error, common.ErrCodeConsensusDispute),
			"all-external winner must be disputed")
		assert.True(t, isCompositionDispute(winner.Error),
			"all-external winner must be a composition dispute")
	})

	t.Run("mixed group wins over same-count all-external group", func(t *testing.T) {
		mixed := groupOf("mixed", ResponseTypeNonEmpty,
			resWithUpstream("internal-1", []string{"type:internal"}, "0x100"),
			resWithUpstream("alchemy-1", []string{"type:external"}, "0x100"),
		)
		ext := groupOf("ext", ResponseTypeNonEmpty,
			resWithUpstream("quicknode-1", []string{"type:external"}, "0x200"),
			resWithUpstream("infura-1", []string{"type:external"}, "0x200"),
		)
		a := mkAnalysis(cfg(), mixed, ext)
		winner := newTestExecutor(a.config).determineWinner(&testNopLogger, a)
		require.Nil(t, winner.Error, "unique quota-satisfying group exists; must not dispute")
		require.NotNil(t, winner.Result)
		jrr, err := winner.Result.JsonRpcResponse()
		require.NoError(t, err)
		assert.Contains(t, jrr.GetResultString(), "0x100", "the mixed group must win")
	})

	t.Run("higher-count non-satisfying group does not bypass via value-tie defer", func(t *testing.T) {
		// 3 externals agree (count 3 > threshold 2); two mixed groups tie at count 2.
		// The non-satisfying group must not win via fall-through — must composition-dispute.
		c := cfg()
		c.maxParticipants = 5
		c.agreementThreshold = 2
		extHigh := groupOf("ext", ResponseTypeNonEmpty,
			resWithUpstream("alchemy-1", []string{"type:external"}, "0x999"),
			resWithUpstream("quicknode-1", []string{"type:external"}, "0x999"),
			resWithUpstream("infura-1", []string{"type:external"}, "0x999"),
		)
		extHigh.Count = 3
		mixedA := groupOf("a", ResponseTypeNonEmpty,
			resWithUpstream("internal-1", []string{"type:internal"}, "0x100"),
			resWithUpstream("alchemy-2", []string{"type:external"}, "0x100"),
		)
		mixedB := groupOf("b", ResponseTypeNonEmpty,
			resWithUpstream("internal-2", []string{"type:internal"}, "0x200"),
			resWithUpstream("quicknode-2", []string{"type:external"}, "0x200"),
		)
		a := mkAnalysis(c, extHigh, mixedA, mixedB)
		winner := newTestExecutor(c).determineWinner(&testNopLogger, a)
		require.NotNil(t, winner.Error)
		assert.True(t, common.HasErrorCode(winner.Error, common.ErrCodeConsensusDispute))
		assert.True(t, isCompositionDispute(winner.Error),
			"non-satisfying group at higher count must produce composition dispute, not standard dispute or success")
	})

	t.Run("two mixed groups tie at same count -> standard dispute", func(t *testing.T) {
		c := cfg()
		c.maxParticipants = 4
		mixedA := groupOf("a", ResponseTypeNonEmpty,
			resWithUpstream("internal-1", []string{"type:internal"}, "0x100"),
			resWithUpstream("alchemy-1", []string{"type:external"}, "0x100"),
		)
		mixedB := groupOf("b", ResponseTypeNonEmpty,
			resWithUpstream("internal-2", []string{"type:internal"}, "0x200"),
			resWithUpstream("quicknode-1", []string{"type:external"}, "0x200"),
		)
		a := mkAnalysis(c, mixedA, mixedB)
		winner := newTestExecutor(c).determineWinner(&testNopLogger, a)
		require.NotNil(t, winner.Error)
		assert.True(t, common.HasErrorCode(winner.Error, common.ErrCodeConsensusDispute))
		assert.False(t, isCompositionDispute(winner.Error),
			"value tie between two mixed groups must be a standard dispute, not composition")
	})

	t.Run("mixed group, no competitor -> result", func(t *testing.T) {
		mixed := groupOf("mixed", ResponseTypeNonEmpty,
			resWithUpstream("internal-1", []string{"type:internal"}, "0x100"),
			resWithUpstream("alchemy-1", []string{"type:external"}, "0x100"),
		)
		a := mkAnalysis(cfg(), mixed)
		winner := newTestExecutor(a.config).determineWinner(&testNopLogger, a)
		require.Nil(t, winner.Error)
		require.NotNil(t, winner.Result)
	})

	t.Run("feature off -> all-external group wins as before", func(t *testing.T) {
		poolOnly := &config{
			maxParticipants:    3,
			agreementThreshold: 2,
			disputeBehavior:    common.ConsensusDisputeBehaviorReturnError,
			requiredParticipants: []*common.ConsensusRequiredParticipant{
				{Tag: "type:internal", MinParticipants: 1}, // no minAgreement
			},
		}
		win := groupOf("ext", ResponseTypeNonEmpty,
			resWithUpstream("alchemy-1", []string{"type:external"}, "0x100"),
			resWithUpstream("quicknode-1", []string{"type:external"}, "0x100"),
		)
		a := mkAnalysis(poolOnly, win)
		winner := newTestExecutor(a.config).determineWinner(&testNopLogger, a)
		require.Nil(t, winner.Error, "feature off: existing behavior, no dispute")
		require.NotNil(t, winner.Result)
	})

	t.Run("enforced even under accept-most-common", func(t *testing.T) {
		c := cfg()
		c.disputeBehavior = common.ConsensusDisputeBehaviorAcceptMostCommonValidResult
		win := groupOf("ext", ResponseTypeNonEmpty,
			resWithUpstream("alchemy-1", []string{"type:external"}, "0x100"),
			resWithUpstream("quicknode-1", []string{"type:external"}, "0x100"),
		)
		a := mkAnalysis(c, win)
		winner := newTestExecutor(c).determineWinner(&testNopLogger, a)
		require.NotNil(t, winner.Error)
		assert.True(t, common.HasErrorCode(winner.Error, common.ErrCodeConsensusDispute),
			"accept-most-common must not bypass the composition requirement")
		assert.True(t, isCompositionDispute(winner.Error),
			"external-only winner must be a composition dispute, not a standard hash dispute")
	})
}

func TestMinAgreement_PreferHighestValueFor(t *testing.T) {
	reqs := []*common.ConsensusRequiredParticipant{
		{Tag: "type:internal", MinParticipants: 1, MinAgreement: 1},
		{Tag: "type:external", MinParticipants: 1, MinAgreement: 1},
	}
	cfg := &config{
		maxParticipants:      3,
		agreementThreshold:   2,
		disputeBehavior:      common.ConsensusDisputeBehaviorReturnError,
		requiredParticipants: reqs,
		preferHighestValueFor: map[string][]string{
			"eth_blockNumber": {"result"},
		},
	}

	t.Run("all-external high-value group must not win via preferHighestValueFor", func(t *testing.T) {
		// External-only group agrees on 0x200 (higher); mixed group agrees on 0x100 (lower).
		// Without the fix, preferHighestValueFor would return 0x200 bypassing minAgreement.
		a := &consensusAnalysis{
			config: cfg,
			method: "eth_blockNumber",
		}
		a.groups = map[string]*responseGroup{
			"ext": groupOf("ext", ResponseTypeNonEmpty,
				resWithUpstream("alchemy-1", []string{"type:external"}, "0x200"),
				resWithUpstream("quicknode-1", []string{"type:external"}, "0x200"),
			),
			"mixed": groupOf("mixed", ResponseTypeNonEmpty,
				resWithUpstream("internal-1", []string{"type:internal"}, "0x100"),
				resWithUpstream("alchemy-2", []string{"type:external"}, "0x100"),
			),
		}
		for _, g := range a.groups {
			a.totalParticipants += g.Count
			a.validParticipants += g.Count
		}
		winner := newTestExecutor(cfg).determineWinner(&testNopLogger, a)
		require.Nil(t, winner.Error, "mixed group must win; all-external high-value group must not bypass minAgreement")
		require.NotNil(t, winner.Result)
		jrr, err := winner.Result.JsonRpcResponse()
		require.NoError(t, err)
		assert.Contains(t, jrr.GetResultString(), "0x100")
	})

	t.Run("mixed high-value group wins when it satisfies quotas", func(t *testing.T) {
		a := &consensusAnalysis{
			config: cfg,
			method: "eth_blockNumber",
		}
		a.groups = map[string]*responseGroup{
			"mixed-high": groupOf("mixed-high", ResponseTypeNonEmpty,
				resWithUpstream("internal-1", []string{"type:internal"}, "0x200"),
				resWithUpstream("alchemy-1", []string{"type:external"}, "0x200"),
			),
			"mixed-low": groupOf("mixed-low", ResponseTypeNonEmpty,
				resWithUpstream("internal-2", []string{"type:internal"}, "0x100"),
				resWithUpstream("quicknode-1", []string{"type:external"}, "0x100"),
			),
		}
		for _, g := range a.groups {
			a.totalParticipants += g.Count
			a.validParticipants += g.Count
		}
		winner := newTestExecutor(cfg).determineWinner(&testNopLogger, a)
		require.Nil(t, winner.Error)
		jrr, err := winner.Result.JsonRpcResponse()
		require.NoError(t, err)
		assert.Contains(t, jrr.GetResultString(), "0x200", "quota-satisfying high-value group must win")
	})
}

func TestMinAgreement_ShortCircuitGuard(t *testing.T) {
	reqs := []*common.ConsensusRequiredParticipant{
		{Tag: "type:internal", MinParticipants: 1, MinAgreement: 1},
		{Tag: "type:external", MinParticipants: 1, MinAgreement: 1},
	}
	cfg := &config{
		maxParticipants:      3,
		agreementThreshold:   2,
		disputeBehavior:      common.ConsensusDisputeBehaviorReturnError,
		requiredParticipants: reqs,
	}

	t.Run("does not short-circuit unsatisfied winner while required upstreams pending", func(t *testing.T) {
		// 2 externals agree (count 2 >= threshold), only 2 of 3 participants in;
		// the internal node (internal-1) has not responded yet. Must NOT short-circuit.
		int1 := common.NewFakeUpstream("internal-1", common.WithTags("type:internal"))
		ext1 := common.NewFakeUpstream("alchemy-1", common.WithTags("type:external"))
		ext2 := common.NewFakeUpstream("quicknode-1", common.WithTags("type:external"))
		win := groupOf("ext", ResponseTypeNonEmpty,
			resWithUpstream("alchemy-1", []string{"type:external"}, "0x100"),
			resWithUpstream("quicknode-1", []string{"type:external"}, "0x100"),
		)
		// allParticipants: [int1, ext1, ext2]; int1 hasn't responded → pending required
		a := mkAnalysisWithParticipants(cfg, []common.Upstream{int1, ext1, ext2}, win)
		e := newTestExecutor(cfg)
		winner := e.determineWinner(&testNopLogger, a)
		_, ok := e.shouldShortCircuit(winner, a)
		require.False(t, ok, "must wait for the pending internal node")
	})

	t.Run("short-circuits once the winning group satisfies the quotas", func(t *testing.T) {
		int1 := common.NewFakeUpstream("internal-1", common.WithTags("type:internal"))
		ext1 := common.NewFakeUpstream("alchemy-1", common.WithTags("type:external"))
		ext2 := common.NewFakeUpstream("quicknode-1", common.WithTags("type:external"))
		mixed := groupOf("mixed", ResponseTypeNonEmpty,
			resWithUpstream("internal-1", []string{"type:internal"}, "0x100"),
			resWithUpstream("alchemy-1", []string{"type:external"}, "0x100"),
		)
		// allParticipants: [int1, ext1, ext2]; int1+ext1 responded, ext2 still pending
		// but ext2 is not a failing-quota upstream (both quotas already satisfied)
		a := mkAnalysisWithParticipants(cfg, []common.Upstream{int1, ext1, ext2}, mixed)
		e := newTestExecutor(cfg)
		winner := e.determineWinner(&testNopLogger, a)
		require.Nil(t, winner.Error)
		reason, ok := e.shouldShortCircuit(winner, a)
		require.True(t, ok, "satisfied + unassailable lead should short-circuit")
		assert.Equal(t, "unassailable_lead", reason)
	})

	t.Run("non-required pending upstream does not block short-circuit", func(t *testing.T) {
		// int1 + ext1 responded and form a mixed winner. ext2 (a second external) is
		// still pending, but both minAgreement quotas are already satisfied.
		// The guard must NOT fire — ext2 cannot change the composition outcome.
		int1 := common.NewFakeUpstream("internal-1", common.WithTags("type:internal"))
		ext1 := common.NewFakeUpstream("alchemy-1", common.WithTags("type:external"))
		ext2 := common.NewFakeUpstream("quicknode-1", common.WithTags("type:external"))
		mixed := groupOf("mixed", ResponseTypeNonEmpty,
			resWithUpstream("internal-1", []string{"type:internal"}, "0x100"),
			resWithUpstream("alchemy-1", []string{"type:external"}, "0x100"),
		)
		// allParticipants has ext2 last; it has not responded (not in any group)
		a := mkAnalysisWithParticipants(cfg, []common.Upstream{int1, ext1, ext2}, mixed)
		e := newTestExecutor(cfg)
		winner := e.determineWinner(&testNopLogger, a)
		require.Nil(t, winner.Error)
		reason, ok := e.shouldShortCircuit(winner, a)
		require.True(t, ok, "non-required pending upstream must not block short-circuit")
		assert.Equal(t, "unassailable_lead", reason)
	})
}

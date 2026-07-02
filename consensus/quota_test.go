package consensus

import (
	"testing"

	"github.com/erpc/erpc/common"
	"github.com/stretchr/testify/require"
)

func quotaUps(specs ...struct {
	id   string
	tags []string
}) []common.Upstream {
	out := make([]common.Upstream, len(specs))
	for i, s := range specs {
		out[i] = common.NewFakeUpstream(s.id, common.WithTags(s.tags...))
	}
	return out
}

func idsOf(ups []common.Upstream) []string {
	out := make([]string, len(ups))
	for i, u := range ups {
		out[i] = u.Id()
	}
	return out
}

func TestResolveMinAgreement(t *testing.T) {
	reqs := []*common.ConsensusRequiredParticipant{
		{Tag: "type:internal", MinParticipants: 1, MinAgreement: 1},
		{Tag: "type:external", MinParticipants: 1, MinAgreement: 1},
	}
	const threshold = 2

	t.Run("NoOp: feature off (no minAgreement entries)", func(t *testing.T) {
		poolOnly := []*common.ConsensusRequiredParticipant{
			{Tag: "type:internal", MinParticipants: 1},
		}
		g := groupOf("ext", ResponseTypeNonEmpty,
			resWithUpstream("a-1", []string{"type:external"}, "0x1"),
			resWithUpstream("a-2", []string{"type:external"}, "0x1"),
		)
		outcome, winner := resolveMinAgreement([]*responseGroup{g}, poolOnly, threshold)
		require.Equal(t, minAgreementNoOp, outcome)
		require.Nil(t, winner)
	})

	t.Run("NoOp: no group at threshold", func(t *testing.T) {
		g := groupOf("mixed", ResponseTypeNonEmpty,
			resWithUpstream("internal-1", []string{"type:internal"}, "0x1"),
		)
		outcome, winner := resolveMinAgreement([]*responseGroup{g}, reqs, threshold)
		require.Equal(t, minAgreementNoOp, outcome)
		require.Nil(t, winner)
	})

	t.Run("UniqueWinner: one mixed group at threshold alongside one external-only", func(t *testing.T) {
		mixed := groupOf("mixed", ResponseTypeNonEmpty,
			resWithUpstream("internal-1", []string{"type:internal"}, "0x100"),
			resWithUpstream("alchemy-1", []string{"type:external"}, "0x100"),
		)
		extOnly := groupOf("ext", ResponseTypeNonEmpty,
			resWithUpstream("quicknode-1", []string{"type:external"}, "0x200"),
			resWithUpstream("infura-1", []string{"type:external"}, "0x200"),
		)
		outcome, winner := resolveMinAgreement([]*responseGroup{mixed, extOnly}, reqs, threshold)
		require.Equal(t, minAgreementUniqueWinner, outcome)
		require.Equal(t, mixed, winner)
	})

	t.Run("CompositionDispute: only external-only group at threshold", func(t *testing.T) {
		extOnly := groupOf("ext", ResponseTypeNonEmpty,
			resWithUpstream("alchemy-1", []string{"type:external"}, "0x100"),
			resWithUpstream("quicknode-1", []string{"type:external"}, "0x100"),
		)
		outcome, winner := resolveMinAgreement([]*responseGroup{extOnly}, reqs, threshold)
		require.Equal(t, minAgreementCompositionDispute, outcome)
		require.Nil(t, winner)
	})

	t.Run("ValueTieDefer: two mixed groups at same count, no higher non-satisfying group", func(t *testing.T) {
		mixedA := groupOf("a", ResponseTypeNonEmpty,
			resWithUpstream("internal-1", []string{"type:internal"}, "0x100"),
			resWithUpstream("alchemy-1", []string{"type:external"}, "0x100"),
		)
		mixedB := groupOf("b", ResponseTypeNonEmpty,
			resWithUpstream("internal-2", []string{"type:internal"}, "0x200"),
			resWithUpstream("quicknode-1", []string{"type:external"}, "0x200"),
		)
		outcome, winner := resolveMinAgreement([]*responseGroup{mixedA, mixedB}, reqs, threshold)
		require.Equal(t, minAgreementValueTieDefer, outcome)
		require.Nil(t, winner)
	})

	t.Run("CompositionDispute: non-satisfying group has higher count than tied satisfying groups", func(t *testing.T) {
		// 3 externals agree (count 3, non-satisfying); two mixed groups tie at count 2.
		// ValueTieDefer would let the external-only group win via later rules — must be CompositionDispute.
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
		outcome, winner := resolveMinAgreement([]*responseGroup{extHigh, mixedA, mixedB}, reqs, threshold)
		require.Equal(t, minAgreementCompositionDispute, outcome)
		require.Nil(t, winner)
	})

	t.Run("UniqueWinner: higher-count quota-satisfying group beats lower-count", func(t *testing.T) {
		mixedHigh := groupOf("high", ResponseTypeNonEmpty,
			resWithUpstream("internal-1", []string{"type:internal"}, "0x100"),
			resWithUpstream("alchemy-1", []string{"type:external"}, "0x100"),
			resWithUpstream("infura-1", []string{"type:external"}, "0x100"),
		)
		mixedHigh.Count = 3
		mixedLow := groupOf("low", ResponseTypeNonEmpty,
			resWithUpstream("internal-2", []string{"type:internal"}, "0x200"),
			resWithUpstream("quicknode-1", []string{"type:external"}, "0x200"),
		)
		outcome, winner := resolveMinAgreement([]*responseGroup{mixedHigh, mixedLow}, reqs, threshold)
		require.Equal(t, minAgreementUniqueWinner, outcome)
		require.Equal(t, mixedHigh, winner)
	})
}

func TestReorderForParticipantQuota(t *testing.T) {
	type spec = struct {
		id   string
		tags []string
	}

	t.Run("disabled: nil requirements is a no-op", func(t *testing.T) {
		ups := quotaUps(spec{"a", nil}, spec{"b", nil})
		got := reorderForParticipantQuota(ups, nil)
		require.Equal(t, []string{"a", "b"}, idsOf(got))
	})

	t.Run("promotes a tag match into the front window", func(t *testing.T) {
		// Selection order puts the us-east upstream last; a min-1 quota on
		// region:us must pull it forward so a small maxParticipants includes it.
		ups := quotaUps(
			spec{"fast-eu", []string{"region:eu"}},
			spec{"mid-eu", []string{"region:eu"}},
			spec{"slow-us", []string{"region:us"}},
		)
		reqs := []*common.ConsensusRequiredParticipant{{Tag: "region:us", MinParticipants: 1}}
		got := idsOf(reorderForParticipantQuota(ups, reqs))
		require.Equal(t, "slow-us", got[0], "the only region:us upstream must be front-loaded")
		require.ElementsMatch(t, []string{"fast-eu", "mid-eu", "slow-us"}, got, "no upstream lost or duplicated")
	})

	t.Run("preserves order when quota already satisfied at the front", func(t *testing.T) {
		ups := quotaUps(
			spec{"us1", []string{"region:us"}},
			spec{"eu1", []string{"region:eu"}},
			spec{"us2", []string{"region:us"}},
		)
		reqs := []*common.ConsensusRequiredParticipant{{Tag: "region:us", MinParticipants: 1}}
		// us1 already matches at position 0 → no reordering needed.
		require.Equal(t, []string{"us1", "eu1", "us2"}, idsOf(reorderForParticipantQuota(ups, reqs)))
	})

	t.Run("min>1 promotes multiple matches in incoming order", func(t *testing.T) {
		ups := quotaUps(
			spec{"eu1", []string{"region:eu"}},
			spec{"us1", []string{"region:us"}},
			spec{"eu2", []string{"region:eu"}},
			spec{"us2", []string{"region:us"}},
		)
		reqs := []*common.ConsensusRequiredParticipant{{Tag: "region:us", MinParticipants: 2}}
		got := idsOf(reorderForParticipantQuota(ups, reqs))
		require.Equal(t, []string{"us1", "us2", "eu1", "eu2"}, got)
	})

	t.Run("best-effort when group smaller than min", func(t *testing.T) {
		ups := quotaUps(
			spec{"eu1", []string{"region:eu"}},
			spec{"us1", []string{"region:us"}},
		)
		reqs := []*common.ConsensusRequiredParticipant{{Tag: "region:us", MinParticipants: 3}}
		got := idsOf(reorderForParticipantQuota(ups, reqs))
		require.Equal(t, []string{"us1", "eu1"}, got, "promotes the one match it has; rest follow")
	})

	t.Run("multiple requirements front-load both groups", func(t *testing.T) {
		ups := quotaUps(
			spec{"public1", []string{"tier:public"}},
			spec{"public2", []string{"tier:public"}},
			spec{"paid1", []string{"tier:paid"}},
			spec{"region-us", []string{"region:us", "tier:public"}},
		)
		reqs := []*common.ConsensusRequiredParticipant{
			{Tag: "tier:paid", MinParticipants: 1},
			{Tag: "region:us", MinParticipants: 1},
		}
		got := idsOf(reorderForParticipantQuota(ups, reqs))
		// paid1 promoted for the first req, region-us for the second; the two
		// tier:public upstreams keep their order behind them.
		require.Equal(t, []string{"paid1", "region-us", "public1", "public2"}, got)
	})

	t.Run("glob tag pattern matches", func(t *testing.T) {
		ups := quotaUps(
			spec{"eu", []string{"region:eu-west"}},
			spec{"us", []string{"region:us-east"}},
		)
		reqs := []*common.ConsensusRequiredParticipant{{Tag: "region:us-*", MinParticipants: 1}}
		require.Equal(t, []string{"us", "eu"}, idsOf(reorderForParticipantQuota(ups, reqs)))
	})

	t.Run("no match leaves the list untouched", func(t *testing.T) {
		ups := quotaUps(spec{"a", []string{"region:eu"}}, spec{"b", []string{"region:eu"}})
		reqs := []*common.ConsensusRequiredParticipant{{Tag: "region:ap", MinParticipants: 1}}
		require.Equal(t, []string{"a", "b"}, idsOf(reorderForParticipantQuota(ups, reqs)))
	})
}

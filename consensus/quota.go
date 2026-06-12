package consensus

import "github.com/erpc/erpc/common"

// reorderForParticipantQuota returns a reordering of `ups` that front-loads
// enough tag-matching upstreams to satisfy each `requiredParticipants`
// entry, so that when the executor draws its first `maxParticipants`
// participants they include at least `minParticipants` from each required
// tag group.
//
// Semantics:
//   - Best-effort: if a required group has fewer matching upstreams than
//     requested (or several quotas can't all fit within maxParticipants),
//     it promotes everything it can and leaves the shortfall to the
//     existing lowParticipantsBehavior / agreementThreshold handling —
//     consensus is not aware this happened, it just sees fewer/uneven
//     participants like any organic low-participation tick.
//   - Minimal disturbance: non-required upstreams keep their incoming
//     (selection-policy) order in the remaining slots, so ranking/quality
//     is preserved wherever the quota doesn't force a change. Order WITHIN
//     the participant set doesn't affect voting — only set membership does.
//   - A single upstream can satisfy multiple entries it matches (we never
//     double-promote the same upstream).
//
// Returns the input slice unchanged when there are no upstreams or no
// requirements (the feature is opt-in and off by default).
func reorderForParticipantQuota(ups []common.Upstream, reqs []*common.ConsensusRequiredParticipant) []common.Upstream {
	if len(ups) == 0 || len(reqs) == 0 {
		return ups
	}

	promoted := make([]common.Upstream, 0, len(ups))
	promotedIDs := make(map[string]struct{}, len(ups))

	for _, r := range reqs {
		if r == nil || r.MinParticipants <= 0 || r.Tag == "" {
			continue
		}
		// Count matches already promoted by an earlier requirement — an
		// upstream that matches several tags counts toward each of them.
		have := 0
		for _, u := range promoted {
			if upstreamMatchesTag(u, r.Tag) {
				have++
			}
		}
		// Promote more matching upstreams, in incoming (quality) order,
		// until the minimum is met or we run out of candidates.
		for _, u := range ups {
			if have >= r.MinParticipants {
				break
			}
			if _, ok := promotedIDs[u.Id()]; ok {
				continue
			}
			if upstreamMatchesTag(u, r.Tag) {
				promoted = append(promoted, u)
				promotedIDs[u.Id()] = struct{}{}
				have++
			}
		}
	}

	if len(promoted) == 0 {
		return ups
	}

	// promoted (quota-required, in priority/quality order) first, then the
	// rest in their original order.
	out := make([]common.Upstream, 0, len(ups))
	out = append(out, promoted...)
	for _, u := range ups {
		if _, ok := promotedIDs[u.Id()]; ok {
			continue
		}
		out = append(out, u)
	}
	return out
}

// anyAgreementQuota reports whether any requiredParticipants entry sets
// MinAgreement > 0 — i.e. the mixed-node winner-composition feature is enabled.
func anyAgreementQuota(reqs []*common.ConsensusRequiredParticipant) bool {
	for _, r := range reqs {
		if r != nil && r.MinAgreement > 0 && r.Tag != "" {
			return true
		}
	}
	return false
}

// groupSatisfiesAgreementQuotas reports whether the response group g contains,
// for every requiredParticipants entry with MinAgreement > 0, at least
// MinAgreement upstreams matching that tag. Entries with MinAgreement == 0 are
// ignored (pool-placement only). Returns true when no entry sets MinAgreement,
// so the feature stays fully opt-in.
func groupSatisfiesAgreementQuotas(g *responseGroup, reqs []*common.ConsensusRequiredParticipant) bool {
	if g == nil {
		return false
	}
	for _, r := range reqs {
		if r == nil || r.MinAgreement <= 0 || r.Tag == "" {
			continue
		}
		have := 0
		for _, res := range g.Results {
			if res != nil && res.Upstream != nil && upstreamMatchesTag(res.Upstream, r.Tag) {
				have++
			}
		}
		if have < r.MinAgreement {
			return false
		}
	}
	return true
}

// minAgreementOutcome describes what the minAgreement rule should do after
// inspecting the current set of valid groups.
type minAgreementOutcome int

const (
	minAgreementNoOp              minAgreementOutcome = iota // feature off or no group at threshold
	minAgreementUniqueWinner                                  // exactly one quota-satisfying group at top count
	minAgreementCompositionDispute                            // threshold met but no group satisfies tag quotas
	minAgreementValueTieDefer                                 // 2+ quota-satisfying groups tied at top count; defer to later rules
)

// resolveMinAgreement classifies the current state of valid groups for the
// minAgreement rule. The caller should not fire the rule when NoOp or
// ValueTieDefer is returned — those cases are handled by later rules.
func resolveMinAgreement(groups []*responseGroup, reqs []*common.ConsensusRequiredParticipant, threshold int) (minAgreementOutcome, *responseGroup) {
	if !anyAgreementQuota(reqs) {
		return minAgreementNoOp, nil
	}

	// Phase 1: collect groups that meet the threshold.
	var atThreshold []*responseGroup
	for _, g := range groups {
		if g.Count >= threshold {
			atThreshold = append(atThreshold, g)
		}
	}
	if len(atThreshold) == 0 {
		return minAgreementNoOp, nil
	}

	// Phase 2: among those, find quota-satisfying groups.
	var satisfying []*responseGroup
	for _, g := range atThreshold {
		if groupSatisfiesAgreementQuotas(g, reqs) {
			satisfying = append(satisfying, g)
		}
	}
	if len(satisfying) == 0 {
		return minAgreementCompositionDispute, nil
	}

	// Phase 3: find the top count among satisfying groups.
	maxCount := 0
	for _, g := range satisfying {
		if g.Count > maxCount {
			maxCount = g.Count
		}
	}
	var top []*responseGroup
	for _, g := range satisfying {
		if g.Count == maxCount {
			top = append(top, g)
		}
	}

	if len(top) == 1 {
		return minAgreementUniqueWinner, top[0]
	}
	// len(top) > 1: same hash ↔ same group in the map, so multiple top groups
	// always means different hashes — a genuine value disagreement. Defer to
	// the existing tie/dispute rules (priorities 9/14).
	return minAgreementValueTieDefer, nil
}

// upstreamMatchesTag reports whether any of the upstream's tags matches the
// given glob pattern (`*`, `?`). Falls back to exact equality first so a
// plain tag like "tier:paid" matches without invoking the glob engine.
func upstreamMatchesTag(u common.Upstream, pattern string) bool {
	if u == nil {
		return false
	}
	cfg := u.Config()
	if cfg == nil {
		return false
	}
	for _, t := range cfg.Tags {
		if t == pattern {
			return true
		}
		if m, err := common.WildcardMatch(pattern, t); err == nil && m {
			return true
		}
	}
	return false
}

package integrity

// Group F: finality-evidence shape checks — what the RPC's own finality
// artifacts must look like when an honest node serves them.
//
// commitmentQuorum pins the getBlockCommitment histogram's internal shape:
// a stake histogram by vote-lockout depth is 32 u64 tiers, each bounded by
// totalStake and non-increasing with depth (stake unlocks monotonically).
// The ⅔ quorum SEMANTICS (confirmed/finalized classification) stay with the
// consumer; this check only proves the histogram wasn't invented.
//
// rootSlotSanity pins getVoteAccounts entries: a vote account's root slot
// never exceeds its last voted slot, and last votes from the future (ahead
// of our own observed head by more than a tolerance) mean the table is
// stale, synthesized, or from the wrong cluster. The head tolerance makes
// this ReorgSensitive-adjacent (a lagging poller sees future votes
// legitimately) — record by default.

import (
	"context"
	"encoding/json"
)

func init() {
	register(commitmentQuorum)
	register(rootSlotSanity)
}

// svm.final.commitmentQuorum — the commitment histogram must have exactly 32
// integer tiers, each ≤ totalStake, non-increasing with depth, and a
// positive totalStake.
var commitmentQuorum = &Check{
	ID:      "svm.final.commitmentQuorum",
	Family:  FamilyCommitment,
	Class:   Deterministic,
	Methods: []string{"getblockcommitment"},
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		c, err := d.BlockCommitment()
		if err != nil || c == nil || c.TotalStake == nil {
			return Skipped
		}
		total := *c.TotalStake
		if total <= 0 {
			return failf("totalStake %d is not positive — invented commitment?", total)
		}
		if len(c.Commitment) != 32 {
			return failf("commitment histogram has %d tiers, expected 32 (vote-lockout depths)", len(c.Commitment))
		}
		var prev int64 = -1
		for i, raw := range c.Commitment {
			n, ok := rawJSONInt64(raw)
			if !ok {
				return failf("commitment tier [%d] is not an integer", i)
			}
			if n < 0 {
				return failf("commitment tier [%d] is negative (%d)", i, n)
			}
			if n > total {
				return failf("commitment tier [%d] = %d exceeds totalStake %d — fabricated histogram", i, n, total)
			}
			if prev >= 0 && n > prev {
				return failf("commitment histogram increases with depth (tier [%d]=%d > tier [%d]=%d) — stake does not re-lock", i, n, i-1, prev)
			}
			prev = n
		}
		return nil
	},
}

// svm.final.rootSlotSanity — per vote-account entry: rootSlot ≤ lastVote
// (you cannot have rooted a slot you never voted through), and lastVote no
// further than maxVoteAhead slots beyond our observed head (a table from
// the future / wrong cluster). Null rootSlot (brand-new validator) skips
// that entry's comparison.
const (
	paramMaxVoteAhead   = "maxVoteAhead"
	defaultMaxVoteAhead = 128
)

var rootSlotSanity = &Check{
	ID:      "svm.final.rootSlotSanity",
	Family:  FamilyCommitment,
	Class:   ReorgSensitive,
	Methods: []string{"getvoteaccounts"},
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		var res struct {
			Current    []voteAccountEntry `json:"current"`
			Delinquent []voteAccountEntry `json:"delinquent"`
		}
		if err := json.Unmarshal(d.raw, &res); err != nil || (len(res.Current) == 0 && len(res.Delinquent) == 0) {
			return Skipped
		}
		entries := append(append([]voteAccountEntry{}, res.Current...), res.Delinquent...)
		// Future-vote bound needs our own head; without it only the
		// per-entry comparison runs.
		var head int64 = -1
		if tr, ok := tipFrom(d); ok {
			if latest, known := tr.Latest(ctx); known && latest > 0 {
				head = latest
			}
		}
		ahead := int64(cfg.intParam(paramMaxVoteAhead, defaultMaxVoteAhead))
		for i, e := range entries {
			if !e.LastVote.ok {
				continue
			}
			if e.RootSlot != nil {
				if r, rok := rawJSONInt64(*e.RootSlot); rok {
					if r > e.LastVote.v {
						return failf("vote account [%d]: rootSlot %d exceeds lastVote %d — impossible progression", i, r, e.LastVote.v)
					}
				}
			}
			if head >= 0 && e.LastVote.v > head+ahead {
				return failf("vote account [%d]: lastVote %d is %d slots beyond our head %d — table from the future or wrong cluster?",
					i, e.LastVote.v, e.LastVote.v-head, head)
			}
		}
		return nil
	},
}

// voteAccountEntry captures the shape-relevant getVoteAccounts fields.
// Slot/stake numbers arrive as JSON numbers (sonic keeps them parseable as
// integers via rawJSONInt64).
type voteAccountEntry struct {
	ActivatedStake json.RawMessage  `json:"activatedStake"`
	LastVote       jsonNumberSlot   `json:"lastVote"`
	RootSlot       *json.RawMessage `json:"rootSlot"`
}

// jsonNumberSlot is a small optional-int envelope (rawJSONInt64 semantics).
type jsonNumberSlot struct {
	v  int64
	ok bool
}

func (s *jsonNumberSlot) UnmarshalJSON(b []byte) error {
	n, ok := rawJSONInt64(b)
	s.v, s.ok = n, ok
	return nil
}

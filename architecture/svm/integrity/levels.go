package integrity

// Level is the single front-door preset over the check catalog
// (specs/svm-integrity/feature.md §3): the one knob most operators set. Higher
// levels are supersets of lower ones - same contract as the EVM engine.
type Level string

const (
	LevelOff           Level = "off"
	LevelIntrinsic     Level = "intrinsic"
	LevelCorroborated  Level = "corroborated"
	LevelAuthoritative Level = "authoritative"
)

// rank orders the levels so a preset can be expressed as "this row and every
// lower row". off/unknown is 0 (enables nothing).
func (l Level) rank() int {
	switch l {
	case LevelIntrinsic:
		return 1
	case LevelCorroborated:
		return 2
	case LevelAuthoritative:
		return 3
	default:
		return 0
	}
}

// levelMembership is the single auditable source of truth for which checks each
// level *introduces*. A level enables the union of its row and all lower rows
// (intrinsic ⊂ corroborated ⊂ authoritative):
//
//   - intrinsic     - pure self-consistency; no upstream cost, always safe.
//   - corroborated  - compare against ground truth already available (follower,
//     cached stake table); no force-fetch.
//   - authoritative - force-fetch the canonical entity to corroborate against.
//
// Every registered check id must appear in exactly one row, and no row may name
// an unknown id - both enforced by TestLevelMembershipCoversAllChecks.
//
// Rows for the commitment/corroboration tiers land with their phases; keeping
// them empty (rather than absent) documents the intended shape.
var levelMembership = map[Level][]string{
	LevelIntrinsic: {
		"svm.struct.blockShape",
		"svm.struct.txShape",
		"svm.struct.sigUniqueness",
		"svm.auth.signatureVerify",
		"svm.auth.genesisHash",
		"svm.shape.magnitude",
		"svm.shape.commitmentParam",
		"svm.shape.slotEncoding",
	},
	LevelCorroborated: {
		// Phase 2: slot-chain continuity over the verified-block index (first
		// ReorgSensitive checks — verdict resolves per finality).
		"svm.commit.parentLink",
		"svm.commit.heightMonotonic",
		// Phase 2: finality consistency against the upstream's own poller tips.
		"svm.final.finalizedBound",
		"svm.final.slotAhead",
		"svm.final.tipBound",
		// Phase 2+: svm.commit.chainFollower, svm.commit.slotEpoch,
		// svm.commit.timeWindow, svm.final.commitmentQuorum,
		// svm.final.stakeTableJoin, svm.final.rootSlotSanity,
		// svm.corr.*, svm.cont.*.
	},
	LevelAuthoritative: {
		// Phase 3+: svm.final.voteEvidence.
	},
}

// CheckSetForLevel returns the checks a level enables: the union of its row and
// all lower rows. This is the single mapping from the front-door knob to the
// check vocabulary; everything else (directives, headers, profiles) composes
// over the resulting set.
func CheckSetForLevel(level Level) CheckSet {
	cs := CheckSet{}
	r := level.rank()
	if r == 0 {
		return cs
	}
	for lvl, ids := range levelMembership {
		if lvl.rank() > r {
			continue
		}
		for _, id := range ids {
			cs.Enable(id, nil)
		}
	}
	return cs
}

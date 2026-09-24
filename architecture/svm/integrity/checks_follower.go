package integrity

// Group E: chain-follower divergence, time window, and epoch self-consistency.
//
// chainFollower is the spec's "same-slot-different-bankhash from one upstream
// = fork evidence" rule against the network's verified-block index: the index
// pins the FIRST verified block per slot (first-seen), so any later served
// block that disagrees with the pinned entry is divergence — a reorg (legit,
// hence ReorgSensitive) or a double-produce/splice (not legit either way).
//
// timeWindow keeps blockTime inside the physically possible envelope:
// [cluster genesis, now + small skew], non-decreasing along the verified
// chain within leader-record tolerance. Bounds are parameters because cluster
// genesis times differ (mainnet default).
//
// slotEpoch is single-response self-consistency for getEpochInfo:
// absoluteSlot == epoch*slotsPerEpoch + slotIndex. The spec's ground truth
// is getEpochSchedule (immutable once fetched); until the aux-fetch
// machinery lands, slotsPerEpoch is an operator parameter (mainnet default).

import (
	"context"
	"time"
)

func init() {
	register(chainFollower)
	register(timeWindow)
	register(slotEpoch)
}

// svm.commit.chainFollower — a served block that disagrees with the verified
// chain's pinned entry at its own slot is divergence: bankhash mismatch
// (double-produce / foreign-chain splice evidence) or parent-link mismatch
// against the pinned entry (slot-shift). Skips on first sight of a slot —
// the index seeds from whatever passed every enabled check.
var chainFollower = &Check{
	ID:      "svm.commit.chainFollower",
	Family:  FamilyCommitment,
	Class:   ReorgSensitive,
	Methods: []string{"getblock", "getconfirmedblock"},
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		if d.chain == nil {
			return Skipped
		}
		b, err := d.Block()
		if err != nil || b == nil || b.Blockhash == "" || b.ParentSlot == nil {
			return Skipped
		}
		slot, ok := d.RequestedSlot()
		if !ok || slot < 0 {
			return Skipped
		}
		stored, ok := d.chain.Entry(slot)
		if !ok {
			return Skipped // first sight — observeBlock pins it after the pass
		}
		self, err := base58DecodeLen(b.Blockhash, 32)
		if err != nil {
			return Skipped
		}
		if toHash32(self) != stored.blockhash {
			return failf("served block at slot %d has bankhash %s but the verified chain pinned %s at this slot — fork/double-produce evidence",
				slot, b.Blockhash, base58Encode(stored.blockhash[:]))
		}
		if b.PreviousBlockhash != "" {
			prev, err := base58DecodeLen(b.PreviousBlockhash, 32)
			if err == nil && toHash32(prev) != stored.parentHash {
				return failf("served block at slot %d names parent %s but the verified chain pinned parent %s — slot-shift/foreign-parent splice",
					slot, b.PreviousBlockhash, base58Encode(stored.parentHash[:]))
			}
		}
		return nil
	},
}

// Time-window parameters (overridable per check via Params).
const (
	paramMinBlockTime     = "minBlockTime"     // unix seconds; mainnet genesis default
	paramFutureSkewSec    = "futureSkewSec"    // wall-clock skew allowance (2 slots ≈ 1s)
	paramParentTimeTolSec = "parentTimeTolSec" // leader-record tolerance vs the pinned parent
	defaultMinBlockTime   = 1584230400         // 2020-03-16T00:00:00Z, mainnet launch day
	defaultFutureSkewSec  = 2
	defaultParentTimeTol  = 10
)

// svm.commit.timeWindow — blockTime inside the possible envelope and not
// grossly earlier than the pinned parent's. Null blockTime is legal (very old
// blocks) — those aspects skip.
var timeWindow = &Check{
	ID:      "svm.commit.timeWindow",
	Family:  FamilyCommitment,
	Class:   ReorgSensitive,
	Methods: []string{"getblock", "getconfirmedblock"},
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		b, err := d.Block()
		if err != nil || b == nil || b.BlockTime == nil {
			return Skipped
		}
		bt := *b.BlockTime
		minBT := int64(cfg.intParam(paramMinBlockTime, defaultMinBlockTime))
		skew := int64(cfg.intParam(paramFutureSkewSec, defaultFutureSkewSec))
		if bt < minBT {
			return failf("blockTime %d predates the cluster genesis bound %d — backdated history?", bt, minBT)
		}
		if max := time.Now().Unix() + skew; bt > max {
			return failf("blockTime %d is %d seconds in the future — time travel?", bt, bt-max+skew)
		}
		// Chain monotonicity within leader-record tolerance.
		if d.chain != nil && b.ParentSlot != nil {
			if parent, ok := d.chain.Entry(*b.ParentSlot); ok && parent.blockTime >= 0 {
				tol := int64(cfg.intParam(paramParentTimeTolSec, defaultParentTimeTol))
				if bt < parent.blockTime-tol {
					return failf("blockTime %d is more than %ds before its verified parent's %d — rewritten history?", bt, tol, parent.blockTime)
				}
			}
		}
		return nil
	},
}

// svm.commit.slotEpoch — getEpochInfo's three slot fields must agree under
// the cluster's slots-per-epoch. Ground truth is the immutable
// getEpochSchedule; until aux fetch lands, slotsPerEpoch is a parameter.
const (
	paramSlotsPerEpoch   = "slotsPerEpoch"
	defaultSlotsPerEpoch = 432000 // mainnet-beta
)

var slotEpoch = &Check{
	ID:      "svm.commit.slotEpoch",
	Family:  FamilyCommitment,
	Class:   Deterministic,
	Methods: []string{"getepochinfo"},
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		e, err := d.EpochInfo()
		if err != nil || e == nil || e.Epoch == nil || e.SlotIndex == nil || e.AbsoluteSlot == nil {
			return Skipped
		}
		spe := int64(cfg.intParam(paramSlotsPerEpoch, defaultSlotsPerEpoch))
		if spe <= 0 {
			return Skipped
		}
		abs, idx, ep := *e.AbsoluteSlot, *e.SlotIndex, *e.Epoch
		if idx < 0 || idx >= spe {
			return failf("slotIndex %d outside [0,%d) — inconsistent epoch fields", idx, spe)
		}
		if ep < 0 || ep*spe+idx != abs {
			return failf("epoch fields disagree: epoch %d * %d + slotIndex %d != absoluteSlot %d", ep, spe, idx, abs)
		}
		return nil
	},
}

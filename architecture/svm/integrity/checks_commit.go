package integrity

// Commitment-tier checks (Phase 2): link a block against the slot-chain index
// of blocks this module already verified. These are the first ReorgSensitive
// checks: a broken link on unfinalized data may be a benign fork race and is
// recorded; on finalized data it is rejected. Both skip when the parent is not
// in the index — unverifiable is never a violation.

import (
	"context"
)

func init() {
	register(parentLink)
	register(heightMonotonic)
}

// linkData is a block's claimed parent link alongside the parent's verified
// entry from the chain index.
type linkData struct {
	claimed chainEntry // this block: slot, blockhash, blockHeight, parentSlot, parentHash
	stored  chainEntry // the verified parent, looked up by parentSlot
}

// blockLinkData decodes the block and resolves its parent from the chain
// index. ok=false (check skips) when there is no block, the link fields are
// absent/undecodable, there is no chain index, or the parent was never
// verified.
func blockLinkData(d *Decoded) (linkData, bool) {
	b, err := d.Block()
	if err != nil || b == nil || b.Blockhash == "" || b.ParentSlot == nil {
		return linkData{}, false
	}
	prev, err := base58Decode(b.PreviousBlockhash)
	if err != nil || len(prev) != 32 {
		return linkData{}, false
	}
	self, err := base58Decode(b.Blockhash)
	if err != nil || len(self) != 32 {
		return linkData{}, false
	}
	if d.chain == nil {
		return linkData{}, false
	}
	stored, found := d.chain.Parent(*b.ParentSlot)
	if !found {
		return linkData{}, false
	}
	height := int64(-1)
	if b.BlockHeight != nil {
		height = *b.BlockHeight
	}
	slot, ok := d.RequestedSlot()
	if !ok {
		return linkData{}, false
	}
	// Genesis (slot 0) self-references an all-zeros previous hash — there is
	// no parent to link against. blockShape admits exactly this one
	// self-parent; skip rather than compare the block against itself.
	if slot == *b.ParentSlot {
		return linkData{}, false
	}
	return linkData{
		claimed: chainEntry{
			slot:        slot,
			blockhash:   toHash32(self),
			blockHeight: height,
			parentSlot:  *b.ParentSlot,
			parentHash:  toHash32(prev),
		},
		stored: stored,
	}, true
}

func toHash32(b []byte) [32]byte {
	var h [32]byte
	copy(h[:], b)
	return h
}

var parentLink = &Check{
	ID:      "svm.commit.parentLink",
	Family:  FamilyContinuity,
	Class:   ReorgSensitive,
	Methods: []string{"getblock", "getconfirmedblock"},
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		ld, ok := blockLinkData(d)
		if !ok {
			return Skipped
		}
		if ld.claimed.parentHash != ld.stored.blockhash {
			return failf("previousBlockhash %x does not match verified block at parentSlot %d (blockhash %x)",
				ld.claimed.parentHash, ld.claimed.parentSlot, ld.stored.blockhash)
		}
		return nil
	},
}

var heightMonotonic = &Check{
	ID:      "svm.commit.heightMonotonic",
	Family:  FamilyContinuity,
	Class:   ReorgSensitive,
	Methods: []string{"getblock", "getconfirmedblock"},
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		ld, ok := blockLinkData(d)
		if !ok || ld.claimed.blockHeight < 0 || ld.stored.blockHeight < 0 {
			return Skipped
		}
		if ld.claimed.blockHeight != ld.stored.blockHeight+1 {
			return failf("blockHeight %d is not verified parent blockHeight %d + 1 (parent slot %d)",
				ld.claimed.blockHeight, ld.stored.blockHeight, ld.stored.slot)
		}
		return nil
	},
}

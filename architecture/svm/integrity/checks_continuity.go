package integrity

// Group G: continuity checks — cross-request head progression and
// request/response slot binding at the context level.
//
// headProgression tracks each commitment bucket's highest reported head per
// network. A later response reporting a LOWER head in the same bucket is
// reorg evidence (record by default); finalized heads must never regress.
// Buckets advance monotonically in the real world (finalized <= confirmed
// <= processed), so cross-bucket inversions are also flagged.
//
// minContextSlot binds a request's minContextSlot floor to the response's
// actual context slot: serving a snapshot OLDER than the floor the client
// explicitly demanded is a silent downgrade.
//
// heightVsSlot is per-block Deterministic truth: Solana counts every tick,
// so a block's height (produced blocks so far) can never exceed its slot.

import (
	"context"
	"strings"
)

func init() {
	register(headProgression)
	register(minContextSlot)
	register(heightVsSlot)
}

// svm.cont.headProgression — getSlot/getBlockHeight/getEpochInfo heads move
// forward within their commitment bucket and never invert across buckets.
// Reorg-sensitive: a reorg IS a backwards move; default policy records it.
var headProgression = &Check{
	ID:      "svm.cont.headProgression",
	Family:  FamilyContinuity,
	Class:   ReorgSensitive,
	Methods: []string{"getslot", "getblockheight", "getepochinfo"},
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		slot, ok := d.SlotNumber()
		if d.method == "getepochinfo" {
			if e, err := d.EpochInfo(); err == nil && e != nil && e.AbsoluteSlot != nil {
				slot, ok = *e.AbsoluteSlot, true
			}
		}
		if !ok || slot < 0 || d.chain == nil {
			return Skipped
		}
		bucket := commitmentOf(d.reqParams)
		if prev, seen := d.chain.LastHead(bucket); seen && slot < prev {
			return failf("head regressed in %q bucket: %d after %d — reorg evidence", bucket, slot, prev)
		}
		// Cross-bucket monotonicity against the other observed buckets.
		if bucket != "finalized" {
			if f, seen := d.chain.LastHead("finalized"); seen && slot < f {
				return failf("%q head %d sits below the observed finalized head %d — inverted finality", bucket, slot, f)
			}
		}
		if bucket == "processed" {
			if cf, seen := d.chain.LastHead("confirmed"); seen && slot < cf {
				return failf("processed head %d sits below the observed confirmed head %d — inverted finality", slot, cf)
			}
		}
		d.chain.NoteHead(bucket, slot)
		return nil
	},
}

// commitmentOf extracts the commitment bucket from params[0] (bare string or
// config object). Unknown/absent means the default ("processed") processing
// commitment.
func commitmentOf(params []any) string {
	if len(params) == 0 {
		return "processed"
	}
	switch p := params[0].(type) {
	case string:
		if p != "" {
			return strings.ToLower(p)
		}
	case map[string]any:
		if c, ok := p["commitment"].(string); ok && c != "" {
			return strings.ToLower(c)
		}
	}
	return "processed"
}

// svm.cont.minContextSlot — a response served for a request that carried
// minContextSlot must have context.slot >= that floor. Deterministic: the
// server either honored the floor or it did not.
var minContextSlot = &Check{
	ID:      "svm.cont.minContextSlot",
	Family:  FamilyContinuity,
	Class:   Deterministic,
	Methods: []string{"getaccountinfo", "gettokenaccountbalance"},
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		if len(d.reqParams) < 2 {
			return Skipped
		}
		cfgMap, ok := d.reqParams[1].(map[string]any)
		if !ok {
			return Skipped
		}
		floorRaw, ok := cfgMap["minContextSlot"]
		if !ok {
			return Skipped
		}
		// Numbers arrive as float64 through encoding/json; accept any
		// integer-valued form.
		var floor int64
		switch v := floorRaw.(type) {
		case float64:
			floor = int64(v)
		case int64:
			floor = v
		default:
			return Skipped
		}
		served, ok := d.ContextSlot()
		if !ok {
			return Skipped
		}
		if served < floor {
			return failf("context.slot %d is below the request's minContextSlot floor %d — silent downgrade to a stale snapshot", served, floor)
		}
		return nil
	},
}

// svm.struct.heightVsSlot — a block's height never exceeds its slot: Solana
// advances one slot per tick but skips unproduced slots, so produced-block
// count <= tick count. Genesis boundary (equal) is legal.
var heightVsSlot = &Check{
	ID:      "svm.struct.heightVsSlot",
	Family:  FamilyStructural,
	Class:   Deterministic,
	Methods: []string{"getblock", "getconfirmedblock"},
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		b, err := d.Block()
		if err != nil || b == nil || b.BlockHeight == nil {
			return Skipped
		}
		// The block payload does not carry its own slot — the slot is the
		// request's. Absent request slot means we cannot bind them.
		slot, ok := paramSlot(d.reqParams, 0)
		if !ok || slot < 0 {
			return Skipped
		}
		if *b.BlockHeight > slot {
			return failf("blockHeight %d exceeds its own slot %d — impossible on a tick-per-slot ledger", *b.BlockHeight, slot)
		}
		return nil
	},
}

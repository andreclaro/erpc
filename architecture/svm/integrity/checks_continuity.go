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

// headSlot is the progression truth for the polled-head methods: the numeric
// result for getSlot/getBlockHeight, absoluteSlot for getEpochInfo (whose
// other slot-shaped fields are epoch-relative, not head positions).
func headSlot(d *Decoded) (int64, bool) {
	slot, ok := d.SlotNumber()
	if d.method == "getepochinfo" {
		if e, err := d.EpochInfo(); err == nil && e != nil && e.AbsoluteSlot != nil {
			slot, ok = *e.AbsoluteSlot, true
		}
	}
	return slot, ok
}

// svm.cont.headProgression — getSlot/getBlockHeight/getEpochInfo heads move
// forward within their commitment bucket and never invert across buckets.
// Reorg-sensitive: a reorg IS a backwards move; default policy records it.
// Detection is read-only; the high-water mark is committed by AfterPass only
// after the whole response validated cleanly — a head sighting tainted by
// any other check's mismatch must not become the baseline future
// regressions are judged against.
var headProgression = &Check{
	ID:      "svm.cont.headProgression",
	Family:  FamilyContinuity,
	Class:   ReorgSensitive,
	Methods: []string{"getslot", "getblockheight", "getepochinfo"},
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		slot, ok := headSlot(d)
		if !ok || slot < 0 || d.chain == nil {
			return Skipped
		}
		bucket := commitmentOf(d.method, d.reqParams)
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
		return nil
	},
	AfterPass: func(ctx context.Context, d *Decoded) {
		slot, ok := headSlot(d)
		if !ok || slot < 0 || d.chain == nil {
			return
		}
		d.chain.NoteHead(commitmentOf(d.method, d.reqParams), slot)
	},
}

// canonicalCommitment maps Agave's commitment vocabulary — the canonical
// three plus the legacy aliases every validator still accepts — onto the
// three canonical buckets used by the progression store.
func canonicalCommitment(level string) string {
	switch strings.ToLower(level) {
	case "finalized", "root", "max":
		return "finalized"
	case "confirmed", "single", "singlegossip":
		return "confirmed"
	default: // "processed", "recent", unknown
		return "processed"
	}
}

// commitmentOf extracts the request's commitment bucket: the param when
// given (bare string or config object), normalized through
// canonicalCommitment; otherwise the METHOD's default. getSlot,
// getBlockHeight and getEpochInfo default to the finalized commitment on
// Agave — a bare call's reported head is a finalized claim, not a
// processed one, and must live in (and be judged against) the finalized
// bucket.
func commitmentOf(method string, params []any) string {
	// The config object carrying "commitment" can sit at any param index
	// (getEpochInfo: params[0]; getBlock/getAccountInfo: params[1]) — scan
	// every param, like RequestCommitments does.
	for _, p := range params {
		if m, ok := p.(map[string]any); ok {
			if c, ok := m["commitment"].(string); ok && c != "" {
				return canonicalCommitment(c)
			}
		}
	}
	// Legacy bare-string commitment as the leading param (getVoteAccounts-
	// style). Only recognized commitment vocabulary counts — an address or
	// other string is not a bucket.
	if len(params) > 0 {
		if s, ok := params[0].(string); ok {
			switch strings.ToLower(s) {
			case "processed", "recent", "confirmed", "single", "singlegossip", "finalized", "root", "max":
				return canonicalCommitment(s)
			}
		}
	}
	switch method {
	case "getslot", "getblockheight", "getepochinfo":
		return "finalized"
	}
	return "processed"
}

// svm.cont.minContextSlot — a response served for a request that carried
// minContextSlot must have context.slot >= that floor. Applies to the whole
// envelope-method set (every one of them answers from a bank whose slot is
// reported in context). Deterministic: the server either honored the floor
// or it did not.
var minContextSlot = &Check{
	ID:      "svm.cont.minContextSlot",
	Family:  FamilyContinuity,
	Class:   Deterministic,
	Methods: envelopeMethodList(),
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		// The config object carrying minContextSlot sits at params[1] for
		// most envelope methods (and params[0] for the bare-config head
		// polls) — scan every param; the key only ever appears inside a
		// config object.
		var floor int64
		found := false
		for _, p := range d.reqParams {
			cfgMap, ok := p.(map[string]any)
			if !ok {
				continue
			}
			floorRaw, ok := cfgMap["minContextSlot"]
			if !ok {
				continue
			}
			// Numbers arrive as float64 through encoding/json; accept any
			// integer-valued form.
			switch v := floorRaw.(type) {
			case float64:
				floor, found = int64(v), true
			case int64:
				floor, found = v, true
			}
		}
		if !found {
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

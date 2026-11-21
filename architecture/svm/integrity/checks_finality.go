package integrity

// Finality-consistency checks (Phase 2): compare response slot claims against
// the upstream's own state-poller tips — ground truth already available, so
// no force-fetch. All three are ReorgSensitive: a poller can lag the node's
// true tip, so by default a contradiction is recorded (or rejected once the
// operator opts into hardReject per check), never silently accepted as
// provable corruption.

import (
	"context"
)

func init() {
	register(finalizedBound)
	register(slotAhead)
	register(tipBound)
}

// TipResolver extends FinalityResolver with the upstream's latest observed
// slot. The engine's Input.Finality carries it when the wiring provides one;
// checks that need the latest tip type-assert and skip otherwise.
type TipResolver interface {
	FinalityResolver
	Latest(ctx context.Context) (slot int64, known bool)
}

// tipFrom decodes Input.Finality as a TipResolver.
func tipFrom(d *Decoded) (TipResolver, bool) {
	if d.finality == nil {
		return nil, false
	}
	tr, ok := d.finality.(TipResolver)
	return tr, ok
}

// commitmentIsFinalized reports whether the response is expected to carry
// finalized data: the request either named a finalized(-aliased) commitment
// or used a method whose own default is finalized (getSlot/getBlockHeight/
// getEpochInfo — a bare head poll asserts a finalized position).
func commitmentIsFinalized(d *Decoded) bool {
	return commitmentOf(d.method, d.reqParams) == "finalized"
}

// responseSlot is the slot a response's data is anchored to: context.slot
// when the envelope carries it, otherwise the best-known slot decoded from
// the result itself (getSlot/getBlockHeight's number, getBlock's requested
// slot, the tx envelope's slot, ...).
func responseSlot(d *Decoded) (int64, bool) {
	if slot, ok := d.ContextSlot(); ok {
		return slot, true
	}
	return d.Slot()
}

var finalizedBound = &Check{
	ID:      "svm.final.finalizedBound",
	Family:  FamilyCommitment,
	Class:   ReorgSensitive,
	Methods: envelopeMethodList(),
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		if !commitmentIsFinalized(d) {
			return Skipped
		}
		slot, ok := responseSlot(d)
		if !ok {
			return Skipped
		}
		tr, ok := tipFrom(d)
		if !ok {
			return Skipped
		}
		final, known := tr.IsFinalized(ctx, slot)
		if !known {
			return Skipped
		}
		if final {
			return nil
		}
		return failf("response slot %d is not finalized on the serving upstream while the request asked for finalized commitment", slot)
	},
}

var slotAhead = &Check{
	ID:      "svm.final.slotAhead",
	Family:  FamilyCommitment,
	Class:   ReorgSensitive,
	Methods: envelopeMethodList(),
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		slot, ok := responseSlot(d)
		if !ok {
			return Skipped
		}
		tr, ok := tipFrom(d)
		if !ok {
			return Skipped
		}
		latest, known := tr.Latest(ctx)
		if !known || slot <= latest+1 {
			return nil
		}
		return failf("response context.slot %d is ahead of the upstream's latest observed slot %d", slot, latest)
	},
}

var tipBound = &Check{
	ID:      "svm.final.tipBound",
	Family:  FamilyCommitment,
	Class:   ReorgSensitive,
	Methods: []string{"getslot", "getblockheight"},
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		n, ok := d.SlotNumber()
		if !ok {
			return Skipped
		}
		tr, ok := tipFrom(d)
		if !ok {
			return Skipped
		}
		latest, known := tr.Latest(ctx)
		if !known || n <= latest+1 {
			return nil
		}
		return failf("reported slot/height %d is ahead of the upstream's latest observed slot %d", n, latest)
	},
}

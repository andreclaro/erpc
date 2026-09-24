package integrity

// Finality-consistency checks (Phase 2): compare response slot claims against
// the upstream's own state-poller tips — ground truth already available, so
// no force-fetch. All three are ReorgSensitive: a poller can lag the node's
// true tip, so by default a contradiction is recorded (or rejected once the
// operator opts into hardReject per check), never silently accepted as
// provable corruption.

import (
	"context"
	"strings"
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

// commitmentIsFinalized reports whether the request explicitly asked for
// finalized commitment (getX with commitment param).
func commitmentIsFinalized(d *Decoded) bool {
	for _, c := range d.RequestCommitments() {
		if strings.EqualFold(c, "finalized") {
			return true
		}
	}
	return false
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
		slot, ok := d.ContextSlot()
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
		return failf("response context.slot %d is not finalized on the serving upstream while the request asked for finalized commitment", slot)
	},
}

var slotAhead = &Check{
	ID:      "svm.final.slotAhead",
	Family:  FamilyCommitment,
	Class:   ReorgSensitive,
	Methods: envelopeMethodList(),
	Run: func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation {
		slot, ok := d.ContextSlot()
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

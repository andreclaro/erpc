package integrity

import (
	"context"
	"fmt"
	"strings"

	"github.com/erpc/erpc/common"
)

// Behavior is what a violation of one check does to the response.
type Behavior int

const (
	// BehaviorError — reject the response (converts to the content-validation
	// error so the caller's retry/failover machinery routes around the upstream).
	BehaviorError Behavior = iota
	// BehaviorRecord — serve the response but record the mismatch (metric +
	// warn log + misbehavior scoring stays untouched).
	BehaviorRecord
	// BehaviorIgnore — do not even run the check.
	BehaviorIgnore
)

func (b Behavior) String() string {
	switch b {
	case BehaviorError:
		return "reject"
	case BehaviorRecord:
		return "recordOnly"
	case BehaviorIgnore:
		return "off"
	}
	return "unknown"
}

// ReorgPolicy maps the response's finality state to the behavior for
// reorg-sensitive checks, where invalid data is ambiguously a node bug or a
// reorg. Deterministic checks ignore it and always reject.
type ReorgPolicy struct {
	Finalized   Behavior
	Unfinalized Behavior
}

// DefaultReorgPolicy is the safe default: a mismatch on finalized data is a
// rejection; on unfinalized data it is recorded (it might be a reorg).
func DefaultReorgPolicy() ReorgPolicy {
	return ReorgPolicy{Finalized: BehaviorError, Unfinalized: BehaviorRecord}
}

func (p ReorgPolicy) behaviorFor(final bool, known bool) Behavior {
	if !known {
		return p.Unfinalized
	}
	if final {
		return p.Finalized
	}
	return p.Unfinalized
}

// FinalityResolver tells the engine whether the response's slot is finalized.
// Wired to the network's finalized-slot view in the commitment tier; nil in
// the intrinsic tier means "unknown", which the default policy records rather
// than rejects (reorg-sensitive checks only).
type FinalityResolver interface {
	IsFinalized(ctx context.Context, slot int64) (final bool, known bool)
}

// Input is everything Validate needs to check one upstream response.
type Input struct {
	Method   string
	Upstream common.Upstream
	Response *common.NormalizedResponse
	Checks   CheckSet
	// Params are the originating request's JSON-RPC params, for checks that must
	// reproduce request semantics (requested slot, commitment vocabulary).
	// Optional.
	Params []any
	// Finality resolves the response's slot finality for reorg-sensitive
	// verdicts. Nil disables it (finality "unknown").
	Finality FinalityResolver
	// Chain is the network's verified-block index, feeding the commitment-tier
	// link checks and receiving blocks that passed every check. Nil disables
	// both (the checks skip).
	Chain *ChainState
	// Reorg maps finality state to the behavior for reorg-sensitive checks.
	Reorg ReorgPolicy
	// ObserveOnly suppresses every rejection: checks run and violations are
	// recorded as "would_reject", but the response is always served. Absolute —
	// it outranks a per-check onFailure and covers Deterministic checks too.
	ObserveOnly bool
}

// Recorded is a reorg-sensitive mismatch that was observed but not rejected
// (the block was unfinalized, so it may be a benign reorg). Callers emit a
// metric/log for it.
type Recorded struct {
	CheckID  string
	Reason   string
	Class    FailureClass
	Finality string // "finalized"/"unfinalized"/"unknown" — for the violation metric
	// Verdict is the label this record carries: "record_only" (a mismatch served
	// by policy) or "would_reject" (observe-only suppressed a real rejection).
	Verdict string
}

// Result is the outcome of validating a response. Err is non-nil when a check
// hard-failed (the response must be rejected). Recorded lists soft-flagged
// reorg-sensitive mismatches the caller should surface but still serve.
// Outcomes lists EVERY check that was evaluated and what happened, for the
// per-check attempts/outcomes metric.
type Result struct {
	Err             error
	RejectedCheckID string
	// RejectedClass is the failing check's FailureClass (meaningful only when
	// Err != nil). Deterministic = provable corruption — callers may feed it
	// into upstream health/misbehavior scoring; ReorgSensitive may still be a
	// transient race, so it should not damage a score.
	RejectedClass  FailureClass
	Finality       string // "finalized"/"unfinalized"/"unknown"; "" if no reject
	RejectedReason string
	Recorded       []Recorded
	Outcomes       []CheckOutcome
}

// CheckOutcome records what one check evaluation did. Outcome is one of:
// "pass" (an actual verification ran and found no violation), "skip" (the
// check could not evaluate this response — absent data, encoding variant
// without verifiable material, data not fully modeled; see Skipped), "reject"
// (failed, response rejected), "record_only" (mismatch recorded but served, no
// correction sought), "would_reject" (observe-only mode suppressed a rejection
// and served the response anyway), "off" (disabled for this check).
type CheckOutcome struct {
	CheckID string
	Outcome string
}

// Validate runs every enabled, applicable integrity check against the response.
// Deterministic violations reject immediately; reorg-sensitive violations are
// resolved against finality via the ReorgPolicy. It returns early on the first
// hard failure.
//
// Chain-safety invariant (spec §3): anything a check cannot fully model skips,
// never rejects. An integrity module must never reject valid data.
func Validate(ctx context.Context, in Input) Result {
	method := strings.ToLower(in.Method)
	checks := checksFor(method)
	if len(checks) == 0 || in.Response == nil {
		return Result{}
	}
	enabled := enabledChecks(checks, in.Checks)
	if len(enabled) == 0 {
		return Result{}
	}
	var res Result
	// Emptyish responses (null result — e.g. a skipped slot, or "no signatures
	// found") mean "nothing there" for result-content checks, which skip them.
	// Request-judging checks (the AllowEmptyish flag) still run — an invalid
	// request parameter is invalid regardless of the result.
	if in.Response.IsObjectNull() {
		return Result{}
	}
	if in.Response.IsResultEmptyish() {
		var kept []*Check
		for _, c := range enabled {
			if c.AllowEmptyish {
				kept = append(kept, c)
			} else {
				res.Outcomes = append(res.Outcomes, CheckOutcome{c.ID, "skip"})
			}
		}
		enabled = kept
		if len(enabled) == 0 {
			return res
		}
	}
	jrr, err := in.Response.JsonRpcResponse(ctx)
	if err != nil || jrr == nil {
		return Result{}
	}
	raw := jrr.GetResultBytes()
	if len(raw) == 0 {
		return Result{}
	}

	d := newDecoded(method, raw)
	d.reqParams = in.Params
	d.chain = in.Chain

	// One finality observation for this whole response: every check here judges
	// the same slot, and the verdict and its metric label must not come from
	// two separate reads of a moving finalized head.
	fin := &finalityOnce{}
	for _, c := range enabled {
		cfg := in.Checks.For(c.ID)
		// Resolve the verdict for this check up front. If it would be ignored
		// (per-check onFailure: off), skip the check entirely.
		behavior := in.verdictFor(ctx, c, cfg, d, fin)
		if behavior == BehaviorIgnore {
			res.Outcomes = append(res.Outcomes, CheckOutcome{c.ID, "off"})
			continue
		}

		v := c.Run(ctx, d, cfg)
		if v == nil {
			res.Outcomes = append(res.Outcomes, CheckOutcome{c.ID, "pass"})
			continue
		}
		if v == Skipped {
			res.Outcomes = append(res.Outcomes, CheckOutcome{c.ID, "skip"})
			continue
		}

		if behavior == BehaviorError {
			// Observe-only: never let a verdict touch the response. The violation
			// is still recorded in full (metric + forensic log) under a distinct
			// outcome, so "would_reject" counts exactly what enforcing on this
			// network would have cost clients. Applied HERE rather than in
			// verdictFor so it covers every path that can reach a rejection —
			// including Deterministic checks, which ignore invalidBehavior, and
			// any check added by a later release.
			if in.ObserveOnly {
				res.Outcomes = append(res.Outcomes, CheckOutcome{c.ID, "would_reject"})
				res.Recorded = append(res.Recorded, Recorded{
					CheckID: c.ID, Reason: v.Reason, Class: c.Class,
					Finality: fin.label(ctx, in, d), Verdict: "would_reject",
				})
				continue
			}
			res.Outcomes = append(res.Outcomes, CheckOutcome{c.ID, "reject"})
			res.Err = contentValidation(c, v, in.Upstream)
			res.RejectedCheckID = c.ID
			res.RejectedClass = c.Class
			res.RejectedReason = v.Reason
			res.Finality = fin.label(ctx, in, d)
			return res
		}
		// recordOnly verdict: surface the violation but serve the response.
		res.Outcomes = append(res.Outcomes, CheckOutcome{c.ID, "record_only"})
		res.Recorded = append(res.Recorded, Recorded{CheckID: c.ID, Reason: v.Reason, Class: c.Class, Finality: fin.label(ctx, in, d), Verdict: "record_only"})
	}
	// The block passed every check with no recorded mismatch — anchor it in
	// the verified chain index so later blocks can link against it. A block
	// that was served WITH a recorded mismatch never becomes ground truth.
	if res.Err == nil && len(res.Recorded) == 0 && in.Chain != nil && d.hasBlock() {
		observeBlock(in.Chain, d)
	}
	return res
}

// finalityOnce caches the response slot's finality for one Validate call so a
// violation's verdict and its metric label can never disagree.
type finalityOnce struct {
	done  bool
	final bool
	known bool
}

func (f *finalityOnce) resolve(ctx context.Context, in Input, d *Decoded) (bool, bool) {
	if !f.done {
		f.done = true
		if in.Finality != nil {
			if slot, ok := d.Slot(); ok {
				f.final, f.known = in.Finality.IsFinalized(ctx, slot)
			}
		}
	}
	return f.final, f.known
}

// label is the observability value — "finalized" / "unfinalized" / "unknown".
func (f *finalityOnce) label(ctx context.Context, in Input, d *Decoded) string {
	final, known := f.resolve(ctx, in, d)
	if !known {
		return "unknown"
	}
	if final {
		return "finalized"
	}
	return "unfinalized"
}

// verdictFor decides what to do on a violation of check c: a per-check override
// wins; otherwise deterministic checks reject and reorg-sensitive checks defer
// to finality (via the resolver) and the ReorgPolicy.
func (in Input) verdictFor(ctx context.Context, c *Check, cfg CheckConfig, d *Decoded, fin *finalityOnce) Behavior {
	if cfg.FailOverride != nil {
		return *cfg.FailOverride
	}
	if c.Class == Deterministic {
		return BehaviorError
	}
	return in.Reorg.behaviorFor(fin.resolve(ctx, in, d))
}

func contentValidation(c *Check, v *Violation, u common.Upstream) error {
	return common.NewErrEndpointContentValidation(
		fmt.Errorf("integrity check %q failed: %s", c.ID, v.Reason), u,
	)
}

// HasChecks reports whether any check is registered for a method, so callers
// can cheaply skip the engine (and building a CheckSet) for unrelated methods.
func HasChecks(method string) bool {
	return len(checksFor(strings.ToLower(method))) > 0
}

func enabledChecks(checks []*Check, cs CheckSet) []*Check {
	out := checks[:0:0]
	for _, c := range checks {
		if cs.For(c.ID).Enabled {
			out = append(out, c)
		}
	}
	return out
}

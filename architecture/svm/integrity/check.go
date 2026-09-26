package integrity

import (
	"context"
	"fmt"

	"github.com/erpc/erpc/common"
)

// Family groups checks by what kind of guarantee they provide. Descriptive
// only (metrics, docs, level presets); does not affect execution. Mirrors the
// EVM integrity engine's taxonomy — see specs/svm-integrity/feature.md §3.
type Family int

const (
	// FamilyStructural — cross-reference invariants over a block/transaction
	// (field shapes, uniqueness, self-consistency).
	FamilyStructural Family = iota
	// FamilyAuthenticity — per-item cryptographic authenticity (ed25519
	// signature batch verification, genesis hash). SVM's pillar-1 substitute
	// for the tx/receipt Merkle membership proofs EVM recomputes.
	FamilyAuthenticity
	// FamilyShape — cheap shape/sanity checks (magnitude, commitment vocabulary,
	// slot encoding).
	FamilyShape
	// FamilyCommitment — chain anchoring (bank-hash links, follower). Stateful;
	// lands with the commitment tier (Phase 2).
	FamilyCommitment
	// FamilyContinuity — cross-slot progression / reorg awareness. Stateful;
	// Phase 2+.
	FamilyContinuity
	// FamilyCorroboration — joins against aux sources. Phase 4.
	FamilyCorroboration
)

// FailureClass decides how a violation should be treated.
type FailureClass int

const (
	// Deterministic — provable from committed data; cannot be a transient race.
	Deterministic FailureClass = iota
	// ReorgSensitive — head-of-chain data can legitimately change (forks within
	// the reorg window, skipped slots); the verdict resolves per finality.
	ReorgSensitive
)

func (c FailureClass) String() string {
	if c == Deterministic {
		return "deterministic"
	}
	return "reorg-sensitive"
}

// Violation is a check's verdict that the response is invalid. Reason is a
// human-readable explanation; the engine prefixes it with the check id.
type Violation struct {
	Reason string
}

// Skipped is the sentinel a check returns when it could not perform its
// verification at all — missing wiring, data the check does not fully model
// (chain-safety: an unparseable tx is never a violation, it is a skip), or an
// encoding variant without verifiable material. It is not a violation and
// never affects the verdict; the engine records outcome "skip" so "pass" means
// an actual verification happened ("N verified, 0 mismatches").
var Skipped = &Violation{Reason: "skipped: check could not evaluate this response"}

// failf builds a Violation with a formatted reason.
func failf(format string, args ...any) *Violation {
	return &Violation{Reason: fmt.Sprintf(format, args...)}
}

// Check is one self-contained integrity validation.
type Check struct {
	// ID is the stable identifier used in config, metrics, and the validation
	// catalog (common.RegisterIntegrityCheckID). Prefix: svm.<family>.<name>.
	ID string
	// Family is the descriptive grouping (see Family).
	Family Family
	// Class is how a violation should be treated (see FailureClass).
	Class FailureClass
	// Methods are the lowercased JSON-RPC methods this check applies to.
	Methods []string
	// AllowEmptyish opts the check into running even when the response result is
	// "emptyish" (null / [] / "" — e.g. no signatures found). Only checks that
	// judge the REQUEST (not the result) qualify; result-content checks must not
	// fire on empty results.
	AllowEmptyish bool
	// Run inspects the decoded response and returns a Violation, nil when the
	// response was verified and satisfies this check, or the Skipped sentinel
	// when the check could not evaluate the response at all (absent data,
	// missing wiring, data not fully modeled) — an absent field is never a
	// violation.
	Run func(ctx context.Context, d *Decoded, cfg CheckConfig) *Violation
	// AfterPass, when non-nil, runs ONLY after the response validated cleanly:
	// no rejection and no recorded mismatch across every enabled check. It is
	// the single point where a check may commit cross-request state (head
	// progression, chain indexes) — committing inside Run would let a rejected
	// or merely-flagged response become ground truth for later requests.
	AfterPass func(ctx context.Context, d *Decoded)
}

// registry maps a lowercased method to the checks that apply to it. Checks
// self-register from their init() functions, mirroring the consensus rules
// pattern, so adding a check is a single localized edit.
var registry = map[string][]*Check{}

// allChecks is the flat registration order, used for introspection/tests.
var allChecks []*Check

func register(c *Check) {
	allChecks = append(allChecks, c)
	for _, m := range c.Methods {
		registry[m] = append(registry[m], c)
	}
	// Feed the config-validation catalog (common can't import this package),
	// so a typo'd check id in config fails validation instead of silently
	// doing nothing.
	common.RegisterIntegrityCheckID(c.ID)
}

// checksFor returns the checks registered for a lowercased method.
func checksFor(method string) []*Check {
	return registry[method]
}

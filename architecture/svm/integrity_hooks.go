package svm

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/erpc/erpc/architecture/svm/integrity"
	"github.com/erpc/erpc/common"
	"github.com/erpc/erpc/telemetry"
	"github.com/rs/zerolog/log"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// upstreamPostForward_integrity runs the SVM data-integrity engine over one
// upstream response and converts a violation into a content-validation error
// so retry/failover route around the upstream. Mirrors the EVM hook
// (architecture/evm/hooks.go): metrics, logs, misbehavior scoring for
// provable (deterministic) corruption, and last-valid-response hygiene.
//
// Differences from EVM at this phase: no chain view / resolver / reconfirm
// machinery yet (commitment tier), so no observeBlockView-style state feeding;
// the misbehaviors archive export (EVM's exportIntegrityCatch) is not wired
// for SVM yet.
func upstreamPostForward_integrity(ctx context.Context, n common.Network, u common.Upstream, rq *common.NormalizedRequest, rs *common.NormalizedResponse) error {
	methodLower := strings.ToLower(mustMethod(rq))
	dirs := rq.Directives()
	cs, policy, observeOnly := resolveIntegrity(n, dirs)
	if len(cs) == 0 || rs == nil {
		return nil
	}

	input := integrity.Input{
		Method:      methodLower,
		Upstream:    u,
		Response:    rs,
		Checks:      cs,
		Reorg:       policy,
		ObserveOnly: observeOnly,
		Chain:       chainStateFor(n),
		Finality:    svmFinalityResolver{u: u},
	}
	// Request-aware checks (commitment vocabulary, requested slot) need the
	// original params; read-only access under the request's RLock.
	if jrq, jerr := rq.JsonRpcRequest(ctx); jerr == nil && jrq != nil {
		jrq.RLockWithTrace(ctx)
		input.Params = jrq.Params
		jrq.RUnlock()
	}

	vctx, span := common.StartSpan(ctx, "Svm.Integrity.Validate",
		trace.WithAttributes(
			attribute.String("integrity.method", methodLower),
			attribute.String("integrity.upstream", u.Id()),
		))
	vStart := time.Now()
	res := integrity.Validate(vctx, input)
	rq.AddIntegrityOverhead(time.Since(vStart))
	if res.Err != nil {
		span.SetAttributes(
			attribute.String("integrity.check", res.RejectedCheckID),
			attribute.String("integrity.finality", res.Finality),
			attribute.String("integrity.reason", res.RejectedReason),
		)
	}
	span.End()

	// Per-check attempts/outcomes — "pass" means an actual verification ran;
	// "skip" means the check couldn't evaluate (encoding variant, absent data).
	for _, oc := range res.Outcomes {
		telemetry.MetricIntegrityCheck.WithLabelValues(
			n.ProjectId(), u.VendorName(), n.Label(), u.Id(), methodLower, oc.CheckID, oc.Outcome,
		).Inc()
	}
	for _, rec := range res.Recorded {
		verdict := rec.Verdict
		if verdict == "" {
			verdict = "record_only"
		}
		msg := "integrity: recorded mismatch (served, not rejected)"
		if verdict == "would_reject" {
			msg = "integrity: observe-only suppressed a rejection (served; enforcement would have failed this request)"
		}
		telemetry.MetricIntegrityViolation.WithLabelValues(
			n.ProjectId(), u.VendorName(), n.Label(), u.Id(), methodLower, rec.CheckID, verdict, rec.Finality,
		).Inc()
		log.Warn().Str("project", n.ProjectId()).Str("network", n.Label()).
			Str("upstream", u.Id()).Str("vendor", u.VendorName()).Str("method", methodLower).
			Str("check", rec.CheckID).Str("finality", rec.Finality).Str("reason", rec.Reason).
			Msg(msg)
	}
	if res.Err != nil {
		telemetry.MetricIntegrityViolation.WithLabelValues(
			n.ProjectId(), u.VendorName(), n.Label(), u.Id(), methodLower, res.RejectedCheckID, "reject", res.Finality,
		).Inc()
		log.Warn().Str("project", n.ProjectId()).Str("network", n.Label()).
			Str("upstream", u.Id()).Str("vendor", u.VendorName()).Str("method", methodLower).
			Str("check", res.RejectedCheckID).Str("finality", res.Finality).
			Str("reason", res.Err.Error()).Msg("integrity: rejected response (caught bad data)")
		rq.MarkIntegrityCaught(res.RejectedCheckID, res.Finality)
		// A Deterministic reject is PROVABLE corruption from this upstream —
		// feed it into misbehavior scoring so routing learns to avoid a
		// chronically-corrupt node. Reorg-sensitive rejects may be transient
		// races; they don't score.
		if res.RejectedClass == integrity.Deterministic {
			if ht := u.Tracker(); ht != nil {
				ht.RecordUpstreamMisbehavior(u, methodLower, rs.Finality(ctx))
			}
		}
		rs.MarkIntegrityRejected()
		rq.ClearLastValidResponseIf(rs)
		return res.Err
	}
	return nil
}

func mustMethod(rq *common.NormalizedRequest) string {
	m, err := rq.Method()
	if err != nil {
		return ""
	}
	return m
}

// chainStates holds one verified-block index per project+network. Single-
// process memory for the commitment tier's Phase-2 scope; the EVM ChainView's
// shared-state connector lands with the full follower.
var chainStates sync.Map // key: projectID + "\x00" + networkLabel → *integrity.ChainState

func chainStateFor(n common.Network) *integrity.ChainState {
	if n == nil {
		return nil
	}
	key := n.ProjectId() + "\x00" + n.Label()
	if v, ok := chainStates.Load(key); ok {
		return v.(*integrity.ChainState)
	}
	cs, _ := chainStates.LoadOrStore(key, integrity.NewChainState())
	return cs.(*integrity.ChainState)
}

// svmFinalityResolver resolves a slot's finality from the upstream's own
// state poller — the same finalized tip the router already trusts for lag
// detection. A poller that has never observed a finalized slot reports
// "unknown", which the default policy records rather than rejects.
type svmFinalityResolver struct {
	u common.Upstream
}

func (r svmFinalityResolver) IsFinalized(ctx context.Context, slot int64) (final bool, known bool) {
	if r.u == nil {
		return false, false
	}
	sup, ok := r.u.(common.SvmUpstream)
	if !ok {
		return false, false
	}
	p := sup.SvmStatePoller()
	if p == nil || p.IsObjectNull() {
		return false, false
	}
	fin := p.FinalizedSlot()
	if fin <= 0 {
		return false, false
	}
	return slot <= fin, true
}

// Latest returns the upstream's latest observed slot, making the resolver a
// TipResolver for the svm.final.* consistency checks.
func (r svmFinalityResolver) Latest(ctx context.Context) (slot int64, known bool) {
	if r.u == nil {
		return 0, false
	}
	sup, ok := r.u.(common.SvmUpstream)
	if !ok {
		return 0, false
	}
	p := sup.SvmStatePoller()
	if p == nil || p.IsObjectNull() {
		return 0, false
	}
	latest := p.LatestSlot()
	if latest <= 0 {
		return 0, false
	}
	return latest, true
}

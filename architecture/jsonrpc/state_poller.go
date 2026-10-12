package jsonrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/erpc/erpc/common"
	"github.com/erpc/erpc/data"
	"github.com/erpc/erpc/health"
	"github.com/rs/zerolog"
)

const (
	// defaultPollInterval applies when the configured interval is unset.
	defaultPollInterval = 5 * time.Second
	// minPollInterval clamps over-eager configs — a sub-second cadence burns
	// upstream RPC quota without materially improving freshness.
	minPollInterval = 1 * time.Second
	// probeCallTimeout bounds a single probe RPC so a hung upstream cannot
	// stall the tick forever (mirrors the SVM poller's per-tick timeout).
	probeCallTimeout = 15 * time.Second

	// defaultToleratedTipRollback mirrors DefaultToleratedSlotRollback: a tip
	// jump backwards past this is a wrong-node / snapshot-restore signal, not
	// chain truth, and is rejected by the shared counter.
	defaultToleratedTipRollback = 1024
)

// maxConsecutiveProbeSkips bounds how many consecutive ticks the traffic gate
// may skip the tip/finalized probes. Live traffic proves freshness, but the
// poller must periodically observe on its own — suggestions from a single busy
// method must never fully starve independent verification (mirrors the SVM
// poller's maxConsecutiveSlotPollSkips).
const maxConsecutiveProbeSkips = 4

var _ common.JsonRpcStatePoller = (*JsonRpcStatePoller)(nil)

// JsonRpcStatePoller is the config-declared tip/floor/health tracker for
// generic JSON-RPC networks (feature-v2 §4.4). It exists only when the network
// declares a jsonrpc.statePoller block — without Configure the poller never
// polls, preserving the v1 no-poller property. All probe behavior (methods,
// paths, encodings, health matchers) comes from operator config; the poller
// knows nothing about the chain it polls.
type JsonRpcStatePoller struct {
	projectId string
	appCtx    context.Context
	logger    *zerolog.Logger
	upstream  common.Upstream
	tracker   *health.Tracker

	latestTipShared    data.CounterInt64SharedVariable
	finalizedTipShared data.CounterInt64SharedVariable

	healthy          atomic.Bool
	cordonedByHealth atomic.Bool

	// floorTip is the availability floor (probe.RangeStart), best-effort in
	// MVP: tracked and logged, feeding nothing else.
	floorTip atomic.Int64

	cfgMu    sync.RWMutex
	cfg      *common.JsonRpcStatePollerConfig
	interval time.Duration

	// lastExternalTipAt / lastExternalFinalizedAt stamp (UnixMilli) the most
	// recent externally-suggested tip values; the poll loop uses them as the
	// traffic gate. Only the public Suggest* entry points stamp these — the
	// poller's own fetches never satisfy their own gate.
	lastExternalTipAt       atomic.Int64
	lastExternalFinalizedAt atomic.Int64
	// probeSkips counts consecutive traffic-gated skips; guarded by pollMu.
	probeSkips int
	// pollMu serializes Poll (the loop and any direct caller share it).
	pollMu sync.Mutex

	// startMu guards loop lifecycle. The loop starts on the first Configure
	// that happens after (or before) Bootstrap provided a context — either
	// order must work because upstream Bootstrap and SetNetworkConfig are
	// invoked from different registration phases.
	startMu      sync.Mutex
	loopRunning  bool
	bootstrapCtx context.Context
}

func NewJsonRpcStatePoller(
	projectId string,
	appCtx context.Context,
	logger *zerolog.Logger,
	up common.Upstream,
	tracker *health.Tracker,
	sharedState data.SharedStateRegistry,
) *JsonRpcStatePoller {
	networkId := up.NetworkId()
	lg := logger.With().Str("component", "jsonRpcStatePoller").Str("networkId", networkId).Logger()

	latestKey := fmt.Sprintf("jsonrpc/latestTip/%s", common.UniqueUpstreamKey(up))
	finalizedKey := fmt.Sprintf("jsonrpc/finalizedTip/%s", common.UniqueUpstreamKey(up))

	latestShared := sharedState.GetCounterInt64(latestKey, defaultToleratedTipRollback)
	finalizedShared := sharedState.GetCounterInt64(finalizedKey, defaultToleratedTipRollback)

	p := &JsonRpcStatePoller{
		projectId:          projectId,
		appCtx:             appCtx,
		logger:             &lg,
		upstream:           up,
		tracker:            tracker,
		latestTipShared:    latestShared,
		finalizedTipShared: finalizedShared,
	}

	// Counter callbacks, mirroring EvmStatePoller/SvmStatePoller. OnValue is
	// the ONLY tracker feed for tips: it fires once per ACCEPTED value
	// whatever the source — this poller's fetch, a traffic suggestion, or a
	// cross-instance propagation — and stays silent when the counter rejects a
	// rollback, so the tracker never sees a value the counter refused.
	if tracker != nil {
		latestShared.OnValue(func(value int64) {
			// Tips carry no block timestamp on generic chains; pass 0 (EVM/SVM
			// do the same, to avoid attributing a remote update's timestamp
			// locally).
			p.tracker.SetLatestBlockNumber(p.upstream, value, 0)
		})
		finalizedShared.OnValue(func(value int64) {
			p.tracker.SetFinalizedBlockNumber(p.upstream, value)
		})
		latestShared.OnLargeRollback(func(currentVal, newVal int64) {
			p.tracker.RecordBlockHeadLargeRollback(p.upstream, "latest", currentVal, newVal)
		})
		finalizedShared.OnLargeRollback(func(currentVal, newVal int64) {
			p.tracker.RecordBlockHeadLargeRollback(p.upstream, "finalized", currentVal, newVal)
		})
	}

	// Start healthy — flipped to false on the first failing tick. "Not yet
	// observed" must never read as unhealthy (see applyHealthToRouting).
	p.healthy.Store(true)
	return p
}

func (p *JsonRpcStatePoller) IsObjectNull() bool {
	return p == nil
}

// recoverPanic keeps a panic in a poll tick or fan-out goroutine from taking
// down the whole erpc process (which serves every other network too).
func (p *JsonRpcStatePoller) recoverPanic(where string) {
	if r := recover(); r != nil {
		p.logger.Error().
			Interface("panic", r).
			Str("where", where).
			Bytes("stack", debug.Stack()).
			Msg("recovered from panic in jsonrpc state poller")
	}
}

// Bootstrap stores the bootstrap context and starts the poll loop when probe
// configuration has already arrived via Configure. Idempotent.
func (p *JsonRpcStatePoller) Bootstrap(ctx context.Context) error {
	if ctx != nil {
		p.startMu.Lock()
		p.bootstrapCtx = ctx
		p.startMu.Unlock()
	}
	p.maybeStartLoop()
	return nil
}

// Configure applies the network-level probe configuration and starts the poll
// loop on first invocation. Without a Configure call the poller never polls.
func (p *JsonRpcStatePoller) Configure(cfg *common.JsonRpcStatePollerConfig) error {
	if cfg == nil {
		return fmt.Errorf("jsonrpc state poller configure: config is nil")
	}
	if cfg.Tip == nil || cfg.Tip.Method == "" {
		return fmt.Errorf("jsonrpc state poller configure: tip probe with a method is required")
	}
	interval := cfg.Interval.Duration()
	if interval <= 0 {
		interval = defaultPollInterval
	}
	if interval < minPollInterval {
		interval = minPollInterval
	}
	p.cfgMu.Lock()
	p.cfg = cfg
	p.interval = interval
	p.cfgMu.Unlock()
	p.maybeStartLoop()
	return nil
}

func (p *JsonRpcStatePoller) maybeStartLoop() {
	p.startMu.Lock()
	defer p.startMu.Unlock()
	if p.loopRunning {
		return
	}
	p.cfgMu.RLock()
	hasCfg := p.cfg != nil
	interval := p.interval
	p.cfgMu.RUnlock()
	ctx := p.bootstrapCtx
	if ctx == nil {
		ctx = p.appCtx
	}
	if !hasCfg || ctx == nil {
		return
	}
	p.loopRunning = true
	go p.loop(ctx, interval)
}

func (p *JsonRpcStatePoller) loop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			p.logger.Debug().Msg("shutting down jsonrpc state poller due to context interruption")
			return
		case <-p.appCtx.Done():
			p.logger.Debug().Msg("shutting down jsonrpc state poller due to app context interruption")
			return
		case <-ticker.C:
			func() {
				defer p.recoverPanic("loop")
				nctx, cancel := context.WithTimeout(ctx, probeCallTimeout)
				defer cancel()
				if err := p.Poll(nctx); err != nil {
					if !errors.Is(nctx.Err(), context.Canceled) {
						p.logger.Warn().Err(err).Msg("jsonrpc state poll failed")
					}
				}
			}()
		}
	}
}

// probeOutcome carries one probe's results. Transport failure, JSON-RPC error,
// and unhealthyWhen-match are kept separate: only the transport/JSON-RPC/parse
// health of the TIP probe (plus an unhealthyWhen match on any probe) gates
// upstream health, while finalized-tip and floor failures are isolated to
// their own counters (feature-v2 §9.6).
type probeOutcome struct {
	ok         bool // transport ok and no JSON-RPC error
	unhealthy  bool // unhealthyWhen matched
	value      int64
	valueValid bool
	floor      int64
	floorValid bool
	raw        interface{} // first-hop extraction for thenCall probes
	rawValid   bool
}

// Poll issues the configured probes and publishes their results. Probes fan
// out in parallel; each writes only its own outcome, joined before any shared
// state is touched (mirrors the SVM poller's per-fetch isolation).
//
// Traffic gate: when live traffic has refreshed BOTH tip views within the
// interval window (via SuggestLatestTip / SuggestFinalizedTip), the tip and
// finalizedTip probes are skipped this tick — bounded by
// maxConsecutiveProbeSkips so suggestions can never fully replace the poller's
// own observations. The health probe always runs: its verdict is what cordons
// (and uncordons) the upstream.
func (p *JsonRpcStatePoller) Poll(ctx context.Context) error {
	p.cfgMu.RLock()
	cfg := p.cfg
	interval := p.interval
	p.cfgMu.RUnlock()
	if cfg == nil {
		return nil
	}

	p.pollMu.Lock()
	defer p.pollMu.Unlock()

	skipProbes := false
	if p.probeSkips < maxConsecutiveProbeSkips {
		nowMs := time.Now().UnixMilli()
		window := interval.Milliseconds()
		tipAt := p.lastExternalTipAt.Load()
		finAt := p.lastExternalFinalizedAt.Load()
		if window > 0 &&
			tipAt > 0 && nowMs-tipAt < window &&
			finAt > 0 && nowMs-finAt < window {
			skipProbes = true
		}
	}
	if skipProbes {
		p.probeSkips++
	} else {
		p.probeSkips = 0
	}

	tickCtx, cancel := context.WithTimeout(ctx, probeCallTimeout)
	defer cancel()

	var wg sync.WaitGroup

	var healthOutcome probeOutcome
	if cfg.Health != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer p.recoverPanic("probe.health")
			healthOutcome = p.executeProbe(tickCtx, cfg.Health)
		}()
	}

	var tipOutcome probeOutcome
	if cfg.Tip != nil && !skipProbes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer p.recoverPanic("probe.tip")
			tipOutcome = p.executeProbe(tickCtx, cfg.Tip)
		}()
	}

	var finalizedOutcome probeOutcome
	if cfg.FinalizedTip != nil && !skipProbes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer p.recoverPanic("probe.finalizedTip")
			finalizedOutcome = p.executeFinalizedProbe(tickCtx, cfg.FinalizedTip)
		}()
	}

	var floorOutcome probeOutcome
	if cfg.Floor != nil && !skipProbes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer p.recoverPanic("probe.floor")
			floorOutcome = p.executeProbe(tickCtx, cfg.Floor)
		}()
	}

	wg.Wait()

	// Publish results. The tracker feeds live in the shared counters' OnValue
	// callbacks (NewJsonRpcStatePoller), so TryUpdate is the only write needed.
	// Values publish even when unhealthyWhen matched — a lagging tip is honest
	// lag data for the tracker; only health is gated by the matcher.
	if tipOutcome.ok && tipOutcome.valueValid && tipOutcome.value > 0 {
		p.latestTipShared.TryUpdate(tickCtx, tipOutcome.value)
	}
	if finalizedOutcome.ok && finalizedOutcome.valueValid && finalizedOutcome.value > 0 {
		p.finalizedTipShared.TryUpdate(tickCtx, finalizedOutcome.value)
	}
	if floorOutcome.floorValid && floorOutcome.floor > 0 {
		p.floorTip.Store(floorOutcome.floor)
		p.logger.Debug().Int64("floor", floorOutcome.floor).Msg("jsonrpc availability floor observed")
	} else if cfg.Floor != nil && !skipProbes && floorOutcome.ok {
		// "empty" (unsynced nodes) and unparseable samples mean "floor unknown",
		// never floor 0 — best-effort tracking stores nothing for the tick.
		p.logger.Debug().Msg("jsonrpc availability floor unknown for this tick")
	}

	p.healthy.Store(p.computeTickHealth(cfg, skipProbes, &healthOutcome, &tipOutcome, &finalizedOutcome, &floorOutcome))
	p.applyHealthToRouting()
	return nil
}

// computeTickHealth assembles the tick verdict. Unhealthy means: the health
// probe failed or matched unhealthyWhen; the tip probe failed, matched
// unhealthyWhen, or its value was unparseable (a stalled/broken tip is a
// liveness signal — "no other signal" covers chains without a health probe);
// or any OTHER probe matched unhealthyWhen. A finalized-tip or floor transport
// failure alone does NOT cordon: those counters are advisory, not liveness
// (feature-v2 §9.6 failure isolation).
func (p *JsonRpcStatePoller) computeTickHealth(cfg *common.JsonRpcStatePollerConfig, skipProbes bool, healthOutcome, tipOutcome, finalizedOutcome, floorOutcome *probeOutcome) bool {
	if cfg.Health != nil {
		if !healthOutcome.ok || healthOutcome.unhealthy {
			return false
		}
	}
	if !skipProbes && cfg.Tip != nil {
		if !tipOutcome.ok || tipOutcome.unhealthy || !tipOutcome.valueValid {
			return false
		}
	}
	if !skipProbes {
		if (cfg.FinalizedTip != nil && finalizedOutcome.unhealthy) ||
			(cfg.Floor != nil && floorOutcome.unhealthy) {
			return false
		}
	}
	return true
}

// executeProbe runs a single-hop probe: call, JSON-RPC error check, path
// extraction, encoding parse, unhealthyWhen match. A failed or unparseable
// sample yields an outcome with ok/valueValid false — the caller decides what
// that means per probe. A parsed value is reported even when unhealthyWhen
// matches: a catching-up node's lagging tip is honest lag data for the
// tracker; the matcher gates ONLY health (and hop 2 of a two-hop probe).
func (p *JsonRpcStatePoller) executeProbe(ctx context.Context, probe *common.JsonRpcStateProbeConfig) probeOutcome {
	out := probeOutcome{}
	respRoot, err := p.callProbe(ctx, probe.Method, probe.Params)
	if err != nil {
		p.logger.Debug().Err(err).Str("probe", probe.Method).Msg("jsonrpc state probe failed")
		return out
	}
	out.ok = true

	if probe.RangeStart {
		out.floor, out.floorValid = p.extractFloor(probe, respRoot)
		out.unhealthy = matchUnhealthyWhen(probe.UnhealthyWhen, respRoot)
		return out
	}

	v, ok := p.extractProbeValue(probe, respRoot)
	if ok {
		out.raw, out.rawValid = v, true
		parsed, err := parseCounterValue(v, probe.Encoding)
		if err != nil {
			p.logger.Debug().Err(err).Str("probe", probe.Method).Str("path", probe.Path).
				Msg("jsonrpc state probe value unparseable; skipping this counter's tick")
		} else {
			out.value, out.valueValid = parsed, true
		}
	}
	out.unhealthy = matchUnhealthyWhen(probe.UnhealthyWhen, respRoot)
	return out
}

// executeFinalizedProbe runs a two-hop probe (feature-v2 §4.4 thenCall, e.g.
// Substrate chain_getFinalizedHead → chain_getHeader): the first hop extracts
// the resolve value (a block hash), which becomes the sole param of the second
// hop when passValueAsParam is set. A failed second hop invalidates ONLY the
// finalized counter for the tick — the tip counter still updates (§9.6).
func (p *JsonRpcStatePoller) executeFinalizedProbe(ctx context.Context, probe *common.JsonRpcStateProbeConfig) probeOutcome {
	out := probeOutcome{}
	respRoot, err := p.callProbe(ctx, probe.Method, probe.Params)
	if err != nil {
		p.logger.Debug().Err(err).Str("probe", probe.Method).Msg("jsonrpc finalized-tip probe failed")
		return out
	}
	out.ok = true

	first, ok := p.extractProbeValue(probe, respRoot)
	if !ok {
		out.unhealthy = matchUnhealthyWhen(probe.UnhealthyWhen, respRoot)
		return out
	}

	// An unhealthy first hop invalidates the tick; the resolve chain of an
	// unhealthy probe is not trusted (and its sole param comes from the same
	// suspect response).
	if matchUnhealthyWhen(probe.UnhealthyWhen, respRoot) {
		out.unhealthy = true
		out.raw, out.rawValid = first, true
		return out
	}

	second := probe
	if probe.ThenCall != nil && probe.ThenCall.PassValueAsParam {
		respRoot2, err := p.callProbe(ctx, probe.ThenCall.Method, []interface{}{first})
		if err != nil {
			p.logger.Debug().Err(err).Str("probe", probe.ThenCall.Method).
				Msg("jsonrpc finalized-tip resolve hop failed; finalized counter not updated this tick")
			return out
		}
		second = &common.JsonRpcStateProbeConfig{
			Method:   probe.ThenCall.Method,
			Path:     probe.ThenCall.Path,
			Encoding: probe.ThenCall.Encoding,
		}
		respRoot = respRoot2
	}

	v, ok := p.extractProbeValue(second, respRoot)
	if ok {
		parsed, err := parseCounterValue(v, second.Encoding)
		if err != nil {
			p.logger.Debug().Err(err).Str("probe", second.Method).Str("path", second.Path).
				Msg("jsonrpc finalized-tip value unparseable; skipping this counter's tick")
		} else {
			out.value, out.valueValid = parsed, true
		}
	}
	out.unhealthy = matchUnhealthyWhen(probe.UnhealthyWhen, respRoot)
	return out
}

func (p *JsonRpcStatePoller) extractProbeValue(probe *common.JsonRpcStateProbeConfig, respRoot map[string]interface{}) (interface{}, bool) {
	if probe.Path == "" {
		return nil, false
	}
	v, ok := walkDotPath(respRoot, probe.Path)
	if !ok {
		p.logger.Debug().Str("probe", probe.Method).Str("path", probe.Path).
			Msg("jsonrpc state probe path did not resolve")
		return nil, false
	}
	return v, true
}

func (p *JsonRpcStatePoller) extractFloor(probe *common.JsonRpcStateProbeConfig, respRoot map[string]interface{}) (int64, bool) {
	v, ok := p.extractProbeValue(probe, respRoot)
	if !ok {
		return 0, false
	}
	s, isStr := v.(string)
	if !isStr {
		p.logger.Debug().Str("probe", probe.Method).Interface("value", v).
			Msg("jsonrpc floor probe value is not a range string; floor unknown")
		return 0, false
	}
	floor, ok := parseRangeStart(s)
	if !ok {
		p.logger.Debug().Str("probe", probe.Method).Str("value", s).
			Msg("jsonrpc floor probe value is not a ledger range; floor unknown")
	}
	return floor, ok
}

// callProbe issues one probe RPC through the upstream, mirroring the SVM
// poller's call helper: a raw JSON-RPC POST built with the probe method and
// verbatim configured params, bypassing method exclusion. The returned root
// materializes both subtrees so configured paths address the whole response.
func (p *JsonRpcStatePoller) callProbe(ctx context.Context, method string, params []interface{}) (map[string]interface{}, error) {
	if params == nil {
		params = []interface{}{}
	}
	body, err := json.Marshal(struct {
		JSONRPC string        `json:"jsonrpc"`
		ID      int           `json:"id"`
		Method  string        `json:"method"`
		Params  []interface{} `json:"params"`
	}{"2.0", 1, method, params})
	if err != nil {
		return nil, fmt.Errorf("marshal probe request: %w", err)
	}
	req := common.NewNormalizedRequest(body)
	resp, err := p.upstream.Forward(ctx, req, true, false)
	if err != nil {
		return nil, fmt.Errorf("probe forward %s: %w", method, err)
	}
	if resp == nil {
		return nil, fmt.Errorf("probe %s returned no response", method)
	}
	defer resp.Release()
	jrr, err := resp.JsonRpcResponse()
	if err != nil {
		return nil, fmt.Errorf("probe response parse %s: %w", method, err)
	}
	if jrr == nil {
		return nil, fmt.Errorf("probe %s returned an empty response body", method)
	}
	if jrr.Error != nil {
		return nil, fmt.Errorf("probe %s returned json-rpc error %d: %s", method, jrr.Error.Code, jrr.Error.Message)
	}
	return buildResponseRoot(jrr), nil
}

func (p *JsonRpcStatePoller) LatestTip() int64 {
	return p.latestTipShared.GetValue()
}

func (p *JsonRpcStatePoller) FinalizedTip() int64 {
	return p.finalizedTipShared.GetValue()
}

// IsHealthy reports the last tick's health verdict. Consumed by
// applyHealthToRouting on every poll: a false verdict cordons the upstream out
// of selection. Unknown never reads as unhealthy — the verdict starts true and
// only a failed/unhealthy tick flips it.
func (p *JsonRpcStatePoller) IsHealthy() bool {
	return p.healthy.Load()
}

// applyHealthToRouting publishes the poller's verdict to upstream selection.
// Cordon is the established mechanism — the default selection policy runs
// .removeCordoned(), so a cordoned upstream is dropped from routing for every
// request until it recovers; SvmStatePoller.applyHealthToRouting is the
// precedent.
//
// EDGE-TRIGGERED via cordonedByHealth: Cordon/Uncordon fire only on a
// transition, never once per tick, and a recovering poller lifts only ITS OWN
// cordon, never an operator's manual one. A cold poller cannot cordon:
// healthy starts true, so "not yet observed" reads as healthy.
//
// Recovery is observable because Upstream.Forward does not consult cordon
// state (only the selection policy does), so the poller keeps polling while
// cordoned.
func (p *JsonRpcStatePoller) applyHealthToRouting() {
	if p.upstream == nil {
		return
	}
	if p.IsHealthy() {
		if p.cordonedByHealth.CompareAndSwap(true, false) {
			p.logger.Info().Msg("jsonrpc upstream recovered; uncordoning")
			p.upstream.Uncordon("*", "jsonrpc state poller: probes healthy")
		}
		return
	}
	if !p.cordonedByHealth.CompareAndSwap(false, true) {
		return
	}
	p.logger.Warn().Msg("jsonrpc upstream unhealthy; cordoning out of rotation")
	p.upstream.Cordon("*", "jsonrpc state poller: probe reported unhealthy")
}

// SuggestLatestTip ingests an externally-observed tip (live traffic or a
// shared-state neighbor) and stamps the traffic gate. External observations
// feed the same counter as the poller's own fetches; the OnValue tracker feed
// covers them without any extra wiring.
func (p *JsonRpcStatePoller) SuggestLatestTip(tip int64) {
	if tip <= 0 {
		return
	}
	p.lastExternalTipAt.Store(time.Now().UnixMilli())
	p.latestTipShared.TryUpdate(p.appCtx, tip)
}

// SuggestFinalizedTip is the finalized-counter sibling of SuggestLatestTip.
func (p *JsonRpcStatePoller) SuggestFinalizedTip(tip int64) {
	if tip <= 0 {
		return
	}
	p.lastExternalFinalizedAt.Store(time.Now().UnixMilli())
	p.finalizedTipShared.TryUpdate(p.appCtx, tip)
}

import type { TimelineScenario } from "../TimelineRace";

/**
 * Scenarios for /inside-erpc/request-tracing: four ways one JSON-RPC call can
 * traverse eRPC, each with its own span signature on a TimelineRace axis.
 *
 * 1. "Cache-hit read" — the short path: Http.ReceivedRequest → Request.Handle →
 *    Network.Forward → Cache.Get HIT → WriteResponse. No upstream is touched
 *    and the async cache writer stays idle (a HIT never produces a Cache.Set).
 * 2. "Cold read · hedge" — cache MISS → selection ordering → the failsafe
 *    executor fires a hedge at its adaptive delay; two attempts race, the
 *    loser is discarded (ErrUpstreamHedgeCancelled), and the async Cache.Set
 *    lands after the response.
 * 3. "Retry on missing data" — a -32014 missing_data answer is retried after a
 *    data-unavailable wait (~one block, EMA block time); the
 *    dataUnavailableCapReached guard caps the loop.
 * 4. "Consensus call" — Consensus.Run fans out three consensus_slot attempts;
 *    two identical responses short-circuit the third, and the analyzer
 *    goroutine keeps draining after the client was answered.
 */
export const tracingScenarios: Record<string, TimelineScenario> = {
	"Cache-hit read": {
		duration: 0.03,
		tickEvery: 0.005,
		timeLabel: "wall time →",
		lanes: [
			{ id: "http", label: "HTTP handler", sub: "http_server.go" },
			{ id: "auth", label: "Auth + Project", sub: "projects.go · no span" },
			{ id: "network", label: "Network", sub: "networks.go:1743" },
			{ id: "cache", label: "Cache", sub: "Cache.Get span" },
			{ id: "response", label: "Response", sub: "WriteResponse" },
			{ id: "writer", label: "Async writer", sub: "networks.go:2426" },
		],
		bars: [
			{ lane: "http", start: 0.0005, end: 0.027, label: "createRequestHandler → goroutine", tone: "blue" },
			{ lane: "auth", start: 0.003, end: 0.008, label: "GetProject → Authenticate", tone: "dim" },
			{ lane: "network", start: 0.008, end: 0.02, label: "Network.Forward → Cache.Get", tone: "blue" },
			{ lane: "cache", start: 0.01, end: 0.018, label: "Cache.Get → HIT", tone: "green" },
			{ lane: "response", start: 0.02, end: 0.027, label: "WriteResponse + traceparent", tone: "blue" },
			{ lane: "writer", start: 0.028, end: 0.03, label: "no Cache.Set on HIT", tone: "dim", hatch: true },
		],
		markers: [
			{ lane: "http", at: 0.0, shape: "flag", tone: "dim", label: "span: Http.ReceivedRequest" },
			{ lane: "auth", at: 0.003, shape: "dot", tone: "dim", label: "auth path emits no span" },
			{ lane: "cache", at: 0.018, shape: "check", tone: "green", label: "HIT — 6 functions, 0 upstream calls" },
			{ lane: "response", at: 0.027, shape: "check", tone: "green", label: "200 OK + traceparent" },
			{ lane: "writer", at: 0.03, shape: "dot", tone: "dim", label: "writer idle" },
		],
	},

	"Cold read · hedge": {
		duration: 0.2,
		tickEvery: 0.02,
		timeLabel: "wall time →",
		lanes: [
			{ id: "http", label: "HTTP handler", sub: "Request.Handle span" },
			{ id: "network", label: "Network", sub: "cache + ordering" },
			{ id: "exec", label: "Failsafe executor", sub: "network_executor.go:164" },
			{ id: "upa", label: "Upstream A", sub: "primary attempt" },
			{ id: "upb", label: "Upstream B", sub: "hedge leg" },
			{ id: "cachew", label: "Async writer", sub: "Cache.Set" },
		],
		bars: [
			{ lane: "http", start: 0.002, end: 0.185, label: "Http ingress → Request.Handle", tone: "blue" },
			{ lane: "network", start: 0.006, end: 0.03, label: "Cache.Get MISS → GetOrderedInLane", tone: "blue" },
			{ lane: "exec", start: 0.03, end: 0.165, label: "timeout( retry( hedge( sweep )))", tone: "blue" },
			{ lane: "upa", start: 0.032, end: 0.15, label: "attempt 1 → upstream A", tone: "amber" },
			{ lane: "upb", start: 0.072, end: 0.138, label: "hedge leg → upstream B", tone: "green" },
			{ lane: "cachew", start: 0.19, end: 0.199, label: "Cache.Set async — 10s budget", tone: "dim", hatch: true },
		],
		markers: [
			{ lane: "network", at: 0.012, shape: "x", tone: "crimson", label: "MISS" },
			{ lane: "exec", at: 0.072, shape: "dot", tone: "blue", label: "hedge fires (adaptive delay)" },
			{ lane: "upb", at: 0.138, shape: "check", tone: "green", label: "winner" },
			{ lane: "upa", at: 0.15, shape: "x", tone: "amber", label: "discard: ErrUpstreamHedgeCancelled" },
			{ lane: "http", at: 0.185, shape: "check", tone: "green", label: "200 OK · X-ERPC-Upstream-Hedges: 1" },
			{ lane: "cachew", at: 0.199, shape: "dot", tone: "dim", label: "SET lands after the response" },
		],
	},

	"Retry on missing data": {
		duration: 1.2,
		tickEvery: 0.2,
		timeLabel: "wall time →",
		lanes: [
			{ id: "http", label: "HTTP handler", sub: "one call, two attempts" },
			{ id: "exec", label: "Failsafe executor", sub: "runRetry loop" },
			{ id: "upa", label: "Upstream A", sub: "attempt 1" },
			{ id: "wait", label: "Unavailable wait", sub: "catch-up" },
			{ id: "upb", label: "Upstream B", sub: "attempt 2" },
			{ id: "cachew", label: "Async writer", sub: "Cache.Set" },
		],
		bars: [
			{ lane: "http", start: 0.01, end: 1.13, label: "one call, two upstream attempts", tone: "blue" },
			{ lane: "exec", start: 0.02, end: 1.1, label: "runRetry → shouldRetryWithReason", tone: "blue" },
			{ lane: "upa", start: 0.03, end: 0.2, label: "attempt 1 → upstream A", tone: "amber" },
			{ lane: "wait", start: 0.2, end: 0.88, label: "wait ≈ one block (EMA block time)", tone: "amber", hatch: true },
			{ lane: "upb", start: 0.88, end: 1.06, label: "attempt 2 → upstream B", tone: "green" },
			{ lane: "cachew", start: 1.13, end: 1.19, label: "Cache.Set async — 10s budget", tone: "dim", hatch: true },
		],
		markers: [
			{ lane: "upa", at: 0.2, shape: "x", tone: "crimson", label: "-32014 missing_data" },
			{ lane: "exec", at: 0.2, shape: "dot", tone: "amber", label: "reason=missing_data" },
			{ lane: "wait", at: 0.54, shape: "dot", tone: "amber", label: "data_unavailable_wait histogram" },
			{ lane: "exec", at: 0.88, shape: "flag", tone: "blue", label: "retry attempt 2" },
			{ lane: "upb", at: 1.06, shape: "check", tone: "green", label: "upstream B replies" },
			{ lane: "exec", at: 1.1, shape: "dot", tone: "dim", label: "guard: dataUnavailableCapReached" },
			{ lane: "http", at: 1.13, shape: "check", tone: "green", label: "200 OK" },
		],
	},

	"Consensus call": {
		duration: 0.25,
		tickEvery: 0.05,
		timeLabel: "wall time →",
		lanes: [
			{ id: "http", label: "HTTP handler", sub: "Request.Handle span" },
			{ id: "exec", label: "Consensus.Run", sub: "consensus/executor.go" },
			{ id: "slotA", label: "Slot 1 · upstream A", sub: "consensus_slot" },
			{ id: "slotB", label: "Slot 2 · upstream B", sub: "consensus_slot" },
			{ id: "slotC", label: "Slot 3 · upstream C", sub: "consensus_slot" },
			{ id: "analyzer", label: "Analyzer", sub: "background" },
		],
		bars: [
			{ lane: "http", start: 0.005, end: 0.235, label: "Http ingress → Request.Handle", tone: "blue" },
			{ lane: "exec", start: 0.02, end: 0.2, label: "Consensus.Run → 3 slots", tone: "blue" },
			{ lane: "slotA", start: 0.03, end: 0.155, label: "slot 1 → upstream A", tone: "green" },
			{ lane: "slotB", start: 0.03, end: 0.155, label: "slot 2 → upstream B", tone: "green" },
			{ lane: "slotC", start: 0.03, end: 0.215, label: "slot 3 → upstream C", tone: "amber" },
			{ lane: "analyzer", start: 0.2, end: 0.245, label: "analyzer drains post-response", tone: "dim", hatch: true },
		],
		markers: [
			{ lane: "slotA", at: 0.03, shape: "flag", tone: "blue", label: "reason=consensus_slot" },
			{ lane: "slotA", at: 0.155, shape: "check", tone: "green", label: "hash == slot 2" },
			{ lane: "slotB", at: 0.155, shape: "check", tone: "green", label: "2/3 agree → short-circuit" },
			{ lane: "slotC", at: 0.215, shape: "x", tone: "amber", label: "cancelled mid-flight" },
			{ lane: "http", at: 0.235, shape: "check", tone: "green", label: "200 OK" },
			{ lane: "analyzer", at: 0.245, shape: "dot", tone: "dim", label: "survives the response" },
		],
	},
};

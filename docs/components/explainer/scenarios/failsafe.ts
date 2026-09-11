import type { TimelineScenario } from "../TimelineRace";

/**
 * Scenarios for /inside-erpc/failsafe-in-depth: the three paths through the
 * network-scope nesting doll — timeout(consensus(retry(hedge(sweep)))).
 *
 * Tab 1 "Block unavailable": a request for a just-produced block N is skipped
 * pre-forward (ErrUpstreamBlockUnavailable), waits ~one EMA block time, and
 * succeeds on the retry. Axis: 2.8s (2s-block chain).
 *
 * Tab 2 "Upstream 5xx": retryable 5xx errors exhaust the pool within one
 * attempt, the retry layer waits a ComputeBackoff delay (configured 250ms
 * base, default factor 1.2), and the next attempt lands on a healthy
 * upstream. Axis: 0.8s.
 *
 * Tab 3 "Hedge race": primary fires immediately, a fast null is rejected by
 * keep() (method not in emptyResultAccept), the quantile-0.7 timer fires the
 * hedge, and the sibling's data wins. Axis: 0.9s.
 */
export const failsafeScenarios: Record<string, TimelineScenario> = {
	"Block unavailable": {
		duration: 2.8,
		tickEvery: 0.4,
		timeLabel: "wall time →",
		lanes: [
			{ id: "client", label: "Client", sub: "your app" },
			{ id: "exec", label: "Retry loop", sub: "network_executor.go" },
			{ id: "sweep", label: "Upstream sweep", sub: "networks.go" },
			{ id: "upsA", label: "Upstream A", sub: "head = N−1" },
			{ id: "wait", label: "Catch-up wait", sub: "computeDelay" },
		],
		bars: [
			{ lane: "client", start: 0, end: 2.44, label: "eth_getBlockByNumber(N)", tone: "blue" },
			{ lane: "exec", start: 0, end: 0.07, label: "attempt 1", tone: "blue" },
			{ lane: "sweep", start: 0, end: 0.07, label: "pre-forward availability check", tone: "dim" },
			{ lane: "upsA", start: 0.01, end: 0.05, label: "skip: N above upper bound", tone: "crimson" },
			{ lane: "wait", start: 0.07, end: 2.07, label: "wait ≈ EMA block time × 1.0 ≈ 2s", tone: "amber", hatch: true },
			{ lane: "exec", start: 2.07, end: 2.42, label: "attempt 2", tone: "blue" },
			{ lane: "upsA", start: 2.09, end: 2.4, label: "round trip — block found", tone: "green" },
		],
		markers: [
			{ lane: "upsA", at: 0.05, shape: "x", tone: "crimson", label: "ErrUpstreamBlockUnavailable (retryable)" },
			{ lane: "exec", at: 0.07, shape: "flag", tone: "amber", label: "shouldRetry → reason: block_unavailable" },
			{ lane: "upsA", at: 1.8, shape: "dot", tone: "green", label: "block N lands on A (~1 block later)" },
			{ lane: "wait", at: 2.07, shape: "dot", tone: "dim", label: "cap: emptyResultMaxAttempts = 2" },
			{ lane: "upsA", at: 2.4, shape: "check", tone: "green", label: "200 OK" },
			{ lane: "client", at: 2.44, shape: "check", tone: "green", label: "response after ~1 block wait" },
		],
	},
	"Upstream 5xx": {
		duration: 0.8,
		tickEvery: 0.1,
		timeLabel: "wall time →",
		lanes: [
			{ id: "client", label: "Client", sub: "your app" },
			{ id: "exec", label: "Retry loop", sub: "network_executor.go" },
			{ id: "sweep", label: "Upstream sweep", sub: "networks.go" },
			{ id: "upsA", label: "Upstream A" },
			{ id: "upsB", label: "Upstream B" },
			{ id: "wait", label: "Backoff", sub: "ComputeBackoff" },
		],
		bars: [
			{ lane: "client", start: 0, end: 0.46, label: "one client request", tone: "blue" },
			{ lane: "exec", start: 0, end: 0.15, label: "attempt 1", tone: "blue" },
			{ lane: "sweep", start: 0.01, end: 0.15, label: "sweep continues on retryable errors", tone: "dim" },
			{ lane: "upsA", start: 0.01, end: 0.06, label: "A: 502 Bad Gateway", tone: "crimson" },
			{ lane: "upsB", start: 0.07, end: 0.14, label: "B: 502 Bad Gateway", tone: "crimson" },
			{ lane: "wait", start: 0.15, end: 0.4, label: "backoff 250ms (attempt 0 → ×1.2^0)", tone: "amber", hatch: true },
			{ lane: "exec", start: 0.4, end: 0.45, label: "attempt 2", tone: "blue" },
			{ lane: "upsA", start: 0.41, end: 0.45, label: "A: healthy now", tone: "green" },
		],
		markers: [
			{ lane: "exec", at: 0, shape: "flag", tone: "dim", label: "policy: maxAttempts 3 · delay 250ms · factor 1.2" },
			{ lane: "upsA", at: 0.06, shape: "x", tone: "crimson", label: "retryable toward network → next upstream" },
			{ lane: "upsB", at: 0.14, shape: "x", tone: "crimson", label: "pool exhausted" },
			{ lane: "exec", at: 0.15, shape: "flag", tone: "amber", label: "reason: retryable_error" },
			{ lane: "wait", at: 0.4, shape: "dot", tone: "dim", label: "a 3rd attempt would wait 300ms (×1.2)" },
			{ lane: "upsA", at: 0.45, shape: "check", tone: "green", label: "200 OK" },
			{ lane: "client", at: 0.46, shape: "check", tone: "green", label: "200 OK" },
		],
	},
	"Hedge race": {
		duration: 0.9,
		tickEvery: 0.15,
		timeLabel: "wall time →",
		lanes: [
			{ id: "client", label: "Client", sub: "your app" },
			{ id: "exec", label: "Hedge race", sub: "failsafe/hedge.go" },
			{ id: "upsA", label: "Primary → A", sub: "upstream A" },
			{ id: "upsB", label: "Hedge → B", sub: "upstream B" },
		],
		bars: [
			{ lane: "client", start: 0, end: 0.52, label: "eth_getTransactionByHash", tone: "blue" },
			{ lane: "exec", start: 0, end: 0.52, label: "race: first kept result wins", tone: "blue" },
			{ lane: "upsA", start: 0.02, end: 0.18, label: "primary leg → null", tone: "amber" },
			{ lane: "upsB", start: 0.25, end: 0.48, label: "hedge leg → data", tone: "green" },
		],
		markers: [
			{ lane: "exec", at: 0, shape: "flag", tone: "dim", label: "primary fires immediately; timer armed to p70" },
			{ lane: "upsA", at: 0.18, shape: "x", tone: "crimson", label: "keep() rejects fast empty — race continues" },
			{ lane: "exec", at: 0.25, shape: "dot", tone: "amber", label: "hedge fires (quantile 0.7 delay ≈ 250ms)" },
			{ lane: "upsB", at: 0.48, shape: "check", tone: "green", label: "keep() accepts — winner" },
			{ lane: "exec", at: 0.52, shape: "check", tone: "green", label: "winner: B → network_hedge_winner_total" },
			{ lane: "client", at: 0.52, shape: "check", tone: "green", label: "200 OK" },
		],
	},
};

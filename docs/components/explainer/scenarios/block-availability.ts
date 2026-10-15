import type { TimelineScenario } from "../TimelineRace";

/**
 * Scenario for /inside-erpc/block-availability: a tip+0 read on a 2s-block
 * chain. The fleet majority already has block N (that is how the served tip
 * became N), but the selection policy ordered this request [A, B] and both
 * are one block behind. A declares it (blockAvailability.upper
 * latestBlockMinus: 0) and is skipped — retryable, because N sits 1 block
 * from A's head, well inside maxRetryableBlockDistance (default 128); the
 * skip fires an async head poll. B declares no bounds, forwards, and its
 * node answers null for the block it hasn't ingested. The executor
 * classifies the miss as empty_result and waits one EMA block time (×1.0);
 * during the wait the chain advances and A ingests N. On retry A's gate
 * recomputes its bound from the fresh head and serves.
 * Axis: 3s of wall time.
 */
export const blockAvailabilityScenario: TimelineScenario = {
	duration: 3.0,
	tickEvery: 0.5,
	timeLabel: "wall time →",
	lanes: [
		{ id: "client", label: "Client", sub: "tip+0 read" },
		{ id: "exec", label: "Network executor", sub: "network_executor.go" },
		{ id: "upsA", label: "Upstream A", sub: "bound: latestBlockMinus 0" },
		{ id: "upsB", label: "Upstream B", sub: "no bounds configured" },
		{ id: "poller", label: "A's state poller", sub: "evm_state_poller.go" },
		{ id: "chain", label: "Chain", sub: "2s block time" },
	],
	bars: [
		{ lane: "client", start: 0.15, end: 2.9, label: "eth_getBlockByNumber(N) in flight", tone: "blue" },
		{ lane: "exec", start: 0.2, end: 0.7, label: "attempt 1 — sweep A → B", tone: "blue" },
		{ lane: "upsB", start: 0.35, end: 0.65, label: "forwarded — node hasn't ingested N", tone: "blue" },
		{ lane: "poller", start: 0.3, end: 0.9, label: "async head poll (10s budget, debounced)", tone: "dim", hatch: true },
		{ lane: "exec", start: 0.7, end: 2.7, label: "catch-up wait ≈ 1 block (EMA 2s × 1.0)", tone: "amber", hatch: true },
		{ lane: "exec", start: 2.7, end: 2.9, label: "attempt 2 — gate passes", tone: "green" },
		{ lane: "upsA", start: 2.7, end: 2.9, label: "serves block N", tone: "green" },
	],
	markers: [
		{ lane: "chain", at: 0.0, shape: "flag", tone: "dim", label: "block N produced — tip is now N" },
		{ lane: "upsA", at: 0.3, shape: "x", tone: "amber", label: "skip: N above A's bound (retryable, 1 ≤ 128)" },
		{ lane: "upsB", at: 0.65, shape: "x", tone: "amber", label: "result: null (emptyish)" },
		{ lane: "exec", at: 0.7, shape: "dot", tone: "amber", label: "retry reason: empty_result" },
		{ lane: "chain", at: 2.0, shape: "flag", tone: "dim", label: "block N+1 · A's node ingests N" },
		{ lane: "poller", at: 2.2, shape: "check", tone: "green", label: "cached head refreshed: N−1 → N" },
		{ lane: "upsA", at: 2.9, shape: "check", tone: "green", label: "200 OK · X-ERPC-Upstream: A" },
	],
};

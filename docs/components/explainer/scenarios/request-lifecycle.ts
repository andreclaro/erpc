import type { TimelineScenario } from "../TimelineRace";

/**
 * Scenario for /inside-erpc/request-lifecycle: one client request (R2) that is
 * byte-identical to a request (R1) already in flight from another client.
 * R1 leads; R2 becomes a multiplexer follower and rides R1's result home.
 * Axis: 450ms of wall time.
 */
export const requestLifecycleScenario: TimelineScenario = {
	duration: 0.45,
	tickEvery: 0.05,
	timeLabel: "wall time →",
	lanes: [
		{ id: "client", label: "Client", sub: "your app" },
		{ id: "http", label: "HTTP handler", sub: "http_server.go" },
		{ id: "mlx", label: "Multiplexer", sub: "multiplexer.go" },
		{ id: "cache", label: "Cache", sub: "evmJsonRpcCache" },
		{ id: "exec", label: "Executor", sub: "network_executor.go" },
		{ id: "upstream", label: "Upstream", sub: "alchemy" },
		{ id: "writer", label: "Async writer", sub: "networks.go:2426" },
	],
	bars: [
		{ lane: "client", start: 0.08, end: 0.4, label: "R2 in flight", tone: "blue" },
		{ lane: "http", start: 0.08, end: 0.4, label: "one goroutine per batch element", tone: "dim" },
		{ lane: "mlx", start: 0.1, end: 0.34, label: "follower: blocked on mlx.done", tone: "amber" },
		{ lane: "cache", start: 0.02, end: 0.05, label: "R1: cache GET", tone: "blue" },
		{ lane: "exec", start: 0.05, end: 0.3, label: "R1: timeout( retry( hedge( )))", tone: "blue" },
		{ lane: "upstream", start: 0.06, end: 0.28, label: "R1 → upstream round trip", tone: "green" },
		{ lane: "writer", start: 0.31, end: 0.44, label: "cache SET — fire-and-forget, 10s budget", tone: "dim", hatch: true },
	],
	markers: [
		{ lane: "mlx", at: 0.0, shape: "flag", tone: "dim", label: "R1 registered (another client)" },
		{ lane: "cache", at: 0.05, shape: "x", tone: "crimson", label: "MISS" },
		{ lane: "mlx", at: 0.1, shape: "dot", tone: "amber", label: "LoadOrStore: twin found → R2 waits" },
		{ lane: "upstream", at: 0.28, shape: "check", tone: "green", label: "replied in 34ms" },
		{ lane: "mlx", at: 0.34, shape: "check", tone: "green", label: "Close → copy + id rewritten for R2" },
		{ lane: "client", at: 0.4, shape: "check", tone: "green", label: "200 OK · X-ERPC-Upstream: alchemy" },
		{ lane: "writer", at: 0.44, shape: "dot", tone: "dim", label: "write lands after the response" },
	],
};

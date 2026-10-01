import type { TimelineScenario } from "../TimelineRace";

/**
 * Scenarios for /inside-erpc/consensus: one request fanned out to three
 * upstreams (maxParticipants: 3, agreementThreshold: 2), three outcomes.
 *
 * - "Unassailable lead": A and B return identical data; the group reaches
 *   threshold with a lead the remaining slot cannot catch, so the analyzer
 *   short-circuits and slot C is cancelled mid-flight. This tab assumes
 *   preferLargerResponses: false — with the default (true) the count-based
 *   short-circuit is disabled and the wait caps bound the tail instead
 *   (consensus/rules.go:927).
 * - "Dispute": A empty, B data-1, C data-2 — three valid groups with one
 *   vote each, so no rule resolves and the caller gets ErrConsensusDispute
 *   (HTTP 409). Dissent is logged against the plurality group, but no cordon:
 *   shouldPunishUpstream requires a group holding >50% of valid participants
 *   (consensus/executor.go:1442).
 * - "Punishment": with punishMisbehavior {disputeThreshold: 2,
 *   disputeWindow: 5m, sitOutPenalty: 10m}, repeat data dissenter C burns its
 *   token bucket across three rapid rounds (refill negligible); the third
 *   Allow() fails and C is cordoned until the sit-out timer uncordons it
 *   (consensus/executor.go:1477, :1468). Axis is compressed minutes.
 */
export const consensusScenarios: Record<string, TimelineScenario> = {
	"Unassailable lead": {
		duration: 0.05,
		tickEvery: 0.01,
		timeLabel: "wall time →",
		lanes: [
			{ id: "exec", label: "Executor", sub: "executor.Run" },
			{ id: "slotA", label: "Slot A", sub: "upstream A" },
			{ id: "slotB", label: "Slot B", sub: "upstream B" },
			{ id: "slotC", label: "Slot C", sub: "upstream C" },
			{ id: "analyzer", label: "Analyzer", sub: "runAnalyzer" },
		],
		bars: [
			{ lane: "exec", start: 0, end: 0.0385, label: "3 slots · consensus(retry(hedge(one upstream)))", tone: "blue" },
			{ lane: "slotA", start: 0.002, end: 0.028, label: "round trip · data-1", tone: "green" },
			{ lane: "slotB", start: 0.003, end: 0.034, label: "round trip · data-1", tone: "green" },
			{ lane: "slotC", start: 0.004, end: 0.038, label: "in flight when cancelled", tone: "dim" },
			{ lane: "analyzer", start: 0.043, end: 0.05, label: "scan: no dissent", tone: "dim", hatch: true },
		],
		markers: [
			{ lane: "exec", at: 0, shape: "flag", tone: "dim", label: "3 slots spawned · threshold 2" },
			{ lane: "slotA", at: 0.028, shape: "check", tone: "green", label: "A replies data-1 → hash h1" },
			{ lane: "analyzer", at: 0.028, shape: "dot", tone: "amber", label: "h1=1 < 2 — waiting" },
			{ lane: "slotB", at: 0.034, shape: "check", tone: "green", label: "B replies data-1 → hash h1" },
			{ lane: "analyzer", at: 0.0365, shape: "check", tone: "green", label: "lead 2 > remaining 1 → short-circuit" },
			{ lane: "exec", at: 0.037, shape: "check", tone: "green", label: "winner → caller at ~37ms" },
			{ lane: "slotC", at: 0.038, shape: "x", tone: "crimson", label: "context cancelled mid-flight" },
		],
	},
	Dispute: {
		duration: 0.06,
		tickEvery: 0.01,
		timeLabel: "wall time →",
		lanes: [
			{ id: "exec", label: "Executor", sub: "executor.Run" },
			{ id: "slotA", label: "Slot A", sub: "upstream A" },
			{ id: "slotB", label: "Slot B", sub: "upstream B" },
			{ id: "slotC", label: "Slot C", sub: "upstream C" },
			{ id: "analyzer", label: "Analyzer", sub: "runAnalyzer" },
		],
		bars: [
			{ lane: "exec", start: 0, end: 0.045, label: "3 slots · consensus(retry(hedge(one upstream)))", tone: "blue" },
			{ lane: "slotA", start: 0.002, end: 0.02, label: "round trip · empty", tone: "amber" },
			{ lane: "slotB", start: 0.003, end: 0.033, label: "round trip · data-1", tone: "green" },
			{ lane: "slotC", start: 0.004, end: 0.041, label: "round trip · data-2", tone: "green" },
			{ lane: "analyzer", start: 0.046, end: 0.058, label: "dissent logged · no cordon", tone: "dim", hatch: true },
		],
		markers: [
			{ lane: "exec", at: 0, shape: "flag", tone: "dim", label: "3 slots spawned · threshold 2" },
			{ lane: "slotA", at: 0.02, shape: "dot", tone: "amber", label: "A replies empty → hash e1" },
			{ lane: "analyzer", at: 0.02, shape: "dot", tone: "amber", label: "{e1}=1 — waiting" },
			{ lane: "slotB", at: 0.033, shape: "check", tone: "green", label: "B replies data-1 → hash h1" },
			{ lane: "analyzer", at: 0.033, shape: "dot", tone: "amber", label: "{e1}=1 {h1}=1 — waiting" },
			{ lane: "slotC", at: 0.041, shape: "check", tone: "green", label: "C replies data-2 → hash h2" },
			{ lane: "analyzer", at: 0.043, shape: "x", tone: "crimson", label: "1-1-1, none ≥ 2 → dispute" },
			{ lane: "exec", at: 0.045, shape: "x", tone: "crimson", label: "409 Conflict → caller" },
		],
	},
	Punishment: {
		duration: 300,
		tickEvery: 60,
		timeLabel: "compressed time →",
		lanes: [
			{ id: "rounds", label: "Consensus rounds", sub: "winner: A,B (data-1)" },
			{ id: "tracker", label: "Misbehavior scan", sub: "executor.go:1017" },
			{ id: "limiter", label: "Dispute limiter", sub: "2 tokens per 5m" },
			{ id: "cordon", label: "Upstream C", sub: "selection state" },
		],
		bars: [
			{ lane: "rounds", start: 0, end: 8, label: "round 1", tone: "blue" },
			{ lane: "rounds", start: 25, end: 33, label: "round 2", tone: "blue" },
			{ lane: "rounds", start: 55, end: 63, label: "round 3", tone: "blue" },
			{ lane: "cordon", start: 63, end: 300, label: "CORDONED — auto-uncordon after 10m sit-out", tone: "crimson", hatch: true },
		],
		markers: [
			{ lane: "rounds", at: 0, shape: "flag", tone: "dim", label: "A,B agree · C dissents — every round" },
			{ lane: "tracker", at: 0, shape: "flag", tone: "dim", label: "each dissent: tracker + limiter" },
			{ lane: "limiter", at: 8, shape: "check", tone: "green", label: "1 left" },
			{ lane: "limiter", at: 33, shape: "check", tone: "green", label: "0 left" },
			{ lane: "limiter", at: 63, shape: "x", tone: "crimson", label: "Allow() ✗ → cordon" },
			{ lane: "cordon", at: 63, shape: "flag", tone: "crimson", label: "cordon(*) — excluded from selection" },
		],
	},
};

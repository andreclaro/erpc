import React, { useEffect, useRef, useState } from "react";
import { gsap } from "gsap";

/**
 * MetricAnatomy — bespoke widget for /inside-erpc/metrics. A horizontal
 * mini-pipeline (Http → Project/Auth → Cache GET → Executor → Upstream →
 * Cache SET → Response) plays one request; as the playhead crosses each
 * stage, the exact erpc_* series that stage emits light up in monospace
 * chips and accumulate in a right-hand ledger with +1 / ×2 / observed
 * badges. Two variants: "cold" (cache miss, hedge fires, upstream race)
 * and "hit" (executor/upstream greyed out — their families are absent,
 * not zero). Replay rebuilds the timeline; reduced motion shows the
 * final ledger fully populated. Styles: styles/dd-metrics.css
 * (scoped to .cv-dd-root.dd-metricanatomy).
 */

export interface MetricAnatomyProps {
	caption?: string;
	autoPlay?: boolean;
	maxWidth?: string;
}

const W = 1200;
const H = 540;

const X0 = 26;
const PITCH = 104;
const BOX_W = 96;
const BOX_Y = 64;
const BOX_H = 46;
const cx = (i: number) => X0 + i * PITCH + BOX_W / 2;

const LEDGER_X = 786;
const LEDGER_W = 388;
const LEDGER_H = 336;
const ROW_Y = 106;
const ROW_H = 27;
const CHIP_H = 52;
const CHIP_PITCH = 58;

type Tone = "blue" | "green" | "amber" | "dim";

interface ChipDef {
	id: string;
	stage: number;
	l1: string;
	l2: string;
	tone: Tone;
	ann?: boolean;
	strike?: boolean;
}

interface RowDef {
	id: string;
	metric: string;
	detail: string;
	badge: string;
	badgeTone: "inc" | "obs" | "skip";
	ann?: boolean;
	strike?: boolean;
}

interface BeatDef {
	t: number;
	chips: string[];
	rows: string[];
	pulse?: number;
}

interface NoteDef {
	top: string;
	bottom: string;
}

interface Scenario {
	duration: number;
	chips: ChipDef[];
	rows: RowDef[];
	beats: BeatDef[];
	notes: NoteDef[];
	header: string;
}

const STAGES = [
	{ label: "HTTP", sub: "ingress" },
	{ label: "Project · Auth", sub: "permits" },
	{ label: "Cache GET", sub: "network scope" },
	{ label: "Executor", sub: "failsafe" },
	{ label: "Upstream", sub: "hedge race" },
	{ label: "Cache SET", sub: "async" },
	{ label: "Response", sub: "200 OK" },
];

// Split a metric name across two lines at the last "_" that keeps the
// first line <= 21 chars (fits the 100px chip at 8px mono).
function splitName(name: string): [string, string] {
	if (name.length <= 21) return [name, ""];
	let cut = -1;
	for (let i = 0; i <= 21 && i < name.length; i++) {
		if (name[i] === "_") cut = i;
	}
	if (cut <= 0) cut = 21;
	return [name.slice(0, cut), name.slice(cut)];
}

const COLD: Scenario = {
	duration: 13.5,
	header: "cold read · hedge fires · 11 series touched",
	chips: [
		{ id: "c-none0", stage: 0, l1: "(nothing emitted)", l2: "healthy pass · silent", tone: "dim", ann: true },
		{ id: "c-auth", stage: 1, l1: "erpc_auth_failed_total", l2: "fires only on failure", tone: "dim", ann: true },
		{ id: "c-recv", stage: 1, l1: "erpc_network_request_received_total", l2: "+1 · project scope", tone: "blue" },
		{ id: "c-miss", stage: 2, l1: "erpc_cache_get_success_miss_total", l2: 'reason="connector_miss"', tone: "blue" },
		{ id: "c-sel", stage: 3, l1: "erpc_upstream_selection_total", l2: 'reason="hedge"', tone: "blue" },
		{ id: "c-delay", stage: 3, l1: "erpc_network_hedge_delay_seconds", l2: "adaptive delay", tone: "amber" },
		{ id: "c-out", stage: 4, l1: "erpc_upstream_attempt_outcome_total", l2: "success + cancelled", tone: "blue" },
		{ id: "c-req", stage: 4, l1: "erpc_upstream_request_total", l2: "winner dial-out", tone: "blue" },
		{ id: "c-win", stage: 4, l1: "erpc_network_hedge_winner_total", l2: "upstream B kept", tone: "green" },
		{ id: "c-disc", stage: 4, l1: "erpc_network_hedge_discards_total", l2: "upstream A wasted", tone: "amber" },
		{ id: "c-set", stage: 5, l1: "erpc_cache_set_success_total", l2: "after the 200 left", tone: "green" },
		{ id: "c-ok", stage: 6, l1: "erpc_network_successful_request_total", l2: "response committed", tone: "green" },
		{ id: "c-dur", stage: 6, l1: "erpc_network_request_duration_seconds", l2: "p95 denominator", tone: "amber" },
	],
	rows: [
		{ id: "r-recv", metric: "erpc_network_request_received_total", detail: "project · network · method · finality", badge: "+1", badgeTone: "inc" },
		{ id: "r-miss", metric: "erpc_cache_get_success_miss_total", detail: 'reason="connector_miss" · cold cache', badge: "+1", badgeTone: "inc" },
		{ id: "r-sel", metric: "erpc_upstream_selection_total", detail: 'reason="hedge" · speculative fan-out', badge: "+1", badgeTone: "inc" },
		{ id: "r-delay", metric: "erpc_network_hedge_delay_seconds", detail: "adaptive delay before the hedge fired", badge: "observed", badgeTone: "obs" },
		{ id: "r-out", metric: "erpc_upstream_attempt_outcome_total", detail: "outcome=success + outcome=cancelled", badge: "×2", badgeTone: "inc" },
		{ id: "r-req", metric: "erpc_upstream_request_total", detail: "winner attempt · hedge churn excluded", badge: "+1", badgeTone: "inc" },
		{ id: "r-win", metric: "erpc_network_hedge_winner_total", detail: "the response that was kept", badge: "+1", badgeTone: "inc" },
		{ id: "r-disc", metric: "erpc_network_hedge_discards_total", detail: "wasted hedge — the tell for hedge churn", badge: "+1", badgeTone: "inc" },
		{ id: "r-ok", metric: "erpc_network_successful_request_total", detail: "200 OK committed to the client", badge: "+1", badgeTone: "inc" },
		{ id: "r-dur", metric: "erpc_network_request_duration_seconds", detail: "end-to-end · feeds p50/p95 panels", badge: "observed", badgeTone: "obs" },
		{ id: "r-set", metric: "erpc_cache_set_success_total", detail: "async goroutine · after the response", badge: "+1", badgeTone: "inc" },
	],
	beats: [
		{ t: 0.9, chips: [], rows: [], pulse: 0 },
		{ t: 2.9, chips: [], rows: [], pulse: 1 },
		{ t: 4.2, chips: ["c-recv", "c-miss"], rows: ["r-recv", "r-miss"], pulse: 2 },
		{ t: 6.4, chips: ["c-sel", "c-delay"], rows: ["r-sel", "r-delay"], pulse: 3 },
		{ t: 8.6, chips: ["c-out", "c-req"], rows: ["r-out", "r-req"], pulse: 4 },
		{ t: 9.7, chips: ["c-win", "c-disc"], rows: ["r-win", "r-disc"] },
		{ t: 11.6, chips: ["c-ok", "c-dur"], rows: ["r-ok", "r-dur"], pulse: 6 },
		{ t: 12.8, chips: ["c-set"], rows: ["r-set"], pulse: 5 },
	],
	notes: [
		{
			top: "The cancelled hedge-loser lands in attempt_outcome{cancelled} — never in",
			bottom: "upstream_request_errors_total. Heavy hedge churn hides in hedge_discards_total.",
		},
		{
			top: "The cache SET fires in a background goroutine after the response leaves —",
			bottom: "your latency never pays for bookkeeping.",
		},
	],
};

const HIT: Scenario = {
	duration: 9,
	header: "cache HIT · 4 series touched",
	chips: [
		{ id: "h-none0", stage: 0, l1: "(nothing emitted)", l2: "healthy pass · silent", tone: "dim", ann: true },
		{ id: "h-auth", stage: 1, l1: "erpc_auth_failed_total", l2: "fires only on failure", tone: "dim", ann: true },
		{ id: "h-recv", stage: 1, l1: "erpc_network_request_received_total", l2: "+1 · project scope", tone: "blue" },
		{ id: "h-hit", stage: 2, l1: "erpc_cache_get_success_hit_total", l2: "served from cache", tone: "green" },
		{ id: "h-exec", stage: 3, l1: "executor families", l2: "absent — not zero", tone: "dim", ann: true, strike: true },
		{ id: "h-ups", stage: 4, l1: "erpc_upstream_*", l2: "absent — not zero", tone: "dim", ann: true, strike: true },
		{ id: "h-ok", stage: 6, l1: "erpc_network_successful_request_total", l2: "response committed", tone: "green" },
		{ id: "h-dur", stage: 6, l1: "erpc_network_request_duration_seconds", l2: "p95 denominator", tone: "amber" },
	],
	rows: [
		{ id: "hr-recv", metric: "erpc_network_request_received_total", detail: "project · network · method · finality", badge: "+1", badgeTone: "inc" },
		{ id: "hr-hit", metric: "erpc_cache_get_success_hit_total", detail: "connector · policy · ttl", badge: "+1", badgeTone: "inc" },
		{ id: "hr-ok", metric: "erpc_network_successful_request_total", detail: "200 OK committed to the client", badge: "+1", badgeTone: "inc" },
		{ id: "hr-dur", metric: "erpc_network_request_duration_seconds", detail: "end-to-end · feeds p50/p95 panels", badge: "observed", badgeTone: "obs" },
		{ id: "hr-skip1", metric: "erpc_upstream_attempt_outcome_total", detail: "no attempt — series stays absent", badge: "absent", badgeTone: "skip", ann: true, strike: true },
		{ id: "hr-skip2", metric: "erpc_upstream_selection_total", detail: "no selection — series stays absent", badge: "absent", badgeTone: "skip", ann: true, strike: true },
	],
	beats: [
		{ t: 0.8, chips: [], rows: [], pulse: 0 },
		{ t: 2.2, chips: ["h-recv"], rows: ["hr-recv"], pulse: 1 },
		{ t: 3.8, chips: ["h-hit"], rows: ["hr-hit"], pulse: 2 },
		{ t: 6.4, chips: ["h-ok", "h-dur"], rows: ["hr-ok", "hr-dur"], pulse: 6 },
	],
	notes: [
		{
			top: "A cache HIT touches 4 series. The executor and upstream families stay silent —",
			bottom: "in Prometheus they are ABSENT, not zero; sum() across them silently drops out.",
		},
	],
};

// Chips hang below their stage box; the async SET chip sits lower to
// underline that it fires after the response beat.
const chipY = (idx: number, id: string) => {
	if (id.endsWith("-set")) return 364;
	return 136 + idx * CHIP_PITCH;
};

export function MetricAnatomy({ caption, autoPlay = true, maxWidth = "1200px" }: MetricAnatomyProps) {
	const rootRef = useRef<HTMLDivElement>(null);
	const [runId, setRunId] = useState(0);
	const [variant, setVariant] = useState<"cold" | "hit">("cold");
	const scenario = variant === "cold" ? COLD : HIT;

	useEffect(() => {
		const root = rootRef.current;
		if (!root) return;
		const RM = matchMedia("(prefers-reduced-motion: reduce)").matches;
		const one = (s: string) => root.querySelector(s) as HTMLElement | null;
		const all = (s: string) => Array.from(root.querySelectorAll(s)) as HTMLElement[];

		const chips = all("[data-chip]");
		const rows = all("[data-row]");
		const boxes = all("[data-box]");
		const ph = one("[data-playhead]");

		if (RM) {
			// Final state, no motion: everything lit, ledger fully populated.
			chips.forEach((el) => el.classList.add("lit"));
			rows.forEach((el) => el.classList.add("on"));
			if (ph) gsap.set(ph, { autoAlpha: 0 });
			return;
		}

		// Chips are CSS-transitioned via .lit; ledger rows slide in with GSAP.
		rows.forEach((el) => {
			if (el.hasAttribute("data-ann")) gsap.set(el, { autoAlpha: 0.55 });
			else gsap.set(el, { autoAlpha: 0, x: 10 });
		});

		const tl = gsap.timeline({ paused: true });
		if (ph) {
			tl.fromTo(
				ph,
				{ attr: { x1: cx(0), x2: cx(0) } },
				{ attr: { x1: cx(6), x2: cx(6) }, duration: scenario.duration, ease: "none" },
				0,
			);
		}
		scenario.beats.forEach((b) => {
			b.chips.forEach((id) => {
				const el = one(`[data-chip="${id}"]`);
				if (el) tl.call(() => el.classList.add("lit"), [], b.t);
			});
			b.rows.forEach((id) => {
				const el = one(`[data-row="${id}"]`);
				if (el) tl.to(el, { autoAlpha: 1, x: 0, duration: 0.35, ease: "power2.out" }, b.t);
			});
			if (b.pulse !== undefined && boxes[b.pulse]) {
				tl.fromTo(
					boxes[b.pulse],
					{ scale: 1 },
					{ scale: 1.07, duration: 0.16, yoyo: true, repeat: 1, ease: "power1.inOut" },
					b.t,
				);
			}
		});

		let played = !autoPlay;
		if (runId > 0) {
			tl.play(0);
			return () => tl.kill();
		}
		const io = new IntersectionObserver(
			(entries) => {
				if (played) return;
				if (entries.some((e) => e.isIntersecting)) {
					played = true;
					io.disconnect();
					tl.play(0);
				}
			},
			{ threshold: 0.25 },
		);
		io.observe(root);
		return () => {
			io.disconnect();
			tl.kill();
		};
	}, [runId, variant, autoPlay, scenario]);

	const chipCount: Record<number, number> = {};
	const noteBoxes = scenario.notes.slice(0, 2);
	const noteW = scenario.notes.length > 1 ? 560 : 1148;

	return (
		<div ref={rootRef} className="cv-dd-root dd-widget dd-metricanatomy" data-component="metric-anatomy" style={{ maxWidth }}>
			<div className="dd-head">
				<div style={{ flex: 1, minWidth: 200 }}>
					<div className="dd-wtitle">One request, eleven series</div>
					{caption && <div className="dd-cap">{caption}</div>}
				</div>
				<button
					type="button"
					className="dd-tab"
					aria-pressed={variant === "hit"}
					onClick={() => {
						setVariant((v) => (v === "cold" ? "hit" : "cold"));
						setRunId((n) => n + 1);
					}}
				>
					{variant === "cold" ? "simulate cache HIT" : "back to cold read"}
				</button>
				<button type="button" className="dd-replay" onClick={() => setRunId((n) => n + 1)} aria-label="Replay metric pass">
					↻ Replay
				</button>
			</div>
			<div className="dd-stage" aria-hidden="true">
				<div key={`${variant}-${runId}`}>
					<svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="xMidYMid meet" role="img" aria-label="Metric chips lighting up along the request pipeline">
						{/* pipeline boxes */}
						{STAGES.map((s, i) => (
							<g key={`st${i}`} data-box className={`ma-box-g${variant === "hit" && (i === 3 || i === 4) ? " greyed" : ""}`}>
								<rect className="ma-box" x={X0 + i * PITCH} y={BOX_Y} width={BOX_W} height={BOX_H} rx={8} />
								<text className="ma-box-label" x={cx(i)} y={BOX_Y + 20} textAnchor="middle">
									{s.label}
								</text>
								<text className="ma-box-sub" x={cx(i)} y={BOX_Y + 35} textAnchor="middle">
									{s.sub}
								</text>
							</g>
						))}
						{Array.from({ length: 6 }, (_, i) => (
							<text key={`ar${i}`} className="ma-arrow" x={X0 + (i + 1) * PITCH - 4} y={BOX_Y + 29} textAnchor="middle">
								→
							</text>
						))}

						{/* playhead */}
						<line data-playhead className="dd-playhead" x1={cx(0)} y1={40} x2={cx(0)} y2={336} />

						{/* metric chips under each stage */}
						{scenario.chips.map((c) => {
							const n = (chipCount[c.stage] = (chipCount[c.stage] ?? 0));
							chipCount[c.stage] += 1;
							const y = chipY(n, c.id);
							const x = X0 + c.stage * PITCH - 2;
							const [n1, n2] = splitName(c.l1);
							return (
								<g key={c.id} data-chip={c.id} data-ann={c.ann || undefined} className={`ma-chip tone-${c.tone}${c.strike ? " strike" : ""}`}>
									<rect className="ma-chip-rect" x={x} y={y} width={100} height={CHIP_H} rx={7} />
									<text className="ma-chip-l1" x={x + 50} y={n2 ? y + 17 : y + 22} textAnchor="middle">
										{n1}
									</text>
									{n2 && (
										<text className="ma-chip-l1" x={x + 50} y={y + 29} textAnchor="middle">
											{n2}
										</text>
									)}
									<text className="ma-chip-l2" x={x + 50} y={y + 43} textAnchor="middle">
										{c.l2}
									</text>
								</g>
							);
						})}

						{/* ledger */}
						<rect className="ma-ledger" x={LEDGER_X} y={BOX_Y} width={LEDGER_W} height={LEDGER_H} rx={10} />
						<text className="ma-ledger-title" x={LEDGER_X + 14} y={BOX_Y + 22}>
							LEDGER — {scenario.header}
						</text>
						{scenario.rows.map((r, i) => {
							const y = ROW_Y + i * ROW_H;
							const badgeW = r.badge === "observed" || r.badge === "absent" ? 56 : 34;
							return (
								<g key={r.id} data-row={r.id} data-ann={r.ann || undefined} className={`ma-row${r.strike ? " strike" : ""}`}>
									<text className="ma-row-metric" x={LEDGER_X + 14} y={y + 10}>
										{r.metric}
									</text>
									<text className="ma-row-detail" x={LEDGER_X + 14} y={y + 22}>
										{r.detail}
									</text>
									<rect className={`ma-badge badge-${r.badgeTone}`} x={LEDGER_X + LEDGER_W - 14 - badgeW} y={y - 2} width={badgeW} height={15} rx={7} />
									<text className="ma-badge-text" x={LEDGER_X + LEDGER_W - 14 - badgeW / 2} y={y + 9} textAnchor="middle">
										{r.badge}
									</text>
								</g>
							);
						})}

						{/* footer notes */}
						{noteBoxes.map((n, i) => (
							<g key={`n${i}`}>
								<rect className="ma-note" x={26 + i * (noteW + 28)} y={462} width={noteW} height={58} rx={9} />
								<text className="ma-note-text" x={42 + i * (noteW + 28)} y={486}>
									{n.top}
								</text>
								<text className="ma-note-text" x={42 + i * (noteW + 28)} y={503}>
									{n.bottom}
								</text>
							</g>
						))}
					</svg>
				</div>
			</div>
		</div>
	);
}

export default MetricAnatomy;

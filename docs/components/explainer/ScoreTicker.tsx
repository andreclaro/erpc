import React, { useEffect, useId, useRef, useState } from "react";
import { gsap } from "gsap";

/**
 * ScoreTicker — bespoke widget for /inside-erpc/health-and-selection.
 *
 * Story (mirrored beat-for-beat by the page's "The workflow" steps):
 *  1. Two healthy upstreams' p70 traces advance across 5 eval ticks, 15s
 *     apart (the default `selectionPolicy.evalInterval`).
 *  2. The incumbent (ups-a) degrades; the challenger (ups-b) takes the score
 *     lead at tick 15s — but the PRIMARY badge does NOT flip: the shaded
 *     ±30% hysteresis band (stickyPrimary) still contains the challenger.
 *  3. At tick 45s the challenger's 1.69× score advantage clears the band and
 *     the badge flips.
 *  4. Below, a third upstream (ups-c) is excluded on an error-rate spike,
 *     accumulates shadow-probe samples (probeExcluded), and is re-admitted —
 *     no timer, the exclusion predicate simply stops tripping.
 *
 * The band geometry is computed from the real formula, not eyeballed:
 * score = 1 / (1 + Σ metricᵢ × weightᵢ) with PREFER_FASTEST's respLatency
 * weight 15 (internal/policy/stdlib/stdlib.js PRESETS); ups-a/ups-b are
 * healthy so the latency term dominates. stickyPrimary flips only when
 * challenger.score > incumbent.score × (1 + 0.30) (default_policy.js).
 *
 * SSR-safe: the SVG renders its final state statically; GSAP hydrates motion
 * in useEffect (clip-rect reveal + playhead, pops, badge spring). Replay
 * rebuilds the timeline; prefers-reduced-motion shows the final state.
 * Shell classes come from styles/deep-dive.css (.cv-dd-root dd-widget).
 */

export interface ScoreTickerProps {
	autoPlay?: boolean;
	maxWidth?: string;
	caption?: string;
}

const W = 1200;
const LABEL_W = 182;
const PAD_R = 130; // room for the trace end labels
const TRACK_W = W - LABEL_W - PAD_R;
const H = 385;

// Axis: 5 eval ticks, 15s apart → 60s of wall time.
const AXIS_S = 60;
const TICKS = [0, 15, 30, 45, 60];
// Chart: p70 milliseconds, 0..500ms mapped onto y 240..50.
const CHART_TOP = 50;
const CHART_BOT = 240;
const Y_MAX_MS = 500;
// ups-c lane + legend.
const LANE_Y = 310;
const LEGEND_Y = 368;

// p70 traces (ms). ups-a is the incumbent and degrades; ups-b is steady.
const A_P70 = [120, 160, 210, 300, 330];
const B_P70 = [140, 140, 150, 150, 145];

// PREFER_FASTEST respLatency weight (stdlib.js:641) and the default policy's
// stickyPrimary hysteresis (default_policy.js:54).
const W_LAT = 15;
const HYST = 0.3;

const scoreOf = (p70ms: number) => 1 / (1 + (W_LAT * p70ms) / 1000);
// Challenger flip threshold on the p70 axis: score(ch) > score(inc) × (1+h)
// ⟺ p70(ch) < (W_LAT·p70(inc) − h·1000) / (W_LAT·(1+h)).
const flipThreshold = (incMs: number) => (W_LAT * incMs - HYST * 1000) / (W_LAT * (1 + HYST));

const x = (t: number) => LABEL_W + (t / AXIS_S) * TRACK_W;
const yMs = (v: number) => CHART_BOT - (v / Y_MAX_MS) * (CHART_BOT - CHART_TOP);

// Real-time pacing: 60s of axis time plays in 9.6s.
const PLAY_S = 9.6;
const K = PLAY_S / AXIS_S;
const FLIP_TICK = 45;

const BLUE = "#60a5fa";
const GREEN = "#34d399";
const AMBER = "#fbbf24";
const CRIMSON = "#f87171";
const TXT = "rgba(255,255,255,0.85)";
const TXT_DIM = "rgba(255,255,255,0.50)";
const TXT_MONO = "rgba(255,255,255,0.45)";
const MONO = '500 10.5px/1 "JetBrains Mono", ui-monospace, SF Mono, Menlo, monospace';
const SANS = "500 11px/1 Inter, system-ui, sans-serif";

/** Step-after path through (tick, value) points: flat until the next tick, then a vertical jump. */
function stepPath(values: number[], yFn: (v: number) => number, fromIdx = 0, toIdx = TICKS.length - 1): string {
	let d = `M${x(TICKS[fromIdx])} ${yFn(values[fromIdx])}`;
	for (let i = fromIdx + 1; i <= toIdx; i++) {
		d += ` L${x(TICKS[i])} ${yFn(values[i - 1])} L${x(TICKS[i])} ${yFn(values[i])}`;
	}
	return d;
}

/** Closed band polygon between an upper and a lower step trace (same tick range). */
function bandPath(upper: number[], lower: number[], fromIdx: number, toIdx: number): string {
	let d = stepPath(upper, yMs, fromIdx, toIdx);
	for (let i = toIdx; i > fromIdx; i--) {
		d += ` L${x(TICKS[i])} ${yMs(lower[i])} L${x(TICKS[i - 1])} ${yMs(lower[i])}`;
	}
	return `${d} L${x(TICKS[fromIdx])} ${yMs(lower[fromIdx])} Z`;
}

export function ScoreTicker({ autoPlay = true, maxWidth = "1200px", caption }: ScoreTickerProps) {
	const rootRef = useRef<HTMLDivElement>(null);
	const [runId, setRunId] = useState(0);
	const uid = useId().replace(/[^a-zA-Z0-9]/g, "");

	useEffect(() => {
		const root = rootRef.current;
		if (!root) return;
		const RM = matchMedia("(prefers-reduced-motion: reduce)").matches;
		const clip = root.querySelector("[data-clip-rect]");
		const ph = root.querySelector("[data-playhead]");
		const pops = root.querySelectorAll("[data-pop]");
		const fades = root.querySelectorAll("[data-fade]");
		const badgeA = root.querySelector("[data-badge-a]");
		const badgeB = root.querySelector("[data-badge-b]");

		if (RM) {
			gsap.set(clip, { attr: { width: TRACK_W } });
			gsap.set([pops, fades], { autoAlpha: 1, scale: 1 });
			gsap.set(badgeA, { autoAlpha: 0 });
			gsap.set(badgeB, { autoAlpha: 1, scale: 1 });
			gsap.set(ph, { autoAlpha: 0 });
			return;
		}

		gsap.set(clip, { attr: { width: 0 } });
		gsap.set(pops, { autoAlpha: 0, scale: 0.3, transformOrigin: "50% 50%" });
		gsap.set(fades, { autoAlpha: 0 });
		gsap.set(badgeB, { autoAlpha: 0, scale: 0.6, transformOrigin: "50% 50%" });
		gsap.set(ph, { autoAlpha: 1, x: 0 });

		const tl = gsap.timeline({ paused: true });
		tl.to(ph, { x: TRACK_W, duration: PLAY_S, ease: "none" }, 0);
		tl.to(clip, { attr: { width: TRACK_W }, duration: PLAY_S, ease: "none" }, 0);
		pops.forEach((el) => {
			const at = Number.parseFloat(el.getAttribute("data-at") ?? "0");
			tl.to(el, { autoAlpha: 1, scale: 1, duration: 0.4, ease: "back.out(2.2)" }, at * K);
		});
		fades.forEach((el) => {
			const at = Number.parseFloat(el.getAttribute("data-at") ?? "0");
			tl.to(el, { autoAlpha: 1, duration: 0.35 }, at * K);
		});
		// The badge flips only when the challenger clears the hysteresis band.
		tl.to(badgeA, { autoAlpha: 0, scale: 0.6, duration: 0.25 }, FLIP_TICK * K);
		tl.to(badgeB, { autoAlpha: 1, scale: 1, duration: 0.5, ease: "back.out(2)" }, FLIP_TICK * K + 0.08);

		let played = !autoPlay;
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
	}, [runId, autoPlay]);

	// Band geometry, derived from the traces + the real score formula.
	const aThreshold = A_P70.map(flipThreshold);
	const bThreshold = B_P70.map(flipThreshold);
	const ratio = (i: number) => scoreOf(B_P70[i]) / scoreOf(A_P70[i]);

	const popBox: React.CSSProperties = { transformBox: "fill-box" };

	return (
		<div ref={rootRef} className="cv-dd-root dd-widget" data-component="score-ticker" style={{ maxWidth }}>
			<div className="dd-head">
				{caption && <div className="dd-cap">{caption}</div>}
				<button type="button" className="dd-replay" onClick={() => setRunId((n) => n + 1)} aria-label="Replay scoring animation">
					↻ Replay
				</button>
			</div>
			<div className="dd-stage" aria-hidden="true">
				<svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="xMidYMid meet" role="img" aria-label="Upstream scores across five 15-second policy evaluations">
					<defs>
						<clipPath id={`stc-clip-${uid}`}>
							<rect data-clip-rect x={LABEL_W} y={0} width={TRACK_W} height={H} />
						</clipPath>
						<pattern id={`stc-hatch-${uid}`} width="7" height="7" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
							<rect width="7" height="7" className="dd-hatch-bg" />
							<line x1="0" y1="0" x2="0" y2="7" className="dd-hatch-line" />
						</pattern>
					</defs>

					{/* ── static grid ── */}
					{TICKS.slice(1).map((t) => (
						<line key={`g${t}`} x1={x(t)} y1={CHART_TOP} x2={x(t)} y2={CHART_BOT} stroke="rgba(148,163,184,0.18)" strokeWidth={1} strokeDasharray="3 4" />
					))}
					{[0, 250, 500].map((v) => (
						<g key={`y${v}`}>
							<line x1={LABEL_W} y1={yMs(v)} x2={W - PAD_R} y2={yMs(v)} stroke={v === 0 ? "rgba(148,163,184,0.35)" : "rgba(148,163,184,0.12)"} strokeWidth={1} />
							<text x={LABEL_W - 8} y={yMs(v) + 3.5} textAnchor="end" fill={TXT_MONO} font={MONO}>
								{v}ms
							</text>
						</g>
					))}
					{TICKS.map((t) => (
						<text key={`x${t}`} x={x(t)} y={CHART_BOT + 17} textAnchor="middle" fill={TXT_MONO} font={MONO}>
							{t}s
						</text>
					))}
					<text x={W - PAD_R} y={CHART_BOT + 33} textAnchor="end" fill={TXT_DIM} font={SANS}>
						eval ticks · 15s apart (selectionPolicy.evalInterval)
					</text>
					<text x={W - PAD_R} y={40} textAnchor="end" fill={TXT_DIM} font={SANS}>
						p70 latency · rolling 1m window (scoreMetricsWindowSize)
					</text>

					{/* ── clip-revealed layer: bands, traces, tick dots, ups-c bars ── */}
					<g clipPath={`url(#stc-clip-${uid})`}>
						{/* hysteresis band around the incumbent: pre-flip ups-a, post-flip ups-b */}
						<path d={bandPath(A_P70, aThreshold, 0, 3)} fill="rgba(96,165,250,0.10)" stroke="rgba(96,165,250,0.30)" strokeWidth={1} strokeDasharray="4 3" />
						<path d={bandPath(bThreshold, B_P70, 3, 4)} fill="rgba(52,211,153,0.10)" stroke="rgba(52,211,153,0.30)" strokeWidth={1} strokeDasharray="4 3" />
						{/* p70 traces */}
						<path d={stepPath(A_P70, yMs)} fill="none" stroke={BLUE} strokeWidth={2.5} strokeLinejoin="round" />
						<path d={stepPath(B_P70, yMs)} fill="none" stroke={GREEN} strokeWidth={2.5} strokeLinejoin="round" />
						{TICKS.map((t, i) => (
							<g key={`d${t}`}>
								<circle cx={x(t)} cy={yMs(A_P70[i])} r={3.2} fill={BLUE} stroke="#0a0e1a" strokeWidth={1.5} />
								<circle cx={x(t)} cy={yMs(B_P70[i])} r={3.2} fill={GREEN} stroke="#0a0e1a" strokeWidth={1.5} />
							</g>
						))}
						{/* ups-c lane bars */}
						<rect x={x(0) + 2} y={LANE_Y - 11} width={x(15) - x(0) - 4} height={22} rx={6} fill="rgba(148,163,184,0.14)" stroke="rgba(148,163,184,0.4)" strokeWidth={1} />
						<text x={(x(0) + x(15)) / 2} y={LANE_Y + 4} textAnchor="middle" fill={TXT} font={SANS}>
							in rotation
						</text>
						<rect x={x(15) + 2} y={LANE_Y - 11} width={x(45) - x(15) - 4} height={22} rx={6} fill={`url(#stc-hatch-${uid})`} stroke="rgba(248,113,113,0.7)" strokeWidth={1} />
						<text x={(x(15) + x(45)) / 2} y={LANE_Y + 4} textAnchor="middle" fill={TXT} font={SANS}>
							excluded — position −1 · real traffic stops
						</text>
						<rect x={x(45) + 2} y={LANE_Y - 11} width={x(60) - x(45) - 4} height={22} rx={6} fill="rgba(52,211,153,0.18)" stroke="rgba(52,211,153,0.55)" strokeWidth={1} />
						<text x={(x(45) + x(60)) / 2} y={LANE_Y + 4} textAnchor="middle" fill={TXT} font={SANS}>
							back in rotation
						</text>
					</g>

					{/* ── tick annotations (fade at their tick) ── */}
					<text data-fade data-at={15.4} x={x(15)} y={70} textAnchor="middle" fill={TXT_DIM} font={MONO}>
						{`lines cross — no flip: ${ratio(1).toFixed(2)}× < 1.30× band`}
					</text>
					<text data-fade data-at={30.4} x={x(30)} y={88} textAnchor="middle" fill={TXT_DIM} font={MONO}>
						{`cooldown cleared · ${ratio(2).toFixed(2)}× < 1.30× — held`}
					</text>
					<text data-fade data-at={45.4} x={x(45)} y={70} textAnchor="middle" fill="#8fe6c3" font={MONO}>
						{`${ratio(3).toFixed(2)}× clears the band → badge flips`}
					</text>
					<text data-fade data-at={60.2} x={x(60)} y={88} textAnchor="end" fill={TXT_DIM} font={MONO}>
						ups-b holds — ups-a needs 1.30× to reclaim
					</text>

					{/* ── trace end labels ── */}
					<text data-fade data-at={59.6} x={x(60) + 8} y={yMs(A_P70[4]) + 4} fill="#9cc3ff" font={MONO}>
						{`ups-a p70 · ${A_P70[4]}ms`}
					</text>
					<text data-fade data-at={59.6} x={x(60) + 8} y={yMs(B_P70[4]) + 4} fill="#8fe6c3" font={MONO}>
						{`ups-b p70 · ${B_P70[4]}ms`}
					</text>

					{/* ── ups-c lane: labels, markers, probe dots ── */}
					<text x={14} y={LANE_Y - 3} fill={TXT} font="600 12.5px/1 Inter, system-ui, sans-serif">
						ups-c
					</text>
					<text x={14} y={LANE_Y + 13} fill={TXT_MONO} font={MONO}>
						prober recovery
					</text>
					<g data-pop data-at={15.2} style={popBox}>
						<text x={x(15)} y={LANE_Y + 5} textAnchor="middle" fill={CRIMSON} font="700 15px/1 Inter, system-ui, sans-serif">
							✕
						</text>
					</g>
					<text data-fade data-at={15.5} x={x(15)} y={LANE_Y - 22} textAnchor="middle" fill="#fca5a5" font={MONO}>
						errorRate 0.82 &gt; 0.70 (≥10 samples) → excluded
					</text>
					{[18, 21, 25, 29, 33, 37, 41, 44].map((t) => (
						<circle key={`p${t}`} data-pop data-at={t} style={popBox} cx={x(t)} cy={LANE_Y} r={3.2} fill={AMBER} />
					))}
					<text data-fade data-at={24} x={560} y={LANE_Y + 34} textAnchor="middle" fill="#fcd34d" font={MONO}>
						shadow probes: ~10% of traffic + minSamples floor of 10
					</text>
					<g data-pop data-at={45.2} style={popBox}>
						<text x={x(45)} y={LANE_Y + 5} textAnchor="middle" fill={GREEN} font="700 15px/1 Inter, system-ui, sans-serif">
							✓
						</text>
					</g>
					<text data-fade data-at={45.5} x={x(45)} y={LANE_Y - 22} textAnchor="middle" fill="#8fe6c3" font={MONO}>
						fresh samples clear the gate → re-admitted (no timer)
					</text>

					{/* ── primary badge (flips at tick 45s) ── */}
					<g data-badge-a style={popBox}>
						<rect x={192} y={16} width={170} height={26} rx={13} fill="rgba(96,165,250,0.14)" stroke={BLUE} strokeWidth={1} />
						<text x={277} y={33} textAnchor="middle" fill="#cfe3ff" font="600 12px/1 Inter, system-ui, sans-serif">
							PRIMARY ▸ ups-a
						</text>
					</g>
					<g data-badge-b style={{ ...popBox, opacity: 0 }}>
						<rect x={192} y={16} width={170} height={26} rx={13} fill="rgba(52,211,153,0.14)" stroke={GREEN} strokeWidth={1} />
						<text x={277} y={33} textAnchor="middle" fill="#d7f5e8" font="600 12px/1 Inter, system-ui, sans-serif">
							PRIMARY ▸ ups-b
						</text>
					</g>

					{/* ── legend ── */}
					<line x1={192} y1={LEGEND_Y - 3} x2={208} y2={LEGEND_Y - 3} stroke={BLUE} strokeWidth={2.5} />
					<text x={214} y={LEGEND_Y} fill={TXT_DIM} font={MONO}>
						ups-a p70 (incumbent)
					</text>
					<line x1={352} y1={LEGEND_Y - 3} x2={368} y2={LEGEND_Y - 3} stroke={GREEN} strokeWidth={2.5} />
					<text x={374} y={LEGEND_Y} fill={TXT_DIM} font={MONO}>
						ups-b p70
					</text>
					<rect x={452} y={LEGEND_Y - 9} width={16} height={10} fill="rgba(96,165,250,0.10)" stroke="rgba(148,163,184,0.5)" strokeWidth={1} strokeDasharray="3 2" />
					<text x={474} y={LEGEND_Y} fill={TXT_DIM} font={MONO}>
						±30% score band
					</text>
					<circle cx={588} cy={LEGEND_Y - 3.5} r={3.2} fill={AMBER} />
					<text x={598} y={LEGEND_Y} fill={TXT_DIM} font={MONO}>
						shadow probe
					</text>
					<rect x={686} y={LEGEND_Y - 9} width={16} height={10} fill={`url(#stc-hatch-${uid})`} stroke="rgba(248,113,113,0.7)" strokeWidth={1} />
					<text x={708} y={LEGEND_Y} fill={TXT_DIM} font={MONO}>
						excluded (position −1)
					</text>

					{/* ── playhead ── */}
					<line data-playhead className="dd-playhead" x1={x(0)} y1={CHART_TOP - 4} x2={x(0)} y2={LANE_Y + 20} />
				</svg>
			</div>
		</div>
	);
}

export default ScoreTicker;

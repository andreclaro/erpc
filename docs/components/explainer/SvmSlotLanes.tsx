import React, { useEffect, useRef, useState } from "react";
import { gsap } from "gsap";

/**
 * SvmSlotLanes — bespoke widget for /inside-erpc/architecture-svm. Three beats:
 *
 *  1. The poller: three upstream lanes whose processed-tip counters advance at
 *     different rates on 400ms ticks; a poller chip hops lane to lane firing
 *     the four per-tick RPC dots (getHealth only every 5th tick); two distinct
 *     aggregate lines — the MAJORITY served tip vs the pool MAX used only by
 *     rejection gates.
 *  2. One finalized getBlock: commitment injection stamp → slot-lag filter
 *     crossing a lagging upstream → the availability gate answering -32014 on
 *     one upstream → reroute to a fresh upstream that serves and harvests
 *     context.slot → the poller's traffic gate skips the next getSlot poll.
 *  3. The cache key: the EVM hasher would collapse case-variant base58 pubkeys
 *     onto one key; svmRequestKey preserves case, and the partition key is
 *     <networkId>:<slotRef> with slotRef = minContextSlot or "*".
 *
 * SSR-safe: the SVG renders statically (initial state); GSAP hydrates motion
 * in useEffect. The stage remounts on Replay so text swaps reset for free.
 * prefers-reduced-motion shows the final state with no animation.
 * Styles: styles/dd-svm.css (scoped to .cv-dd-root.dd-svmlanes).
 */

export interface SvmSlotLanesProps {
	caption?: string;
	autoPlay?: boolean;
	maxWidth?: string;
}

const W = 1200;
const H = 680;

// ── Beat 1 geometry ─────────────────────────────────────────────────────────
const TICK_X0 = 190;
const TICK_PITCH = 155;
const TICK_N = 6;
const tickX = (k: number) => TICK_X0 + k * TICK_PITCH;

const LANE_Y = [96, 146, 196];
const LANE_ID = ["ups-a", "ups-b", "ups-c"];
const LANE_SUB = ["vendor RPC · steady", "regional node · slower", "fast endpoint · runs ahead"];
const BASE = 93728400;
// processed-tip per tick: a +1/tick, b +1 every other tick, c sprints ahead.
const LANE_SLOTS = [
	[70, 71, 72, 73, 74, 75],
	[70, 71, 71, 72, 72, 73],
	[70, 71, 73, 76, 80, 85],
];
// poller visits one lane per tick: a,b,c,a,b,c. getHealth fires on tick 4.
const POLLER_VISIT = [0, 1, 2, 0, 1, 2];
const HEALTH_TICK = 4;

const MAJORITY_TIP = BASE + 75;
const MAX_TIP = BASE + 85;

// ── Beat 2 geometry ─────────────────────────────────────────────────────────
const B2_Y = 352;
const B2_W = 178;
const B2_H = 84;
const B2_PITCH = 196;
const B2_X0 = 24;
const b2x = (i: number) => B2_X0 + i * B2_PITCH;

const B2_STEPS: { step: string; l1: string; l2: string; sub: string; tone: string }[] = [
	{ step: "1 · request", l1: "getBlock(93728460,", l2: '{commitment:"finalized"})', sub: "arrives · network pre-forward", tone: "blue" },
	{ step: "2 · injection stamp", l1: "commitment stamped", l2: "CacheHash invalidated", sub: "hooks.go:188 · project layer", tone: "blue" },
	{ step: "3 · slot-lag filter", l1: "ups-c root −130", l2: "lag > 100 → excluded", sub: "slot_lag.go:32 · fails open", tone: "crimson" },
	{ step: "4 · availability gate", l1: "93728460 > tip + 3", l2: "-32014 missing data", sub: "hooks.go:542 · ups-b indexed low", tone: "crimson" },
	{ step: "5 · reroute", l1: "ups-a serves block", l2: "context.slot harvested", sub: "hooks.go:700 · ups-a", tone: "green" },
	{ step: "6 · poller skip", l1: "getSlot calls skip", l2: "traffic gate 1 / 4", sub: "max 4 consecutive skips", tone: "amber" },
];

// ── Beat 3 geometry ─────────────────────────────────────────────────────────
const B3_IN_W = 150;
const B3_KEY_W = 262;

export function SvmSlotLanes({ caption, autoPlay = true, maxWidth = "1200px" }: SvmSlotLanesProps) {
	const rootRef = useRef<HTMLDivElement>(null);
	const [runId, setRunId] = useState(0);

	useEffect(() => {
		const root = rootRef.current;
		if (!root) return;
		const RM = matchMedia("(prefers-reduced-motion: reduce)").matches;
		const one = (s: string) => root.querySelector(s);
		const all = (s: string) => root.querySelectorAll(s);

		const cells = all("[data-cell]");
		const dots = all("[data-dots]");
		const poller = one("[data-poller]");
		const playhead = one("[data-playhead]");
		const tipG = one("[data-tip-g]");
		const tipA = one("[data-tip-a]");
		const tipLabels = all("[data-tip-label]");
		const b2 = all("[data-b2]");
		const b2Arrows = all("[data-b2-arrow]");
		const cross = one("[data-cross]");
		const b3 = all("[data-b3]");
		const b3Arrows = all("[data-b3-arrow]");

		if (RM) {
			// Final state, no motion: all counters shown, lines drawn, beats 2-3 up.
			gsap.set([cells, dots, b2, b3, tipLabels], { autoAlpha: 1, x: 0, y: 0, scale: 1 });
			gsap.set([b2Arrows, b3Arrows], { autoAlpha: 1 });
			gsap.set([tipG, tipA, cross], { autoAlpha: 1, scaleX: 1 });
			gsap.set(poller, { x: tickX(TICK_N - 1), y: LANE_Y[POLLER_VISIT[TICK_N - 1]] - 34 });
			if (playhead) gsap.set(playhead, { x: tickX(TICK_N - 1) });
			return;
		}

		// Initial states (stage remounts on replay, so texts are already fresh).
		gsap.set(cells, { autoAlpha: 0, scale: 0.6 });
		gsap.set(dots, { autoAlpha: 0, scale: 0.4 });
		gsap.set([tipG, tipA], { autoAlpha: 0, scaleX: 0, transformOrigin: "left center" });
		gsap.set(tipLabels, { autoAlpha: 0, y: 8 });
		gsap.set(b2, { autoAlpha: 0, y: 14 });
		gsap.set(b2Arrows, { autoAlpha: 0 });
		gsap.set(cross, { autoAlpha: 0, scaleX: 0, transformOrigin: "center center" });
		gsap.set(b3, { autoAlpha: 0, y: 12 });
		gsap.set(b3Arrows, { autoAlpha: 0 });
		gsap.set(poller, { x: tickX(0), y: LANE_Y[POLLER_VISIT[0]] - 34, autoAlpha: 0, scale: 0.5 });
		if (playhead) gsap.set(playhead, { x: tickX(0), autoAlpha: 0 });

		const tl = gsap.timeline({ paused: true });

		// Beat 1 intro: lanes + tick ruler.
		tl.to(all("[data-b1-base]"), { autoAlpha: 1, duration: 0.3 }, 0);
		tl.to(poller, { autoAlpha: 1, scale: 1, duration: 0.3, ease: "back.out(2)" }, 0.15);
		if (playhead) tl.to(playhead, { autoAlpha: 1, duration: 0.2 }, 0.15);

		// Six 400ms ticks: counters bump, poller hops, RPC dots flash.
		for (let k = 0; k < TICK_N; k++) {
			const t0 = 0.5 + k * 0.62;
			const at = `[data-cell][data-tick='${k}']`;
			tl.to(root.querySelectorAll(at), { autoAlpha: 1, scale: 1, duration: 0.22, stagger: 0.07, ease: "back.out(2.5)" }, t0);
			tl.to(poller, { x: tickX(k), y: LANE_Y[POLLER_VISIT[k]] - 34, duration: 0.3, ease: "power2.inOut" }, t0);
			const d = root.querySelector(`[data-dots='${k}']`);
			if (d) {
				tl.to(d, { autoAlpha: 1, scale: 1, duration: 0.12, ease: "back.out(3)" }, t0 + 0.28);
				tl.to(d, { autoAlpha: 0.25, duration: 0.25 }, t0 + 0.75);
			}
			if (playhead) tl.to(playhead, { x: tickX(k), duration: 0.3, ease: "power1.inOut" }, t0);
		}
		// getHealth only every 5th poll.
		const hd = one("[data-dots-health]");
		if (hd) {
			tl.to(hd, { autoAlpha: 1, scale: 1, duration: 0.15, ease: "back.out(3)" }, 0.5 + HEALTH_TICK * 0.62 + 0.30);
			tl.to(hd, { autoAlpha: 0.9, duration: 0.25 }, 0.5 + HEALTH_TICK * 0.62 + 0.8);
		}

		// The SVM signature: majority line vs pool-max line.
		const tTip = 0.5 + TICK_N * 0.62 + 0.25;
		tl.to(tipG, { autoAlpha: 1, scaleX: 1, duration: 0.5, ease: "power2.out" }, tTip);
		tl.to(tipA, { autoAlpha: 1, scaleX: 1, duration: 0.5, ease: "power2.out" }, tTip + 0.35);
		tl.to(tipLabels, { autoAlpha: 1, y: 0, duration: 0.3, stagger: 0.15 }, tTip + 0.2);

		// Beat 2: one finalized getBlock through the gates.
		const tB2 = tTip + 1.1;
		for (let i = 0; i < B2_STEPS.length; i++) {
			tl.to(b2[i], { autoAlpha: 1, y: 0, duration: 0.35, ease: "power2.out" }, tB2 + i * 0.62);
			if (i > 0) tl.to(b2Arrows[i - 1], { autoAlpha: 1, duration: 0.2 }, tB2 + i * 0.62 - 0.12);
		}
		tl.to(cross, { autoAlpha: 1, scaleX: 1, duration: 0.25, ease: "power2.out" }, tB2 + 2 * 0.62 + 0.25);

		// Beat 3: the case-sensitive cache key.
		const tB3 = tB2 + B2_STEPS.length * 0.62 + 0.6;
		tl.to(b3[0], { autoAlpha: 1, y: 0, duration: 0.35 }, tB3); // EVM hasher label + inputs
		tl.to(b3Arrows, { autoAlpha: 1, duration: 0.25, stagger: 0.15 }, tB3 + 0.3);
		tl.to(b3[1], { autoAlpha: 1, y: 0, duration: 0.35 }, tB3 + 0.55); // collapsed (wrong) chip
		tl.to(b3[2], { autoAlpha: 1, y: 0, duration: 0.35 }, tB3 + 0.9); // SVM label
		tl.to([b3[3], b3[4]], { autoAlpha: 1, y: 0, duration: 0.3, stagger: 0.2, ease: "back.out(1.8)" }, tB3 + 1.15);
		tl.to(b3[5], { autoAlpha: 1, y: 0, duration: 0.35 }, tB3 + 1.75); // partition keys

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
			{ threshold: 0.2 },
		);
		io.observe(root);
		return () => {
			io.disconnect();
			tl.kill();
		};
	}, [runId, autoPlay]);

	return (
		<div ref={rootRef} className="cv-dd-root dd-widget dd-svmlanes" data-component="svm-slot-lanes" style={{ maxWidth }}>
			<div className="dd-head">
				<div style={{ flex: 1, minWidth: 200 }}>
					<div className="sl-wtitle">Three upstreams, two tips, one case-sensitive key</div>
					{caption && <div className="dd-cap">{caption}</div>}
				</div>
				<button type="button" className="dd-replay" onClick={() => setRunId((n) => n + 1)} aria-label="Replay SVM slot animation">
					↻ Replay
				</button>
			</div>
			<div className="dd-stage" aria-hidden="true">
				<div key={runId}>
					<svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="xMidYMid meet" role="img" aria-label="Animated SVM slot poller, finalized getBlock journey, and cache key">
						{/* ═══ Beat 1 · the poller ═══ */}
						<text className="sl-beat" x={24} y={30}>
							beat 1 · the poller — four RPCs per upstream every 400 ms tick
						</text>

						{/* tick ruler */}
						<g data-b1-base style={{ opacity: 0 }}>
							{Array.from({ length: TICK_N }, (_, k) => (
								<g key={`tick${k}`}>
									<line className="sl-tick" x1={tickX(k)} y1={56} x2={tickX(k)} y2={212} />
									<text className="dd-tm" x={tickX(k)} y={52} textAnchor="middle">
										{`+${k * 400}ms`}
									</text>
								</g>
							))}
							<text className="dd-ts" x={24} y={52}>
								poller ticks (debounce gate)
							</text>
							{/* poller visit dots: 3 per-tick RPCs + getHealth on the 5th poll */}
							{POLLER_VISIT.map((lane, k) => (
								<g key={`dots${k}`} data-dots={k} style={{ opacity: 0 }}>
									{[0, 1, 2].map((d) => (
										<circle key={d} className="sl-dot-b" cx={tickX(k) - 18 + d * 12} cy={LANE_Y[lane] + 32} r={3.2} />
									))}
									{k === HEALTH_TICK && (
										<g data-dots-health style={{ opacity: 0 }}>
											<circle className="sl-dot-a" cx={tickX(k) + 24} cy={LANE_Y[lane] + 32} r={3.2} />
											<text className="sl-health-label" x={tickX(k) + 34} y={LANE_Y[lane] + 35}>
												getHealth · 5th tick
											</text>
										</g>
									)}
								</g>
							))}
							<text className="sl-legend-b" x={TICK_X0} y={246}>
								●●● = getSlot(processed) · getSlot(finalized) · getMaxShredInsertSlot — every tick
							</text>
							<text className="sl-legend-a" x={TICK_X0} y={258}>
								● = getHealth — every 5th tick only
							</text>
						</g>
						{LANE_ID.map((id, li) => (
							<g key={id} data-b1-base style={{ opacity: 0 }}>
								<text className="sl-lane-label" x={24} y={LANE_Y[li] - 4}>
									{id}
								</text>
								<text className="dd-tm" x={24} y={LANE_Y[li] + 10}>
									{LANE_SUB[li]}
								</text>
								{Array.from({ length: TICK_N }, (_, k) => (
									<g key={`c${li}-${k}`} data-cell data-tick={k} style={{ opacity: 0 }}>
										<rect className="sl-cell" x={tickX(k) - 52} y={LANE_Y[li] - 15} width={104} height={30} rx={7} />
										<text className="sl-cell-num" x={tickX(k)} y={LANE_Y[li] + 4} textAnchor="middle">
											{BASE + LANE_SLOTS[li][k]}
										</text>
									</g>
								))}
							</g>
						))}

						{/* poller chip (hops lane to lane) */}
						<g data-poller style={{ opacity: 0 }}>
							<circle className="sl-poller" cx={0} cy={0} r={11} />
							<text className="sl-poller-glyph" x={0} y={4} textAnchor="middle">
								P
							</text>
						</g>

						{/* playhead */}
						<g data-playhead style={{ opacity: 0 }}>
							<line className="sl-playhead" x1={0} y1={60} x2={0} y2={216} />
						</g>

						{/* majority vs max tip lines */}
						<g>
							<line data-tip-g className="sl-tip-g" x1={TICK_X0 - 60} y1={286} x2={TICK_X0 + 5 * TICK_PITCH + 40} y2={286} />
							<line data-tip-a className="sl-tip-a" x1={TICK_X0 - 60} y1={312} x2={TICK_X0 + 5 * TICK_PITCH + 40} y2={312} />
							<text data-tip-label className="sl-tip-label g" x={TICK_X0 - 60} y={280}>
								{`served tip = MAJORITY ${MAJORITY_TIP} — what we advertise (PickServedTip)`}
							</text>
							<text data-tip-label className="sl-tip-label a" x={TICK_X0 - 60} y={304}>
								{`pool MAX ${MAX_TIP} — rejection gates only, never advertised`}
							</text>
						</g>

						{/* ═══ Beat 2 · one finalized getBlock ═══ */}
						<text className="sl-beat" x={24} y={B2_Y - 18}>
							beat 2 · one finalized getBlock(93728460) through the gates
						</text>
						{B2_STEPS.map((s, i) => (
							<g key={`b2-${i}`}>
								<g data-b2 style={{ opacity: 0 }}>
									<rect className={`sl-chip ${s.tone}`} x={b2x(i)} y={B2_Y} width={B2_W} height={B2_H} rx={9} />
									<text className="sl-chip-step" x={b2x(i) + 12} y={B2_Y + 17}>
										{s.step}
									</text>
									<text className="sl-chip-mono" x={b2x(i) + 12} y={B2_Y + 37}>
										{s.l1}
									</text>
									<text className="sl-chip-mono" x={b2x(i) + 12} y={B2_Y + 53}>
										{s.l2}
									</text>
									<text className="sl-chip-sub" x={b2x(i) + 12} y={B2_Y + 72}>
										{s.sub}
									</text>
								</g>
								{i === 2 && (
									<g data-cross style={{ opacity: 0 }}>
										<line className="sl-cross" x1={b2x(i) + 12} y1={B2_Y + 10} x2={b2x(i) + B2_W - 12} y2={B2_Y + B2_H - 10} />
										<line className="sl-cross" x1={b2x(i) + B2_W - 12} y1={B2_Y + 10} x2={b2x(i) + 12} y2={B2_Y + B2_H - 10} />
									</g>
								)}
								{i > 0 && (
									<text data-b2-arrow className="sl-arrow" x={b2x(i) - 14} y={B2_Y + B2_H / 2 + 5} textAnchor="middle" style={{ opacity: 0 }}>
										→
									</text>
								)}
							</g>
						))}

						{/* ═══ Beat 3 · the cache key ═══ */}
						<text className="sl-beat" x={24} y={476}>
							beat 3 · the cache key is case-sensitive on purpose
						</text>

						{/* EVM hasher: collapses case */}
						<g data-b3 style={{ opacity: 0 }}>
							<text className="dd-ts" x={24} y={500}>
								EVM hasher (lowercases every string param)
							</text>
							<rect className="sl-key dim" x={24} y={510} width={B3_IN_W} height={28} rx={7} />
							<text className="sl-key-text" x={24 + B3_IN_W / 2} y={528} textAnchor="middle">
								sv1pQa7k…
							</text>
							<rect className="sl-key dim" x={24} y={544} width={B3_IN_W} height={28} rx={7} />
							<text className="sl-key-text" x={24 + B3_IN_W / 2} y={562} textAnchor="middle">
								SV1pQa7k…
							</text>
						</g>
						{[0, 1].map((i) => (
							<line
								key={`b3a${i}`}
								data-b3-arrow
								className="sl-b3-conn"
								x1={24 + B3_IN_W}
								y1={i === 0 ? 524 : 558}
								x2={246}
								y2={541}
								style={{ opacity: 0 }}
							/>
						))}
						<g data-b3 style={{ opacity: 0 }}>
							<rect className="sl-key crimson" x={250} y={527} width={196} height={28} rx={7} />
							<text className="sl-key-text err" x={250 + 98} y={545} textAnchor="middle">
								COLLAPSED · one key ✕
							</text>
							<text className="sl-key-sub" x={250} y={574}>
								two distinct accounts, each other&apos;s data
							</text>
						</g>

						{/* SVM key: preserves case */}
						<g data-b3 style={{ opacity: 0 }}>
							<text className="dd-ts" x={500} y={500}>
								SVM svmRequestKey (case preserved)
							</text>
						</g>
						<g data-b3 style={{ opacity: 0 }}>
							<rect className="sl-key green" x={500} y={510} width={B3_KEY_W} height={28} rx={7} />
							<text className="sl-key-text" x={500 + B3_KEY_W / 2} y={528} textAnchor="middle">
								getBlock:9f3a1c…(sv1pQa7k)
							</text>
						</g>
						<g data-b3 style={{ opacity: 0 }}>
							<rect className="sl-key green" x={500} y={544} width={B3_KEY_W} height={28} rx={7} />
							<text className="sl-key-text" x={500 + B3_KEY_W / 2} y={562} textAnchor="middle">
								getBlock:71c2be…(SV1pQa7k)
							</text>
							<text className="sl-key-sub ok" x={500} y={588}>
								two distinct keys ✓
							</text>
						</g>

						{/* partition key */}
						<g data-b3 style={{ opacity: 0 }}>
							<text className="dd-ts" x={820} y={500}>
								partition key = &lt;networkId&gt;:&lt;slotRef&gt;
							</text>
							<rect className="sl-key blue" x={820} y={510} width={200} height={28} rx={7} />
							<text className="sl-key-text" x={820 + 100} y={528} textAnchor="middle">
								svm:mainnet:93728481
							</text>
							<text className="sl-key-sub" x={820} y={540}>
								minContextSlot pinned
							</text>
							<rect className="sl-key blue" x={820} y={550} width={200} height={28} rx={7} />
							<text className="sl-key-text" x={820 + 100} y={568} textAnchor="middle">
								svm:mainnet:*
							</text>
							<text className="sl-key-sub" x={820} y={580}>
								no slot awareness
							</text>
						</g>
					</svg>
				</div>
			</div>
		</div>
	);
}

export default SvmSlotLanes;

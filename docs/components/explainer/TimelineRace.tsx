import React, { useEffect, useId, useRef, useState } from "react";
import { gsap } from "gsap";

/**
 * TimelineRace — data-driven multi-lane timeline player for the Inside eRPC
 * deep-dive pages. Lanes are rows; bars grow as a playhead sweeps across;
 * markers pop at their timestamp. The whole animation is one gsap.timeline
 * built from plain scenario data, so pages teach with data, not code.
 *
 * SSR-safe: the SVG renders statically from props; GSAP only hydrates motion
 * in useEffect. prefers-reduced-motion shows the final state with no playhead.
 * Styles: styles/deep-dive.css (scoped to .cv-dd-root).
 */

export type TimelineTone = "blue" | "green" | "amber" | "crimson" | "dim";

export interface TimelineLane {
	id: string;
	label: string;
	sub?: string;
}

export interface TimelineBar {
	lane: string;
	start: number; // axis seconds
	end: number;
	label?: string;
	tone?: TimelineTone;
	hatch?: boolean;
}

export interface TimelineMarker {
	lane: string;
	at: number;
	label?: string;
	tone?: TimelineTone;
	shape?: "dot" | "check" | "x" | "flag";
}

export interface TimelineScenario {
	duration: number; // axis length in seconds
	tickEvery?: number; // axis tick spacing (default ~duration/6)
	formatTime?: (t: number) => string;
	timeLabel?: string;
	lanes: TimelineLane[];
	bars: TimelineBar[];
	markers: TimelineMarker[];
}

export interface TimelineRaceProps {
	scenario?: TimelineScenario;
	/** Tabbed variant: record of tab-label → scenario. */
	scenarios?: Record<string, TimelineScenario>;
	autoPlay?: boolean;
	maxWidth?: string;
	caption?: string;
}

const W = 1200;
const LABEL_W = 182;
const PAD_R = 30;
const PAD_T = 16;
const LANE_H = 64;
const AXIS_H = 48;

const defaultFmt = (t: number) => {
	if (t === 0) return "0";
	if (t < 1) return `${Math.round(t * 1000)}ms`;
	return `${Number.isInteger(t) ? t : t.toFixed(1)}s`;
};

export function TimelineRace({ scenario, scenarios, autoPlay = true, maxWidth = "1200px", caption }: TimelineRaceProps) {
	const tabs = scenarios ? Object.keys(scenarios) : null;
	const [active, setActive] = useState(tabs ? tabs[0] : "");
	const sc = scenario ?? (scenarios ? scenarios[active] : undefined);
	const rootRef = useRef<HTMLDivElement>(null);
	const [runId, setRunId] = useState(0);
	const uid = useId().replace(/[^a-zA-Z0-9]/g, "");

	useEffect(() => {
		const root = rootRef.current;
		if (!root || !sc) return;
		const RM = matchMedia("(prefers-reduced-motion: reduce)").matches;
		const bars = root.querySelectorAll("[data-bar]");
		const barLabels = root.querySelectorAll("[data-bar-label]");
		const markers = root.querySelectorAll("[data-marker]");
		const markerLabels = root.querySelectorAll("[data-marker-label]");
		const ph = root.querySelector("[data-playhead]");
		const trackW = ((W - LABEL_W - PAD_R) * sc.duration) / sc.duration; // full track
		const x0 = LABEL_W;

		if (RM) {
			gsap.set(bars, { scaleX: 1 });
			gsap.set([barLabels, markers, markerLabels], { autoAlpha: 1 });
			gsap.set(markers, { scale: 1 });
			gsap.set(ph, { autoAlpha: 0 });
			return;
		}

		gsap.set(bars, { scaleX: 0, transformOrigin: "left center" });
		gsap.set(barLabels, { autoAlpha: 0 });
		gsap.set(markers, { autoAlpha: 0, scale: 0.3, transformOrigin: "50% 50%" });
		gsap.set(markerLabels, { autoAlpha: 0 });
		gsap.set(ph, { autoAlpha: 1, x: 0 });

		const playDur = Math.min(10, Math.max(6, sc.duration * 18));
		const tl = gsap.timeline({ paused: true });
		tl.to(ph, { x: trackW, duration: playDur, ease: "none" }, 0);
		sc.bars.forEach((b, i) => {
			if (!bars[i]) return;
			const t0 = (b.start / sc.duration) * playDur;
			tl.to(bars[i], { scaleX: 1, duration: ((b.end - b.start) / sc.duration) * playDur, ease: "none" }, t0);
			if (barLabels[i]) tl.to(barLabels[i], { autoAlpha: 1, duration: 0.3 }, t0 + 0.15);
		});
		sc.markers.forEach((m, i) => {
			if (!markers[i]) return;
			const t0 = (m.at / sc.duration) * playDur;
			tl.to(markers[i], { autoAlpha: 1, scale: 1, duration: 0.4, ease: "back.out(2.2)" }, t0);
			if (markerLabels[i]) tl.to(markerLabels[i], { autoAlpha: 1, duration: 0.3 }, t0 + 0.1);
		});

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
	}, [sc, runId, autoPlay]);

	if (!sc) return null;

	const x = (t: number) => LABEL_W + (t / sc.duration) * (W - LABEL_W - PAD_R);
	const laneY = (i: number) => PAD_T + i * LANE_H + LANE_H / 2;
	const laneIndex = new Map(sc.lanes.map((l, i) => [l.id, i]));
	const H = PAD_T + sc.lanes.length * LANE_H + AXIS_H;
	const axisY = PAD_T + sc.lanes.length * LANE_H + 14;
	const fmt = sc.formatTime ?? defaultFmt;
	const tickEvery = sc.tickEvery ?? sc.duration / 6;
	const ticks: number[] = [];
	for (let t = 0; t <= sc.duration + 1e-9; t += tickEvery) ticks.push(Math.round(t * 1000) / 1000);

	return (
		<div ref={rootRef} className="cv-dd-root dd-timeline" data-component="timeline-race" style={{ maxWidth }}>
			{(tabs || caption) && (
				<div className="dd-head">
					{tabs && (
						<div className="dd-tabs" role="group" aria-label="Timeline scenarios">
							{tabs.map((label) => (
								<button
									key={label}
									type="button"
									className="dd-tab"
									aria-pressed={label === active ? "true" : "false"}
									onClick={() => setActive(label)}
								>
									{label}
								</button>
							))}
						</div>
					)}
					{caption && <div className="dd-cap">{caption}</div>}
					<button type="button" className="dd-replay" onClick={() => setRunId((n) => n + 1)} aria-label="Replay timeline">
						↻ Replay
					</button>
				</div>
			)}
			<svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="xMidYMid meet" role="img" aria-label="Animated timeline">
				<defs>
					<pattern id={`hatch-${uid}`} width="7" height="7" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
						<rect width="7" height="7" className="dd-hatch-bg" />
						<line x1="0" y1="0" x2="0" y2="7" className="dd-hatch-line" />
					</pattern>
				</defs>

				{/* lane rows */}
				{sc.lanes.map((lane, i) => (
					<g key={lane.id}>
						{i > 0 && <line className="dd-lane-sep" x1={10} y1={PAD_T + i * LANE_H} x2={W - 14} y2={PAD_T + i * LANE_H} />}
						<text className="dd-lane-label" x={14} y={laneY(i) - 3}>
							{lane.label}
						</text>
						{lane.sub && (
							<text className="dd-lane-sub" x={14} y={laneY(i) + 13}>
								{lane.sub}
							</text>
						)}
					</g>
				))}

				{/* bars */}
				{sc.bars.map((b, i) => {
					const li = laneIndex.get(b.lane);
					if (li === undefined || b.end <= b.start) return null;
					const bw = x(b.end) - x(b.start);
					return (
						<g key={`b${i}`}>
							<rect
								data-bar
								className={`dd-bar tone-${b.tone ?? "blue"}`}
								x={x(b.start)}
								y={laneY(li) - 11}
								width={bw}
								height={22}
								rx={6}
								{...(b.hatch ? { fill: `url(#hatch-${uid})` } : {})}
							/>
							{b.label && (
								<text data-bar-label className="dd-bar-label" x={x(b.start) + 8} y={laneY(li) + 4}>
									{b.label}
								</text>
							)}
						</g>
					);
				})}

				{/* markers */}
				{sc.markers.map((m, i) => {
					const li = laneIndex.get(m.lane);
					if (li === undefined) return null;
					const cx = x(m.at);
					const cy = laneY(li);
					const tone = m.tone ?? "blue";
					return (
						<g key={`m${i}`}>
							<g data-marker className={`dd-marker tone-${tone}`}>
								{(m.shape ?? "dot") === "dot" && <circle cx={cx} cy={cy} r={5.5} />}
								{m.shape === "check" && (
									<text className="dd-marker-glyph" x={cx} y={cy + 5} textAnchor="middle">
										✓
									</text>
								)}
								{m.shape === "x" && (
									<text className="dd-marker-glyph" x={cx} y={cy + 5} textAnchor="middle">
										✕
									</text>
								)}
								{m.shape === "flag" && <path className="dd-marker-flag" d={`M${cx} ${cy - 9} l12 4.5 l-12 4.5 z`} />}
							</g>
							{m.label && (
								<text data-marker-label className={`dd-marker-label tone-${tone}`} x={cx} y={cy - 18} textAnchor="middle">
									{m.label}
								</text>
							)}
						</g>
					);
				})}

				{/* playhead */}
				<line data-playhead className="dd-playhead" x1={x(0)} y1={PAD_T - 6} x2={x(0)} y2={axisY - 8} />

				{/* axis */}
				<line className="dd-axis" x1={LABEL_W} y1={axisY} x2={W - PAD_R} y2={axisY} />
				{ticks.map((t) => (
					<g key={`t${t}`}>
						<line className="dd-axis-tick" x1={x(t)} y1={axisY} x2={x(t)} y2={axisY + 5} />
						<text className="dd-axis-label" x={x(t)} y={axisY + 19} textAnchor="middle">
							{fmt(t)}
						</text>
					</g>
				))}
				{sc.timeLabel && (
					<text className="dd-axis-title" x={W - PAD_R} y={axisY + 34} textAnchor="end">
						{sc.timeLabel}
					</text>
				)}
			</svg>
		</div>
	);
}

export default TimelineRace;

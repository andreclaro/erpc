import React, { useEffect, useRef, useState } from "react";
import { MotionConfig, motion, useInView, type Variants } from "motion/react";

/**
 * Animated feature gallery for the "Inside eRPC" page. Each widget is a small
 * inline SVG (the selection-policy widget is HTML) animated with the Motion
 * library: spring pops, staggered path drawing, layout FLIP re-sorting.
 * Sequences start when the card scrolls into view (whileInView on each root
 * motion.svg; useInView for the HTML widget) and the Replay button remounts
 * the stage to restart them. OS reduced-motion is honored via MotionConfig.
 * Stages are aria-hidden — the title + caption carry the same information.
 * Static fills/strokes/fonts come from styles/explainer.css (.cv-fw-root).
 */

function Widget({
	title,
	cap,
	children,
}: {
	title: string;
	cap: string;
	children?: React.ReactNode;
}) {
	const [runId, setRunId] = useState(0);
	return (
		<div className="fw-card" data-component="feature-widget">
			<div className="fw-head">
				<div>
					<div className="fw-title">{title}</div>
					<div className="fw-cap">{cap}</div>
				</div>
				<button
					type="button"
					className="fw-replay"
					onClick={() => setRunId((n) => n + 1)}
					aria-label={"Replay " + title + " animation"}
				>
					↻ Replay
				</button>
			</div>
			<div className="fw-stage" aria-hidden="true">
				<div key={runId}>{children}</div>
			</div>
		</div>
	);
}

/* Shared stage props: every SVG widget starts hidden and plays its "show"
   variant choreography once 30% of it has scrolled into view. */
const stageProps = {
	viewBox: "0 0 320 150",
	initial: "hidden",
	whileInView: "show",
	viewport: { once: true, amount: 0.3 },
};

/* Scale transforms on SVG elements need a fill-box origin, otherwise they
   pivot around the whole viewBox instead of the element itself. */
const originCenter: React.CSSProperties = { transformBox: "fill-box", transformOrigin: "center" };
const originLeft: React.CSSProperties = { transformBox: "fill-box", transformOrigin: "left center" };

const MONO_FONT = '500 10px/1 "JetBrains Mono", ui-monospace, SF Mono, Menlo, monospace';

/* Variant helpers — per-element delays carry each widget's narrative. */

const fadeV = (delay = 0, duration = 0.4): Variants => ({
	hidden: { opacity: 0 },
	show: { opacity: 1, transition: { delay, duration } },
});

const popV = (delay = 0): Variants => ({
	hidden: { opacity: 0, scale: 0.2 },
	show: {
		opacity: 1,
		scale: 1,
		transition: { delay, type: "spring", stiffness: 520, damping: 17 },
	},
});

/* Solid connectors draw themselves in via pathLength. */
const drawV = (delay = 0, duration = 0.55): Variants => ({
	hidden: { pathLength: 0, opacity: 0 },
	show: {
		pathLength: 1,
		opacity: 1,
		transition: { delay, duration, ease: "easeInOut", opacity: { delay, duration: 0.15 } },
	},
});

/* Dashed connectors keep their dash pattern: they fade in while the dash
   offset marches along the path. */
const dashV = (delay = 0, duration = 0.5): Variants => ({
	hidden: { opacity: 0, strokeDashoffset: "16px" },
	show: {
		opacity: 1,
		strokeDashoffset: "0px",
		transition: { delay, duration, ease: "easeOut" },
	},
});

/* Horizontal bars growing from the left edge. */
const growV = (delay = 0, duration = 1, target = 1, ease: "linear" | "easeOut" = "easeOut"): Variants => ({
	hidden: { scaleX: 0, opacity: 0 },
	show: {
		scaleX: target,
		opacity: 1,
		transition: { delay, duration, ease, opacity: { delay, duration: 0.2 } },
	},
});

/* Cards dropping in from above with a soft spring landing. */
const dropV = (delay = 0): Variants => ({
	hidden: { opacity: 0, y: -18 },
	show: {
		opacity: 1,
		y: 0,
		transition: { delay, type: "spring", stiffness: 320, damping: 22 },
	},
});

/* Request dots crossing the widget, fading out at the far end. */
const passV = (delay: number, duration = 1.2): Variants => ({
	hidden: { x: 0, opacity: 0 },
	show: {
		x: [0, 240],
		opacity: [0, 1, 1, 0],
		transition: {
			x: { delay, duration, ease: "linear" },
			opacity: { delay, duration, times: [0, 0.06, 0.82, 1], ease: "linear" },
		},
	},
});

function RetryWidget() {
	return (
		<motion.svg {...stageProps}>
			<motion.line className="fw-line" x1="30" y1="128" x2="290" y2="128" variants={drawV(0, 0.5)} />
			<motion.path className="fw-line" d="M56 60 C 90 32, 124 32, 154 60" variants={drawV(1.15, 0.55)} />
			<motion.path className="fw-line" d="M166 60 C 196 32, 226 32, 264 60" variants={drawV(2.65, 0.55)} />
			<motion.text className="fw-ma" x="105" y="30" textAnchor="middle" variants={fadeV(1.3)}>backoff 250ms</motion.text>
			<motion.text className="fw-ma" x="215" y="30" textAnchor="middle" variants={fadeV(2.8)}>500ms</motion.text>
			<motion.circle className="fw-dotb" cx="50" cy="60" r="6" variants={popV(0.25)} style={originCenter} />
			<motion.text className="fw-err" x="50" y="65" textAnchor="middle" fontSize="13" fontWeight="700" variants={popV(0.95)} style={originCenter}>✕</motion.text>
			<motion.circle className="fw-dotb" cx="160" cy="60" r="6" variants={popV(1.85)} style={originCenter} />
			<motion.text className="fw-err" x="160" y="65" textAnchor="middle" fontSize="13" fontWeight="700" variants={popV(2.45)} style={originCenter}>✕</motion.text>
			<motion.circle className="fw-dotb" cx="270" cy="60" r="6" variants={popV(3.35)} style={originCenter} />
			<motion.text className="fw-ok" x="270" y="65" textAnchor="middle" fontSize="13" fontWeight="700" variants={popV(3.95)} style={originCenter}>✓</motion.text>
			<motion.text className="fw-m" x="50" y="142" textAnchor="middle" variants={fadeV(0.35, 0.3)}>attempt 1</motion.text>
			<motion.text className="fw-m" x="160" y="142" textAnchor="middle" variants={fadeV(1.95, 0.3)}>attempt 2</motion.text>
			<motion.text className="fw-m" x="270" y="142" textAnchor="middle" variants={fadeV(3.45, 0.3)}>attempt 3</motion.text>
			<motion.rect className="fw-box" x="230" y="10" width="76" height="22" rx="6" variants={popV(4.25)} style={originCenter} />
			<motion.text className="fw-mg" x="268" y="25" textAnchor="middle" variants={fadeV(4.35)}>200 OK</motion.text>
		</motion.svg>
	);
}

/* Primary bar grows to 55% then dims as it loses; the hedge bar starts late
   and sweeps the full width. */
const hedgePrimaryV: Variants = {
	hidden: { scaleX: 0, opacity: 1 },
	show: {
		scaleX: 0.55,
		opacity: 0.25,
		transition: {
			scaleX: { delay: 0.35, duration: 1.4, ease: "linear" },
			opacity: { delay: 2.5, duration: 0.4 },
		},
	},
};

function HedgeWidget() {
	return (
		<motion.svg {...stageProps}>
			<motion.text className="fw-ts" x="10" y="48" variants={fadeV(0.05)}>primary · self-hosted</motion.text>
			<motion.text className="fw-ts" x="10" y="98" variants={fadeV(0.15)}>hedge · alchemy</motion.text>
			<motion.rect className="fw-box" x="110" y="40" width="190" height="8" rx="4" variants={fadeV(0.1)} />
			<motion.rect className="fw-box" x="110" y="90" width="190" height="8" rx="4" variants={fadeV(0.2)} />
			<motion.rect className="fw-warn" x="110" y="40" width="190" height="8" rx="4" variants={hedgePrimaryV} style={originLeft} />
			<motion.rect className="fw-dotb" x="110" y="90" width="190" height="8" rx="4" variants={growV(1.15, 0.8, 1, "linear")} style={originLeft} />
			<motion.text className="fw-err" x="220" y="48" textAnchor="middle" fontSize="12" fontWeight="700" variants={popV(2.55)} style={originCenter}>✕</motion.text>
			<motion.text className="fw-ma" x="232" y="48" variants={fadeV(2.65, 0.3)}>cancelled</motion.text>
			<motion.text className="fw-ok" x="306" y="98" textAnchor="middle" fontSize="12" fontWeight="700" variants={popV(2.1)} style={originCenter}>✓</motion.text>
			<motion.text className="fw-mg" x="252" y="84" variants={fadeV(2.2, 0.3)}>wins the race</motion.text>
			<motion.text className="fw-m" x="110" y="24" variants={fadeV(0.6)}>hedge delay ≈ 120ms (quantile 0.7)</motion.text>
		</motion.svg>
	);
}

/* Door overlay: glows red while the breaker is open, then vanishes. */
const doorV: Variants = {
	hidden: { opacity: 0 },
	show: {
		opacity: [0, 0.18, 0.18, 0],
		transition: { delay: 2.15, duration: 2.6, times: [0, 0.1, 0.7, 1] },
	},
};

const openV: Variants = {
	hidden: { opacity: 0, scale: 0.3 },
	show: {
		opacity: [0, 1, 1, 0],
		scale: [0.3, 1.12, 1, 1],
		transition: {
			opacity: { delay: 2.3, duration: 2.3, times: [0, 0.08, 0.82, 1] },
			scale: { delay: 2.3, duration: 2.3, times: [0, 0.12, 0.2, 1], ease: "easeOut" },
		},
	},
};

/* Dots that hit the open gate, rebound, and fade. */
const bounceV = (delay: number): Variants => ({
	hidden: { x: 0, opacity: 0 },
	show: {
		x: [0, 102, 92, 92],
		opacity: [0, 1, 1, 0.3],
		transition: {
			x: { delay, duration: 0.9, times: [0, 0.45, 0.6, 1], ease: "easeOut" },
			opacity: { delay, duration: 0.9, times: [0, 0.1, 0.6, 1], ease: "easeOut" },
		},
	},
});

function BreakerWidget() {
	return (
		<motion.svg {...stageProps}>
			<motion.rect className="fw-box" x="140" y="52" width="6" height="46" rx="2" variants={fadeV(0.1)} />
			<motion.rect className="fw-box" x="180" y="52" width="6" height="46" rx="2" variants={fadeV(0.1)} />
			<motion.circle className="fw-dotb" cx="30" cy="75" r="4" variants={passV(0.35)} />
			<motion.circle className="fw-dotb" cx="30" cy="75" r="4" variants={passV(0.7)} />
			<motion.circle className="fw-dotb" cx="30" cy="75" r="4" variants={passV(1.05)} />
			<motion.text className="fw-err" x="152" y="46" textAnchor="middle" fontSize="11" fontWeight="700" variants={popV(1.55)} style={originCenter}>✕</motion.text>
			<motion.text className="fw-err" x="174" y="46" textAnchor="middle" fontSize="11" fontWeight="700" variants={popV(1.85)} style={originCenter}>✕</motion.text>
			<motion.rect className="fw-err" x="142" y="55" width="42" height="40" rx="6" variants={doorV} />
			<motion.text className="fw-err" x="163" y="79" textAnchor="middle" fontSize="11" fontWeight="700" variants={openV} style={originCenter}>OPEN</motion.text>
			<motion.circle className="fw-dotb" cx="30" cy="75" r="4" variants={bounceV(2.45)} />
			<motion.circle className="fw-dotb" cx="30" cy="75" r="4" variants={bounceV(2.9)} />
			<motion.text className="fw-mr" x="240" y="79" variants={fadeV(2.6, 0.3)}>skipped</motion.text>
			<motion.circle className="fw-warn" cx="30" cy="75" r="4" variants={passV(3.6, 1.1)} />
			<motion.text className="fw-ok" x="284" y="79" textAnchor="middle" fontSize="12" fontWeight="700" variants={popV(4.65)} style={originCenter}>✓</motion.text>
			<motion.text className="fw-mg" x="140" y="118" variants={fadeV(4.75, 0.3)}>half-open probe ok → closed</motion.text>
			<motion.text className="fw-ts" x="30" y="118" variants={fadeV(0.2)}>requests</motion.text>
		</motion.svg>
	);
}

function ConsensusWidget() {
	const upstreams = [
		{ id: "u1", cy: 35, ty: 39, delay: 0.05 },
		{ id: "u2", cy: 75, ty: 79, delay: 0.15 },
		{ id: "u3", cy: 115, ty: 119, delay: 0.25 },
	];
	return (
		<motion.svg {...stageProps}>
			{upstreams.map((u) => (
				<motion.g key={u.id} variants={fadeV(u.delay)}>
					<circle className="fw-box" cx="35" cy={u.cy} r="9" />
					<text className="fw-m" x="14" y={u.ty}>{u.id}</text>
				</motion.g>
			))}
			<motion.line className="fw-line dashed" x1="44" y1="35" x2="105" y2="42" variants={dashV(0.35)} />
			<motion.line className="fw-line dashed" x1="44" y1="75" x2="105" y2="72" variants={dashV(0.5)} />
			<motion.line className="fw-line dashed" x1="44" y1="115" x2="105" y2="102" variants={dashV(0.65)} />
			<motion.text className="fw-mg" x="112" y="46" variants={fadeV(0.95)}>0x8f…c1</motion.text>
			<motion.text className="fw-mg" x="112" y="76" variants={fadeV(1.15)}>0x8f…c1</motion.text>
			<motion.text className="fw-mr" x="112" y="106" variants={fadeV(1.35)}>0x22…9e</motion.text>
			<motion.g variants={fadeV(1.5)}>
				<rect className="fw-box" x="196" y="56" width="106" height="34" rx="8" />
				<text className="fw-ts" x="249" y="70" textAnchor="middle">agreement</text>
				<rect className="fw-box" x="204" y="76" width="90" height="7" rx="3.5" />
			</motion.g>
			<motion.rect className="fw-ok" x="204" y="76" width="90" height="7" rx="3.5" variants={growV(2.0, 1.0, 0.66, "linear")} style={originLeft} />
			<motion.text className="fw-ok" x="249" y="104" textAnchor="middle" fontSize="12" fontWeight="700" variants={popV(2.9)} style={originCenter}>✓ 2/3 ≥ threshold 2</motion.text>
			<motion.text className="fw-mr" x="196" y="128" variants={fadeV(3.3)}>u3 dispute → punished</motion.text>
		</motion.svg>
	);
}

/* Unfinalized bar: fills, holds, then shrinks away as the TTL expires. */
const ttlShrinkV: Variants = {
	hidden: { scaleX: 0 },
	show: {
		scaleX: [0, 1, 1, 0.12],
		transition: { delay: 1.0, duration: 2.9, times: [0, 0.28, 0.45, 1], ease: "linear" },
	},
};

function CacheTtlWidget() {
	return (
		<motion.svg {...stageProps}>
			<motion.g variants={fadeV(0.05)}>
				<rect className="fw-box" x="20" y="26" width="16" height="16" rx="3" />
				<text className="fw-t" x="44" y="38">finalized block</text>
			</motion.g>
			<motion.rect className="fw-box" x="44" y="48" width="240" height="8" rx="4" variants={fadeV(0.15)} />
			<motion.rect className="fw-ok" x="44" y="48" width="240" height="8" rx="4" variants={growV(0.35, 1.2, 1, "linear")} style={originLeft} />
			<motion.text className="fw-mg" x="44" y="76" variants={fadeV(1.7)}>∞ immutable — kept forever</motion.text>
			<motion.g variants={fadeV(0.5)}>
				<rect className="fw-box" x="20" y="92" width="16" height="16" rx="3" />
				<text className="fw-t" x="44" y="104">unfinalized · ttl: 5s</text>
			</motion.g>
			<motion.rect className="fw-box" x="44" y="114" width="240" height="8" rx="4" variants={fadeV(0.6)} />
			<motion.rect className="fw-warn" x="44" y="114" width="240" height="8" rx="4" variants={ttlShrinkV} style={originLeft} />
			<motion.text className="fw-ma" x="44" y="142" variants={fadeV(4.1)}>expires → re-fetched (re-org safe)</motion.text>
		</motion.svg>
	);
}

/* HTML widget: rows start in config order and FLIP into descending score
   order ~1s after scrolling into view (layout animation). */
const SEL_ROWS = [
	{ id: "self-hosted", score: 61, tone: "var(--amber)" },
	{ id: "alchemy", score: 96, tone: "var(--green)" },
	{ id: "quicknode", score: 93, tone: "var(--green)" },
];
const SEL_ROW_H = 30;
const SEL_GAP = 10;
const SEL_TOP = 34;

function SelectionWidget() {
	const ref = useRef<HTMLDivElement>(null);
	const inView = useInView(ref, { once: true, amount: 0.3 });
	const [sorted, setSorted] = useState(false);
	useEffect(() => {
		if (!inView) return;
		const t = setTimeout(() => setSorted(true), 1000);
		return () => clearTimeout(t);
	}, [inView]);
	const order = sorted ? [...SEL_ROWS].sort((a, b) => b.score - a.score) : SEL_ROWS;
	return (
		<div
			ref={ref}
			style={{ position: "relative", minHeight: 172, width: "100%", fontFamily: "Inter, system-ui, sans-serif" }}
		>
			<motion.div
				initial={{ opacity: 0 }}
				animate={inView ? { opacity: 1 } : {}}
				transition={{ duration: 0.4, delay: 0.05 }}
				style={{ position: "absolute", top: 12, left: 16, font: "500 10px/1 Inter, system-ui, sans-serif", color: "var(--text-dim)" }}
			>
				score
			</motion.div>
			{SEL_ROWS.map((r, i) => {
				const idx = order.findIndex((o) => o.id === r.id);
				return (
					<motion.div
						key={r.id}
						layout
						initial={{ opacity: 0, y: 14 }}
						animate={inView ? { opacity: 1, y: 0 } : {}}
						transition={{
							layout: { type: "spring", stiffness: 320, damping: 28 },
							opacity: { duration: 0.4, delay: 0.1 + i * 0.12, ease: "easeOut" },
							y: { type: "spring", stiffness: 300, damping: 26, delay: 0.1 + i * 0.12 },
						}}
						style={{
							position: "absolute",
							top: SEL_TOP + idx * (SEL_ROW_H + SEL_GAP),
							left: 44,
							right: 16,
							height: SEL_ROW_H,
							display: "flex",
							alignItems: "center",
							justifyContent: "space-between",
							padding: "0 12px",
							borderRadius: 8,
							background: "rgba(255,255,255,0.022)",
							border: "1px solid rgba(255,255,255,0.1)",
						}}
					>
						<span style={{ font: "600 11px/1 Inter, system-ui, sans-serif", color: "var(--text)" }}>{r.id}</span>
						<span style={{ font: MONO_FONT, color: r.tone }}>{r.score}</span>
					</motion.div>
				);
			})}
			<motion.div
				initial={{ opacity: 0 }}
				animate={inView ? { opacity: 1 } : {}}
				transition={{ duration: 0.4, delay: 1.7 }}
				style={{
					position: "absolute",
					top: SEL_TOP + 3 * (SEL_ROW_H + SEL_GAP) + 4,
					left: 44,
					font: MONO_FONT,
					color: "var(--text-mono)",
				}}
			>
				re-sorted every evalInterval from live health metrics
			</motion.div>
		</div>
	);
}

/* Tokens drip into the budget, get consumed, and drop back in on refill. */
const tokenV = (delay: number): Variants => ({
	hidden: { opacity: 0, y: -12 },
	show: {
		opacity: [0, 1, 1, 0, 0, 1],
		y: [-12, 0, 0, 0, -12, 0],
		transition: { delay, duration: 2.6, times: [0, 0.15, 0.4, 0.55, 0.65, 0.8], ease: "easeInOut" },
	},
});

function RateLimitWidget() {
	return (
		<motion.svg {...stageProps}>
			<motion.rect className="fw-box" x="120" y="26" width="80" height="104" rx="10" variants={fadeV(0.1)} />
			<motion.circle className="fw-dotb" cx="140" cy="108" r="5" variants={tokenV(0.7)} />
			<motion.circle className="fw-dotb" cx="160" cy="108" r="5" variants={tokenV(1.1)} />
			<motion.circle className="fw-dotb" cx="180" cy="108" r="5" variants={tokenV(1.5)} />
			<motion.circle className="fw-dotb" cx="150" cy="86" r="5" variants={tokenV(1.9)} />
			<motion.circle className="fw-dotb" cx="170" cy="86" r="5" variants={tokenV(2.3)} />
			<motion.circle className="fw-err" cx="230" cy="60" r="4" variants={popV(3.0)} style={originCenter} />
			<motion.text className="fw-mr" x="242" y="64" variants={fadeV(3.1, 0.3)}>429 · budget empty</motion.text>
			<motion.text className="fw-m" x="120" y="145" variants={fadeV(4.4)}>refills every period (1m)</motion.text>
		</motion.svg>
	);
}

function MultiplexWidget() {
	return (
		<motion.svg {...stageProps}>
			{[20, 60, 100].map((y, i) => (
				<motion.g key={y} variants={fadeV(0.05 + i * 0.1)}>
					<rect className="fw-box" x="15" y={y} width="52" height="22" rx="6" />
					<text className="fw-m" x="41" y={y + 15} textAnchor="middle">req</text>
				</motion.g>
			))}
			<motion.path className="fw-line" d="M67 31 C 95 31, 95 63, 118 63" variants={drawV(0.35, 0.5)} />
			<motion.path className="fw-line" d="M67 71 L 118 71" variants={drawV(0.5, 0.4)} />
			<motion.path className="fw-line" d="M67 111 C 95 111, 95 79, 118 79" variants={drawV(0.65, 0.5)} />
			<motion.g variants={popV(0.95)} style={originCenter}>
				<rect className="fw-box" x="118" y="54" width="84" height="34" rx="8" />
				<text className="fw-ts" x="160" y="74" textAnchor="middle">multiplexer</text>
			</motion.g>
			<motion.path className="fw-line" d="M202 71 L 244 71" variants={drawV(1.35, 0.4)} />
			<motion.text className="fw-mg" x="223" y="62" textAnchor="middle" variants={fadeV(1.55, 0.3)}>×1</motion.text>
			<motion.g variants={popV(1.65)} style={originCenter}>
				<rect className="fw-box" x="244" y="54" width="62" height="34" rx="8" />
				<text className="fw-ts" x="275" y="74" textAnchor="middle">upstream</text>
			</motion.g>
			<motion.path className="fw-line dashed" d="M118 63 C 95 63, 95 35, 67 35" variants={dashV(2.25)} />
			<motion.path className="fw-line dashed" d="M118 75 L 67 75" variants={dashV(2.4, 0.4)} />
			<motion.path className="fw-line dashed" d="M118 79 C 95 79, 95 107, 67 107" variants={dashV(2.55)} />
			<motion.text className="fw-mg" x="160" y="140" textAnchor="middle" variants={fadeV(3.0)}>3 identical in-flight → 1 upstream call</motion.text>
		</motion.svg>
	);
}

function GetLogsWidget() {
	const chunks = [
		{ x: 20, label: "0–10k", delay: 0.5, fillDelay: 1.15, checkDelay: 2.45 },
		{ x: 117, label: "10–20k", delay: 0.7, fillDelay: 1.35, checkDelay: 2.65 },
		{ x: 214, label: "20–30k", delay: 0.9, fillDelay: 1.55, checkDelay: 2.85 },
	];
	return (
		<motion.svg {...stageProps}>
			<motion.g variants={fadeV(0.05)}>
				<rect className="fw-box" x="20" y="14" width="280" height="20" rx="6" />
				<text className="fw-m" x="160" y="27" textAnchor="middle">eth_getLogs [0 … 30000]</text>
			</motion.g>
			{chunks.map((c) => (
				<motion.g key={c.label} variants={dropV(c.delay)}>
					<rect className="fw-box" x={c.x} y="52" width="86" height="14" rx="4" />
					<motion.rect className="fw-dotb" x={c.x} y="52" width="86" height="14" rx="4" variants={growV(c.fillDelay, 1.0, 1, "linear")} style={originLeft} />
					<text className="fw-m" x={c.x + 43} y="62" textAnchor="middle" fill="#081019">{c.label}</text>
				</motion.g>
			))}
			{chunks.map((c) => (
				<motion.text key={"ok" + c.label} className="fw-ok" x={c.x + 43} y="46" textAnchor="middle" fontSize="11" fontWeight="700" variants={popV(c.checkDelay)} style={originCenter}>✓</motion.text>
			))}
			<motion.path className="fw-line" d="M63 66 C 63 92, 120 92, 140 108" variants={drawV(3.05, 0.5)} />
			<motion.path className="fw-line" d="M160 66 L 160 108" variants={drawV(3.15, 0.45)} />
			<motion.path className="fw-line" d="M257 66 C 257 92, 200 92, 180 108" variants={drawV(3.25, 0.5)} />
			<motion.g variants={fadeV(3.7)}>
				<rect className="fw-box" x="90" y="108" width="140" height="24" rx="7" />
				<text className="fw-ts" x="160" y="124" textAnchor="middle">merged · block-continuous</text>
			</motion.g>
			<motion.text className="fw-ok" x="244" y="124" textAnchor="middle" fontSize="12" fontWeight="700" variants={popV(4.0)} style={originCenter}>✓</motion.text>
		</motion.svg>
	);
}

function ShadowWidget() {
	return (
		<motion.svg {...stageProps}>
			<motion.g variants={fadeV(0.05)}>
				<rect className="fw-box" x="15" y="58" width="52" height="26" rx="7" />
				<text className="fw-ts" x="41" y="75" textAnchor="middle">client</text>
			</motion.g>
			<motion.g variants={fadeV(0.15)}>
				<rect className="fw-box" x="120" y="58" width="60" height="26" rx="7" />
				<text className="fw-ts" x="150" y="75" textAnchor="middle">eRPC</text>
			</motion.g>
			<motion.path className="fw-line" d="M67 71 L 120 71" variants={drawV(0.3, 0.4)} />
			<motion.path className="fw-line" d="M180 66 C 205 60, 205 44, 232 40" variants={drawV(0.8, 0.5)} />
			<motion.g variants={popV(1.15)} style={originCenter}>
				<rect className="fw-box" x="232" y="26" width="74" height="26" rx="7" />
				<text className="fw-ts" x="269" y="43" textAnchor="middle">upstream A</text>
			</motion.g>
			<motion.path className="fw-line dashed" d="M180 78 C 205 84, 205 100, 232 104" variants={dashV(1.5)} />
			<motion.text className="fw-m" x="196" y="96" variants={fadeV(1.7, 0.3)}>mirror 10%</motion.text>
			<motion.g variants={fadeV(2.0)}>
				<rect className="fw-box" x="232" y="91" width="74" height="26" rx="7" strokeDasharray="4 3" />
				<text className="fw-ts" x="269" y="108" textAnchor="middle">shadow B</text>
			</motion.g>
			<motion.text className="fw-mg" x="269" y="66" textAnchor="middle" variants={popV(2.2)} style={originCenter}>200 ✓ served</motion.text>
			<motion.text className="fw-ma" x="269" y="133" textAnchor="middle" variants={fadeV(2.6)}>compared · never served</motion.text>
		</motion.svg>
	);
}

/* Status dots pop in, then keep breathing with an infinite soft pulse. */
const pulseV = (delay: number): Variants => ({
	hidden: { opacity: 0, scale: 0.3 },
	show: {
		scale: 1,
		opacity: [0.35, 1, 0.35],
		transition: {
			scale: { delay, type: "spring", stiffness: 500, damping: 16 },
			opacity: { delay, duration: 2, repeat: Infinity, ease: "easeInOut" },
		},
	},
});

function MultiChainWidget() {
	const chains = [
		{ x: 20, y: 30, label: "evm:1" },
		{ x: 105, y: 30, label: "evm:42161" },
		{ x: 215, y: 30, label: "evm:137" },
		{ x: 20, y: 75, label: "evm:10" },
		{ x: 105, y: 75, label: "evm:8453" },
		{ x: 215, y: 75, label: "svm:mainnet" },
	];
	return (
		<motion.svg {...stageProps}>
			{chains.map((c, i) => (
				<motion.g key={c.label} variants={popV(0.05 + i * 0.09)} style={originCenter}>
					<rect className="fw-box" x={c.x} y={c.y} width={c.label.length > 7 ? 92 : 72} height="26" rx="13" />
					<motion.circle className="fw-ok" cx={c.x + 13} cy={c.y + 13} r="3" variants={pulseV(0.45 + i * 0.12)} style={originCenter} />
					<text className="fw-m" x={c.x + 22} y={c.y + 17}>{c.label}</text>
				</motion.g>
			))}
			<motion.text className="fw-ts" x="160" y="135" textAnchor="middle" variants={fadeV(1.0)}>one URL pattern — every chain, no config change per chain</motion.text>
		</motion.svg>
	);
}

export function FeatureWidgets() {
	return (
		<MotionConfig reducedMotion="user">
			<div className="cv-fw-root" data-component="feature-widgets">
				<div className="fw-grid">
					<Widget title="Retry with backoff" cap={"failsafe[].retry — maxAttempts: 3 · backoffMaxDelay: 1s"}><RetryWidget /></Widget>
					<Widget title="Hedged requests" cap={"failsafe[].hedge — maxCount: 1 · delay: quantile 0.7"}><HedgeWidget /></Widget>
					<Widget title="Circuit breaker" cap={"upstreams[].failsafe[].circuitBreaker — upstream scope only"}><BreakerWidget /></Widget>
					<Widget title="Consensus" cap={"failsafe[].consensus — maxParticipants: 3 · agreementThreshold: 2"}><ConsensusWidget /></Widget>
					<Widget title="Finality-aware cache" cap={"database.evmJsonRpcCache.policies[] — finality + ttl"}><CacheTtlWidget /></Widget>
					<Widget title="Selection policy" cap={"networks[].selectionPolicy — evalFunc · evalInterval"}><SelectionWidget /></Widget>
					<Widget title="Rate limiting" cap={"rateLimiters.budgets[].rules[] — maxCount · period · waitTime"}><RateLimitWidget /></Widget>
					<Widget title="Multiplexing" cap={"networks[].multiplexing.enabled — in-flight dedup"}><MultiplexWidget /></Widget>
					<Widget title="eth_getLogs splitting" cap={"evm.getLogsSplitOnError · getLogsMaxAllowedRange: 30000"}><GetLogsWidget /></Widget>
					<Widget title="Shadow upstreams" cap={"upstreams[].shadow — sampleRate · compared, never served"}><ShadowWidget /></Widget>
					<Widget title="Multi-chain & multi-architecture" cap={"/<project>/<architecture>/<chain> — evm & svm"}><MultiChainWidget /></Widget>
				</div>
			</div>
		</MotionConfig>
	);
}

export default FeatureWidgets;

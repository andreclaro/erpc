import React, { useEffect, useRef, useState } from "react";
import { gsap } from "gsap";

/**
 * ErrorJourney — bespoke widget for /inside-erpc/errors. Three raw JSON-RPC
 * wire errors travel the same pipeline side by side: raw → vendor hook →
 * architecture normalizer → taxonomy card (with the retryableTowardNetwork
 * flag flipping to its verdict) → decision gate → what the client receives.
 * Lanes: revert (code 3), getLogs too large (-32012), missing data (-32014,
 * which retries and succeeds). A footer strip renders the exhausted bundle:
 * three upstream causes → orderCauses (retryable first) → dominant code →
 * the single wire error the client sees.
 *
 * SSR-safe: the SVG renders statically (initial state); GSAP hydrates motion
 * in useEffect. The stage remounts on Replay so text swaps reset for free.
 * prefers-reduced-motion shows the final state with no animation.
 * Styles: styles/dd-errors.css (scoped to .cv-dd-root.dd-errorjourney).
 */

export interface ErrorJourneyProps {
	caption?: string;
	autoPlay?: boolean;
	maxWidth?: string;
}

const W = 1200;
const H = 620;

// Column geometry (x, width).
const COLS = {
	raw: { x: 16, w: 186 },
	vendor: { x: 226, w: 140 },
	norm: { x: 390, w: 140 },
	taxo: { x: 554, w: 260 },
	gate: { x: 838, w: 140 },
	client: { x: 1002, w: 182 },
};
const ARROW_XS = [214, 378, 542, 826, 990]; // between consecutive columns

const LANE_Y = [96, 220, 344];
const HEADERS_Y = 64;
const GUIDE_TOP = 74;
const GUIDE_BOTTOM = 492;

interface LaneSpec {
	tag: string;
	rawLines: string[];
	vendorName: string;
	vendorSubInit: string;
	vendorSubHit?: string; // when set, chip highlights and text swaps
	normName: string;
	normSub: string;
	taxoName: string;
	flagFinal: string;
	flagOk: boolean;
	taxoSub: string;
	gateL1: string;
	gateL2: string;
	gateSub: string;
	clientOk: boolean;
	clientLines: string[];
}

const LANES: LaneSpec[] = [
	{
		tag: "1 · REVERT — eth_call",
		rawLines: ['{ "code": 3,', '"message": "execution', '  reverted", "data":', '  "0x08c379a0…" }'],
		vendorName: "Alchemy hook",
		vendorSubInit: "code 3 → ?",
		vendorSubHit: "code 3 → vendor match",
		normName: "EVM normalizer",
		normSub: "vendor already classified",
		taxoName: "ErrEndpointExecutionException",
		flagFinal: "retryableTowardNetwork: false",
		flagOk: false,
		taxoSub: "deterministic — same result on every upstream",
		gateL1: "no retry",
		gateL2: "no hedge save",
		gateSub: "IsRetryableTowardNetwork = false",
		clientOk: false,
		clientLines: ['{ "code": 3,', '"message": "execution', '  reverted", "data":', '  "0x08c379a0…" }'],
	},
	{
		tag: "2 · TOO LARGE — eth_getLogs",
		rawLines: ['{ "code": -32012,', '"message": "Log response', '  size exceeded." }'],
		vendorName: "vendor hooks",
		vendorSubInit: "no match → generic rules",
		normName: "EVM normalizer",
		normSub: "range guardrail tripped",
		taxoName: "ErrEndpointRequestTooLarge",
		flagFinal: "retryableTowardNetwork: false",
		flagOk: false,
		taxoSub: "complaint: evm_block_range",
		gateL1: "no blind retry",
		gateL2: "→ proactive split",
		gateSub: "bisect · see evm-translation",
		clientOk: false,
		clientLines: ['{ "code": -32012,', '"message": "getLogs request', '  exceeded max allowed', '  range" }'],
	},
	{
		tag: "3 · MISSING DATA — eth_getBlockByNumber",
		rawLines: ['{ "code": -32014,', '"message": "block not found', '  with number 0x14f2c90" }'],
		vendorName: "vendor hooks",
		vendorSubInit: "no match → generic rules",
		normName: "EVM normalizer",
		normSub: "near-tip read → missing data",
		taxoName: "ErrEndpointMissingData",
		flagFinal: "retryableTowardNetwork: true",
		flagOk: true,
		taxoSub: "permanentMissingData: false",
		gateL1: "catch-up retry",
		gateL2: "upstream B: OK",
		gateSub: "reason = missing_data",
		clientOk: true,
		clientLines: ["200 OK", '{ "result": { "number":', '  "0x14f2c90", … } }'],
	},
];

const CAUSES = [
	{ name: "ups-a · ErrEndpointMissingData", sub: "not-yet-indexed" },
	{ name: "ups-b · ErrEndpointTimeout", sub: "-32015 · node timeout" },
	{ name: "ups-c · ErrEndpointMissingData", sub: "not-yet-indexed" },
];
const WIRE_LINES = [
	'{ "code": -32014,',
	'"message": "all upstream attempts',
	'  failed (2 upstream missing data,',
	'  1 upstream timeout)" }',
];

const FT_TITLE_Y = 522;
const FT_CHIP_Y = 536;
const FT_CHIP_H = 46;

const colCx = (c: { x: number; w: number }) => c.x + c.w / 2;

export function ErrorJourney({ caption, autoPlay = true, maxWidth = "1200px" }: ErrorJourneyProps) {
	const rootRef = useRef<HTMLDivElement>(null);
	const [runId, setRunId] = useState(0);

	useEffect(() => {
		const root: HTMLDivElement | null = rootRef.current;
		if (!root) return;
		const RM = matchMedia("(prefers-reduced-motion: reduce)").matches;
		const one = (s: string) => root.querySelector(s);
		const all = (s: string) => root.querySelectorAll(s);

		const raws = all("[data-raw]");
		const vendors = all("[data-vendor]");
		const norms = all("[data-norm]");
		const taxos = all("[data-taxo]");
		const flags = all("[data-flag]");
		const gates = all("[data-gate]");
		const clients = all("[data-client]");
		const arrows = all("[data-arrow]");
		const ftTitle = one("[data-ft-title]");
		const causes = all("[data-cause]");
		const sortBits = all("[data-sort]");
		const dom = one("[data-dom]");
		const wireBits = all("[data-wire-bit]");

		const vendorChip0 = one("[data-vendor-chip='0']");
		const vendorSub0 = one("[data-vendor-sub='0']");
		const flagText = (i: number) => one(`[data-flag-text='${i}']`);
		const flagRect = (i: number) => one(`[data-flag='${i}']`);

		const applyFlag = (i: number) => {
			const t = flagText(i);
			const r = flagRect(i);
			if (t) {
				t.textContent = LANES[i].flagFinal;
				t.setAttribute("class", LANES[i].flagOk ? "ej-flag-text ok" : "ej-flag-text err");
			}
			if (r) r.setAttribute("class", LANES[i].flagOk ? "ej-flag ok" : "ej-flag err");
		};
		const applyVendorHit = () => {
			if (vendorSub0) {
				vendorSub0.textContent = LANES[0].vendorSubHit ?? "";
				vendorSub0.setAttribute("class", "ej-chip-sub ok");
			}
			if (vendorChip0) vendorChip0.setAttribute("class", "ej-chip hit");
		};

		if (RM) {
			// Final state, no motion: full pipeline resolved, footer rendered.
			gsap.set([raws, vendors, norms, taxos, gates, clients, causes, sortBits, wireBits, ftTitle], {
				autoAlpha: 1, x: 0, y: 0, scale: 1,
			});
			gsap.set(arrows, { autoAlpha: 1 });
			gsap.set(dom, { autoAlpha: 1, scale: 1 });
			applyVendorHit();
			for (let i = 0; i < LANES.length; i++) applyFlag(i);
			return;
		}

		// Initial states (stage remounts on replay, so texts are already fresh).
		gsap.set([raws, vendors, norms, gates, clients], { autoAlpha: 0, x: -22 });
		gsap.set([taxos, dom], { autoAlpha: 0, scale: 0.72 });
		gsap.set(causes, { autoAlpha: 0, y: 14 });
		gsap.set([sortBits, wireBits, ftTitle], { autoAlpha: 0 });
		gsap.set(arrows, { autoAlpha: 0 });

		const tl = gsap.timeline({ paused: true });

		// 1) Raw wire errors enter, top to bottom.
		tl.to(raws, { autoAlpha: 1, x: 0, duration: 0.4, stagger: 0.45, ease: "power2.out" }, 0);
		tl.to([arrows[0], arrows[5], arrows[10]], { autoAlpha: 1, duration: 0.25, stagger: 0.45 }, 0.35);

		// 2) Vendor hooks evaluate (lane 1 matches, lanes 2–3 fall through).
		tl.to(vendors, { autoAlpha: 1, x: 0, duration: 0.35, stagger: 0.2, ease: "power2.out" }, 1.4);
		tl.call(applyVendorHit, [], 2.0);
		tl.to([arrows[1], arrows[6], arrows[11]], { autoAlpha: 1, duration: 0.25, stagger: 0.2 }, 1.6);

		// 3) Architecture normalizers.
		tl.to(norms, { autoAlpha: 1, x: 0, duration: 0.35, stagger: 0.2, ease: "power2.out" }, 2.6);
		tl.to([arrows[2], arrows[7], arrows[12]], { autoAlpha: 1, duration: 0.25, stagger: 0.2 }, 2.8);

		// 4) Taxonomy cards pop; the retryableTowardNetwork flag flips to its verdict.
		tl.to(taxos, { autoAlpha: 1, scale: 1, duration: 0.45, stagger: 0.25, ease: "back.out(1.8)" }, 3.5);
		tl.call(() => applyFlag(0), [], 4.6);
		tl.call(() => applyFlag(1), [], 4.75);
		tl.call(() => applyFlag(2), [], 4.9);
		tl.to(flags, { scale: 1.07, duration: 0.14, yoyo: true, repeat: 1, stagger: 0.15 }, 4.6);
		tl.to([arrows[3], arrows[8], arrows[13]], { autoAlpha: 1, duration: 0.25, stagger: 0.25 }, 3.8);

		// 5) Decision gates read the flags.
		tl.to(gates, { autoAlpha: 1, x: 0, duration: 0.35, stagger: 0.2, ease: "power2.out" }, 5.5);
		tl.to([arrows[4], arrows[9], arrows[14]], { autoAlpha: 1, duration: 0.25, stagger: 0.2 }, 5.7);

		// 6) The client receives its verdict (two errors, one rescued 200 OK).
		tl.to(clients, { autoAlpha: 1, x: 0, duration: 0.4, stagger: 0.25, ease: "power2.out" }, 6.3);

		// 7) Exhausted-bundle footer: causes → orderCauses → dominant → wire error.
		tl.to(ftTitle, { autoAlpha: 1, duration: 0.3 }, 7.5);
		tl.to(causes, { autoAlpha: 1, y: 0, duration: 0.35, stagger: 0.2, ease: "power2.out" }, 7.8);
		tl.to(sortBits, { autoAlpha: 1, duration: 0.3, stagger: 0.1 }, 8.8);
		tl.to(dom, { autoAlpha: 1, scale: 1, duration: 0.4, ease: "back.out(1.8)" }, 9.4);
		tl.to(wireBits, { autoAlpha: 1, x: 0, duration: 0.4, stagger: 0.1, ease: "power2.out" }, 10.0);

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
	}, [runId, autoPlay]);

	return (
		<div ref={rootRef} className="cv-dd-root dd-widget dd-errorjourney" data-component="error-journey" style={{ maxWidth }}>
			<div className="dd-head">
				<div style={{ flex: 1, minWidth: 200 }}>
					<div className="dd-wtitle">One taxonomy, every provider</div>
					{caption && <div className="dd-cap">{caption}</div>}
				</div>
				<button type="button" className="dd-replay" onClick={() => setRunId((n) => n + 1)} aria-label="Replay error journey animation">
					↻ Replay
				</button>
			</div>
			<div className="dd-stage" aria-hidden="true">
				<div key={runId}>
					<svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="xMidYMid meet" role="img" aria-label="Three error journeys through normalization, decision gates, and client rendering">
						{/* column guides */}
						{Object.values(COLS).map((c, i) => (
							<line key={`g${i}`} className="ej-guide" x1={colCx(c)} y1={GUIDE_TOP} x2={colCx(c)} y2={GUIDE_BOTTOM} />
						))}

						{/* column headers */}
						<text className="ej-head" x={colCx(COLS.raw)} y={HEADERS_Y} textAnchor="middle">RAW WIRE ERROR</text>
						<text className="ej-head" x={colCx(COLS.vendor)} y={HEADERS_Y} textAnchor="middle">VENDOR HOOK</text>
						<text className="ej-head" x={colCx(COLS.norm)} y={HEADERS_Y} textAnchor="middle">NORMALIZER</text>
						<text className="ej-head" x={colCx(COLS.taxo)} y={HEADERS_Y} textAnchor="middle">eRPC TAXONOMY</text>
						<text className="ej-head" x={colCx(COLS.gate)} y={HEADERS_Y} textAnchor="middle">DECISION GATE</text>
						<text className="ej-head" x={colCx(COLS.client)} y={HEADERS_Y} textAnchor="middle">CLIENT SEES</text>

						{/* lane separators */}
						<line className="ej-lane-sep" x1={16} y1={204} x2={1184} y2={204} />
						<line className="ej-lane-sep" x1={16} y1={328} x2={1184} y2={328} />

						{/* per-lane flow arrows */}
						{LANES.map((_, li) =>
							ARROW_XS.map((ax, ai) => (
								<text key={`a${li}-${ai}`} data-arrow className="ej-arrow" x={ax} y={LANE_Y[li] + 46} textAnchor="middle" style={{ opacity: 0 }}>
									→
								</text>
							)),
						)}

						{LANES.map((lane, li) => {
							const y = LANE_Y[li];
							const flagCx = COLS.taxo.x + 130;
							return (
								<g key={`lane${li}`}>
									{/* raw wire error */}
									<g data-raw data-pop style={{ opacity: 0 }}>
										<rect className="ej-raw" x={COLS.raw.x} y={y} width={COLS.raw.w} height={76} rx={8} />
										<text className="ej-raw-tag" x={COLS.raw.x + 10} y={y + 16}>{lane.tag}</text>
										{lane.rawLines.map((l, i) => (
											<text key={`r${i}`} className="ej-raw-mono" x={COLS.raw.x + 10} y={y + 32 + i * 12}>{l}</text>
										))}
									</g>

									{/* vendor hook */}
									<g data-vendor data-pop style={{ opacity: 0 }}>
										<rect data-vendor-chip={li} className={`ej-chip ${li === 0 ? "" : "dim"}`} x={COLS.vendor.x} y={y + 14} width={COLS.vendor.w} height={48} rx={8} />
										<text className="ej-chip-name" x={colCx(COLS.vendor)} y={y + 32} textAnchor="middle">{lane.vendorName}</text>
										<text data-vendor-sub={li} className="ej-chip-sub" x={colCx(COLS.vendor)} y={y + 47} textAnchor="middle">{lane.vendorSubInit}</text>
									</g>

									{/* normalizer */}
									<g data-norm data-pop style={{ opacity: 0 }}>
										<rect className="ej-chip" x={COLS.norm.x} y={y + 14} width={COLS.norm.w} height={48} rx={8} />
										<text className="ej-chip-name" x={colCx(COLS.norm)} y={y + 32} textAnchor="middle">{lane.normName}</text>
										<text className="ej-chip-sub" x={colCx(COLS.norm)} y={y + 47} textAnchor="middle">{lane.normSub}</text>
									</g>

									{/* taxonomy card + retryable flag */}
									<g data-taxo data-pop style={{ opacity: 0 }}>
										<rect className="ej-taxo" x={COLS.taxo.x} y={y} width={COLS.taxo.w} height={76} rx={8} />
										<text className="ej-taxo-name" x={COLS.taxo.x + 14} y={y + 18}>{lane.taxoName}</text>
										<rect data-flag={li} className="ej-flag" x={COLS.taxo.x + 14} y={y + 28} width={232} height={18} rx={6} />
										<text data-flag-text={li} className="ej-flag-text" x={flagCx} y={y + 41} textAnchor="middle">retryableTowardNetwork: ?</text>
										<text className="ej-taxo-sub" x={COLS.taxo.x + 14} y={y + 63}>{lane.taxoSub}</text>
									</g>

									{/* decision gate */}
									<g data-gate data-pop style={{ opacity: 0 }}>
										<rect className="ej-chip" x={COLS.gate.x} y={y + 14} width={COLS.gate.w} height={48} rx={8} />
										<text className="ej-chip-name" x={colCx(COLS.gate)} y={y + 31} textAnchor="middle">{lane.gateL1}</text>
										<text className="ej-chip-name" x={colCx(COLS.gate)} y={y + 44} textAnchor="middle">{lane.gateL2}</text>
										<text className="ej-chip-sub" x={colCx(COLS.gate)} y={y + 66} textAnchor="middle">{lane.gateSub}</text>
									</g>

									{/* client-rendered object */}
									<g data-client data-pop style={{ opacity: 0 }}>
										<rect className={`ej-client ${lane.clientOk ? "ok" : "err"}`} x={COLS.client.x} y={y + 8} width={COLS.client.w} height={60} rx={8} />
										{lane.clientLines.map((l, i) => (
											<text key={`c${i}`} className={`ej-client-mono ${lane.clientOk && i === 0 ? "ok" : ""}`} x={COLS.client.x + 10} y={y + 26 + i * 12}>{l}</text>
										))}
									</g>
								</g>
							);
						})}

						{/* exhausted-bundle footer */}
						<line className="ej-ft-sep" x1={16} y1={506} x2={1184} y2={506} />
						<text data-ft-title className="ej-ft-title" x={16} y={FT_TITLE_Y} style={{ opacity: 0 }}>
							When every upstream fails — the exhausted bundle collapses to ONE deterministic verdict
						</text>
						{CAUSES.map((c, i) => (
							<g key={`cause${i}`} data-cause data-pop style={{ opacity: 0 }}>
								<rect className="ej-cause" x={16 + i * 212} y={FT_CHIP_Y} width={200} height={FT_CHIP_H} rx={8} />
								<text className="ej-cause-mono" x={26 + i * 212} y={FT_CHIP_Y + 19}>{c.name}</text>
								<text className="ej-cause-sub" x={26 + i * 212} y={FT_CHIP_Y + 34}>{c.sub}</text>
							</g>
						))}
						<text data-sort className="ej-arrow" x={662} y={FT_CHIP_Y + 28} textAnchor="middle" style={{ opacity: 0 }}>→</text>
						<text data-sort className="ej-arrow-label" x={662} y={FT_CHIP_Y + 62} textAnchor="middle" style={{ opacity: 0 }}>orderCauses</text>
						<g data-dom data-pop style={{ opacity: 0 }}>
							<rect className="ej-dom" x={688} y={FT_CHIP_Y} width={196} height={FT_CHIP_H} rx={8} />
							<text className="ej-dom-mono" x={698} y={FT_CHIP_Y + 19}>ErrEndpointMissingData ×2</text>
							<text className="ej-dom-sub" x={698} y={FT_CHIP_Y + 34}>most frequent → first of bucket</text>
						</g>
						<text data-wire-bit className="ej-arrow" x={898} y={FT_CHIP_Y + 28} textAnchor="middle" style={{ opacity: 0 }}>→</text>
						<g data-wire-bit data-pop style={{ opacity: 0 }}>
							<rect className="ej-wire" x={912} y={FT_CHIP_Y - 8} width={272} height={62} rx={8} />
							{WIRE_LINES.map((l, i) => (
								<text key={`w${i}`} className="ej-wire-mono" x={922} y={FT_CHIP_Y + 6 + i * 12}>{l}</text>
							))}
						</g>
						<text data-wire-bit className="ej-wire-code" x={922} y={FT_CHIP_Y + 64} style={{ opacity: 0 }}>wire code -32014 · HTTP 200</text>
					</svg>
				</div>
			</div>
		</div>
	);
}

export default ErrorJourney;

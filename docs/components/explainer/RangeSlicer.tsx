import React, { useEffect, useRef, useState } from "react";
import { gsap } from "gsap";

/**
 * RangeSlicer — bespoke widget for /inside-erpc/evm-translation.
 *
 * Pass 1 (proactive split): a 175,000-block eth_getLogs exceeds the effective
 * upstream threshold (5,000), so the network pre-forward hook slices it into 35
 * contiguous chunks before any upstream is called. Each chunk is its own
 * request with its own cache key: chips flip "cache HIT" (served locally) or
 * "fetch" (parallel upstream round trip). Results merge in chunk order.
 *
 * Pass 2 (reactive bisect): a range under the threshold is sent whole, the
 * upstream still refuses it (-32012), so the network post-forward hook bisects
 * the block range once and merges the halves through the same writer.
 *
 * SSR-safe: the SVG renders in its final state; GSAP only hydrates motion in
 * useEffect (one timeline, killed/rebuilt on Replay). prefers-reduced-motion
 * keeps the static final state. Styles: styles/deep-dive.css (.cv-dd-root).
 */

export interface RangeSlicerProps {
	autoPlay?: boolean;
	maxWidth?: string;
	caption?: string;
}

const W = 1200;
const H = 584;
const LEFT = 40;
const TRACK = 1120;

const CHUNKS = 35;
/* Deterministic HIT/fetch pattern: 9 chunks fetch, 26 hit. */
const isFetch = (i: number) => i % 4 === 2;

const FILL_HIT = "rgba(52,211,153,0.55)";
const FILL_FETCH = "rgba(96,165,250,0.55)";
const TEXT_HIT = "#8fe6c3";
const TEXT_FETCH = "#9cc3ff";
const STROKE_ARROW = "rgba(96,165,250,0.75)";
const STROKE_HAIRLINE = "rgba(148,163,184,0.5)";
const STROKE_ERR = "rgba(248,113,113,0.8)";

const CHIP_DEFS = [
	{ range: "0x0–0x1387", fetch: false },
	{ range: "0x1388–0x270F", fetch: false },
	{ range: "0x2710–0x3A97", fetch: true },
	{ range: "0x3A98–0x4E1F", fetch: false },
	{ range: "0x4E20–0x61A7", fetch: false },
	{ range: "0x61A8–0x752F", fetch: false },
	{ range: "0x7530–0x88B7", fetch: true },
];

const CHIP_Y = 176;
const CHIP_W = 128;
const CHIP_H = 44;
const CHIP_SLOT = 138;

function Arrow({
	name,
	d,
	head,
	stroke,
}: {
	name: string;
	d: string;
	head: string;
	stroke: string;
}) {
	return (
		<g data-arrow={name}>
			<path data-line d={d} stroke={stroke} strokeWidth={1.5} fill="none" />
			<path data-head d={head} stroke={stroke} strokeWidth={1.5} fill="none" strokeLinecap="round" strokeLinejoin="round" />
		</g>
	);
}

export function RangeSlicer({ autoPlay = true, maxWidth = "1200px", caption }: RangeSlicerProps) {
	const rootRef = useRef<HTMLDivElement>(null);
	const [runId, setRunId] = useState(0);

	useEffect(() => {
		/* No @types/react in this workspace: rootRef is any-typed, so pin the
		   element type explicitly to keep the DOM calls below checked. */
		const root = rootRef.current as HTMLDivElement | null;
		if (!root) return;
		const RM = matchMedia("(prefers-reduced-motion: reduce)").matches;
		/* Static render IS the final state (queued tags carry opacity 0), so
		   reduced motion needs no fixups at all. */
		if (RM) return;

		const q = (name: string) => Array.from(root.querySelectorAll(`[data-rs="${name}"]`));
		const one = (name: string) => root.querySelector(`[data-rs="${name}"]`);

		/* Prepare arrows: measure each line, hide it behind its own dash. */
		const arrows = new Map<string, { line: SVGPathElement | null; head: Element | null }>();
		root.querySelectorAll("[data-arrow]").forEach((g) => {
			const line = g.querySelector<SVGPathElement>("[data-line]");
			const head = g.querySelector("[data-head]");
			if (line) {
				const L = line.getTotalLength();
				gsap.set(line, { strokeDasharray: L, strokeDashoffset: L });
			}
			if (head) gsap.set(head, { autoAlpha: 0 });
			arrows.set(g.getAttribute("data-arrow") ?? "", { line, head });
		});

		const tl = gsap.timeline({ paused: true });

		const rise = (targets: (Element | null)[], at: number, dur = 0.45) => {
			const els = targets.filter(Boolean) as Element[];
			if (!els.length) return;
			gsap.set(els, { autoAlpha: 0, y: -8 });
			tl.to(els, { autoAlpha: 1, y: 0, duration: dur, ease: "power2.out" }, at);
		};
		const fade = (targets: (Element | null)[], at: number, dur = 0.35) => {
			const els = targets.filter(Boolean) as Element[];
			if (!els.length) return;
			gsap.set(els, { autoAlpha: 0 });
			tl.to(els, { autoAlpha: 1, duration: dur }, at);
		};
		const pop = (targets: Element[], at: number, stagger = 0) => {
			if (!targets.length) return;
			gsap.set(targets, { autoAlpha: 0, scale: 0.5, transformOrigin: "50% 50%" });
			tl.to(targets, { autoAlpha: 1, scale: 1, duration: 0.4, ease: "back.out(2)", stagger }, at);
		};
		const draw = (name: string, at: number, dur = 0.4) => {
			const a = arrows.get(name);
			if (!a) return;
			if (a.line) tl.to(a.line, { strokeDashoffset: 0, duration: dur, ease: "none" }, at);
			if (a.head) tl.to(a.head, { autoAlpha: 1, duration: 0.15 }, at + dur * 0.8);
		};

		const segs = q("seg");
		const chips = q("chip");
		const fills = q("chipfill");
		const tags = q("chiptag");
		const waits = q("chipwait");
		const ticks = q("tick");
		const ticks2 = q("tick2");

		/* ── Pass 1 · proactive split ── */
		rise([one("s1"), one("req1")], 0.0);
		draw("conn1", 0.5);
		rise([one("hook1")], 0.55);
		fade([one("striplbl")], 0.9);
		gsap.set(segs, { scaleX: 0, transformOrigin: "left center" });
		tl.to(segs, { scaleX: 1, duration: 0.25, ease: "none", stagger: 0.04 }, 0.95);
		pop(chips, 2.7, 0.07);
		/* Chip flip: queued → cache HIT / fetch. */
		gsap.set(fills, { autoAlpha: 0 });
		gsap.set(tags, { autoAlpha: 0 });
		gsap.set(waits, { autoAlpha: 1 });
		tl.to(fills, { autoAlpha: 1, duration: 0.3, stagger: 0.1 }, 3.7);
		tl.to(waits, { autoAlpha: 0, duration: 0.2, stagger: 0.1 }, 3.7);
		tl.to(tags, { autoAlpha: 1, duration: 0.3, stagger: 0.1 }, 3.85);
		fade([one("parlbl")], 4.9);
		draw("a1", 4.9);
		draw("a2", 5.0);
		rise([one("ups1")], 5.4);
		fade([one("m1frame")], 5.8);
		pop(ticks, 5.9, 0.03);
		fade([one("m1done")], 7.2);
		pop([one("count")].filter(Boolean) as Element[], 7.5);

		/* ── Pass 2 · reactive bisect on -32012 ── */
		rise([one("s2"), one("req2")], 8.1);
		draw("arrow2", 8.5);
		rise([one("ups2")], 8.7);
		draw("errarrow", 9.1, 0.3);
		pop([one("err")].filter(Boolean) as Element[], 9.35);
		fade([one("bisectlbl")], 9.8);
		if (one("req2")) tl.to(one("req2"), { autoAlpha: 0.3, duration: 0.4 }, 9.8);
		const halfA = one("halfA");
		const halfB = one("halfB");
		if (halfA) {
			gsap.set(halfA, { scaleX: 0, transformOrigin: "right center" });
			tl.to(halfA, { scaleX: 1, duration: 0.45, ease: "power2.out" }, 10.0);
		}
		if (halfB) {
			gsap.set(halfB, { scaleX: 0, transformOrigin: "left center" });
			tl.to(halfB, { scaleX: 1, duration: 0.45, ease: "power2.out" }, 10.05);
		}
		draw("ah1", 10.55);
		draw("ah2", 10.6);
		rise([one("ups2b")], 10.6);
		pop([one("okh")].filter(Boolean) as Element[], 11.0);
		fade([one("m2frame")], 11.3);
		pop(ticks2, 11.35, 0.04);
		fade([one("m2done")], 11.6);
		pop([one("note")].filter(Boolean) as Element[], 11.8);

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

	return (
		<div ref={rootRef} className="cv-dd-root dd-widget" data-component="range-slicer" style={{ maxWidth }}>
			<div className="dd-head">
				{caption && <div className="dd-cap">{caption}</div>}
				<button
					type="button"
					className="dd-replay"
					onClick={() => setRunId((n) => n + 1)}
					aria-label="Replay range slicing animation"
				>
					↻ Replay
				</button>
			</div>
			<div className="dd-stage" aria-hidden="true">
				<svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="xMidYMid meet">
					{/* ═══════════ Pass 1 · proactive split ═══════════ */}
					<text data-rs="s1" className="dd-t" x={LEFT} y={32}>
						{"1 · proactive split — sliced before any upstream is called"}
					</text>

					<g data-rs="req1">
						<rect className="dd-bar tone-dim" x={LEFT} y={46} width={TRACK} height={28} rx={7} />
						<text className="dd-t" x={600} y={64} textAnchor="middle">
							{'eth_getLogs { fromBlock: "0x0", toBlock: "0x2AB97" } — 175,000 blocks'}
						</text>
					</g>

					<Arrow name="conn1" d="M 600 74 L 600 82 L 300 82 L 300 90" head="M 295 85 L 300 92 L 305 85" stroke={STROKE_HAIRLINE} />

					<g data-rs="hook1">
						<rect className="dd-bar tone-amber" x={LEFT} y={90} width={520} height={26} rx={7} />
						<text className="dd-ts" x={300} y={107} textAnchor="middle">
							{"networkPreForward_eth_getLogs · threshold 5000 → 35 chunks"}
						</text>
					</g>

					<text data-rs="striplbl" className="dd-tm" x={LEFT} y={132}>
						{"35 contiguous chunks of 5,000 blocks — each its own request, its own cache key"}
					</text>

					{Array.from({ length: CHUNKS }, (_, i) => (
						<rect
							key={`seg${i}`}
							data-rs="seg"
							className={`dd-bar ${isFetch(i) ? "tone-blue" : "tone-green"}`}
							x={LEFT + i * 32}
							y={140}
							width={30}
							height={22}
							rx={4}
						/>
					))}

					{CHIP_DEFS.map((c, i) => {
						const x = LEFT + i * CHIP_SLOT;
						const cx = x + CHIP_W / 2;
						return (
							<g data-rs="chip" key={`chip${i}`}>
								<rect className="dd-bar tone-dim" x={x} y={CHIP_Y} width={CHIP_W} height={CHIP_H} rx={8} />
								<rect
									data-rs="chipfill"
									className={`dd-bar ${c.fetch ? "tone-blue" : "tone-green"}`}
									x={x}
									y={CHIP_Y}
									width={CHIP_W}
									height={CHIP_H}
									rx={8}
								/>
								<text className="dd-tm" x={cx} y={CHIP_Y + 18} textAnchor="middle">
									{c.range}
								</text>
								<text data-rs="chipwait" className="dd-tm" x={cx} y={CHIP_Y + 36} textAnchor="middle" opacity={0}>
									{"queued"}
								</text>
								<text
									data-rs="chiptag"
									className="dd-ts"
									x={cx}
									y={CHIP_Y + 36}
									textAnchor="middle"
									style={{ fill: c.fetch ? TEXT_FETCH : TEXT_HIT }}
								>
									{c.fetch ? "fetch" : "cache HIT"}
								</text>
							</g>
						);
					})}
					<g data-rs="chip">
						<rect className="dd-bar tone-dim" x={LEFT + 7 * CHIP_SLOT} y={CHIP_Y} width={CHIP_W} height={CHIP_H} rx={8} />
						<text className="dd-tm" x={LEFT + 7 * CHIP_SLOT + CHIP_W / 2} y={CHIP_Y + 26} textAnchor="middle">
							{"… ×28 more"}
						</text>
					</g>

					<text data-rs="parlbl" className="dd-ts" x={660} y={240} textAnchor="middle">
						{"fetch chunks run in parallel — getLogsSplitConcurrency: 10"}
					</text>
					<Arrow name="a1" d="M 380 220 L 380 246" head="M 375 241 L 380 248 L 385 241" stroke={STROKE_ARROW} />
					<Arrow name="a2" d="M 932 220 L 932 246" head="M 927 241 L 932 248 L 937 241" stroke={STROKE_ARROW} />

					<g data-rs="ups1">
						<rect className="dd-bar tone-blue" x={LEFT} y={250} width={TRACK} height={26} rx={7} />
						<text className="dd-ts" x={600} y={267} textAnchor="middle">
							{"upstream — one round trip per fetched chunk (9 of 35)"}
						</text>
					</g>

					<g data-rs="m1frame">
						<text className="dd-t" x={46} y={322}>
							{"["}
						</text>
						<text className="dd-t" x={416} y={322}>
							{"]"}
						</text>
					</g>
					{Array.from({ length: CHUNKS }, (_, i) => (
						<rect
							key={`tick${i}`}
							data-rs="tick"
							x={62 + i * 10}
							y={306}
							width={6}
							height={16}
							rx={2}
							fill={isFetch(i) ? FILL_FETCH : FILL_HIT}
						/>
					))}
					<g data-rs="m1done">
						<text className="dd-ok" x={436} y={322} fontSize={13}>
							{"✓"}
						</text>
						<text className="dd-ts" x={456} y={322}>
							{"merged in chunk order — one JSON array"}
						</text>
					</g>

					<g data-rs="count">
						<rect className="dd-bar tone-dim" x={720} y={300} width={440} height={26} rx={7} />
						<text className="dd-ts" x={940} y={317} textAnchor="middle">
							{"26 cache HIT · 9 fetched — fromCache needs 35/35 HIT"}
						</text>
					</g>

					{/* ═══════════ Pass 2 · reactive bisect ═══════════ */}
					<text data-rs="s2" className="dd-t" x={LEFT} y={374}>
						{"2 · reactive bisect — the upstream still says too large"}
					</text>

					<g data-rs="req2">
						<rect className="dd-bar tone-dim" x={LEFT} y={388} width={520} height={26} rx={7} />
						<text className="dd-ts" x={300} y={405} textAnchor="middle">
							{"eth_getLogs [0x40000 … 0x40FFF] · 4,096 blocks — sent whole"}
						</text>
					</g>

					<Arrow name="arrow2" d="M 568 401 L 632 401" head="M 627 396 L 634 401 L 627 406" stroke={STROKE_ARROW} />

					<g data-rs="ups2">
						<rect className="dd-bar tone-blue" x={640} y={388} width={240} height={26} rx={7} />
						<text className="dd-ts" x={760} y={405} textAnchor="middle">
							{"upstream"}
						</text>
					</g>

					<Arrow name="errarrow" d="M 888 401 L 912 401" head="M 907 396 L 914 401 L 907 406" stroke={STROKE_ERR} />

					<g data-rs="err">
						<rect className="dd-bar tone-crimson" x={920} y={388} width={240} height={26} rx={7} />
						<text className="dd-ts" x={1040} y={405} textAnchor="middle">
							{"✕ -32012 · request too large"}
						</text>
					</g>

					<text data-rs="bisectlbl" className="dd-tm" x={LEFT} y={436}>
						{"bisect: block_range first — then the address list, then topics[0]"}
					</text>

					<g data-rs="halfA">
						<rect className="dd-bar tone-amber" x={LEFT} y={446} width={250} height={26} rx={7} />
						<text className="dd-tm" x={165} y={463} textAnchor="middle">
							{"[0x40000–0x407FF]"}
						</text>
					</g>
					<g data-rs="halfB">
						<rect className="dd-bar tone-amber" x={310} y={446} width={250} height={26} rx={7} />
						<text className="dd-tm" x={435} y={463} textAnchor="middle">
							{"[0x40800–0x40FFF]"}
						</text>
					</g>

					<Arrow name="ah1" d="M 568 452 L 632 452" head="M 627 447 L 634 452 L 627 457" stroke={STROKE_ARROW} />
					<Arrow name="ah2" d="M 568 470 L 632 470" head="M 627 465 L 634 470 L 627 475" stroke={STROKE_ARROW} />

					<g data-rs="ups2b">
						<rect className="dd-bar tone-blue" x={640} y={446} width={240} height={26} rx={7} />
						<text className="dd-ts" x={760} y={463} textAnchor="middle">
							{"upstream ×2"}
						</text>
					</g>

					<g data-rs="okh">
						<rect className="dd-bar tone-green" x={920} y={446} width={240} height={26} rx={7} />
						<text className="dd-ts" x={1040} y={463} textAnchor="middle">
							{"both halves ✓"}
						</text>
					</g>

					<g data-rs="m2frame">
						<text className="dd-t" x={46} y={516}>
							{"["}
						</text>
						<text className="dd-t" x={148} y={516}>
							{"]"}
						</text>
					</g>
					{Array.from({ length: 8 }, (_, i) => (
						<rect key={`tick2${i}`} data-rs="tick2" x={62 + i * 10} y={500} width={6} height={16} rx={2} fill={FILL_FETCH} />
					))}
					<g data-rs="m2done">
						<text className="dd-ok" x={168} y={516} fontSize={13}>
							{"✓"}
						</text>
						<text className="dd-ts" x={188} y={516}>
							{"same merge path — GetLogsMultiResponseWriter"}
						</text>
					</g>

					<g data-rs="note">
						<rect className="dd-bar tone-dim" x={LEFT} y={540} width={TRACK} height={28} rx={7} />
						<text className="dd-ts" x={600} y={558} textAnchor="middle">
							{"halves carry ParentRequestId — the hook splits a request once; a half still too large surfaces the original error"}
						</text>
					</g>
				</svg>
			</div>
		</div>
	);
}

export default RangeSlicer;

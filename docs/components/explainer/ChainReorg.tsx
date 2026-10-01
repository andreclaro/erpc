import React, { useEffect, useRef, useState } from "react";
import { gsap } from "gsap";

/**
 * ChainReorg — bespoke widget for /inside-erpc/finality-and-caching. A chain
 * of 10 blocks sits under a "finalized tip" horizon; cache-entry chips hang
 * below individual blocks. On play: tip blocks arrive → a re-org orphans the
 * last two (old fade down, replacements slide in) → the orphaned realtime
 * chip is age-guard-rejected and vanishes → the horizon advances and the
 * surviving unfinalized chip still HITs (finalized reads also consult
 * unfinalized policies). Below: one cache key's journey from "latest" to a
 * finality-tagged entry.
 *
 * SSR-safe: the SVG renders statically (initial state); GSAP hydrates motion
 * in useEffect. The stage remounts on Replay so text swaps reset for free.
 * prefers-reduced-motion shows the final state with no animation.
 * Styles: styles/dd-finality.css (scoped to .cv-dd-root.dd-chainreorg).
 */

export interface ChainReorgProps {
	caption?: string;
	autoPlay?: boolean;
	maxWidth?: string;
}

const W = 1200;
const H = 380;

// Chain geometry: 10 blocks, 0x3f9a20..0x3f9a29; finalized tip = 0x3f9a25.
const BLOCK_W = 92;
const BLOCK_H = 46;
const BLOCK_Y = 73;
const PITCH = 106;
const X0 = 40;
const HORIZON_X = 669; // between block 5 (0x3f9a25) and block 6
const FORK_I = 8; // re-org orphans blocks 8 and 9 (0x3f9a28, 0x3f9a29)

const bx = (i: number) => X0 + i * PITCH;
const cx = (i: number) => bx(i) + BLOCK_W / 2;
const blockNum = (i: number) => `0x${(0x3f9a20 + i).toString(16)}`;

const STRIP: { step: string; l1: string; l2: string; sub: string }[] = [
	{ step: "1 · request", l1: "eth_getBlockByNumber", l2: '("latest")', sub: "client request" },
	{ step: "2 · tag → concrete hex", l1: 'params[0] := "0x3f9a29"', l2: "highest known tip", sub: "evm/json_rpc.go:92" },
	{ step: "3 · finality computed", l1: "finality = realtime", l2: '"latest" is a moving tag', sub: "erpc/networks.go:2594" },
	{ step: "4 · async write", l1: "SET · policy: realtime", l2: "ttl-bounded · after response", sub: "erpc/networks.go:2426" },
	{ step: "5 · after the re-org", l1: '"latest" → 0x3f9a29′', l2: "new key → MISS · refetch", sub: "stale entry dies with its TTL" },
];
const STRIP_W = 205;
const STRIP_PITCH = 232;
const STRIP_X0 = 34;
const STRIP_Y = 288;

export function ChainReorg({ caption, autoPlay = true, maxWidth = "1200px" }: ChainReorgProps) {
	const rootRef = useRef<HTMLDivElement>(null);
	const [runId, setRunId] = useState(0);

	useEffect(() => {
		const root = rootRef.current;
		if (!root) return;
		const RM = matchMedia("(prefers-reduced-motion: reduce)").matches;
		const one = (s: string) => root.querySelector(s);
		const all = (s: string) => root.querySelectorAll(s);

		const keeps = all("[data-keep]");
		const conns = all("[data-conn]");
		const orphs = all("[data-orph]");
		const oconns = all("[data-oconn]");
		const repls = all("[data-repl]");
		const rconns = all("[data-rconn]");
		const chips = all("[data-chip]");
		const chipB = one("[data-chip='b']");
		const chipBL2 = one("[data-chip-b-l2]");
		const chipC = one("[data-chip='c']");
		const chipCL2 = one("[data-chip-c-l2]");
		const fork = one("[data-fork]");
		const horizon = one("[data-horizon]");
		const horizonLabel = one("[data-horizon-label]");
		const zoneG = one("[data-zone-g]");
		const zoneA = one("[data-zone-a]");
		const strips = all("[data-strip]");
		const arrows = all("[data-strip-arrow]");

		const CHIP_B_FINAL = "finalized read → still HIT ✓";
		const CHIP_C_STALE = "re-org → stale, served ≤ ttl";
		const CHIP_C_DEAD = "age > ttl → rejected ✕";
		const HORIZON_FINAL = "finalized tip 0x3f9a26";

		if (RM) {
			// Final state, no motion: re-org done, horizon advanced, stale chip gone.
			gsap.set([orphs, oconns], { autoAlpha: 0 });
			gsap.set([repls, rconns], { autoAlpha: 1, y: 0, scaleX: 1 });
			gsap.set([chips, fork, strips, arrows], { autoAlpha: 1, scale: 1, y: 0 });
			gsap.set(chipC, { autoAlpha: 0 });
			if (chipBL2) {
				chipBL2.textContent = CHIP_B_FINAL;
				chipBL2.setAttribute("class", "cr-chip-l2 ok");
			}
			if (chipB) gsap.set(chipB.querySelector("rect"), { stroke: "rgba(52,211,153,0.8)", fill: "rgba(52,211,153,0.12)" });
			if (chipCL2) {
				chipCL2.textContent = CHIP_C_DEAD;
				chipCL2.setAttribute("class", "cr-chip-l2 err");
			}
			if (horizonLabel) horizonLabel.textContent = HORIZON_FINAL;
			gsap.set(horizon, { x: PITCH });
			gsap.set(zoneG, { attr: { width: 751 } });
			gsap.set(zoneA, { attr: { x: 775, width: 345 } });
			return;
		}

		// Initial states (stage remounts on replay, so texts are already fresh).
		gsap.set([keeps, conns, orphs, oconns], { autoAlpha: 0, x: 46 });
		gsap.set(chips, { autoAlpha: 0, scale: 0.5 });
		gsap.set(fork, { autoAlpha: 0, scale: 0.3 });
		gsap.set(repls, { autoAlpha: 0, y: -26 });
		gsap.set(rconns, { autoAlpha: 1, scaleX: 0, transformOrigin: "left center" });
		gsap.set(strips, { autoAlpha: 0, y: 12 });
		gsap.set(arrows, { autoAlpha: 0 });

		const tl = gsap.timeline({ paused: true });

		// 1) Chain fills in up to the fork point; tip blocks keep arriving.
		tl.to([keeps, conns], { autoAlpha: 1, x: 0, duration: 0.35, stagger: 0.06, ease: "power2.out" }, 0);
		tl.to([orphs, oconns], { autoAlpha: 1, x: 0, duration: 0.35, stagger: 0.12, ease: "power2.out" }, 0.9);

		// 2) Cache chips pop in under their blocks.
		tl.to(chips, { autoAlpha: 1, scale: 1, duration: 0.4, stagger: 0.2, ease: "back.out(2.2)" }, 1.5);

		// 3) Cache-key journey, stages 1–4.
		tl.to([strips[0], strips[1], strips[2], strips[3]], { autoAlpha: 1, y: 0, duration: 0.4, stagger: 0.5, ease: "power2.out" }, 2.3);
		tl.to([arrows[0], arrows[1], arrows[2]], { autoAlpha: 1, duration: 0.25, stagger: 0.5 }, 2.6);

		// 4) Re-org: fork marker, orphaned blocks flush crimson then drop out.
		tl.to(fork, { autoAlpha: 1, scale: 1, duration: 0.4, ease: "back.out(3)" }, 4.9);
		tl.to(all("[data-orph] rect"), { fill: "rgba(248,113,113,0.30)", stroke: "rgba(248,113,113,0.85)", duration: 0.25 }, 5.4);
		tl.to(orphs, { y: 22, autoAlpha: 0, duration: 0.5, ease: "power2.in", stagger: 0.08 }, 5.8);
		tl.to(oconns, { autoAlpha: 0, duration: 0.3 }, 5.8);

		// 5) Replacement blocks slide in on the canonical fork.
		tl.to(repls, { autoAlpha: 1, y: 0, duration: 0.5, stagger: 0.15, ease: "back.out(1.6)" }, 6.3);
		tl.to(rconns, { scaleX: 1, duration: 0.3, stagger: 0.15 }, 6.4);

		// 6) The orphaned realtime chip is bounded by its TTL, then rejected.
		tl.call(() => { if (chipCL2) chipCL2.textContent = CHIP_C_STALE; }, [], 6.4);
		tl.call(() => {
			if (chipCL2) {
				chipCL2.textContent = CHIP_C_DEAD;
				chipCL2.setAttribute("class", "cr-chip-l2 err");
			}
		}, [], 7.3);
		tl.to(chipC, { y: 14, autoAlpha: 0, duration: 0.5, ease: "power2.in" }, 7.9);

		// 7) Horizon advances past the surviving chip's block.
		tl.to(zoneG, { attr: { width: 751 }, duration: 0.6, ease: "power2.inOut" }, 6.9);
		tl.to(zoneA, { attr: { x: 775, width: 345 }, duration: 0.6, ease: "power2.inOut" }, 6.9);
		tl.to(horizon, { x: PITCH, duration: 0.6, ease: "power2.inOut" }, 6.9);
		tl.call(() => { if (horizonLabel) horizonLabel.textContent = HORIZON_FINAL; }, [], 7.3);

		// 8) Surviving unfinalized chip still HITs on finalized reads.
		tl.to(chipB ? chipB.querySelector("rect") : null, { stroke: "rgba(52,211,153,0.8)", fill: "rgba(52,211,153,0.12)", duration: 0.4 }, 7.5);
		tl.call(() => {
			if (chipBL2) {
				chipBL2.textContent = CHIP_B_FINAL;
				chipBL2.setAttribute("class", "cr-chip-l2 ok");
			}
		}, [], 7.6);
		tl.to(chipB, { scale: 1.05, duration: 0.18, yoyo: true, repeat: 1 }, 7.7);

		// 9) Strip finale + finalized chip persists.
		tl.to(arrows[3], { autoAlpha: 1, duration: 0.25 }, 8.3);
		tl.to(strips[4], { autoAlpha: 1, y: 0, duration: 0.4, ease: "power2.out" }, 8.4);
		tl.to(one("[data-chip='a']"), { scale: 1.05, duration: 0.18, yoyo: true, repeat: 1 }, 8.8);

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
		<div ref={rootRef} className="cv-dd-root dd-widget dd-chainreorg" data-component="chain-reorg" style={{ maxWidth }}>
			<div className="dd-head">
				<div style={{ flex: 1, minWidth: 200 }}>
					<div className="dd-wtitle">A re-org meets the cache</div>
					{caption && <div className="dd-cap">{caption}</div>}
				</div>
				<button type="button" className="dd-replay" onClick={() => setRunId((n) => n + 1)} aria-label="Replay re-org animation">
					↻ Replay
				</button>
			</div>
			<div className="dd-stage" aria-hidden="true">
				<div key={runId}>
					<svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="xMidYMid meet" role="img" aria-label="Animated chain re-org vs. cache entries">
						{/* finality zones */}
						<rect data-zone-g className="cr-zone-g" x={24} y={52} width={645} height={150} rx={8} />
						<rect data-zone-a className="cr-zone-a" x={669} y={52} width={451} height={150} rx={8} />
						<text className="cr-zone-label-g" x={34} y={44}>
							FINALIZED · IMMUTABLE · NO TTL
						</text>
						<text className="cr-zone-label-a" x={1110} y={44} textAnchor="end">
							ABOVE THE HORIZON · RE-ORG-ABLE · TTL-BOUNDED
						</text>

						{/* finalized-tip horizon (slides one block right as finality advances) */}
						<g data-horizon>
							<line className="cr-horizon" x1={HORIZON_X} y1={56} x2={HORIZON_X} y2={198} />
							<text data-horizon-label className="cr-horizon-label" x={HORIZON_X} y={216} textAnchor="middle">
								finalized tip 0x3f9a25
							</text>
						</g>

						{/* connectors between canonical blocks */}
						{Array.from({ length: 7 }, (_, i) => (
							<line key={`c${i}`} data-conn className="cr-conn" x1={bx(i) + BLOCK_W} y1={96} x2={bx(i + 1)} y2={96} />
						))}
						{/* connectors into the orphaned segment */}
						{[7, 8].map((i) => (
							<line key={`oc${i}`} data-oconn className="cr-conn" x1={bx(i) + BLOCK_W} y1={96} x2={bx(i + 1)} y2={96} />
						))}
						{/* connectors redrawn to the replacement blocks */}
						{[7, 8].map((i) => (
							<line key={`rc${i}`} data-rconn data-pop className="cr-conn repl" style={{ opacity: 0 }} x1={bx(i) + BLOCK_W} y1={96} x2={bx(i + 1)} y2={96} />
						))}

						{/* blocks 0..7 (survive the re-org) */}
						{Array.from({ length: 8 }, (_, i) => (
							<g key={`b${i}`} data-keep>
								<rect className={`cr-block ${i <= 5 ? "fin" : "unf"}`} x={bx(i)} y={BLOCK_Y} width={BLOCK_W} height={BLOCK_H} rx={7} />
								<text className="cr-block-label" x={cx(i)} y={100} textAnchor="middle">
									{blockNum(i)}
								</text>
							</g>
						))}

						{/* blocks 8,9 — orphaned by the re-org */}
						{[8, 9].map((i) => (
							<g key={`o${i}`} data-orph>
								<rect className="cr-block unf" x={bx(i)} y={BLOCK_Y} width={BLOCK_W} height={BLOCK_H} rx={7} />
								<text className="cr-block-label" x={cx(i)} y={100} textAnchor="middle">
									{blockNum(i)}
								</text>
							</g>
						))}

						{/* replacement blocks on the new canonical fork */}
						{[8, 9].map((i) => (
							<g key={`r${i}`} data-repl data-pop style={{ opacity: 0 }}>
								<rect className="cr-block repl" x={bx(i)} y={BLOCK_Y} width={BLOCK_W} height={BLOCK_H} rx={7} />
								<text className="cr-block-label" x={cx(i)} y={100} textAnchor="middle">
									{`${blockNum(i)}′`}
								</text>
							</g>
						))}

						{/* fork marker */}
						<g data-fork data-pop style={{ opacity: 0 }}>
							<text className="cr-fork-glyph" x={881} y={103} textAnchor="middle">
								⚡
							</text>
							<text className="cr-fork-label" x={881} y={132} textAnchor="middle">
								RE-ORG
							</text>
						</g>

						{/* cache-entry chips */}
						<g data-chip="a" data-pop style={{ opacity: 0 }}>
							<line className="cr-chip-link" x1={cx(1)} y1={119} x2={cx(1)} y2={146} />
							<rect className="cr-chip fin" x={82} y={146} width={220} height={36} rx={8} />
							<text className="cr-chip-l1" x={192} y={160} textAnchor="middle">
								getBlockByNumber(0x3f9a21)
							</text>
							<text className="cr-chip-l2 ok" x={192} y={175} textAnchor="middle">
								finalized · no TTL
							</text>
						</g>
						<g data-chip="b" data-pop style={{ opacity: 0 }}>
							<line className="cr-chip-link" x1={cx(6)} y1={119} x2={cx(6)} y2={146} />
							<rect className="cr-chip unf" x={624} y={146} width={196} height={36} rx={8} />
							<text className="cr-chip-l1" x={722} y={160} textAnchor="middle">
								getBalance(0xde…, 0x3f9a26)
							</text>
							<text data-chip-b-l2 className="cr-chip-l2 warn" x={722} y={175} textAnchor="middle">
								unfinalized · TTL-bounded
							</text>
						</g>
						<g data-chip="c" data-pop style={{ opacity: 0 }}>
							<line className="cr-chip-link" x1={cx(9)} y1={119} x2={cx(9)} y2={146} />
							<rect className="cr-chip rt" x={930} y={146} width={226} height={36} rx={8} />
							<text className="cr-chip-l1" x={1043} y={160} textAnchor="middle">
								getBlockByNumber(&quot;latest&quot;)
							</text>
							<text data-chip-c-l2 className="cr-chip-l2 warn" x={1043} y={175} textAnchor="middle">
								realtime · age-guarded
							</text>
						</g>

						{/* cache-key evolution strip */}
						<text className="cr-strip-title" x={24} y={266}>
							One cache key&apos;s journey — from a moving tag to a finality-tagged entry
						</text>
						{STRIP.map((s, i) => (
							<g key={`s${i}`} data-strip data-pop style={{ opacity: 0 }}>
								<rect className="cr-strip-chip" x={STRIP_X0 + i * STRIP_PITCH} y={STRIP_Y} width={STRIP_W} height={58} rx={8} />
								<text className="cr-strip-step" x={STRIP_X0 + i * STRIP_PITCH + 12} y={STRIP_Y + 15}>
									{s.step}
								</text>
								<text className="cr-strip-mono" x={STRIP_X0 + i * STRIP_PITCH + 12} y={STRIP_Y + 32}>
									{s.l1}
								</text>
								<text className="cr-strip-dim" x={STRIP_X0 + i * STRIP_PITCH + 12} y={STRIP_Y + 47}>
									{s.l2}
								</text>
								<text className="cr-strip-sub" x={STRIP_X0 + i * STRIP_PITCH + 12} y={362}>
									{s.sub}
								</text>
							</g>
						))}
						{Array.from({ length: 4 }, (_, i) => (
							<text key={`a${i}`} data-strip-arrow className="cr-strip-arrow" x={STRIP_X0 + STRIP_W + i * STRIP_PITCH + 13} y={322} textAnchor="middle" style={{ opacity: 0 }}>
								→
							</text>
						))}
					</svg>
				</div>
			</div>
		</div>
	);
}

export default ChainReorg;

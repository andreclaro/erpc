import React, { useEffect, useRef } from "react";
import { PIPE_SCENARIOS, PIPE_STAGES, PIPE_SVG_HTML, initPipeline } from "./pipeline-explorer.internals";

export interface PipelineExplorerProps {
	/** Max width in CSS units. Default `1200px`. */
	maxWidth?: string;
}

/**
 * Interactive request-flow explorer. The SVG diagram + animation engine live
 * in `./pipeline-explorer.internals.ts`; styles in `styles/explainer.css`
 * (scoped to .cv-pipe-root, imported globally from `_app.tsx`).
 *
 * The SVG itself is aria-hidden: every piece of information it animates is
 * also available through the HTML controls below it (scenario buttons, stage
 * chips, event log, stage detail panel), which are keyboard accessible.
 *
 * Interaction surface:
 *   • Pick a scenario (or press Play) to send a request through the pipeline.
 *   • Click a stage card in the diagram, or a stage chip below it, to open
 *     its detail panel (what it does + the real config keys).
 */
export function PipelineExplorer({ maxWidth = "1200px" }: PipelineExplorerProps) {
	const rootRef = useRef<HTMLDivElement>(null);

	useEffect(() => {
		if (!rootRef.current) return;
		return initPipeline(rootRef.current);
	}, []);

	return (
		<div
			ref={rootRef}
			className="cv-pipe-root"
			data-component="pipeline-explorer"
			style={{ width: "100%", maxWidth, margin: "1.5rem auto 2rem" }}
		>
			<div className="pipe-controls">
				<div className="pipe-scenarios" role="group" aria-label="Request scenarios">
					{PIPE_SCENARIOS.map((s) => (
						<button
							key={s.id}
							type="button"
							className="pipe-scenario"
							data-scenario={s.id}
							aria-pressed={s.id === "cold" ? "true" : "false"}
						>
							{s.label}
						</button>
					))}
				</div>
				<button type="button" className="pipe-play" data-play>
					▶ Play
				</button>
			</div>

			<div className="pipe-svg" aria-hidden="true">
				<div dangerouslySetInnerHTML={{ __html: PIPE_SVG_HTML }} />
			</div>

			<div className="pipe-stages" role="group" aria-label="Pipeline stages — open details">
				{PIPE_STAGES.map((st) => (
					<button
						key={st.id}
						type="button"
						className="pipe-stage-chip"
						data-stage-chip={st.id}
						aria-pressed="false"
					>
						<span className="chip-num">{st.num}</span>
						{st.name}
					</button>
				))}
			</div>

			<div className="pipe-log" aria-live="polite">
				<ol className="pipe-log-lines">
					<li className="tone-dim">
						Pick a scenario and press Play — the packet traces the real code path through eRPC.
					</li>
				</ol>
			</div>

			<div className="pipe-detail" hidden />
		</div>
	);
}

export default PipelineExplorer;

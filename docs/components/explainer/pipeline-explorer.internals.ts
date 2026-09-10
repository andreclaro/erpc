// @ts-nocheck
/**
 * "Inside eRPC" pipeline explorer — SVG markup + animation engine.
 *
 * Split follows the hero diagram convention:
 *   - styles/explainer.css (CSS, scoped to .cv-pipe-root)
 *   - components/explainer/pipeline-explorer.internals.ts (this file)
 *   - components/explainer/PipelineExplorer.tsx (the React wrapper)
 *
 * The engine is GSAP-driven: each scenario is a gsap.timeline(). Straight
 * wire segments are direct x/y tweens (they sit exactly on the wires' own
 * lines); the genuinely curved rails (network→failsafe, sweep→alchemy /
 * self-hosted) are followed with MotionPathPlugin. Pulses are always placed
 * on a curved rail's exact start/end point before riding it, because
 * MotionPathPlugin's align shifts the path to the element's current spot.
 * ts-nocheck keeps TS out of relitigating the imperative engine's types.
 */

import { gsap } from "gsap";
import { MotionPathPlugin } from "gsap/MotionPathPlugin";

export const PIPE_SCENARIOS = [
	{ id: "cache", label: "Cache hit" },
	{ id: "cold", label: "Cold path" },
	{ id: "hedge", label: "Hedge race" },
	{ id: "retry", label: "Retry on error" },
	{ id: "consensus", label: "Consensus dispute" },
];

export const PIPE_STAGES = [
	{
		id: "s1",
		num: 1,
		name: "HTTP ingress",
		file: "erpc/http_server.go",
		bullets: [
			"Parses the URL /<project>/<architecture>/<chain> — here main/evm/42161. Domain aliasing rules can shorten it.",
			"Batch bodies ([{...},{...}]) fan out to one goroutine per JSON-RPC call and reassemble before the response is written.",
			"gzip, CORS, trusted-proxy client-IP resolution, and X-ERPC-Version headers are all handled at this layer.",
		],
		yaml: `server:
  httpPortV4: 4000
  enableGzip: true
  aliasing:
    rules:
      - matchDomain: rpc.example.com
        serveProject: main
        serveArchitecture: evm
        serveChain: 42161`,
	},
	{
		id: "s2",
		num: 2,
		name: "Auth",
		file: "auth/registry.go",
		bullets: [
			"Strategies are tried in order until one succeeds: secret, database, jwt, siwe, network (IP allowlist).",
			"Each strategy can scope methods (allowMethods / ignoreMethods) and attach its own rateLimitBudget.",
			"No auth block means an open project — fine for internal deployments behind a VPC.",
		],
		yaml: `projects:
  - id: main
    auth:
      strategies:
        - type: secret
          secret:
            value: \${MAIN_API_KEY}
          # per-strategy method gating + budget
          allowMethods: ["eth_*", "net_*"]
          rateLimitBudget: default-tier`,
	},
	{
		id: "s3",
		num: 3,
		name: "Project",
		file: "erpc/projects.go",
		bullets: [
			"Acquires the project-level rateLimitBudget permit before anything downstream runs.",
			"EVM pre-forward hooks can answer early: eth_chainId from config, proactive eth_getLogs / trace_filter range splitting.",
			"After the response, shadow upstreams get a sampled copy of the traffic for comparison — never served to the client.",
		],
		yaml: `projects:
  - id: main
    rateLimitBudget: frontend-tier
    upstreams:
      - id: alchemy
        endpoint: https://eth-mainnet.g.alchemy.com/v2/KEY
      - id: quicknode
        endpoint: https://example.quiknode.pro/KEY
      - id: self-hosted
        endpoint: http://10.0.0.11:8545`,
	},
	{
		id: "s4",
		num: 4,
		name: "Network",
		file: "erpc/networks.go",
		bullets: [
			"Short-circuits in order: static responses → multiplexer (identical in-flight calls share one upstream request) → cache read.",
			"Cache policies match on network + method + params + finality. Finalized data is immutable (cached forever); unfinalized data gets short TTLs — that is what makes the cache re-org safe.",
			"The selection policy then scores and orders upstreams from live health-tracker metrics; a request for a future block short-circuits to null without touching any upstream.",
		],
		yaml: `database:
  evmJsonRpcCache:
    connectors:
      - { id: hot, driver: memory }
      - { id: cold, driver: redis, redis: { addr: redis-cache:6379 } }
    policies:
      # finalized = immutable: no ttl → kept forever
      - { connector: cold, finality: finalized }
      - { connector: hot, finality: finalized }
      # near-tip data: short TTL, re-org safe
      - { connector: hot, finality: unfinalized, ttl: 5s }`,
	},
	{
		id: "s5",
		num: 5,
		name: "Failsafe executor",
		file: "erpc/network_executor.go",
		bullets: [
			"One executor per failsafe[] entry; the first whose matchMethod / matchFinality matches the request runs.",
			"Policies nest in a fixed order: timeout( consensus( retry( hedge( upstream )))). Circuit breaker is upstream-scope only.",
			"Delays are adaptive: hedge delay and timeout can track a latency quantile; data-unavailable retries wait ~one block time.",
		],
		yaml: `networks:
  - architecture: evm
    evm: { chainId: 42161 }
    failsafe:
      - matchMethod: "*"
        timeout: { duration: 10s }
        retry: { maxAttempts: 3, backoffMaxDelay: 1s }
        hedge:
          maxCount: 1
          delay: { quantile: 0.7, min: 100ms, max: 2s }`,
	},
	{
		id: "s6",
		num: 6,
		name: "Upstream sweep",
		file: "upstream/upstream.go",
		bullets: [
			"Each candidate is gated before use: block availability for the requested block, then its own rateLimitBudget permit.",
			"The circuit breaker lives at this scope — repeated failures open it and the upstream is skipped until the half-open probe succeeds.",
			"Every attempt's outcome and latency feed the health tracker, which the selection policy reads on the next request.",
		],
		yaml: `upstreams:
  - id: alchemy
    endpoint: https://eth-mainnet.g.alchemy.com/v2/KEY
    failsafe:
      - matchMethod: "*"
        timeout: { duration: 6s }
        circuitBreaker:
          failureThresholdCount: 8
          failureThresholdCapacity: 10
          halfOpenAfter: 5s
          successThresholdCount: 3
          successThresholdCapacity: 5`,
	},
	{
		id: "s7",
		num: 7,
		name: "Response",
		file: "erpc/http_server.go",
		bullets: [
			"The JSON-RPC id is rewritten byte-for-byte — 64-bit integer ids survive the round trip intact.",
			"The cache write is asynchronous with a finality-aware TTL; a client disconnect never aborts it.",
			"X-ERPC-* headers report cache status, winning upstream, attempts / retries / hedges, and consensus slots.",
		],
		yaml: `server:
  # full per-attempt trace in X-ERPC-* headers
  executionHeaders: all   # all | summary | off`,
	},
];

export const PIPE_SVG_HTML = `<svg viewBox="0 0 1200 700" preserveAspectRatio="xMidYMid meet">
	<defs>
		<linearGradient id="pipe-grad" x1="0" x2="1" y1="0" y2="0">
			<stop offset="0%" stop-color="#60a5fa"/>
			<stop offset="100%" stop-color="#3b82f6"/>
		</linearGradient>
	</defs>

	<!-- ════════ RAILS ════════ -->
	<g id="pipe-rails">
		<path id="rail-c-s1" class="flow" d="M145 110 L190 110"/>
		<path id="rail-s1-s2" class="flow" d="M350 110 L385 110"/>
		<path id="rail-s2-s3" class="flow" d="M545 110 L580 110"/>
		<path id="rail-s3-s4" class="flow" d="M740 110 L775 110"/>
		<path id="rail-s4-fs" class="flow" d="M935 110 C 1030 110 1045 170 1045 230 C 1045 272 820 285 635 285"/>
		<path id="rail-fs-in" class="flow faint" d="M635 285 L635 405"/>
		<path id="rail-fs-sw" class="flow" d="M635 418 L635 478"/>
		<path id="rail-sw-u0" class="flow" d="M635 511 C 635 545 300 545 300 560"/>
		<path id="rail-sw-u1" class="flow" d="M635 511 L635 560"/>
		<path id="rail-sw-u2" class="flow" d="M635 511 C 635 545 970 545 970 560"/>
		<path id="rail-c-resp" class="flow" d="M85 142 L85 200"/>
	</g>

	<!-- ════════ CLIENT ════════ -->
	<g id="stage-client" class="stage-card is-client">
		<rect class="card-bg client-bg" x="25" y="78" width="120" height="64" rx="12"/>
		<text class="t-label" x="85" y="103" text-anchor="middle">Your app</text>
		<text class="t-mono-s" x="85" y="124" text-anchor="middle">eth_getBalance</text>
	</g>

	<!-- ════════ STAGE CARDS ════════ -->
	<g id="stage-s1" class="stage-card" data-stage="s1">
		<rect class="card-bg" x="190" y="78" width="160" height="64" rx="12"/>
		<circle class="stage-num" cx="208" cy="96" r="9"/>
		<text class="stage-num-t" x="208" y="100" text-anchor="middle">1</text>
		<text class="t-label" x="223" y="100">HTTP ingress</text>
		<text class="t-mono-s" x="206" y="126">http_server.go</text>
	</g>
	<g id="stage-s2" class="stage-card" data-stage="s2">
		<rect class="card-bg" x="385" y="78" width="160" height="64" rx="12"/>
		<circle class="stage-num" cx="403" cy="96" r="9"/>
		<text class="stage-num-t" x="403" y="100" text-anchor="middle">2</text>
		<text class="t-label" x="418" y="100">Auth</text>
		<text class="t-mono-s" x="401" y="126">auth/registry.go</text>
	</g>
	<g id="stage-s3" class="stage-card" data-stage="s3">
		<rect class="card-bg" x="580" y="78" width="160" height="64" rx="12"/>
		<circle class="stage-num" cx="598" cy="96" r="9"/>
		<text class="stage-num-t" x="598" y="100" text-anchor="middle">3</text>
		<text class="t-label" x="613" y="100">Project</text>
		<text class="t-mono-s" x="596" y="126">projects.go</text>
	</g>
	<g id="stage-s4" class="stage-card" data-stage="s4">
		<rect class="card-bg" x="775" y="78" width="160" height="64" rx="12"/>
		<circle class="stage-num" cx="793" cy="96" r="9"/>
		<text class="stage-num-t" x="793" y="100" text-anchor="middle">4</text>
		<text class="t-label" x="808" y="100">Network</text>
		<text class="t-mono-s" x="791" y="126">networks.go</text>
	</g>
	<text id="cache-chip" class="cache-chip" x="855" y="66" text-anchor="middle"></text>

	<!-- ════════ RESPONSE CHIP ════════ -->
	<g id="stage-s7" class="stage-card" data-stage="s7">
		<rect class="card-bg" x="25" y="200" width="120" height="54" rx="10"/>
		<text class="t-label" x="85" y="222" text-anchor="middle">7 · Response</text>
		<text class="t-mono-s" x="85" y="240" text-anchor="middle">X-ERPC-* headers</text>
	</g>

	<!-- ════════ FAILSAFE NEST ════════ -->
	<g id="stage-s5" class="stage-card" data-stage="s5">
		<text class="t-lane-sub nest-caption" x="300" y="272">5 · FAILSAFE EXECUTOR · network_executor.go</text>
		<rect id="nest-timeout" class="nest-layer" x="300" y="285" width="640" height="170" rx="14"/>
		<text class="nest-label" x="318" y="307">timeout</text>
		<text class="nest-sub" x="318" y="321">duration: 10s</text>
		<rect id="nest-consensus" class="nest-layer" x="330" y="322" width="610" height="118" rx="12"/>
		<text class="nest-label" x="348" y="344">consensus</text>
		<text class="nest-sub" x="348" y="357">agreementThreshold: 2</text>
		<rect id="nest-retry" class="nest-layer" x="360" y="358" width="550" height="68" rx="10"/>
		<text class="nest-label" x="378" y="375">retry</text>
		<text class="nest-sub" x="378" y="389">maxAttempts: 3</text>
		<rect id="nest-hedge" class="nest-layer hedge-pill" x="390" y="392" width="490" height="26" rx="13"/>
		<text class="nest-label hedge-label" x="635" y="409" text-anchor="middle">hedge · maxCount: 1 · delay: quantile 0.7</text>
	</g>

	<!-- ════════ SWEEP ════════ -->
	<g id="stage-s6" class="stage-card" data-stage="s6">
		<circle id="sweep-node" class="sweep-node" cx="635" cy="495" r="16"/>
		<text class="t-label" x="660" y="491">6 · Upstream sweep</text>
		<text class="t-mono-s" x="660" y="508">upstream.go</text>
	</g>
	<text id="agree-chip" class="agree-chip" x="668" y="533">✓ 2/3 agree — threshold met</text>

	<!-- ════════ UPSTREAMS ════════ -->
	<g id="up-u0" class="up-card">
		<rect class="card-bg" x="190" y="560" width="220" height="76" rx="12"/>
		<circle class="up-dot" cx="208" cy="582" r="4"/>
		<text class="t-label" x="220" y="586">alchemy</text>
		<text class="t-mono-s" x="206" y="608">p50 34ms · score 96</text>
		<text id="hash-u0" class="hash-chip" x="206" y="628">0x8f…c1</text>
	</g>
	<g id="up-u1" class="up-card">
		<rect class="card-bg" x="525" y="560" width="220" height="76" rx="12"/>
		<circle class="up-dot" cx="543" cy="582" r="4"/>
		<text class="t-label" x="553" y="586">quicknode</text>
		<text class="t-mono-s" x="541" y="608">p50 41ms · score 93</text>
		<text id="hash-u1" class="hash-chip" x="541" y="628">0x8f…c1</text>
	</g>
	<g id="up-u2" class="up-card">
		<rect class="card-bg" x="860" y="560" width="220" height="76" rx="12"/>
		<circle class="up-dot" cx="878" cy="582" r="4"/>
		<text class="t-label" x="888" y="586">self-hosted</text>
		<text class="t-mono-s" x="876" y="608">p50 280ms · score 61</text>
		<text id="hash-u2" class="hash-chip bad" x="876" y="628">0x22…9e</text>
	</g>

	<!-- ════════ DYNAMIC LAYER ════════ -->
	<circle id="burst-a" class="burst" r="10" cx="-50" cy="-50"/>
	<circle id="burst-b" class="burst" r="10" cx="-50" cy="-50"/>
	<text id="mark-x" class="mark-x" x="-50" y="-50">✕</text>
	<g id="pulses"></g>
</svg>`;

/**
 * Mount the pipeline engine against a .cv-pipe-root element.
 * Each scenario is a gsap.timeline(); pulses follow the visible rails via
 * MotionPathPlugin. Returns a cleanup that kills the timeline and listeners.
 */
export function initPipeline(root: HTMLElement): () => void {
	gsap.registerPlugin(MotionPathPlugin);

	const SVG = root.querySelector("svg");
	const gid = (id) => root.querySelector("#" + id);
	const NS = "http://www.w3.org/2000/svg";
	const RM = matchMedia("(prefers-reduced-motion: reduce)").matches;

	const els = {
		s1: gid("stage-s1"), s2: gid("stage-s2"), s3: gid("stage-s3"), s4: gid("stage-s4"),
		s5: gid("stage-s5"), s6: gid("stage-s6"), s7: gid("stage-s7"),
		timeout: gid("nest-timeout"), consensus: gid("nest-consensus"),
		retry: gid("nest-retry"), hedge: gid("nest-hedge"),
		hashU0: gid("hash-u0"), hashU1: gid("hash-u1"), hashU2: gid("hash-u2"),
		agreeChip: gid("agree-chip"), cacheChip: gid("cache-chip"),
		burstA: gid("burst-a"), burstB: gid("burst-b"), markX: gid("mark-x"),
		pulses: gid("pulses"),
	};

	const logList = root.querySelector(".pipe-log-lines");
	const playBtn = root.querySelector("[data-play]");
	const detail = root.querySelector(".pipe-detail");

	const state = { scenario: "cold", playing: false, autoPlayed: false };
	let master = null;
	let pulseEls = [];
	let burstFlip = false;
	let observer = null;

	/* ─── Timeline helpers ─── */

	function createPulse(cls) {
		const el = document.createElementNS(NS, "circle");
		el.setAttribute("class", "pulse" + (cls ? " " + cls : ""));
		el.setAttribute("r", "5");
		el.setAttribute("cx", "0");
		el.setAttribute("cy", "0");
		els.pulses.appendChild(el);
		pulseEls.push(el);
		gsap.set(el, { autoAlpha: 0 });
		return el;
	}
	// Travel a rail (optionally reversed) at absolute time t.
	function ride(tl, target, railId, t, dur, rev) {
		tl.to(target, {
			motionPath: {
				path: "#" + railId,
				align: "#" + railId,
				alignOrigin: [0.5, 0.5],
				start: rev ? 1 : 0,
				end: rev ? 0 : 1,
			},
			duration: dur,
			ease: "power1.inOut",
		}, t);
	}
	function move(tl, target, x, y, t, dur) {
		tl.to(target, { x, y, duration: dur || 0.12, ease: "power1.inOut" }, t);
	}
	function throb(tl, target, t, dur) {
		const halfCycles = Math.max(2, Math.round(dur / 0.25));
		tl.to(target, { attr: { r: 7.2 }, duration: 0.25, ease: "sine.inOut", yoyo: true, repeat: halfCycles - 1 }, t);
		tl.set(target, { attr: { r: 5 } }, t + dur);
	}
	function fadeIn(tl, target, t) { tl.to(target, { autoAlpha: 1, duration: 0.18 }, t); }
	function fadeOut(tl, target, t, dur) { tl.to(target, { autoAlpha: 0, duration: dur || 0.5 }, t); }
	function setCls(tl, target, cls, t) {
		tl.call(() => target.setAttribute("class", "pulse" + (cls ? " " + cls : "")), [], t);
	}
	function actOn(tl, el, t0, t1, cls) {
		if (!el) return;
		(cls || "active").split(" ").forEach((c) => {
			tl.call(() => el.classList.add(c), [], t0);
			tl.call(() => el.classList.remove(c), [], t1);
		});
	}
	function actChip(tl, text, cls, t0, t1) {
		tl.call(() => {
			els.cacheChip.textContent = text;
			els.cacheChip.classList.add("show", cls);
		}, [], t0);
		tl.call(() => els.cacheChip.classList.remove("show", cls), [], t1);
	}
	function log(tl, text, tone, t) { tl.call(() => appendLog(text, tone), [], t); }
	function burst(tl, x, y, cls, t) { tl.call(() => fireBurst({ x, y, cls }), [], t); }

	function appendLog(text, tone) {
		const li = document.createElement("li");
		li.setAttribute("class", "tone-" + (tone || "info"));
		li.textContent = text;
		Array.from(logList.children).forEach((c) => c.classList.add("older"));
		logList.appendChild(li);
		while (logList.children.length > 4) logList.removeChild(logList.firstChild);
	}

	function fireBurst(bur) {
		if (bur.cls === "mark-x") {
			const el = els.markX;
			el.setAttribute("x", bur.x);
			el.setAttribute("y", bur.y);
			el.setAttribute("class", "mark-x");
			void el.getBBox();
			el.setAttribute("class", "mark-x show");
			return;
		}
		burstFlip = !burstFlip;
		const el = burstFlip ? els.burstA : els.burstB;
		el.setAttribute("cx", bur.x);
		el.setAttribute("cy", bur.y);
		el.setAttribute("class", "burst");
		void el.getBBox();
		el.setAttribute("class", "burst show " + bur.cls);
	}

	/* ─── Shared choreography ─── */

	/* Outbound prefix: client → HTTP → auth → project → network.
	   Horizontal hops are straight moves along the wires' own y=110 line;
	   MotionPath is reserved for the genuinely curved rails. */
	function prefix(tl, p) {
		let t = 0;
		tl.set(p, { x: 85, y: 110 }, 0);
		fadeIn(tl, p, 0);
		log(tl, 'POST /main/evm/42161 · {"method":"eth_getBalance","params":["0x8ba1…","latest"]}', "info", 0);
		const hop = (cx, dur, stage, logLine) => {
			move(tl, p, cx, 110, t, dur);
			if (stage) actOn(tl, stage, t + dur * 0.55, t + dur + 0.5);
			t += dur;
			throb(tl, p, t, 0.22);
			t += 0.22;
			if (logLine) log(tl, logLine, "info", t);
		};
		hop(270, 0.55, els.s1, 'HTTP ingress — path parsed: project "main" · evm:42161 · single request (no batch)');
		hop(465, 0.5, els.s2, 'auth — strategy "secret" matched · consumer authenticated');
		hop(660, 0.5, els.s3, "project — rate-limit permit ok · EVM pre-forward hooks: no shortcut");
		hop(855, 0.5, null, null);
		actOn(tl, els.s4, t - 0.3, t + 1.2);
		log(tl, "network evm:42161 — static responses: no match · multiplexer: no twin in flight", "info", t);
		return t;
	}

	/* Network card → home along the top wire → response chip. */
	function homeFromNetwork(tl, p, t, respLog) {
		move(tl, p, 85, 110, t, 1.25);
		t += 1.25;
		move(tl, p, 85, 210, t, 0.3);
		t += 0.3;
		actOn(tl, els.s7, t, t + 1.1);
		log(tl, respLog, "ok", t);
		throb(tl, p, t, 0.5);
		t += 0.5;
		fadeOut(tl, p, t, 0.4);
		t += 0.4;
		return t;
	}

	/* Sweep node → straight up through the nest → curved rail back to network → home. */
	function homeFromSweep(tl, p, t, cacheLog, respLog) {
		move(tl, p, 635, 285, t, 0.5);
		t += 0.5;
		ride(tl, p, "rail-s4-fs", t, 0.7, true);
		t += 0.7;
		move(tl, p, 855, 110, t, 0.2);
		t += 0.2;
		if (cacheLog) log(tl, cacheLog, "info", t);
		throb(tl, p, t, 0.15);
		t += 0.15;
		return homeFromNetwork(tl, p, t, respLog);
	}

	/* Nest drill-down with per-layer highlights + logs. Ends at the sweep exit (635,511). */
	function nestDrill(tl, p, t, logs) {
		move(tl, p, 935, 110, t, 0.22);
		t += 0.22;
		ride(tl, p, "rail-s4-fs", t, 0.75, false);
		actOn(tl, els.timeout, t + 0.15, t + 1.6);
		log(tl, logs.timeout, "info", t + 0.15);
		t += 0.75;
		move(tl, p, 635, 478, t, 0.55);
		actOn(tl, els.consensus, t + 0.06, t + 0.62);
		log(tl, logs.consensus, "info", t + 0.06);
		actOn(tl, els.retry, t + 0.2, t + 0.76);
		log(tl, logs.retry, "info", t + 0.2);
		actOn(tl, els.hedge, t + 0.3, t + 0.88);
		log(tl, logs.hedge, "info", t + 0.3);
		t += 0.55;
		move(tl, p, 635, 511, t, 0.1);
		t += 0.1;
		return t;
	}

	function coldOut(tl, p, t, orderLog) {
		actChip(tl, "✗ cache MISS", "miss", t, t + 0.9);
		log(tl, orderLog, "info", t);
		throb(tl, p, t, 0.35);
		return t + 0.35;
	}

	/* ─── Scenario builders (each returns a paused master timeline) ─── */

	function buildCache() {
		const tl = gsap.timeline({ paused: true, onComplete: onDone });
		const p = createPulse("");
		let t = prefix(tl, p);
		actChip(tl, "✓ cache HIT · finalized policy", "ok", t, t + 1.3);
		log(tl, 'cache read — policy matched: method "eth_getBalance" · finality: finalized', "info", t);
		throb(tl, p, t, 0.45);
		t += 0.45;
		burst(tl, 855, 110, "ok", t);
		log(tl, 'HIT — served from connector "hot" (memory) · zero upstream calls', "ok", t);
		setCls(tl, p, "p-green", t);
		homeFromNetwork(tl, p, t, "response — id echoed byte-for-byte · X-ERPC-Cache: HIT");
		return tl;
	}

	function buildCold() {
		const tl = gsap.timeline({ paused: true, onComplete: onDone });
		const p = createPulse("");
		let t = prefix(tl, p);
		t = coldOut(tl, p, t, "cache MISS — selection policy scores: alchemy 96 › quicknode 93 › self-hosted 61");
		t = nestDrill(tl, p, t, {
			timeout: "failsafe executor — timeout 10s wraps the whole invocation",
			consensus: 'consensus — not configured for "*" · pass through',
			retry: "retry — attempt 1 of 3",
			hedge: "hedge armed — fires only if the upstream stalls (delay: quantile 0.7)",
		});
		actOn(tl, els.s6, t, t + 1.4);
		log(tl, "sweep → alchemy: block availability ok · breaker closed · budget permit ok", "info", t);
		ride(tl, p, "rail-sw-u0", t, 0.6, false);
		t += 0.6;
		move(tl, p, 300, 578, t, 0.08);
		t += 0.08;
		throb(tl, p, t, 0.5);
		t += 0.5;
		burst(tl, 300, 584, "ok", t - 0.1);
		log(tl, "alchemy replied in 34ms — non-empty · id normalized", "ok", t - 0.1);
		setCls(tl, p, "p-green", t);
		move(tl, p, 300, 560, t, 0.1);
		t += 0.1;
		ride(tl, p, "rail-sw-u0", t, 0.5, true);
		t += 0.5;
		homeFromSweep(
			tl, p, t,
			'async cache write — finality: finalized → connector "cold" (redis) · kept forever',
			"response — X-ERPC-Upstream: alchemy · X-ERPC-Cache: MISS · attempts: 1",
		);
		return tl;
	}

	function buildHedge() {
		const tl = gsap.timeline({ paused: true, onComplete: onDone });
		const pA = createPulse("");
		let t = prefix(tl, pA);
		t = coldOut(tl, pA, t, "cache MISS — policy probing a lagging upstream: self-hosted goes first");
		t = nestDrill(tl, pA, t, {
			timeout: "failsafe executor — timeout 10s wraps the whole invocation",
			consensus: 'consensus — not configured for "*" · pass through',
			retry: "retry — attempt 1 of 3",
			hedge: "hedge policy — maxCount 1 · delay quantile 0.7 (≈120ms warm)",
		});
		actOn(tl, els.s6, t, t + 3.2);
		log(tl, "sweep → self-hosted: availability ok · breaker closed", "info", t);
		// Primary leg stalls at self-hosted.
		ride(tl, pA, "rail-sw-u2", t, 0.65, false);
		t += 0.65;
		move(tl, pA, 970, 578, t, 0.08);
		t += 0.08;
		const tArriveU2 = t;
		throb(tl, pA, t, 2.05);
		log(tl, "self-hosted thinking… p50 280ms · no response yet", "warn", t + 0.35);
		// Hedge leg fires from the sweep after the adaptive delay.
		const tHedge = tArriveU2 + 0.8;
		const pB = createPulse("p-amber");
		tl.set(pB, { x: 635, y: 511 }, tHedge);
		fadeIn(tl, pB, tHedge);
		log(tl, "hedge delay exceeded — racing alchemy in parallel (hedge 1 of 1)", "warn", tHedge);
		ride(tl, pB, "rail-sw-u0", tHedge, 0.6, false);
		let tb = tHedge + 0.6;
		move(tl, pB, 300, 578, tb, 0.08);
		tb += 0.08;
		throb(tl, pB, tb, 0.45);
		tb += 0.45;
		const tWin = tb;
		burst(tl, 300, 584, "ok", tWin);
		log(tl, "alchemy returned non-empty — wins the race · self-hosted leg cancelled", "ok", tWin);
		setCls(tl, pB, "p-green", tWin);
		// Primary leg gets cancelled where it waits.
		setCls(tl, pA, "p-crimson", tWin + 0.05);
		burst(tl, 1002, 552, "mark-x", tWin + 0.05);
		fadeOut(tl, pA, tWin + 0.1, 0.4);
		// Winner returns home.
		move(tl, pB, 300, 560, tWin + 0.1, 0.1);
		ride(tl, pB, "rail-sw-u0", tWin + 0.2, 0.5, true);
		tb = tWin + 0.7;
		homeFromSweep(
			tl, pB, tb,
			'async cache write — finality: finalized → connector "cold" (redis)',
			"response — X-ERPC-Upstream-Hedges: 1 · winner: alchemy",
		);
		return tl;
	}

	function buildRetry() {
		const tl = gsap.timeline({ paused: true, onComplete: onDone });
		const p = createPulse("");
		let t = prefix(tl, p);
		t = coldOut(tl, p, t, "cache MISS — selection policy order: self-hosted · quicknode · alchemy");
		t = nestDrill(tl, p, t, {
			timeout: "failsafe executor — timeout 10s wraps the whole invocation",
			consensus: 'consensus — not configured for "*" · pass through',
			retry: 'retry — attempt 1 of 3 (matchMethod "*")',
			hedge: "hedge armed — not needed unless an attempt stalls",
		});
		actOn(tl, els.s6, t, t + 3.4);
		log(tl, "sweep → self-hosted: availability ok · permit ok", "info", t);
		ride(tl, p, "rail-sw-u2", t, 0.65, false);
		t += 0.65;
		move(tl, p, 970, 578, t, 0.08);
		t += 0.08;
		throb(tl, p, t, 0.45);
		t += 0.45;
		burst(tl, 970, 584, "err", t);
		log(tl, "self-hosted ✕ connection reset — retryable error · breaker failure recorded", "err", t);
		setCls(tl, p, "p-crimson", t);
		move(tl, p, 970, 560, t + 0.1, 0.1);
		ride(tl, p, "rail-sw-u2", t + 0.2, 0.45, true);
		t += 0.65;
		throb(tl, p, t, 0.55);
		actOn(tl, els.retry, t, t + 0.6);
		log(tl, "backoff 250ms × factor — attempt 2 · next upstream: quicknode", "warn", t);
		t += 0.55;
		setCls(tl, p, "p-amber", t);
		move(tl, p, 635, 560, t, 0.45);
		t += 0.45;
		move(tl, p, 635, 578, t, 0.08);
		t += 0.08;
		throb(tl, p, t, 0.45);
		t += 0.45;
		burst(tl, 635, 584, "ok", t - 0.05);
		log(tl, "quicknode replied in 41ms — success on attempt 2", "ok", t - 0.05);
		setCls(tl, p, "p-green", t);
		move(tl, p, 635, 511, t, 0.45);
		t += 0.45;
		homeFromSweep(
			tl, p, t,
			'async cache write — finality: finalized → connector "cold" (redis)',
			"response — X-ERPC-Network-Retries: 1 · served by quicknode",
		);
		return tl;
	}

	function buildConsensus() {
		const tl = gsap.timeline({ paused: true, onComplete: onDone });
		const pA = createPulse("");
		let t = prefix(tl, pA);
		t = coldOut(tl, pA, t, 'cache MISS — consensus required (matchMethod "eth_getBalance")');
		t = nestDrill(tl, pA, t, {
			timeout: "failsafe executor — timeout wraps the whole consensus round",
			consensus: "consensus — maxParticipants 3 · agreementThreshold 2 · disputes punished",
			retry: "retry + hedge still apply per participant slot",
			hedge: "each slot draws its own upstream via the sweep",
		});
		actOn(tl, els.s6, t, t + 3.4);
		log(tl, "fan-out — querying 3 participants in parallel", "info", t);
		const tSweep = t;
		// Winner leg.
		ride(tl, pA, "rail-sw-u0", t, 0.65, false);
		t += 0.65;
		move(tl, pA, 300, 578, t, 0.08);
		t += 0.08;
		const tArr = t;
		// Two more participant legs spawned in parallel.
		const mkLeg = (rail, x) => {
			const pl = createPulse("p-amber");
			tl.set(pl, { x: 635, y: 511 }, tSweep);
			fadeIn(tl, pl, tSweep);
			if (rail === "rail-sw-u1") {
				move(tl, pl, 635, 560, tSweep, 0.55);
			} else {
				ride(tl, pl, rail, tSweep, 0.65, false);
			}
			move(tl, pl, x, 578, tArr - 0.08, 0.08);
			throb(tl, pl, tArr, 0.9);
			return pl;
		};
		mkLeg("rail-sw-u1", 635);
		const pC = mkLeg("rail-sw-u2", 970);
		throb(tl, pA, tArr, 0.9);
		const tHashes = tArr + 0.35;
		actOn(tl, els.hashU0, tHashes, tHashes + 2.2, "show");
		actOn(tl, els.hashU1, tHashes + 0.15, tHashes + 2.35, "show");
		actOn(tl, els.hashU2, tHashes + 0.3, tHashes + 2.5, "show");
		log(tl, "responses hashed — 2 × 0x8f…c1 · 1 × 0x22…9e", "info", tHashes + 0.3);
		log(tl, "dispute — self-hosted disagrees · misbehavior recorded to health tracker", "err", tHashes + 0.65);
		const tAgree = tHashes + 0.95;
		actOn(tl, els.agreeChip, tAgree, tAgree + 1.8, "show");
		log(tl, "agreement 2/3 ≥ threshold 2 — consensus reached", "ok", tAgree);
		const tWin = tAgree + 0.3;
		burst(tl, 300, 584, "ok", tWin);
		setCls(tl, pA, "p-green", tWin);
		setCls(tl, pC, "p-crimson", tWin);
		fadeOut(tl, pC, tWin + 0.1, 0.4);
		move(tl, pA, 300, 560, tWin + 0.1, 0.1);
		ride(tl, pA, "rail-sw-u0", tWin + 0.2, 0.5, true);
		const tr = tWin + 0.7;
		homeFromSweep(
			tl, pA, tr,
			"async cache write — agreed value stored (finality: finalized)",
			"response — X-ERPC-Consensus-Slots: 3 · X-ERPC-Consensus-Disputes: 1",
		);
		return tl;
	}

	const BUILDERS = {
		cache: buildCache,
		cold: buildCold,
		hedge: buildHedge,
		retry: buildRetry,
		consensus: buildConsensus,
	};

	/* ─── Scene control ─── */

	function resetScene() {
		if (master) {
			master.kill();
			master = null;
		}
		pulseEls.forEach((el) => el.remove());
		pulseEls = [];
		root
			.querySelectorAll(".stage-card.active, .nest-layer.active, .hash-chip.show, .agree-chip.show, .cache-chip.show")
			.forEach((el) => el.classList.remove("active", "show", "ok", "miss"));
		[els.burstA, els.burstB].forEach((el) => el.setAttribute("class", "burst"));
		els.markX.setAttribute("class", "mark-x");
		els.cacheChip.textContent = "";
		logList.innerHTML = "";
	}

	function onDone() {
		state.playing = false;
	}

	function play(name) {
		if (!BUILDERS[name]) name = "cold";
		state.scenario = name;
		state.playing = true;
		resetScene();
		master = BUILDERS[name]();
		root.querySelectorAll("[data-scenario]").forEach((btn) => {
			btn.setAttribute("aria-pressed", btn.getAttribute("data-scenario") === name ? "true" : "false");
		});
		playBtn.textContent = "↻ Replay";
		master.play(0);
	}

	/* ─── Stage detail panel ─── */
	function closeDetail() {
		detail.hidden = true;
		detail.innerHTML = "";
		root.querySelectorAll("[data-stage-chip]").forEach((c) => c.setAttribute("aria-pressed", "false"));
	}
	function openDetail(id) {
		const st = PIPE_STAGES.find((s) => s.id === id);
		if (!st) return;
		if (!detail.hidden && detail.getAttribute("data-open") === id) {
			closeDetail();
			return;
		}
		detail.setAttribute("data-open", id);
		detail.hidden = false;
		detail.innerHTML =
			'<div class="pd-head"><span class="pd-num">' + st.num + "</span>" +
			'<div class="pd-titles"><div class="pd-title">' + st.name + '</div>' +
			'<div class="pd-file">' + st.file + "</div></div>" +
			'<button type="button" class="pd-close" aria-label="Close stage details">✕</button></div>' +
			'<ul class="pd-bullets">' + st.bullets.map((x) => "<li>" + x + "</li>").join("") + "</ul>" +
			'<pre class="pd-yaml"><code>' + st.yaml + "</code></pre>";
		detail.querySelector(".pd-close").addEventListener("click", closeDetail);
		root.querySelectorAll("[data-stage-chip]").forEach((c) => {
			c.setAttribute("aria-pressed", c.getAttribute("data-stage-chip") === id ? "true" : "false");
		});
	}

	/* ─── Event wiring (one delegated listener so re-init stays clean) ─── */
	function onRootClick(e) {
		const scenBtn = e.target.closest("[data-scenario]");
		if (scenBtn && root.contains(scenBtn)) {
			play(scenBtn.getAttribute("data-scenario"));
			return;
		}
		if (e.target.closest("[data-play]")) {
			play(state.scenario);
			return;
		}
		const chip = e.target.closest("[data-stage-chip]");
		if (chip && root.contains(chip)) {
			openDetail(chip.getAttribute("data-stage-chip"));
			return;
		}
		const stageG = e.target.closest(".stage-card[data-stage]");
		if (stageG && root.contains(stageG)) {
			openDetail(stageG.getAttribute("data-stage"));
		}
	}
	function onKeydown(e) {
		if (e.key === "Escape") closeDetail();
	}
	root.addEventListener("click", onRootClick);
	window.addEventListener("keydown", onKeydown);

	/* Auto-play the cold path once, when the diagram scrolls into view. */
	if ("IntersectionObserver" in window) {
		observer = new IntersectionObserver(
			(entries) => {
				if (state.autoPlayed) return;
				if (entries.some((en) => en.isIntersecting)) {
					state.autoPlayed = true;
					observer.disconnect();
					if (!RM) play("cold");
				}
			},
			{ threshold: 0.3 },
		);
		observer.observe(SVG);
	}

	return () => {
		state.playing = false;
		if (master) master.kill();
		if (observer) observer.disconnect();
		root.removeEventListener("click", onRootClick);
		window.removeEventListener("keydown", onKeydown);
	};
}

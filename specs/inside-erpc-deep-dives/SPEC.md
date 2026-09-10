# SPEC — Inside eRPC deep-dive subpages

Status: approved for implementation (user: "spec everything before implementation", 2026-09-11).
Branch: `docs/inside-erpc-page`.

## 1. Goal

Turn `/inside-erpc` into a section. The existing animated overview page stays as
the landing page; 7 new deep-dive subpages teach engineers:

- **the source code** — real files, functions, goroutines, constants;
- **the design/implementation** — why the pieces are shaped the way they are;
- **first principles** — the cross-cutting rules the code embodies.

Each subpage = one bespoke animated workflow + a numbered walkthrough + a code
tour + first-principles callouts + an `AISection` that links OUT to the existing
static reference pages. Never duplicate config schema tables — link them.

Audience: software engineers who operate eRPC and/or want to read the codebase.
Tone: direct, concrete, code-forward. Every claim citable to a file.

## 2. Architecture

- Nextra 2.13, pages router. Pattern: sibling page + directory (same as
  `config/failsafe.mdx` + `config/failsafe/`).
  - Keep `docs/pages/inside-erpc.mdx` (landing page, unchanged showcase layout).
  - Add `docs/pages/inside-erpc/_meta.js` — the 7 children, default docs layout
    (sidebar + toc ON; do NOT inherit the landing page's full-width theme).
  - Add `docs/pages/inside-erpc/<topic>.mdx` per subpage.
- Imports in subpages: `from "../../components"`.
- New components live in `docs/components/explainer/` and are re-exported from
  `docs/components/index.ts`.
- New stylesheet `docs/styles/deep-dive.css`, imported from
  `docs/pages/_app.tsx` (one line, next to explainer.css). All selectors scoped
  under `.cv-dd-root`.
- `.llms.txt` build strips self-closing components ⇒ **every animated teaching
  point must also exist as markdown** on the same page (workflow steps, tour,
  AISection). Animations are the sugar, prose is the substance.
- Root `docs/public/llms.txt` regenerates automatically at build (prebuild).

### Animation stack (already in docs/package.json)

- **GSAP 3.15** for all timeline-driven widgets (one `gsap.timeline` per play,
  killed/rebuilt on replay; reduced-motion ⇒ static end state).
- **Motion 13** for scroll-into-view reveals and layout micro-interactions.
- Follow the verified conventions of
  `docs/components/explainer/pipeline-explorer.internals.ts`: SSR-safe static
  markup + `useEffect` hydration, `init*(root)` returning a cleanup, delegated
  listeners, `@ts-nocheck` on imperative engines.

## 3. Shared component: `TimelineRace`

Data-driven multi-lane timeline player. Powers pages 1, 3, 4, 6.

```ts
export interface TimelineLane { id: string; label: string; sub?: string }
export interface TimelineBar {
  lane: string; start: number; end: number;        // seconds on the axis
  label?: string; tone?: "blue"|"green"|"amber"|"crimson"|"dim"; hatch?: boolean
}
export interface TimelineMarker {
  lane: string; at: number; label?: string;
  tone?: "blue"|"green"|"amber"|"crimson"|"dim";
  shape?: "dot"|"check"|"x"|"flag"
}
export interface TimelineScenario {
  duration: number;                                 // axis length, seconds
  timeLabel?: string;                               // e.g. "time →"
  lanes: TimelineLane[]; bars: TimelineBar[]; markers: TimelineMarker[]
}
export function TimelineRace(props: {
  scenario: TimelineScenario;
  scenarios?: Record<string, TimelineScenario>;    // tabbed variant (pages 3, 4)
  autoPlay?: boolean;                               // default true on first in-view
  maxWidth?: string;
})
```

Behavior contract:

- Renders static SVG (viewBox, lanes as rows, axis) on first paint — SSR-safe.
- On play: a playhead line sweeps left→right over `duration` (scaled to ~6–10s
  real time); bars scaleX-grow as the playhead reaches `[start,end]`; markers
  pop (spring) at `at`; labels fade in after their bar/marker.
- Replay button rebuilds the timeline. `prefers-reduced-motion`: final state,
  no playhead. Tab buttons (when `scenarios`) switch dataset + replay.
- Root element: `<div className="cv-dd-root dd-timeline">`.

## 4. Bespoke widgets (pages 2, 5, 7)

- **`ChainReorg`** (page 2): SVG chain of ~10 blocks + cache-entry chips per
  block. On play: blocks arrive → a fork marker → tip blocks replaced (old fade
  down, new slide in) → unfinalized chips show TTL countdown then vanish;
  finalized chips glow and persist. Below: cache-key evolution strip
  (`eth_getBlockByNumber("latest")` → `0x3f9a2c` → finality computed → policy
  matched). GSAP timeline.
- **`ScoreTicker`** (page 5): two upstream rows; p70 trace lines advance across
  5 eval ticks (15s apart on axis); order badge flips only when the challenger
  beats the hysteresis band (±30% shaded); a third upstream gets excluded then
  recovers via probe samples. GSAP timeline, Motion badge springs optional.
- **`RangeSlicer`** (page 7): a `[0x0 … 0x2AAAA]` range bar sliced into 5000-
  block chunks; each chunk chip flips `cache HIT`/`fetch`; fetch chunks fire
  parallel arrows to an upstream node; merged log array assembles below; second
  pass shows reactive bisect on `-32012`. GSAP timeline.

All three: same shell conventions as `FeatureWidgets` (card, title, caption with
the real config key, Replay, `aria-hidden` stage, `.cv-dd-root` scoping).

## 5. Page template (all 7 pages)

```mdx
---
title: <Page title> - Inside eRPC
description: <one line>
---

import { LLMsTxtLink, AISection, SourceLink, PromptExample, ConfigTabs, Callout } from "../../components";
import { TimelineRace } from "../../components";   // or the bespoke widget
import { <scenario data> } from "<./<topic>.data?>"; // data may live in the mdx via a tsx component instead

<LLMsTxtLink />

# <H1>

<2–4 short paragraphs: the problem, the promise, what the animation shows.>

<Widget … />

## The workflow

<Numbered steps that mirror the animation beat-for-beat.>

## In the source

<Subsections per file/function with GitHub links; short; cite constants/defaults
with exact values.>

## First principles

<1–3 callouts tying the topic to the cross-cutting rules.>

## Agent reference

<1–2 PromptExample blocks, goal-phrased, reference URL = this page's .llms.txt.>

<AISection title="<Topic> — full agent reference" hint="...">
  ### How it works          (dense implementation summary)
  ### Observability         (metric table: Metric | Type | Labels | When it fires)
  ### Source code entry points  (SourceLink list)
  ### Related pages         (links to the static reference pages)
</AISection>
```

Rules: no schema duplication (link to the reference page); every default value
verified against `common/defaults.go` or the owning file; new metrics cited by
exact name.

## 6. The 7 pages

### 6.1 `request-lifecycle` — "The life of a request, in slow motion"

- Hero: `TimelineRace`. Lanes: Client / HTTP handler / Multiplexer / Cache /
  Executor / Upstream / Async writer. Story: batch of 2 arrives → per-element
  goroutines → element 2 joins an in-flight twin as a follower (multiplexer
  `LoadOrStore`) → leader does cache MISS → executor → upstream 34ms →
  followers copy + rewrite id → responses stream → async cache write outlives
  the response.
- Tour: `erpc/http_server.go` batch fan-out (`:428`), context/deadline
  propagation, `erpc/multiplexer.go` leader/followers (`networks.go:3050`,
  `CopyResponseForRequest`, `copyWg`), response id echo, X-ERPC-* headers.
- Principles: caller latency decoupled from bookkeeping; one leader per
  identical in-flight call.
- Links: `operation/batch`.

### 6.2 `finality-and-caching` — "Cache like a blockchain, not a CDN"

- Hero: `ChainReorg` (above).
- Tour: `Network.GetFinality` (`erpc/networks.go:2594`); set vs get match
  semantics (`data/cache_policy.go:82/124` — finalized reads may consult
  unfinalized+finalized policies, not vice versa); `shouldCacheResponse`
  (`architecture/evm/json_rpc_cache.go:1158` — future-block empty never
  cached); realtime age guard (`:926`); async writes (`networks.go:2426`,
  10s budget, refcounted response).
- Principles: finality is the immutability horizon; TTL only bounds the
  mutable zone; empty is data, not error — except when it isn't.
- Links: `config/database/evm-json-rpc-cache`, `reference/evm/block-tracking`.

### 6.3 `failsafe-in-depth` — "The nesting doll"

- Hero: `TimelineRace` with `scenarios` tabs:
  - "Block unavailable": attempt 1 → skip/`block_unavailable` → wait
    EMA block time ×1.0 → attempt 2 succeeds. Cap: `emptyResultMaxAttempts: 2`.
  - "Upstream 5xx": attempt 1 → `retryable_error` → backoff 250ms ×1.2 →
    attempt 2 on next upstream. Cap: `maxAttempts: 3`.
  - "Hedge race": primary fires, quantile-0.7 delay marker, hedge fires,
    sibling wins; fast-empty `keep` rejection shown as a rejected marker.
- Tour: composition (`erpc/network_executor.go:164` —
  `timeout(consensus(retry(hedge(upstream))))`); policy match chain
  (`networks.go:1708` — matchMethod/matchFinality/matchRequestKind);
  `shouldRetryWithReason` (`network_executor.go:396`); `computeDelay` (`:554`);
  adaptive hedge delay (`common/adaptive_duration.go`); upstream-scope breaker
  (`failsafe/breaker.go` — 20/80 ring, `halfOpenAfter: 5m`).
- Principles: empty ≠ error (three-way classification everywhere); waits are
  meaningful only for not-yet-data.
- Links: `config/failsafe` + children, `config/failsafe/hedge`.

### 6.4 `consensus` — "Agreeing on the truth"

- Hero: `TimelineRace` with tabs:
  - "Unassailable lead": 3 slots race; A,B hash-identical → group{AB}=2 ≥
    threshold with 1 outstanding → short-circuit; C cancelled mid-flight.
  - "Dispute": A empty, B data-1, C data-2 → no rule resolves →
    `ErrConsensusDispute`; punishment tab shows rate-limiter → cordon.
- Tour: `executor.Run` (`consensus/executor.go:120`); analyzer loop (`:359`);
  classify/hash/dedupe (`analysis.go:68`, per-method `ignoreFields`); rule
  table priority (`rules.go`); short-circuit rules (`:858`); data-dissent-only
  misbehavior (`:1017`); punishment limiter (`:1477`); headers/metrics.
- Principles: unknown-input fallthrough; only provable dissent is punished.
- Links: `config/failsafe/consensus`, `config/failsafe/integrity`.

### 6.5 `health-and-selection` — "The scoring engine"

- Hero: `ScoreTicker` (above).
- Tour: tracker (`health/tracker.go` — rolling 10-bucket windows, DDSketch
  quantiles, success-only duration samples); slot tick (`internal/policy/slot.go`
  — 15s eval, 100ms timeout, cached order read wait-free by requests);
  default policy walk (`internal/policy/default_policy.js` — removeCordoned →
  error/throttle/latency/lag exclusions → `whenEmpty` fallback → stickyPrimary
  0.30/30s → probeExcluded 0.1); blockHeadLag via EMA block time.
- Principles: routing decided from live measurements; hysteresis beats
  reactivity; excluded ≠ forgotten.
- Links: `config/projects/selection-policies`.

### 6.6 `block-availability` — "Who actually has block N?"

- Hero: `TimelineRace`. Request for tip+0 on a 2s chain: policy order [A,B] →
  A head=N−1, upper bound → retryable skip (+ async head poll) → B returns
  null (`empty_result`) → wait EMA×1.0 ≈ 2s → retry: A serves.
- Tour: `checkUpstreamBlockAvailability` (`erpc/networks.go:2869` — configured
  bounds only, fail-open); `maxRetryableBlockDistance` = max(128, 60s/blockTime)
  (`:1324`); future-block short-circuit (`:2001`); state poller
  (`architecture/evm/evm_state_poller.go`, FullySyncedThreshold 4); catch-up
  metrics (`erpc_network_retry_attempt_total{reason}`,
  `erpc_network_data_unavailable_wait_seconds`; runbook
  `monitoring/catch-up-metrics.md` — pressure ≈ concurrent waiters).
- Principles: waiting only works below the finality horizon; fail-open when
  bounds unknown.
- Links: `reference/evm/block-tracking`.

### 6.7 `evm-translation` — "The translation layer"

- Hero: `RangeSlicer` (above).
- Tour: block-ref normalization (`architecture/evm/json_rpc.go:92`,
  `resolveBlockTagToHex` — why identical logical reads must produce identical
  cache keys; fail-open on unresolvable tags); proactive getLogs split
  (`eth_getLogs.go:181`, thresholds 5000/30000, parallel order-preserving
  chunks, each chunk independently cached); reactive split (`:411`, bisect on
  `-32012`/request-too-large, then address list, then topics[0]);
  eth_chainId short-circuit (`eth_chainId.go` — answered from config).
- Principles: normalize early so every downstream subsystem can key on
  concrete numbers; big requests are many small requests.
- Links: `reference/evm/getlogs-splitting`, `reference/evm/method-handlers`.

## 7. Landing page change

Add a "## Go deeper" section to `docs/pages/inside-erpc.mdx` (after the feature
gallery): `CapabilityGrid` with the 7 subpages (title, href, one-line summary).
Nothing else on the landing page changes.

## 8. Verification requirements (definition of done per page)

- `npx tsc --noEmit` clean; `pnpm build` green (page appears in build output).
- Headless-Chrome CDP check per page: widget root present, Replay restarts,
  prose sections (`#workload` anchors) present, AISection present, zero console
  errors; screenshot eyeballed at 1440px and 320px.
- `make fmt`-equivalent for docs (Biome) where it applies; no hand-edited
  `docs/public/*.llms.txt`.

## 9. Out of scope

- SVM-specific deep-dives (architecture/svm) — follow-up candidate.
- The integrity chain-follower subsystem beyond a mention in 6.4/6.6.
- Changes to Go source, config schema, or reference pages.

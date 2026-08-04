# Weighted Round Robin (WRR) — Specification

**Status**: Draft — design review
**Owner**: TBD
**Last revised**: 2026-07-18

---

## 1. Purpose

Add **per-request Weighted Round Robin** distribution of primary traffic across
upstreams. Today eRPC selects exactly one primary per `(network, method,
finality)` slot per tick: the policy engine caches a single ordered list and
every request in the tick window walks it from the head
(`specs/selection-policy/feature.md` §1). The only "round robin" available —
`rotateBy(ctx.tickCount)` — rotates that head **once per tick** (default 15s),
so load spreading is time-granular, not request-granular. Shortening
`evalInterval` only shrinks the time slices; it never becomes per-request
spreading. Distribution is orthogonal to tick frequency.

WRR gives operators a way to say *"send ~3× more requests to upstream A than
upstream B"* — for capacity-based splitting (a paid 500 rps plan next to a
100 rps plan), cost shaping, or gradual traffic shifting between providers —
while the existing health predicates keep full authority over **who is allowed
to receive traffic at all**.

The design extends the selection-policy architecture instead of adding a
parallel subsystem, honoring its core invariant:

> Routing behavior is entirely expressed by the eval function. No separate
> "scoring" subsystem, no `routingStrategy` knob.

### 1.1 Non-goals

- **Per-client sticky sessions** — WRR distributes requests, not clients.
- **Consensus participant selection** — consensus requests bypass WRR (§7.4).
- **Weighted failover order** — weights influence only the *primary* pick;
  retry/hedge fall-through order remains the policy-ranked list.
- **Reactive/congestion-aware weighting** (adjusting weights from live
  latency) — possible later via `weightSource: 'score'`; not required for v1.

---

## 2. Design questions & decisions

These are the questions raised during design review, with the chosen answer
and rationale. Each is reversible at this stage; flag any disagreement before
implementation.

### Q1. Where does the rotation live — eval chain (JS) or request path (Go)?

**Decision: eval chain declares *intent + weights*; Go request path executes
the per-request rotation.**

The slot's cached ordered list is shared by all requests in a tick, so a pure
JS/list-order solution can only rotate the head once per tick — that is
`rotateBy` today, and it pins 100% of a 15s window on one upstream. True WRR
needs per-request state, which must live next to the slot cache in Go. The
eval chain keeps its declarative role: a stdlib step marks the slot as
distributed and attaches weights; a wait-free Go cursor picks the starting
upstream per request. The eval chain still owns eligibility — an upstream
dropped by `excludeIf`/`removeCordoned` never enters the rotation.

### Q2. Where do weights come from?

**Decision: static per-upstream `routing.weight` (integer, default 1),
overridable per policy via the `distribute` step's `weight` option.**

`routing.weight` sits in `UpstreamRoutingConfig` next to `scoreMultipliers`
and `probe` (`common/config.go:875`) — the established home for per-upstream
routing hints, inheriting `upstreamDefaults.routing` like its siblings. A JS
`weight: (u) => ...` hook in the step keeps the door open for computed
weights without new config schema.

### Q3. Which WRR algorithm?

**Decision: smooth WRR (the nginx algorithm).**

Per pick over the candidate set `C`:

```
total = Σ weight(u) for u in C
for each u in C: current(u) += weight(u)
pick = argmax current(u)
current(pick) -= total
```

Deterministic, burst-free (a weight-3 upstream serves picks 1, 3, 5 — never
three in a row against a weight-1 peer), O(n) per pick, trivially unit-testable.
With all weights equal it degenerates to plain round robin. Randomized WRR
was rejected for v1: harder to test, burst-prone, and offers no operability
advantage; the `strategy` field leaves room to add `'weighted-random'` later.

### Q4. How do retries and hedges interact with the WRR pick?

**Decision: rotate, don't reorder.** The request receives the policy-ranked
list rotated so the WRR pick is at index 0; the remainder follows in policy
rank order, wrapping around. `NextUpstream` (`common/request.go:1524`),
hedging, and the `use-upstream` directive work **unchanged** — retries and
hedges naturally land on the next-best upstreams *other than* the primary
pick. Failover quality is fully preserved.

### Q5. `stickyPrimary` vs `distribute` — conflict?

**Decision: mutually exclusive per chain; `distribute` wins; the engine warns.**

`stickyPrimary` pins position 0 across ticks; `distribute` deliberately varies
the primary per request. Using both is a config mistake. At tick time the
slot detects both steps in the chain (both annotations present), logs a
warning once per slot, and increments
`erpc_selection_policy_warning_total{reason="sticky_with_distribute"}`.
`distribute` still executes (it acts after the cached order is fixed).

### Q6. What is the WRR state scope?

**Decision: per slot — identical grain to the eval (`evalScope`).**

State lives on the slot next to `cache`, so `network-method-finality` scopes
get independent rotations per (method, finality), and the wildcard slot's
rotation is the fallback exactly as with `GetOrdered` today. Counters are
reconciled by upstream id each tick (§5.3).

### Q7. What do `weight: 0` and all-zero mean?

**Decision: `weight: 0` = "fallback only"** — never selected as primary by the
rotation, but stays in the list tail for `use-upstream` targeting and
last-resort failover (it is still health-ranked; it simply never wins a pick).
**All candidates at weight 0** = misconfiguration: the slot behaves as if
`distribute` were absent (head-of-list, today's behavior) and emits a
one-time warning. Negative weights are rejected at config validation.

### Q8. Consensus requests?

**Decision: bypass WRR.** Consensus needs a deterministic, rank-stable
participant set (`reason=consensus_slot`); rotating the head would churn the
participant quorum per request for no benefit. The rotation is applied only
on the standard primary/hedge forward path (§7.4).

### Q9. Backward compatibility?

**Decision: fully opt-in.** No `distribute` step in the chain → zero behavior
change, zero per-request allocations added to the hot path. The default
policy (`internal/policy/default_policy.js`) is unchanged.

### Q10. What happens on membership changes and cold start?

**Decision:** cold start serves registration order exactly as today (no
distribution state exists before the first tick). On each tick the slot
reconciles counters against the new eligible set: departed upstreams are
dropped, survivors keep their `current` values (smoothness across ticks), and
**newcomers enter at `current = 0`** — they join the rotation on the next
pick cycle rather than receiving a thundering burst (nginx behavior for added
servers).

---

## 3. Configuration

### 3.1 Per-upstream weight

```yaml
projects:
  - id: main
    upstreams:
      - id: alchemy-paid
        endpoint: https://eth-mainnet.g.alchemy.com/v2/KEY
        routing:
          weight: 3            # ~3/4 of primary picks among eligible upstreams
      - id: public-rpc
        endpoint: https://ethereum.publicnode.com
        routing:
          weight: 1            # ~1/4 of primary picks
```

`UpstreamRoutingConfig` gains one field (`common/config.go:875`):

```go
// Weight is this upstream's relative share of primary traffic when the
// network's selection policy includes the `distribute('wrr')` step.
// Interpreted relative to the other eligible upstreams in the slot:
// share(u) = weight(u) / Σ weights. Default 1. 0 keeps the upstream in
// the ranked fallback list but excludes it from primary rotation.
// Ignored entirely when the policy chain has no `distribute` step.
Weight int `yaml:"weight,omitempty" json:"weight,omitempty"`
```

- Default: `1` when the key is absent (`ApplyDefaults` on
  `UpstreamRoutingConfig`, mirroring the existing defaults pattern in
  `common/defaults.go`).
- Validation: negative → config error naming the upstream id.
- TS surface: `tstype` picks the field up via the normal `generated.ts`
  regeneration; docs land in the upstreams config page (§10).

### 3.2 Policy step

```yaml
networks:
  - architecture: evm
    evm: { chainId: 1 }
    selectionPolicy:
      evalInterval: 15s
      evalFunc: |
        return upstreams
          .removeCordoned()
          .excludeIf(all(samplesAbove(10), errorRateAbove(0.7)))
          .excludeIf(blockNumberLagAbove(16))
          .sortByScore(PREFER_FASTEST)
          .distribute('wrr')
```

```ts
.distribute(strategy: 'wrr', opts?: {
  // Weight source per upstream. Default: the `routing.weight` config
  // (already resolved to u.weight by the engine's Go→JS bridge).
  // A function overrides config per upstream, e.g. derive from score:
  //   weight: (u) => Math.max(1, Math.round((u.score || 0) * 10))
  weight?: (u: Upstream) => number,
}) => Upstream[]
```

Semantics inside the chain:

1. The step is a **transparent pass-through on list content** — it returns
   `this.slice()` unmodified. The policy order still defines failover rank.
2. It stamps each upstream in the returned array with `u.wrrWeight = n`,
   exported to Go through the same per-upstream channel `sortByScore` uses
   for `u.score` (`EvalResult.Scores`, `internal/policy/decision.go:52`) —
   concretely a `WrrWeights map[string]int` sibling on the eval result.
   A slot whose returned upstreams carry `wrrWeight` is distributed; when
   none do, the slot is undistributed. (Strategy versioning, if ever
   needed beyond `'wrr'`, rides as a well-known entry in `u.annotations`
   rather than a new array-level metadata channel.)
3. Placement in the chain is free, but the annotation captures the weights of
   the upstreams **present at that point**; recommended placement is *after*
   all exclusion/scoring steps (last in the chain) so excluded upstreams
   never carry weight. Weights of upstreams `forceInclude`d afterwards
   default to their `routing.weight`.

Default policy: unchanged. `distribute` never appears in
`default_policy.js`.

---

## 4. Go-side design

### 4.1 Slot state

`internal/policy/slot.go` gains, alongside the existing `cache
atomic.Pointer[[]common.Upstream]`:

```go
// dist is nil for slots whose policy chain has no distribute step —
// the zero-cost common case. Written once per tick (under slot.mu),
// read per request; pick counters guarded by dist.mu.
type distState struct {
    strategy string             // "wrr"
    weight   map[string]int     // upstream id → resolved weight (≥0)
    current  map[string]int64   // smooth-WRR running counters, by id
    mu       sync.Mutex         // pick critical section ~O(n), tens of ns
}
```

Per-tick write path (in the slot's tick completion, where `cache` is stored
today): if the eval result carries `wrrWeight` annotations, build/reuse
`distState` and **reconcile** — drop ids absent from the new ordered list,
keep `current` for survivors, insert newcomers at `0`. If the annotations
disappear (policy edited to remove the step), clear `dist` to nil.

### 4.2 Request path

`erpc/networks.go:951-1003`, between `GetOrdered` and `SetUpstreams`:

```go
upsList = n.policyEngine.GetOrdered(networkId, method, finality)   // as today
// ... cold-start fallback + filterMethodEligible, as today ...
if !resolvedExecutorHasConsensus { // consensus keeps raw policy order (§7.4)
    upsList = n.policyEngine.RotateForRequest(networkId, method, finality, upsList)
}
req.SetUpstreams(upsList)                                          // as today
```

`RotateForRequest` (new on `Engine`):

1. Look up the slot (same `lookupSlotWithFallback` as `GetOrdered`). No slot
   or `dist == nil` → return the input slice **unmodified** (no allocation —
   the common non-WRR path is untouched).
2. Compute the candidate set: upstreams in `upsList` with `weight > 0`
   (post `filterMethodEligible`, so an ineligible upstream never consumes a
   pick — its counter simply doesn't advance this request, matching nginx's
   handling of downed servers).
3. Run one smooth-WRR pick under `dist.mu`.
4. Return a **rotated copy**: `append(slices.Clone(list[k:]), list[:k]...)`
   where `k` is the pick's index. One small slice allocation per request,
   only for distributed slots.

`NextUpstream`, hedge execution, failsafe sweep, `UseUpstream` directive, and
`ConsumedUpstreams` bookkeeping are **completely unchanged** — they already
operate on whatever per-request list `SetUpstreams` stored.

### 4.3 Why rotate-copy instead of a start-index

Passing a start offset into `NormalizedRequest` would avoid the allocation
but threads WRR awareness through `NextUpstream`, the consensus executor,
and every `upstreamList` consumer. The rotate-copy keeps WRR a strictly
additive layer: everything downstream sees an ordinary ordered list whose
head happens to vary per request.

### 4.4 Concurrency & performance

- One mutex per slot, held for an O(n) counter update (n = eligible
  upstreams, typically ≤ 10). No atomics trickery needed; contention is
  negligible next to the JSON-RPC work per request.
- `GetOrdered`'s wait-free atomic read is preserved; the copy happens only
  for distributed slots. Non-distributed slots add one nil check.
- Budget parity with the spec's `BenchmarkGetOrdered < 50ns` target is
  unaffected for the default path; a new `BenchmarkRotateForRequest`
  documents the WRR-path cost.

---

## 5. Semantics

### 5.1 Traffic shares

For eligible upstreams with weights `w₁…wₙ` (Σ > 0), upstream *i* receives a
`wᵢ / Σw` share of **primary picks** over any window of Σw consecutive picks,
with maximal smoothness (picks interleave rather than burst). Retries and
hedges are not weighted — they follow policy rank from the pick onward — so
realized traffic shares drift from the nominal ratio in proportion to the
error/retry rate. This is deliberate: weights express *offered* load;
health ranking keeps authority over *survival* load.

### 5.2 Interaction with health predicates

Eligibility is still 100% policy-owned. A weight-9 upstream tripping
`errorRateAbove` leaves the list at the next tick; the remaining upstreams'
shares renormalize automatically (`3:1` over `{A,B}` becomes `1` when A is
excluded). `whenEmpty` safety nets and `probeExcluded` re-admission are
orthogonal and unchanged — a probed upstream that earns re-admission rejoins
the rotation at `current = 0`.

### 5.3 Tick reconciliation example

Slot list across ticks, weights `A:3, B:1, C:1`:

| Tick | Ordered list (policy rank) | distState after reconcile |
|---|---|---|
| t0 | A, B, C | counters A:0 B:0 C:0 |
| t1 | A, B (C excluded, lag) | C dropped; A,B keep counters |
| t2 | A, B, C (C readmitted) | C re-added at 0; A,B keep counters |

Picks over t0's window (weights 3:1:1): A, B, A, C, A, B, A, C, … — A serves
~60%, B and C ~20% each, interleaved.

### 5.4 Equal weights

All-equal weights degenerate to strict round robin — so `.distribute('wrr')`
with no `routing.weight` config at all gives operators plain per-request
round robin, closing the gap left by the removed legacy
`routingStrategy: round-robin` (whose replacement, `rotateBy`, only rotates
per tick — `specs/selection-policy/plan.md:944`).

---

## 6. Edge cases

1. **Single eligible upstream** — pick loop trivially returns it; overhead is
   one map lookup.
2. **All weights zero** — misconfiguration guard (Q7): behave as no
   `distribute` step + one-time warning log and
   `erpc_selection_policy_warning_total{reason="all_weights_zero"}`.
3. **`weight: 0` upstreams** — excluded from picks, retained in tail for
   `use-upstream` and last-resort failover.
4. **Method-ineligible upstreams** — filtered by `filterMethodEligible`
   before the pick; their counters don't advance, so per-method eligibility
   differences don't skew the rotation.
5. **Cold start (no tick yet)** — registration order, head-of-list, exactly
   as today; distribution begins with the first tick that carries the
   annotation.
6. **Policy edited at runtime** (reload removes/changes the step) — next tick
   clears or rebuilds `distState`; in-flight requests keep their already-set
   list (immutable per request).
7. **Slot idle sweep** — `distState` dies with the slot; a lazy-recreated
   slot starts fresh at zero counters. Acceptable: rotation smoothness
   resets, shares do not drift.
8. **`stickyPrimary` + `distribute` in one chain** — warning + `distribute`
   wins (Q5).
9. **Negative weight** — rejected at config load with the upstream id named.

---

## 7. Interactions with existing subsystems

### 7.1 Retries & failsafe
Unchanged. `NextUpstream` walks the rotated per-request list; retryable
failures advance to the next policy-ranked upstream. Circuit breakers still
refuse permits per upstream independently of rotation.

### 7.2 Hedges
Hedge attempts consume the next entries of the rotated list — i.e., they go
to upstreams *other than* the WRR primary, ranked by policy order. Hedge
quality (second-best upstream) is preserved.

### 7.3 `use-upstream` directive
Unchanged semantics: the directive filters the rotated list by id/tag
(`common/request.go:1541-1565`). A directive pinning one upstream collapses
the list to it regardless of rotation. Weight-0 upstreams remain reachable
via directives.

### 7.4 Consensus
Consensus requests take the **unrotated** ordered list — a deterministic
participant set (top-N by policy rank), keeping `reason=consensus_slot`
accounting meaningful. Mechanism: the consensus executor does **not** call
`GetOrdered`; it consumes the request's own list
(`originalReq.Upstreams()`, `consensus/executor.go:140`). Rotation must
therefore be skipped upstream of `SetUpstreams` for consensus requests.
The network already resolves the method's `networkExecutor` at request
time, and each executor knows whether it carries a consensus runner
(`erpc/networks_registry.go:121`); `networks.go` takes that match before
rotating and applies `RotateForRequest` only when the resolved executor
has no consensus policy.

### 7.5 `probeExcluded`
Unchanged. Probing mirrors traffic to *excluded* upstreams; distribution only
governs *included* ones. A readmitted upstream rejoins at `current = 0`.

### 7.6 Rate-limit budgets
Weights and `rateLimitBudget` are independent knobs: weights shape offered
traffic, budgets cap it. Setting `weight: 3` on an upstream whose budget
saturates first just converts its share into throttle errors the policy can
then exclude on (`throttledRate`). Document the pairing in the docs page.

---

## 8. Telemetry

Reuse first, add minimally:

| Metric | Type | Labels | Notes |
|---|---|---|---|
| `erpc_upstream_selection_total` | counter | `reason=primary` | Already exists (`upstream/upstream.go:628`) — with WRR it directly shows the realized per-upstream primary distribution. No change needed. |
| `erpc_selection_wrr_weight` | gauge | `network`, `upstream`, (`method`, `finality` per scope) | Resolved effective weight per slot, emitted at tick beside `erpc_selection_score`. Makes config↔behavior auditable. |
| `erpc_selection_policy_warning_total` | counter | `network`, `reason` | New generic family: `sticky_with_distribute`, `all_weights_zero`. |
| trace attrs | — | `upstream.wrr_pick=true`, `upstream.wrr_weight` | On the forward span next to `upstreams.sorted` (`networks.go:982-990`). |

The existing `erpc_selection_position` gauge stays meaningful: a distributed
upstream oscillates around position 0 across *requests*, not across ticks, so
its tick-level position reflects failover rank, not pick frequency — document
this distinction on the observability docs page.

---

## 9. Testing

- **Unit — smooth WRR** (`internal/policy`): share exactness over Σw picks
  (3:1 → AABA-like interleave, never 3-in-a-row vs a live peer), equal-weight
  degeneracy to RR, weight-0 exclusion, all-zero guard, reconcile
  (drop/add/survive), candidate-subset picks (method-ineligible upstream
  doesn't consume picks).
- **Unit — stdlib** (`stdlib.js`): `distribute` pass-through order,
  annotation content, config-weight default, JS `weight` override, placement
  before/after exclusions.
- **Config** (`common`): defaults (`weight` absent → 1), negative-weight
  validation error, `upstreamDefaults.routing` inheritance, TS regeneration
  includes the field.
- **Integration** (`erpc/`): 2-upstream network, weights 3:1, N=400 requests
  → primary distribution within ±5% of 75/25; failure of the heavy upstream
  → retries land on the light one; exclusion tick → 100% light; readmission →
  shares renormalize. Hedge path: hedges never target the same upstream as
  the primary pick of the same request. Consensus path: participant set
  stable across requests.
- **Bench**: `BenchmarkRotateForRequest` (4 and 50 upstreams, distributed and
  nil-dist paths).

---

## 10. Docs & rollout

Per the repo's docs-ride-along rule, implementation ships with:

- `docs/pages/config/projects/upstreams.mdx` (or the routing page carrying
  `scoreMultipliers`/`probe`) — `routing.weight` row in the AISection config
  schema table, default cited to `common/defaults.go`, numbered gotchas
  (all-zero guard, sticky conflict, retry-share drift, consensus bypass).
- Selection-policy docs page — `distribute` stdlib reference + the §3.2
  recipe.
- Observability section — the two new metric names with labels, and the
  `position` vs pick-frequency clarification (§8).
- `.llms.txt` regeneration is build-time; no hand edits.

Rollout: single PR, opt-in feature, no migration. Legacy configs are
untouched (no `routingStrategy` revival). Announce as: *per-request weighted
and plain round-robin primary distribution via `distribute('wrr')`.*

---

## 11. Alternatives considered

| Alternative | Why rejected |
|---|---|
| **Pure JS: weighted interleave per tick** (expand list by weight, e.g. A×3,B×1, interleaved) | Only changes order once per tick; within the tick every request still hits the same head — time-granular sharing, not per-request WRR. This is `rotateBy`'s existing limitation. |
| **Weighted random per request** | Non-deterministic, burst-prone at small windows, harder to test and to reason about during incidents. Smooth WRR is strictly better behaved; `strategy` field allows adding it later. |
| **Start-index plumbed into `NextUpstream`** | Avoids one slice copy but spreads WRR awareness through `NormalizedRequest`, consensus, and the sweep path. Rotate-copy keeps the layer additive (§4.3). |
| **Score-derived weights only** (no static config) | Couples distribution to scoring presets operators may not use; static `routing.weight` is explicit, predictable, and auditable. JS `weight` hook still permits score-derived weights. |
| **New top-level `loadBalancing` config block** | Violates the selection-policy invariant that routing is expressed by the eval function; creates a second, competing routing subsystem. |

---

## 12. Implementation checklist (summary)

Full task breakdown lands in `specs/weighted-round-robin/plan.md` at
implementation time. Heads-up list:

1. `common/config.go` — `UpstreamRoutingConfig.Weight` + validation;
   `common/defaults.go` default=1; regenerate `typescript/config/src/generated.ts`.
2. `internal/policy/stdlib/stdlib.js` — `distribute` step + `wrrWeight`
   annotation export; `eval.go` bridge exposing resolved `routing.weight` as
   `u.weight`; `DecisionOutput.WrrWeights` (`internal/policy/decision.go`) so
   `Engine.RecentDecisions` and `erpc-simulator` show resolved weights per tick.
3. `internal/policy/slot.go` — `distState`, per-tick reconcile, clear-on-removal.
4. `internal/policy/engine.go` — `RotateForRequest`.
5. `erpc/networks.go` — resolve the method's `networkExecutor` (consensus
   flag) before rotation; invoke `RotateForRequest` on the forward path only
   when no consensus policy is attached (§7.4).
6. `telemetry/metrics.go` — `erpc_selection_wrr_weight`,
   `erpc_selection_policy_warning_total`.
7. `typescript/config` — typings for the `distribute` step in the
   `SelectionPolicyEvalFunction` stdlib surface (TS configs compile real
   arrow functions, so the step must exist in the TS-facing types too).
8. Tests per §9; docs per §10.

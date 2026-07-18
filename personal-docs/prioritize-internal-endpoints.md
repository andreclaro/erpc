# Prioritize internal endpoints

Route traffic to your own RPC nodes first to cut third-party provider costs, with safe fallback when they are unhealthy.

Running your own nodes is usually cheaper per request than paying a third-party RPC provider. The problem is that internal nodes can be slower, smaller, or occasionally unhealthy. eRPC can still prefer them for every request while automatically failing over to paid providers only when your own nodes are genuinely unusable.

This document covers the selection-policy patterns that make that trade-off explicit and measurable. The examples assume **consensus is disabled** (the common cost-cutting setup), so the request path is `retry(hedge(runUpstreamSweep))` — the ordered list the policy returns is the complete set of upstreams a single request can use.

## Quick taste

Illustrative config — prefer internal nodes, fall back to providers only when internals are unhealthy:

```yaml
projects:
  - id: main
    upstreams:
      - id: internal-1
        endpoint: http://10.0.0.10:8545
        tags:
          - tier:internal
      - id: internal-2
        endpoint: http://10.0.0.11:8545
        tags:
          - tier:internal
      - id: alchemy
        endpoint: alchemy://${ALCHEMY_API_KEY}
    networks:
      - architecture: evm
        evm: { chainId: 1 }
        selectionPolicy:
          evalFunc: |
            (upstreams, ctx) => upstreams
              .keepHealthy({ maxErrorRate: 0.5, maxBlockHeadLag: 16, maxP95Ms: 5000 })
              .whenEmpty(() => upstreams)
              .preferTag('tier:internal', { minHealthy: 1, fallback: '!tier:internal' })
              .sortByScore(PREFER_FASTEST)
              .stickyPrimary({ hysteresis: 0.30, minSwitchInterval: '30s' })
              .probeExcluded({ sampleRate: 0.1 })
```

TypeScript equivalent:

```ts
projects: [{
  id: "main",
  upstreams: [
    { id: "internal-1", endpoint: "http://10.0.0.10:8545", tags: ["tier:internal"] },
    { id: "internal-2", endpoint: "http://10.0.0.11:8545", tags: ["tier:internal"] },
    { endpoint: `alchemy://${process.env.ALCHEMY_API_KEY}` },
  ],
  networks: [{
    architecture: "evm",
    evm: { chainId: 1 },
    selectionPolicy: {
      evalFunc: `(upstreams, ctx) => upstreams
        .keepHealthy({ maxErrorRate: 0.5, maxBlockHeadLag: 16, maxP95Ms: 5000 })
        .whenEmpty(() => upstreams)
        .preferTag('tier:internal', { minHealthy: 1, fallback: '!tier:internal' })
        .sortByScore(PREFER_FASTEST)
        .stickyPrimary({ hysteresis: 0.30, minSwitchInterval: '30s' })
        .probeExcluded({ sampleRate: 0.1 })`,
    },
  }],
}]
```

## How the policy output is consumed

The selection policy returns an ordered upstream list once per tick. At request time, `Network.Forward` fetches that list and installs it on the request with `req.SetUpstreams(upsList)` (`erpc/networks.go:951-1003`). `NextUpstream()` then round-robins through that exact list for the primary attempt, retries, and hedge legs (`common/request.go:1524-1594`).

Because consensus is disabled, the executor composition is `retry(hedge(runUpstreamSweep))`: each retry attempt sweeps through the same ordered list (`erpc/network_executor.go:141-203`). Anything the policy removes from the list is unavailable for the whole request.

## Two ways to express "internal first"

### 1. Hard tier pin with `preferTag` (maximum cost reduction)

`preferTag('tier:internal', { minHealthy, fallback })` filters the list down to the matching subset when enough internal nodes survive the health gate; otherwise it falls back to the external tier (`internal/policy/stdlib/stdlib.js:942-953`). The shipped default policy uses exactly this shape (`internal/policy/default_policy.js:43-54`).

Trade-offs:

- **Pros:** External providers receive zero traffic while any healthy internal node exists. Maximum cost savings.
- **Cons:** Retries and hedge legs inside the same request can only use internals. If the internal tier is selected but the chosen node fails, the request cannot fall back to a paid provider until the next policy tick drops the unhealthy internals.

Best for: fleets with at least 2 internal nodes and workloads where occasional extra latency is cheaper than provider requests.

### 2. Soft score bias (maximum availability)

Instead of removing externals, keep both tiers in the list and boost internal scores so they rank first.

Options:

- **Per-upstream config:** set `routing.scoreMultipliers[].overall` on each internal node (`common/config.go:876-882`).
- **Policy-level bias:** pass an `overall` function to `sortByScore(..., { overall })`.

Trade-offs:

- **Pros:** Externals remain in the request's upstream list, so retries and hedges can still reach them instantly if an internal node fails mid-request.
- **Cons:** Some traffic still reaches paid providers (e.g. when an internal is slow enough that its boosted score is still worse than a fast external, or via hedges).

Best for: mixed fleets where a single internal node must carry traffic, or where request-level failover is more important than squeezing every last provider request.

## Config schema

Selection policy primitives are defined in `typescript/config/src/types/policyEval.ts`.

| Primitive | What it does | Relevant options |
|---|---|---|
| `keepHealthy(opts)` | Drops upstreams whose metrics exceed thresholds. Default thresholds: `maxErrorRate: 0.5`, `maxBlockHeadLag: 10`, `maxP95Ms: 5000`, `maxThrottledRate: 0.3` (`internal/policy/stdlib/stdlib.js:426-439`). | `maxErrorRate`, `maxBlockHeadLag`, `maxP95Ms`, `maxThrottledRate` |
| `excludeIf(predicate, opts)` | Composable alternative to `keepHealthy`; records exclusion reasons and probe eligibility. | predicate factories like `errorRateAbove`, `blockNumberLagAbove`; `{ probe: false }` for state-poller-fed metrics |
| `preferTag(pat, opts)` | Returns only upstreams matching `pat` if `>= minHealthy` match; otherwise tries `fallback`. | `minHealthy` (default 1), `fallback` (tag pattern) |
| `sortByScore(base, opts)` | Ranks best-first: `score = overall / (1 + Σ metric × weight)`. Merges per-upstream `scoreMultipliers` by default. | base preset or function; `opts.multipliers`, `opts.overall` |
| `stickyPrimary(opts)` | Holds the primary stable across ticks unless a challenger is meaningfully better. | `hysteresis` (default 0.3), `minSwitchInterval` (default 30s) |
| `probeExcluded(opts)` | Enables shadow probes to excluded upstreams so traffic-fed metrics recover and they re-admit. | `sampleRate` (default 0.1), `minSamples` (default 10), `minSamplesWindow` (default 60s), `maxConcurrent` (default 4), `timeout` (default 10s) |
| `whenEmpty(fn)` | Restores the given set if a previous step emptied the list. | fallback supplier, usually `() => upstreams` |

Per-upstream score bias is configured through `routing.scoreMultipliers` (`common/config.go:911-933`):

| Field | Type | Behavior |
|---|---|---|
| `routing.scoreMultipliers[].network` | `string` | Scope entry to a network id; `*` or empty matches all |
| `routing.scoreMultipliers[].method` | `string` | Scope entry to a method; `*` or empty matches all |
| `routing.scoreMultipliers[].finality` | `DataFinalityState[]` | Scope entry to finality states |
| `routing.scoreMultipliers[].overall` | `float64` | Multiplies the final score; `>1` prefers this upstream |
| `routing.scoreMultipliers[].respLatency` | `float64` | Overrides the latency weight multiplier |
| `routing.scoreMultipliers[].errorRate` | `float64` | Overrides the error-rate weight multiplier |
| `routing.scoreMultipliers[].blockHeadLag` | `float64` | Overrides the head-lag weight multiplier |
| `routing.scoreMultipliers[].throttledRate` | `float64` | Overrides the throttling weight multiplier |

## Worked examples

### Hard pin to internal tier with tick-level fallback (cost-first)

Use this when you have at least two internal nodes and want provider requests to stop almost entirely while internals are merely "good enough."

```yaml
selectionPolicy:
  evalFunc: |
    (upstreams, ctx) => upstreams
      # Tolerate slow internals; evict only when clearly broken
      .keepHealthy({ maxErrorRate: 0.5, maxBlockHeadLag: 16, maxP95Ms: 5000 })
      .whenEmpty(() => upstreams)
      # Pin to internal while >=1 healthy internal exists; otherwise use externals
      .preferTag('tier:internal', { minHealthy: 1, fallback: '!tier:internal' })
      .sortByScore(PREFER_FASTEST)
      .stickyPrimary({ hysteresis: 0.30, minSwitchInterval: '30s' })
      .probeExcluded({ sampleRate: 0.1 })
```

```ts
selectionPolicy: {
  evalFunc: `(upstreams, ctx) => upstreams
    .keepHealthy({ maxErrorRate: 0.5, maxBlockHeadLag: 16, maxP95Ms: 5000 })
    .whenEmpty(() => upstreams)
    .preferTag('tier:internal', { minHealthy: 1, fallback: '!tier:internal' })
    .sortByScore(PREFER_FASTEST)
    .stickyPrimary({ hysteresis: 0.30, minSwitchInterval: '30s' })
    .probeExcluded({ sampleRate: 0.1 })`,
}
```

### Soft policy-level bias (availability-first)

Use this when you want internal-first ranking but need externals available for retries and hedges within the same request.

```yaml
selectionPolicy:
  evalFunc: |
    (upstreams, ctx) => upstreams
      .keepHealthy({ maxErrorRate: 0.5, maxBlockHeadLag: 16, maxP95Ms: 5000 })
      .whenEmpty(() => upstreams)
      .sortByScore(PREFER_FASTEST, {
        overall: (u) => (u.tags || []).includes('tier:internal') ? 10 : 1
      })
      .stickyPrimary({ hysteresis: 0.30, minSwitchInterval: '30s' })
      .probeExcluded({ sampleRate: 0.1 })
```

```ts
selectionPolicy: {
  evalFunc: `(upstreams, ctx) => upstreams
    .keepHealthy({ maxErrorRate: 0.5, maxBlockHeadLag: 16, maxP95Ms: 5000 })
    .whenEmpty(() => upstreams)
    .sortByScore(PREFER_FASTEST, {
      overall: (u) => (u.tags || []).includes('tier:internal') ? 10 : 1
    })
    .stickyPrimary({ hysteresis: 0.30, minSwitchInterval: '30s' })
    .probeExcluded({ sampleRate: 0.1 })`,
}
```

### Static per-upstream score multiplier (tag-agnostic)

Use this when internal nodes are configured once and you do not want the policy itself to know about tiers.

```yaml
upstreams:
  - id: internal-1
    endpoint: http://10.0.0.10:8545
    routing:
      scoreMultipliers:
        - overall: 10
  - id: internal-2
    endpoint: http://10.0.0.11:8545
    routing:
      scoreMultipliers:
        - overall: 10
selectionPolicy:
  evalFunc: (upstreams, ctx) => upstreams
    .keepHealthy({ maxErrorRate: 0.5, maxBlockHeadLag: 16, maxP95Ms: 5000 })
    .whenEmpty(() => upstreams)
    .sortByScore(PREFER_FASTEST)
    .stickyPrimary({ hysteresis: 0.30, minSwitchInterval: '30s' })
    .probeExcluded({ sampleRate: 0.1 })
```

```ts
upstreams: [
  { id: "internal-1", endpoint: "http://10.0.0.10:8545", routing: { scoreMultipliers: [{ overall: 10 }] } },
  { id: "internal-2", endpoint: "http://10.0.0.11:8545", routing: { scoreMultipliers: [{ overall: 10 }] } },
],
selectionPolicy: {
  evalFunc: `(upstreams, ctx) => upstreams
    .keepHealthy({ maxErrorRate: 0.5, maxBlockHeadLag: 16, maxP95Ms: 5000 })
    .whenEmpty(() => upstreams)
    .sortByScore(PREFER_FASTEST)
    .stickyPrimary({ hysteresis: 0.30, minSwitchInterval: '30s' })
    .probeExcluded({ sampleRate: 0.1 })`,
}
```

## Request/response behavior

- The policy output is fetched once per request and fixed for that request's lifetime. Retries and hedges cannot reach upstreams that were removed by `preferTag`.
- With `preferTag`, the fallback tier only enters consideration on the next policy tick after the health gate drops the internal tier. There is no in-request tier switch.
- With score bias, the primary is usually internal, but hedge legs and retries can still race/fall back to external nodes in the same request.
- `stickyPrimary` reduces primary flapping across ticks but does **not** add hysteresis to `preferTag`'s tier switch. A flapping internal node near the health threshold can still cause tick-level tier oscillation.
- `probeExcluded` is required for traffic-fed metrics (`errorRate`, `p95`, `throttledRate`) to recover on excluded nodes. `blockHeadLag` is fed by the state poller and recovers without probes.

## Best practices

- **Always put a health gate before `preferTag`.** Without `keepHealthy`/`excludeIf`, `minHealthy` counts tag matches, not health, and a degraded internal node will keep receiving 100% of traffic.
- **Set `keepHealthy` thresholds loose enough for your internal nodes.** If your nodes are expected to be slow, raising `maxP95Ms` or `maxBlockHeadLag` keeps them in rotation longer and reduces provider fallbacks.
- **Use `minHealthy: 2` or higher if you have capacity headroom.** This drains to externals before the internal tier is down to a single node.
- **Run at least two internal nodes if you use `preferTag` + hedge.** With only one internal in the list, hedge has no alternative leg and cannot cut tail latency.
- **Keep `probeExcluded` in the chain** so internal nodes that temporarily fail can prove recovery and re-admit automatically.
- **Match `probeExcluded.minSamples` to your exclusion sample threshold.** If you exclude with `samplesAbove(N)`, set `minSamples` ≥ `N` so the probe subsystem generates enough samples for re-admission.
- **Use `fallback: '!tier:internal'` instead of `'*'`.** A positive `'*'` only matches upstreams that actually have tags; negation also covers untagged providers.

## Edge cases & gotchas

1. **`preferTag` discards non-matching upstreams, it does not sort them to the tail.** If the internal tier wins, externals are removed from the selection set entirely for that tick (`internal/policy/stdlib/stdlib.js:942-953`).
2. **`minHealthy` is a hard count, not a target.** The primitive never relaxes the pattern to reach the desired number of healthy nodes (`internal/policy/stdlib/stdlib.js:946-947`).
3. **Fallback only happens at policy-tick granularity.** A request that starts while internals are still healthy will not see newly-unhealthy internals excluded until the next tick; conversely, a recovering internal is not re-selected until the next tick.
4. **`whenEmpty(() => upstreams)` restores the raw set including unhealthy internals.** In a total outage this fail-open behavior is usually the right choice, but it means a brief all-unhealthy window can re-pin to broken internals. Tighten `keepHealthy` or add an explicit `excludeIf` guard if that is unacceptable.
5. **A single internal node + `preferTag` disables hedge.** Hedge legs need distinct upstreams to race; with one internal in the list, hedge gets `ErrNoUpstreamsLeftToSelect` and waits for the primary (`erpc/network_executor.go:572-589`).
6. **Score bias is not a hard pin.** A very slow internal can still be outranked by a fast external if the metric penalty overwhelms the `overall` multiplier.
7. **`stickyPrimary` stabilizes the primary inside a tier, not the tier choice.** It will not prevent `preferTag` from flipping between internal and external on consecutive ticks when the internal count crosses `minHealthy`.
8. **`keepHealthy` exclusions are probed by default.** `keepHealthy` is not an `excludeIf`-family step, so it does not emit a probe verdict; the zero-value verdict resolves to `ShouldProbe() == true`, meaning excluded nodes still receive shadow probes (`internal/policy/eval.go:873-889`).
9. **Traffic-fed metrics freeze without probes.** If you omit `probeExcluded`, an internal node excluded for high error rate will stay excluded because its error rate receives no new samples. Block-head lag recovers via the state poller regardless.
10. **Provider cost is bounded by `minHealthy`.** Lower `minHealthy` keeps traffic on internals longer (cheaper); higher `minHealthy` drains to providers earlier (safer).

## Observability

| Metric | Type | Labels | What to watch |
|---|---|---|---|
| `erpc_upstream_request_total` | counter | project, network, upstream, category, ... | Direct cost proxy: compare internal vs external request volumes |
| `erpc_upstream_selection_total` | counter | project, network, upstream, category, reason, finality | Which upstreams were selected and why |
| `erpc_selection_exclusion_total` | counter | project, network, reason, ... | Why upstreams were excluded; spike here explains provider fallback |
| `erpc_network_retry_attempt_total` | counter | project, network, method, reason, finality | Retry reasons; elevated `retryable_error` or `missing_data` may mean internals are failing |
| `erpc_network_hedged_request_total` | counter | project, network, upstream, category, attempt, finality, user, agent_name | Hedge fan-out volume |
| `erpc_network_hedge_winner_total` | counter | project, network, upstream, category, finality | Which upstream won the hedge race; external winners under `preferTag` are impossible |
| `erpc_upstream_request_duration_seconds` | histogram | project, network, upstream, category, ... | Tail latency per upstream; internals should be higher but bounded |

Dashboard signals that the balance is healthy:

- `erpc_upstream_request_total{upstream=internal-*}` is the dominant line.
- `erpc_selection_exclusion_total{reason=...}` spikes only during known internal issues.
- `erpc_network_retry_attempt_total` and hedges are not climbing relative to total traffic.

## Source code entry points

- [`internal/policy/stdlib/stdlib.js:L942-L953`](https://github.com/erpc/erpc/blob/main/internal/policy/stdlib/stdlib.js#L942-L953) — `preferTag` implementation
- [`internal/policy/default_policy.js:L43-L54`](https://github.com/erpc/erpc/blob/main/internal/policy/default_policy.js#L43-L54) — shipped default policy using `preferTag`
- [`common/config.go:L876-L882`](https://github.com/erpc/erpc/blob/main/common/config.go#L876-L882) — `UpstreamRoutingConfig` and `scoreMultipliers`
- [`common/config.go:L911-L933`](https://github.com/erpc/erpc/blob/main/common/config.go#L911-L933) — `ScoreMultiplierConfig` schema
- [`erpc/networks.go:L951-L1003`](https://github.com/erpc/erpc/blob/main/erpc/networks.go#L951-L1003) — policy list fetched and installed on the request
- [`common/request.go:L1524-L1594`](https://github.com/erpc/erpc/blob/main/common/request.go#L1524-L1594) — `NextUpstream` round-robins the installed list
- [`erpc/network_executor.go:L141-L203`](https://github.com/erpc/erpc/blob/main/erpc/network_executor.go#L141-L203) — retry/hedge/sweep composition
- [`erpc/network_executor.go:L516-L645`](https://github.com/erpc/erpc/blob/main/erpc/network_executor.go#L516-L645) — `runHedge` wiring and `keep` predicate
- [`erpc/networks.go:L1128-L1265`](https://github.com/erpc/erpc/blob/main/erpc/networks.go#L1128-L1265) — `runUpstreamSweep` iterates the ordered list
- [`internal/policy/eval.go:L873-L889`](https://github.com/erpc/erpc/blob/main/internal/policy/eval.go#L873-L889) — probe verdict matrix and default probe behavior

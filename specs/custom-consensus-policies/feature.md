# Custom Consensus Policies Engine — Specification

**Status**: Accepted direction, pre-implementation
**Issue**: [#1088](https://github.com/erpc/erpc/issues/1088)
**Direction**: [@aramalipoor](https://github.com/erpc/erpc/issues/1088#issuecomment-5492667906)
**Last revised**: 2026-09-09

Companion: [plan.md](./plan.md)

---

## 1. Purpose

The **Custom Consensus Policies Engine** lets operators define **named declarative
consensus policies** and a **pre-round freeform JS selector** (`customPolicy`) that
picks which policy governs each request.

Freeform JS decides **which** declarative policy to run — never **how** the round
is graded. The consensus executor and its `ConsensusPolicyConfig` contract stay
unchanged. Open-ended operator logic (caller role, upstream health, block
availability) resolves into one bounded interface — a named
`ConsensusPolicyConfig`. This mirrors the selection-policy pattern
(`internal/policy`): freeform JS at the edge, declarative result into the core.

### Goals

- `consensus.policies.<name>`: named map of full `ConsensusPolicyConfig`.
- `consensus.customPolicy.evalFunction`: JS evaluated **per request, before the
  round**, returning a policy name (or inline policy object).
- Eval context: request (method, blockNumber, network), user/roles, upstreams
  (tags, health, blockAvailability).
- Role-gated automatic fallback when internals are unavailable for availability
  reasons (not punishment).
- Historical serving: recent → internal+external; historical (outside internal
  `blockAvailability`) → external-only with ≥2 agreeing, via a declarative
  MissingData composition waiver.
- Observability: chosen policy name on every served round (header + metric);
  optional decision cache.
- Zero migration: today's inline `consensus:` block becomes the default policy.

### Non-goals

- No post-round JS grading. `agreeing` / `punished` / `valueGroups` are **not**
  exposed to JS.
- No mid-round policy switching. A round runs under the policy selected at
  start (fail-visible, not fail-silent).
- No separate claim→policy allowlist outside JS. Role→policy lives in
  `customPolicy.evalFunction` via `ctx.user`.
- #1069-style bounded-deviation value logic stays executor-side (separate
  change).
- Empty-response waiver with block proof is **spec'd for v1.1**, not in v1
  milestones (build when dispute traces force it).

---

## 2. Configuration

```yaml
consensus:
  policies:                          # each value = full ConsensusPolicyConfig
    standard:
      maxParticipants: 3
      agreementThreshold: 2
      disputeBehavior: returnError
      requiredParticipants:
        - { tag: "type:internal", minParticipants: 1, minAgreement: 1, waiveAgreementOnMissingData: true }
        - { tag: "type:external", minParticipants: 2, minAgreement: 2 }  # never waived
    fallback:                        # internals down; healthy pool is the externals
      maxParticipants: 3
      agreementThreshold: 2
    generous-dev:
      maxParticipants: 2
      agreementThreshold: 1
      disputeBehavior: acceptMostCommonValidResult

  customPolicy:                      # optional; omitted = default policy for all
    evalTimeout: 50ms                # hard cap; fail closed to default on timeout
    evalFunction: |
      (ctx) => {
        const internals = ctx.upstreams.withTag("type:internal")
        if (internals.healthy().length === 0 && !internals.anyPunished())
          return ctx.user?.hasRole("brp:consensus-fallback") ? "fallback" : "standard"
        if (ctx.user?.hasRole("brp:dev")) return "generous-dev"
        return "standard"
      }
```

| Field | Type | Description |
|---|---|---|
| `policies` | `map[string]ConsensusPolicyConfig` | Named policies. Each value is a complete consensus config (same fields as today's inline block). |
| `customPolicy.evalFunction` | `string` | JS function. Signature `(ctx) => string \| object \| null`. If omitted, the default/inline policy applies to every request. |
| `customPolicy.evalTimeout` | `Duration` | Hard wall-clock cap on each eval. Fail closed to default on timeout. |
| `requiredParticipants[].waiveAgreementOnMissingData` | `bool` | Opt-in, default `false`. See §6. |

**Backward compatibility:** an inline `consensus:` block with no `policies` map
is one anonymous default policy — zero operator migration. All existing fields
(`agreementThreshold`, `requiredParticipants`, `punishMisbehavior`, …) keep
working. From #1041 lift the `X-eRPC-Consensus-Policy` header and
`consensus_policy` metric; ordered YAML `acceptancePolicies` is superseded.

---

## 3. Eval interface

```js
(ctx) => string | object | null
// ctx.request   { method, blockNumber: number|null, network }
// ctx.user      { roles: string[], claims: object } | null
// ctx.upstreams [{ id, tags, health, blockAvailability: {lower, upper} | null }]
//
// return "name" → run policies[name]
//                 (unknown name → fail closed to default + warn log)
// return {…}    → inline ConsensusPolicyConfig (escape hatch; weaker observability)
// return null   → default policy
```

The eval never sees round outcomes. It answers one question: *under which rule
set should this request's round run?*

### 3.1 Stdlib (v1, chainable — mirrors selection policy)

Grow only when forced by observed configs:

| Helper | Behavior |
|---|---|
| `upstreams.withTag(t)` | Filter by tag |
| `upstreams.healthy()` | Exclude unhealthy |
| `upstreams.anyPunished()` | True iff any upstream that **would otherwise be eligible to participate** (healthy or sitout) is currently in sitout — not "ever punished" |
| `upstreams.canServeBlock(n)` | Uses existing `EvmAssertBlockAvailability` |
| `user.hasRole(r)` | Role check |

---

## 4. Architecture

```
internal/consensus/policy/   ← new standalone engine: sobek pool (compile once,
                               borrow VM per eval), stdlib, no executor imports
consensus/executor.go        ← resolve policy BEFORE the round; round code untouched
auth/ , health/              ← supply ctx.user and upstream health/availability
```

### 4.1 Request flow

1. Auth resolves user/roles (existing).
2. Build `EvalContext` (request, user, upstream refs, network).
3. `selector.Evaluate(ctx)` bounded by `evalTimeout` → policy name
   (decision-cache lookup first — §5).
4. Resolve name → `ConsensusPolicyConfig` (unknown/empty → default).
5. Existing executor runs under that config — untouched code path.
6. Emit `X-eRPC-Consensus-Policy: <name>` header + `consensus_policy` metric
   label.

Eval failure, timeout, or unknown name fails **closed** to the default policy
with an error/warn log — never to a more permissive policy.

### 4.2 Latency

Eval runs once per request **before** fan-out and does not extend consensus wait
windows. Cache hit ≈ map lookup (µs). Cache miss ≈ Sobek bytecode on a
pre-warmed VM (tens–low hundreds of µs) — negligible vs upstream RTT.

---

## 5. Decision cache

Freeform JS cannot declare its dependencies — the cache key is **auto-derived
from what the eval actually reads**:

- First eval per (network, method) runs with a tracking ctx; property getters
  record accessed paths (`user.roles`, `request.blockNumber`,
  `upstreams[].health`, …). Key schema = accessed paths; key = their values.
  Fallback: operator-declared `cacheKeys` if sobek getter tracking proves
  impractical.
- Health/availability reads fold into one health-tracker **generation counter**
  (bumped on any upstream state transition) — O(1) invalidation. Tradeoff: a
  single flapping upstream invalidates all cached decisions for the network;
  correctness over hit rate.
- `request.blockNumber` buckets by the union of configured `blockAvailability`
  boundaries; a per-(network, method) **cardinality guard** disables caching
  for evals whose keys explode.
- Evals touching time/random/external state are marked **uncacheable** and
  always execute — safe fallthrough.
- Cache stores the policy **name** only; name→config resolution happens after
  the hit (survives config reload). Errors are never cached.
- Bounded LRU + TTL backstop.
- Metrics: `consensus_policy_cache_hits_total`, `_misses_total`,
  `_bypassed_total{reason}`.

---

## 6. Automatic fallback (role-gated)

When internals are unavailable for **availability** reasons, authorized roles
run a plain consensus over whoever is healthy — no tag quotas needed, because
the healthy pool *is* the externals:

```yaml
fallback:
  maxParticipants: 3
  agreementThreshold: 2
```

Eval (pre-round):

```js
internals.healthy().length === 0 && !internals.anyPunished()
  && ctx.user.hasRole("brp:consensus-fallback")
  → "fallback"
```

Unauthorized callers stay on `standard` and dispute — never silently
downgraded.

| Rule | Behavior |
|---|---|
| Availability vs punishment | Sitout must be distinct from unhealthy in `ctx.upstreams[].health`. Punished → stay on `standard` → hard dispute. An attacker who gets internals punished must not force a downgrade. |
| In-flight failure | No mid-round switch. That round disputes under `standard`; the tracker records it; the *next* request's eval picks `fallback`. Cache invalidation is immediate via the health generation counter. |
| Recovery | Internals healthy again → eval returns `standard`. Automatic both ways. Header `X-eRPC-Consensus-Policy: fallback` makes the degraded grade explicit. |

---

## 7. Serving historical data

Goal: recent → internal+external; historical (outside internal
`blockAvailability`) → external-only with ≥2 agreeing.

### 7.1 Three layers

1. **Pre-round (primary).** Internals configure `blockAvailability`; the
   block-availability guard already short-circuits out-of-range requests with
   `ErrEndpointMissingData` before any provider call (`common/config.go`).
2. **MissingData waiver (safety net, v1).** New
   `requiredParticipants[].waiveAgreementOnMissingData`: in
   `enforceWinnerComposition` a failing `minAgreement` quota is waived iff
   **every** tag-matching participant returned `ErrEndpointMissingData` —
   already the normalized, terminal, non-misbehavior edge classification
   (`consensus/analysis.go`). Covers stale/misconfigured `blockAvailability`
   and error-shaped pruning. A single value-voting match holds the quota.
3. **Empty-waiver with block proof (v1.1 — spec now, not in milestones).**
   Null-shaped pruning (`eth_getTransactionByHash` post-EIP-4444) is ambiguous
   at the source — but the winning tx/receipt/log carries its own
   `blockNumber` (wire-stable). `waiveAgreementOnEmptyOutsideRetention` fires
   only when all matching participants returned empty **and** the winner's
   block is outside the abstainer's configured range. Field extraction is
   config-driven (`blockEvidenceFields`, same pattern as
   `preferHighestValueFor`); unlisted method → no waiver → dispute. Fabricated
   recent tx + internal null → proof fails → quota holds → dispute.

Role-gating historical access (optional): two policies differing only in the
waiver flags; eval picks via `hasRole`. If historical is open to all, one
policy + waiver covers it with zero JS.

### 7.2 Edge-case matrix

| Scenario | Outcome |
|----------|---------|
| Old block, `blockAvailability` correct | Guard short-circuit → MissingData → waiver → external 2-agreement; internal never called |
| Old block, `blockAvailability` stale/wrong | Provider error normalized to MissingData → waiver (net when `blockAvailability` lies) |
| Pruned tx, node returns MissingData error | Waiver → external 2-agreement |
| Pruned tx, node returns `null` | v1: dispute. v1.1: empty-waiver with block proof serves |
| Data never existed (all null / all MissingData) | `null` served (empty ≥ threshold) / agreed MissingData error — correct |
| Just-mined tx not yet on internal (internal null) | Dispute → retry succeeds. Serving it needs minAgreement-0 + preferNonEmpty = externals outvote internal — rejected |
| Internal wrong value (misbehavior) | Quota holds → composition dispute + `punishMisbehavior` |
| Internal outage (infra error ≠ MissingData) | Quota holds → dispute for unauthorized; authorized roles get `fallback` on the next request (§6) |
| Internal punished/sitout (misbehavior) | Eval stays on `standard` → hard dispute — never falls through to `fallback` |
| Externals disagree (one archive, one not) | No group ≥ 2 → dispute. Operator pins archive externals via tags |

### 7.3 Requirements

- **R1** waiver fields on `requiredParticipants[]` (opt-in, default off) +
  executor change localized to `enforceWinnerComposition`.
- **R2** `blockAvailability` on every internal upstream;
  `canServeBlock(n)` uses existing `EvmAssertBlockAvailability`.
- **R3** archive externals tagged, `minAgreement: 2`, never waivable.
- **R4** blockNumber extraction for eval ctx.
- **R5** observability: `consensus_composition_waived_total{tag,reason}` +
  log on every waiver fire; policy name header/metric as above.
- **R6** tests: one per matrix row; fallthrough cases (nil user, unknown
  policy name, unlisted method) first.
- **R7** punished/sitout distinct from unhealthy in eval ctx; fallback eval
  must refuse to fire when any matching internal is punished.

---

## 8. Observability

| Signal | Meaning |
|---|---|
| `X-eRPC-Consensus-Policy` response header | Chosen policy name on every served round |
| `consensus_policy` metric label | Same, on consensus metrics |
| `consensus_composition_waived_total{tag,reason}` | Waiver fires (`missing_data` / later `empty_outside_retention`) |
| `consensus_policy_cache_hits_total` / `_misses_total` / `_bypassed_total{reason}` | Decision-cache health |
| Warn log on unknown policy name | Config typo signal |
| Error log on eval failure/timeout | Fail-closed fallback |

---

## 9. Security

- JS is operator-supplied config, same trust level as selection-policy eval.
- Auth data flows **into** the eval (`ctx.user`) — role-aware decisions are the
  point. No separate claim→policy allowlist outside JS.
- Eval failure / timeout / unknown name → default policy (never more
  permissive).
- Fallback must not fire on punishment (R7).
- Empty-waiver (v1.1) must not fire without block proof outside retention —
  closes the correlated-externals-outvote-internal hole for recent data.

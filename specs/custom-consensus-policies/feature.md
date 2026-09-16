# Custom Consensus Policies Engine — Specification

**Status**: Implemented — v1 MVP + v1.1 empty-outside-retention waiver shipped
**Issue**: [#1088](https://github.com/erpc/erpc/issues/1088)
**Direction**: [@aramalipoor](https://github.com/erpc/erpc/issues/1088#issuecomment-5492667906)
**Last revised**: 2026-09-16
**Synced to**: `crcl-main/erpc` PR
[#136](https://github.com/crcl-main/erpc/pull/136) as of `70e16699`

Companion: [plan.md](./plan.md) (phased delivery) ·
[feature-details.md](./feature-details.md) (implementation edge cases, proof
order, wait-cap seal, hedge vote, metric names, failure modes). Operator-facing
docs shipped at `docs/pages/config/failsafe/consensus-policies.mdx`.

> This document is the promise-level design contract. It now describes what was
> **built** (not just proposed); wire-level and executor-level mechanics live in
> [feature-details.md](./feature-details.md). The one behavior most changed from
> the original proposal: waivers release a quota on **≥ `minAgreement`** distinct
> tag-matching abstentions, not on a unanimous "every matching participant
> abstained" (see §7 / details §1).

---

## 1. Purpose

The **Custom Consensus Policies Engine** lets operators define **named declarative
consensus policies** and a **pre-round freeform JS selector** (`customPolicy`) that
picks which policy governs each request.

After v1, an operator can:

- Run strict internal+external consensus for production callers, and a
  permissive `generous-dev` policy for a dev role — without code changes.
- Automatically fall back to external-only consensus when internal nodes are
  unhealthy (out of sync, down, unreachable) and not punished for consensus
  misbehaviour — but only for authorized roles.
- Serve historical / pruned data only for authorized roles: known-old blocks
  via an external-only `historical` policy; unknown-block methods (tx-hash)
  via `standard-waive-missing` (same composition as `standard` + MissingData
  waiver). Unauthorized callers stay on strict `standard` and dispute.

Freeform JS decides **which** declarative policy to run — never **how** the round
is graded. The consensus executor and its `ConsensusPolicyConfig` contract stay
unchanged. Open-ended operator logic (caller role, upstream health, block
availability) resolves into one bounded interface — a named
`ConsensusPolicyConfig`. This mirrors the selection-policy pattern
(`internal/policy`): freeform JS at the edge, declarative result into the core.

### Goals

- `consensus.policies.<name>`: named map of full `ConsensusPolicyConfig`.
- `consensus.customPolicy.evalFunction`: JS evaluated **per request, before the
  round**, returning a policy name.
- Eval context: request (method, blockNumber, network), user/roles, upstreams
  (tags, health, blockAvailability).
- Role-gated automatic fallback when internals are unavailable for availability
  reasons (not punishment).
- Role-gated historical serving via three integrity grades: `standard` (no
  waiver), `standard-waive-missing` (waiver for pruned/MissingData),
  `historical` (external-only for known-old blocks).
- Observability: chosen policy name on every served round (header + metric).
- Zero migration: today's inline `consensus:` block becomes the default policy.

### Non-goals

- No post-round JS grading. `agreeing` / `punished` / `valueGroups` are **not**
  exposed to JS.
- No mid-round policy switching. A round runs under the policy selected at
  start (fail-visible, not fail-silent).
- No separate claim→policy allowlist outside JS. Role→policy lives in
  `customPolicy.evalFunction` via `ctx.user`.
- No inline policy object return in v1 — eval returns a **name** only.
- No decision cache in v1 — added only if measured eval cost forces it (still
  future; not built).
- #1069-style bounded-deviation value logic stays executor-side (separate
  change).
- ~~Empty-response waiver with block proof is **spec'd for v1.1**, not in v1
  milestones~~ → **SHIPPED as v1.1** (`waiveAgreementOnEmptyOutsideRetention`,
  `retentionBlocks`, `blockEvidenceFields`), with block proof by availability
  lower bound then chain-head depth. See §7 and details §2.

---

## 2. Configuration

```yaml
consensus:
  policies:                          # each value = full ConsensusPolicyConfig
    standard:                        # default / fail-closed — no MissingData waiver
      maxParticipants: 3
      agreementThreshold: 2
      disputeBehavior: returnError
      requiredParticipants:
        - { tag: "type:internal", minParticipants: 1, minAgreement: 1 }
        - { tag: "type:external", minParticipants: 2, minAgreement: 2 }
    standard-waive-missing:          # same composition as standard; waiver for pruned/MissingData
      maxParticipants: 3
      agreementThreshold: 2
      disputeBehavior: returnError
      requiredParticipants:
        - { tag: "type:internal", minParticipants: 1, minAgreement: 1, waiveAgreementOnMissingData: true }
        - { tag: "type:external", minParticipants: 2, minAgreement: 2 }  # never waived
    historical:                      # known-old block — external-only (skip internals)
      maxParticipants: 3
      agreementThreshold: 2
      requiredParticipants:
        - { tag: "type:external", minParticipants: 2, minAgreement: 2 }
    fallback:                        # internals unhealthy (not punished); healthy pool is the externals
      # Use only when internals are out of sync / down / unreachable.
      # If internals are absent from the round because they were punished for
      # consensus misbehaviour, stay on standard — never select fallback.
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
        // Punishment or admin cordon anywhere → never downgrade.
        if (ctx.upstreams.anyPunished()) return "standard"
        if (ctx.upstreams.anyOperatorCordon()) return "standard"
        const internals = ctx.upstreams.withTag("type:internal")
        // Genuine outage (unhealthy or availability-cordoned only) → fallback.
        if (internals.allUnavailable())
          return ctx.user?.hasRole("brp:consensus-fallback") ? "fallback" : "standard"
        // Historical access is role-gated — unauthorized stay on standard (dispute on miss).
        if (ctx.user?.hasRole("brp:historical")) {
          const n = ctx.request.blockNumber
          if (n !== null && internals.canServeBlock(n).length === 0)
            return "historical"              // known-old: external-only, no internal call
          if (n === null)
            return "standard-waive-missing"  // tx-hash etc.: try internals, waive MissingData
        }
        if (ctx.user?.hasRole("brp:dev")) return "generous-dev"
        return "standard"
      }
```

Three integrity grades for data serving (plus `fallback` / `generous-dev`):

| Policy | Composition | MissingData waiver | When selected |
|---|---|---|---|
| `standard` | internal + external | No | Default / fail-closed; unauthorized callers |
| `standard-waive-missing` | same as `standard` | Yes (internals) | Authorized role + unknown block (`blockNumber` null, e.g. tx-hash) |
| `historical` | external only | n/a | Authorized role + known-old block (`canServeBlock` empty) |

| Field | Type | Description |
|---|---|---|
| `policies` | `map[string]ConsensusPolicyConfig` | Named policies. Each value is a complete consensus config (same fields as today's inline block). |
| `customPolicy.evalFunction` | `string` | JS function. Signature `(ctx) => string \| null`. If omitted, the default/inline policy applies to every request. |
| `customPolicy.evalTimeout` | `Duration` | Hard wall-clock cap on each eval; default `50ms` (`common/defaults.go`). Fail closed to default on timeout (VM discarded). |
| `requiredParticipants[].waiveAgreementOnMissingData` | `bool` | Opt-in, default `false`. Put on `standard-waive-missing`, not on the fail-closed `standard`. See §7. |
| `requiredParticipants[].waiveAgreementOnEmptyOutsideRetention` | `bool` | v1.1 opt-in, default `false`. Waives the quota when ≥ `minAgreement` matching participants returned **empty** and the winner's block is proven outside retention. Requires `retentionBlocks > 0` and `blockEvidenceFields`. See §7 / details §2. |
| `requiredParticipants[].retentionBlocks` | `int64` | Default `0`. Retained depth behind chain head for the chain-head fallback proof (`head − winnerBlock > retentionBlocks`). Must be `> 0` when the empty-waiver is set. |
| `blockEvidenceFields` | `map[string][]string` | Per-method field paths carrying the winner's own block number (same shape as `preferHighestValueFor`). The block proof the empty-waiver reads; required when any entry opts into that waiver. |

**Backward compatibility:** an inline `consensus:` block with no `policies` map
is one anonymous default policy — zero operator migration. All existing fields
(`agreementThreshold`, `requiredParticipants`, `punishMisbehavior`, …) keep
working. This design **reimplements** the `X-ERPC-Consensus-Policy` header and
`consensus_policy` metric (not dependent on #1041). Ordered YAML
`acceptancePolicies` is superseded.

**Default policy must be the strictest.** The **inline `consensus:` block itself**
is the default policy — the round falls back to it whenever the eval returns
null/unknown, throws, times out, or the VM pool is exhausted. Operators must
configure it as the most restrictive policy; the spec assumes `standard` is that
policy. The reserved label `"default"` (`common.ConsensusDefaultPolicyName`) is
what the metric/header report for that fall-closed round, so `policies["default"]`
is rejected at load. A named policy may **not** itself define `customPolicy` /
`policies` (selection is top-level only) — also a load-time error.

---

## 3. Eval interface

```js
(ctx) => string | null
// ctx.request   { method, blockNumber: number|null, network }
// ctx.user      { roles: string[], claims: object } | null
// ctx.upstreams [{ id, tags, health, blockAvailability: {lower, upper} | null }]
//   health = { state: "healthy" | "unhealthy" | "cordoned",
//              cordonClasses: ("punishment"|"availability"|"operator")[],
//              cordonReason?: string }   // diagnostic only — never matched
//   An upstream can hold several cordons at once (§4.5), so the class is a
//   SET. Empty ⟺ not cordoned. `state == "cordoned"` if the set is non-empty,
//   and takes precedence over "unhealthy".
//
// return "name" → run policies[name]
//                 (unknown name → fail closed to default + warn log)
// return null   → default policy
```

The eval never sees round outcomes. It answers one question: *under which rule
set should this request's round run?*

### 3.1 Stdlib (v1, chainable — mirrors selection policy)

Grow only when forced by observed configs:

| Helper | Behavior |
|---|---|
| `upstreams.withTag(t)` | Filter by tag |
| `upstreams.healthy()` | Keep only `state == "healthy"` — cordoned upstreams of any class are excluded (cordon = out of rotation) |
| `upstreams.anyPunished()` | True if any **network** upstream holds a `punishment` cordon — **including nodes selection already dropped** (`removeCordoned`), which is why the shipped ctx carries a separate `PunishedOrOperator` scan list. Not "ever punished" |
| `upstreams.anyOperatorCordon()` | True if any network upstream holds an `operator` (admin) cordon (same dropped-node scan) — deliberate human action, undifferentiated between maintenance and distrust |
| `upstreams.allUnavailable()` | True if the set is **non-empty** and every member is either unhealthy-but-uncordoned or holds `availability` cordons and nothing else. **False on an empty set** — a tag matching no upstream is a config error, not an outage. Whitelist of availability only: `punishment`, `operator`, and any future cordon class make it false |
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

1. Auth resolves user/roles (**new plumbing** — see §4.3).
2. Build `EvalContext` (request, user, upstream refs, network).
3. `selector.Evaluate(ctx)` bounded by `evalTimeout` → policy name.
4. Resolve name → `ConsensusPolicyConfig` (unknown/empty → default).
5. Existing executor runs under that config — untouched code path.
6. Emit `X-ERPC-Consensus-Policy: <name>` header + `consensus_policy` metric
   label.

Eval failure, timeout, or unknown name fails **closed** to the default policy
with an error/warn log — never to a more permissive policy.

### 4.2 Latency

Eval runs once per request **before** fan-out and does not extend consensus wait
windows. Sobek bytecode on a pre-warmed VM is tens–low hundreds of µs —
negligible vs upstream RTT. A decision cache is **not** in v1; add only if
profiles force it.

### 4.3 Auth plumbing (new)

`common.User` today carries `Id`, `RateLimitBudget`, `AllowClientDirectives` —
**no roles or claims**. `auth/strategy_jwt.go` validates required claims and
claim matchers, then discards the claim map. `ctx.user.hasRole()` therefore
requires new plumbing:

- Add `Roles []string` to `common.User`.
- JWT strategy populates `Roles` from a configurable claim (default
  `roles`, comma-separated or array).
- All other strategies (`secret`, `network`, `database`, `siwe`) leave
  `Roles` empty — `hasRole` returns false.
- Roles are **never** accepted from client-controlled headers, query params,
  or body fields — only from the verified token.

This is a hard dependency of UC2 (role-gated fallback) and role-gated
historical access.

### 4.4 Sitout state ownership

Today `punishMisbehavior` sitout lives in a private executor map
(`misbehavingUpstreamsSitoutTimer`) plus `upstream.Cordon("*", "misbehaving in
consensus")`. The selector package must not import `consensus/`, so sitout
state moves to **`health.Tracker`**:

- Executor records sitout via `upstream.Cordon("*", "misbehaving in
  consensus", CordonClassPunishment)` and the matching
  `upstream.Uncordon("*", "end of consensus penalty", CordonClassPunishment)`
  on timer expiry. The claim is idempotent: a second
  punishment for the same upstream while already in sitout is a no-op (the
  timer is not reset).
- The misbehavior **rate limiter** also moves to `health.Tracker` (keyed by
  upstream ID) so that reaching a sitout is global across policies. The
  misbehavior **exporter** stays per-policy — it is a reporting sink, not a
  correctness signal. Because one destination can now back several policies,
  load-time validation rejects two policies sharing an S3
  `misbehaviorsDestination.path` unless each `filePattern` contains
  `{timestampMs}`; without it two exporters resolve the same key and an S3
  PUT overwrites rather than appends.
- **`cordonClass` is a required parameter on `Cordon` / `Uncordon`, and the
  tracker holds cordon state per class** (§4.5). The selector reads the class
  set — never the reason string.
- `anyPunished()` is true if any upstream that would otherwise be eligible
  holds a `punishment` cordon; `anyOperatorCordon()` is the matching helper
  for the `operator` class. Both are the explicit fail-closed gates in the
  reference eval (§2 / §6).

This keeps the zero-import rule for `internal/consensus/policy/` and gives
both executor and selector a shared, race-safe source of truth.

### 4.5 Cordon classes

Not every cordon is punishment, and not every non-punishment cordon is an
availability failure. `Cordon` takes a **typed class** alongside the
free-text reason; the selector matches on the class, never the reason string
(otherwise the eval would couple to the message text of every
cordon-producing call site):

| `cordonClass` | Set by | Meaning | Fallback |
|---|---|---|---|
| `punishment` | Consensus executor misbehavior sitout (`consensus/executor.go`) | Punished for misbehavior | **Blocks** fallback |
| `availability` | EVM state poller chain-identity mismatch (`architecture/evm/evm_state_poller.go`); SVM state poller lag/unhealthy (`architecture/svm/svm_state_poller.go`) | Automatic availability failure | **Allows** fallback |
| `operator` | Admin API (`erpc/admin.go`) | Deliberate operator cordon | **Blocks** fallback (fail closed) |

The class is required at the call site, so there is no runtime "unknown"
row: a cordon class added in the future blocks fallback until the selector
explicitly maps it to `availability` — fail closed by construction.

**Why `operator` fails closed.** An admin cordon is undifferentiated: the
same call serves planned maintenance and "take this node out, I do not trust
it". The API cannot tell them apart, so the class is read as the second one.
An operator who wants authorized callers to keep getting `fallback` during
maintenance changes the policy config, which is an explicit and audited act,
rather than relying on a cordon to imply it. The case for the opposite
reading is real — an admin cordon is the most deliberate, most declared
signal in the table — but an availability reading is unrecoverable when it is
wrong, and a config change is available when it is right.

#### Cordon state is held per class

An upstream can be cordoned by several sources at once, and today they share
one flag: `health.Tracker` stores a single `Cordoned` bool plus one
`LastCordonedReason` per `(upstream, method)` (`health/tracker.go`), and
`Uncordon` clears it without checking who set it. The three automatic
sources above all use method `"*"`, as does the admin API whenever the
operator passes no method, so a single scalar class would be last-writer-wins
on one shared entry:

- A state poller cordoning an already-punished upstream would overwrite
  `punishment` with `availability`, and `allUnavailable()` would then permit
  the downgrade while the sit-out is still running.
- The SVM poller's recovery `Uncordon` (`architecture/svm/svm_state_poller.go`)
  would clear a live consensus punishment cordon outright, and the sit-out
  timer's `Uncordon` would likewise clear a poller's live availability cordon.

So the tracker holds **one flag per class** — a bitmask over the closed class
enum on the existing `(upstream, method)` entry, replacing the single bool.
`IsCordoned` is "any bit set". `Uncordon(class)` clears one bit and leaves
the others. `CordonedAtMs` is stamped on the empty→non-empty transition and
cleared on the return to empty, so duration accounting is unchanged. This is
order-independent by construction: `allUnavailable()` is false while any
`punishment` bit is held, whatever arrived last.

**Method scope.** Cordons are keyed by method, and `IsCordoned` matches the
exact method or `"*"`. The three automatic sources use `"*"`; the admin API
passes an operator-supplied method, so an upstream can hold `operator` on one
method and `availability` on `"*"`. The eval sees the union of both scopes for
the request's method, which is why `health.cordonClasses` is a set.

The eval ctx exposes `health` as a structured value:
`{ state, cordonClasses, cordonReason? }` (§3). `cordonReason` is diagnostic
only, and `state` is `"cordoned"` whenever the class set is non-empty — a
cordoned upstream never reports `"unhealthy"`, so a failing health check
cannot mask a punishment cordon. The reference fallback predicate is
`allUnavailable()` (§3.1), which is true only for a non-empty set whose every
member is unhealthy-but-uncordoned or `availability`-cordoned and nothing
else.

### 4.6 Sobek pool

- Pool size: **8 pre-warmed VMs** (bounded; matches selection-policy order of
  magnitude).
- Pool exhaustion under burst: **fail closed to default policy** and count a
  bypass (`consensus_policy_eval_bypassed_total{reason="pool_exhausted"}`).
  Do not block the request path on a VM borrow.
- Memory per VM: small (empty Sobek runtime + stdlib); bounded by pool size.
- `evalTimeout` caps wall-clock per eval; a runaway eval poisons only the
  borrowed VM, which is discarded (rebuilt async). A plain throw leaves the VM
  clean and it returns to the pool.
- **Sandboxed runtime.** Each VM is a bare `sobek.New()` (`newSandboxRuntime`)
  exposing only the `EvalContext` + stdlib — **no `env` / `process.env` /
  `console`**, unlike the shared `common.NewRuntime()`. `evalFunction` is
  operator-writable config, so it never gets host-process env access. The
  one-time evaluation of the operator function expression at pool build is
  itself bounded by `evalTimeout`, so a runaway expression fails config load
  loudly instead of hanging startup. Details §6.

---

## 5. Decision cache (future work)

Not in v1. The eval is measured in microseconds against upstream RTT in
milliseconds; a cache adds dependency-tracking complexity for no forced
benefit. If profiles later show eval cost matters, the design is:

- Auto-derive cache keys from accessed ctx paths (getter tracking), with
  operator-declared `cacheKeys` as fallback.
- Health/availability reads fold into a health-tracker generation counter for
  O(1) invalidation.
- `blockNumber` buckets by `blockAvailability` boundaries; cardinality guard
  disables caching for exploding keys.
- Cache stores policy **name** only; errors never cached.

---

## 6. Automatic fallback (role-gated)

When internals are **unhealthy** (out of sync, down, unreachable) or
availability-cordoned — and **not** punished for consensus misbehaviour —
authorized roles run a plain consensus over whoever is healthy. No tag quotas
needed, because the healthy pool *is* the externals. If internals are missing
from the round only because they were punished, stay on `standard` — never
select `fallback`.

```yaml
fallback:
  maxParticipants: 3
  agreementThreshold: 2
```

Eval (pre-round) — two explicit block checks over **all** upstreams, then
availability:

```js
if (ctx.upstreams.anyPunished())       → "standard"  // integrity sit-out, any node
if (ctx.upstreams.anyOperatorCordon()) → "standard"  // admin cordon, any node
if (internals.allUnavailable()
    && ctx.user.hasRole("brp:consensus-fallback")) → "fallback"
```

Both block checks run on the full upstream set, not just internals: an active
sit-out or admin cordon anywhere means no degraded grade is served — even when
internals are genuinely down. Both lines are redundant in safety terms for
internals (`allUnavailable()` is already false while any member holds a
`punishment` or `operator` cordon); stating them first makes the two
fail-closed reasons readable and keeps them from drifting apart in operator
edits.

Unauthorized callers stay on `standard` and dispute — never silently
downgraded.

| Rule | Behavior |
|---|---|
| Availability vs punishment / admin | Cordons carry a typed class (§4.5). The reference eval checks `anyPunished()` and `anyOperatorCordon()` over **all** upstreams first: either pins the request to `standard`. Fallback then requires `allUnavailable()` on internals (whitelist of availability only). An attacker who gets any node punished, or an admin who cordons a node they distrust, must not force a downgrade. |
| In-flight failure | No mid-round switch. That round disputes under `standard`; the tracker records it; the *next* request's eval picks `fallback`. |
| Recovery | Internals healthy again → eval returns `standard`. Automatic both ways. Header `X-ERPC-Consensus-Policy: fallback` makes the degraded grade explicit. |

---

## 7. Serving historical data

Goal: recent → internal+external under `standard`; pruned / historical data
only for authorized roles — via `standard-waive-missing` (unknown block) or
`historical` (known-old block, external-only). Unauthorized callers dispute
when internals miss.

### 7.1 Policy split (primary) + waiver (safety net)

Three named grades (§2 table):

| Policy | Role | Pre-round | Post-round |
|---|---|---|---|
| `standard` | everyone (default) | Mixed internal+external | No waiver — MissingData on internals → composition dispute |
| `standard-waive-missing` | `brp:historical` + `blockNumber == null` | Same mixed composition | Waiver releases internal quota when **≥ `minAgreement`** distinct matching participants returned `ErrEndpointMissingData` and none returned data |
| `historical` | `brp:historical` + known-old (`canServeBlock` empty) | External-only — internals never called | External `minAgreement: 2` |

Layers that make those grades work:

1. **Pre-round (primary for known blocks).** Internals configure
   `blockAvailability`; the block-availability guard already short-circuits
   out-of-range requests with `ErrEndpointMissingData` before any provider call
   (`common/config.go`). Eval uses `canServeBlock(n)` to pick `historical`
   instead of calling internals at all.
2. **MissingData waiver (safety net for unknown-block methods).**
   `requiredParticipants[].waiveAgreementOnMissingData`: in
   `enforceWinnerComposition` (`missingDataWaivedQuotas`) a failing
   `minAgreement` quota is waived when **at least `minAgreement`** distinct
   tag-matching participants returned `ErrEndpointMissingData` — the
   normalized, terminal, non-misbehavior edge classification
   (`consensus/analysis.go`) — **and no matching participant returned data**.
   Sibling **transport / infra errors do not block** the count; a matching
   value vote holds the quota (that is disagreement, not abstention). Covers
   stale/misconfigured `blockAvailability` and error-shaped pruning on tx-hash /
   block-hash methods where `blockNumber` is null.

   The waiver is **round-complete**: it reports no waivers while
   `analysis.hasRemaining()` — a slower tagged upstream could still turn an
   abstention into a data vote. Because composition disputes deliberately do
   **not** short-circuit while responses can still arrive, a round with an
   unanswered required slot would otherwise defer the waiver forever. The
   wait-cap closes that: when `maxWaitOnResult` / `maxWaitOnEmpty` fires and
   cancels the stragglers, the analyzer **seals** the collection
   (`sealCollection`) and re-runs `determineWinner`, so `hasRemaining()` is
   false and the waiver evaluates even at `maxParticipants: 4` with only three
   answers. `fireAndForget` does not cancel/seal (its slots keep running), so
   it stays fail-closed. See details §3.

   Load-time validation: a policy using any waiver is rejected unless at least
   one entry is never-waivable **and** carries `minAgreement > 0`. The quota
   condition is what makes the floor a floor: `anyAgreementQuota` and
   `resultsSatisfyAgreementQuotas` both skip entries with `minAgreement <= 0`
   (`consensus/quota.go`), so a never-waivable entry without a quota is
   invisible to composition enforcement and would satisfy the rule while
   enforcing nothing.
3. **Empty-waiver with block proof (v1.1 — SHIPPED).**
   Null-shaped pruning (`eth_getTransactionByHash` post-EIP-4444) is ambiguous
   at the source — but the winning tx/receipt/log carries its own
   `blockNumber` (wire-stable). `waiveAgreementOnEmptyOutsideRetention` fires
   only when **≥ `minAgreement`** distinct matching participants returned empty,
   **no** matching participant returned non-empty data, **and** the winner's
   block is proven outside retention. Proof prefers each abstainer's finite
   `blockAvailability` lower bound (`winnerBlock < lower`); else chain-head
   depth (`head − winnerBlock > retentionBlocks`), where `head` is trusted from
   the network served tip **only** under majority served-tip mode
   (`evm.servedTip.enabledFor: latest`), otherwise the min of ≥ 2 corroborated
   participant poller latests. Field extraction is config-driven
   (`blockEvidenceFields`, same pattern as `preferHighestValueFor`); unlisted
   method → no proof → dispute. Fabricated recent tx + internal null → proof
   fails → quota holds → dispute. Full order and trust model: details §2.

   The synthesized default mixed-node tie (a lone non-empty archive group tying
   an equally-sized empty internal group at `agreementThreshold`) produces an
   `ErrConsensusDispute` with no backing group, which the waiver path above
   would never see. `promoteAbstentionWinner` rescues exactly that case under
   the same guards — see details §2.1.

Role-gating is the default recommendation when archive cost or blast radius
matters: never put the waiver on fail-closed `standard`. If historical is
open to all, put the waiver on `standard` (or omit the role check) with zero
extra JS.

### 7.2 Edge-case matrix

| Scenario | Outcome |
|----------|---------|
| Old block, `blockAvailability` correct | Guard short-circuit → MissingData. **`historical`**: external-only serve. **`standard-waive-missing`**: waiver → external 2-agreement. **`standard`**: composition dispute |
| Old block, `blockAvailability` stale/wrong | Provider error normalized to MissingData → same split by selected policy |
| Pruned tx, node returns MissingData error | Tx-hash → `standard-waive-missing` (role) → waiver; unauthorized on `standard` → dispute |
| Pruned tx, node returns `null` | **v1.1 (shipped):** empty-waiver serves when ≥ `minAgreement` matching nulls + winner block proven outside retention; recent null (inside retention/bounds) → dispute |
| Data never existed (all null / all MissingData) | `null` served (empty ≥ threshold) / agreed MissingData error — correct |
| Internal + external both return MissingData, one external returns the value | **Composition dispute** — MissingData is a `ResponseTypeConsensusError`, so the generic threshold rule can make it the winner, but `enforceWinnerComposition` does **not** exempt consensus-error groups. The external `minAgreement: 2` quota fails (only one external voted) and the round disputes. A matching participant that returned data holds the quota — the waiver only fires when ≥ `minAgreement` matching participants abstained **and none returned data**; here one external returned a value, so the waiver does not fire. |
| Just-mined tx not yet on internal (internal null) | Dispute → retry succeeds. Serving it needs minAgreement-0 + preferNonEmpty = externals outvote internal — rejected |
| Internal wrong value (misbehavior) | Quota holds → composition dispute + `punishMisbehavior` |
| Internal outage (infra error ≠ MissingData) | Quota holds → dispute for unauthorized; authorized roles get `fallback` on the next request (§6) |
| Internal punished/sitout (misbehavior) | Eval stays on `standard` → hard dispute — never falls through to `fallback` |
| External punished while internals down | Eval stays on `standard` — any punishment pins the network to the strictest policy, even at availability cost |
| Admin cordons an internal (any reason) | Eval stays on `standard` via `anyOperatorCordon()` — admin action is undifferentiated, so fail closed |
| Externals disagree (one archive, one not) | No group ≥ 2 → dispute. Operator pins archive externals via tags |

### 7.3 Requirements

- **R1** waiver fields on `requiredParticipants[]` (opt-in, default off) —
  `waiveAgreementOnMissingData` (v1) and `waiveAgreementOnEmptyOutsideRetention`
  + `retentionBlocks` + policy-level `blockEvidenceFields` (v1.1) — with the
  executor change localized to `enforceWinnerComposition` /
  `promoteAbstentionWinner`. Waivers are round-complete: composition disputes do
  not short-circuit while `hasRemaining`, and the wait-cap **seals** the
  collection so waivers evaluate when slots stay unanswered (`fireAndForget` does
  not seal). Details §3.
- **R2** `blockAvailability` on every internal upstream;
  `canServeBlock(n)` uses existing `EvmAssertBlockAvailability`.
- **R3** archive externals tagged, `minAgreement: 2`, never waivable.
- **R4** blockNumber extraction for eval ctx.
- **R5** observability: `erpc_consensus_composition_waived_total{project,network,tag,reason}`
  (`reason`: `missing_data` / `empty_outside_retention`) + log on every waiver
  fire; `erpc_consensus_composition_waiver_unproven_total{project,network,reason}`
  for fail-closed empty-waiver holds; policy name header/metric as §8.
- **R6** tests: one per matrix row; fallthrough cases (nil user, unknown
  policy name, unlisted method) first.
- **R7** punished/sitout and operator cordons distinct from unhealthy /
  availability in eval ctx; the fallback eval must refuse to fire when **any**
  upstream — internal or external — is punished (`anyPunished`) or admin-
  cordoned (`anyOperatorCordon`).
- **R8** cordons carry a typed `cordonClass` (`punishment` / `availability` /
  `operator`) read by the selector — never the reason string. The tracker
  holds one flag per class, so classes do not overwrite each other and
  `Uncordon` clears only its own class. The reference fallback predicate
  `allUnavailable()` is fail closed for every class except `availability`,
  including any class added in the future, and is false on an empty set.
- **R9** load-time validation rejects a policy where every
  `requiredParticipants` quota is waivable; at least one entry must be both
  never-waivable **and** carry `minAgreement > 0`. An entry with
  `minAgreement: 0` is skipped by both `anyAgreementQuota` and
  `resultsSatisfyAgreementQuotas` (`consensus/quota.go`), so it enforces
  nothing and cannot serve as the floor.
- **R10** two policies must not share an S3 `misbehaviorsDestination.path`
  unless each `filePattern` contains `{timestampMs}` (§4.4).

---

## 8. Observability

Metric names are `erpc_`-prefixed; all carry `project` + `network` labels. See
details §10 for the full table.

| Signal | Meaning |
|---|---|
| `X-ERPC-Consensus-Policy` response header | Chosen policy name; **output-only** (never read from the request), emitted only when a selector is configured |
| `erpc_consensus_policy_selected_total{consensus_policy}` | Named policy chosen per served round (`default` = fell closed / null) |
| `erpc_consensus_composition_waived_total{tag,reason}` | Waiver fires (`missing_data` / `empty_outside_retention`) |
| `erpc_consensus_composition_waiver_unproven_total{reason}` | Empty-waiver held for missing proof (`no_winner_block` / `no_avail_bound` / `no_head`); mid-round deferral is **debug-only**, not counted |
| `erpc_consensus_policy_eval_duration_seconds` | Eval latency histogram |
| `erpc_consensus_policy_eval_failed_total{reason}` | Eval **ran** and failed (`timeout`, `throw`, `unknown_name`) |
| `erpc_consensus_policy_eval_bypassed_total{reason}` | Eval **never ran** (`pool_exhausted`) — note this is separate from `eval_failed_total` |
| Warn log on unknown policy name | Config typo signal |
| Warn log on eval failure/timeout | Fail-closed fallback |

**Alerting:** page on sustained non-default policy selection (e.g. `fallback`
> 5 min) or a step change in `consensus_composition_waived_total`. These are
security signals, not only availability signals.

**Metric cardinality:** `consensus_policy` label cardinality is bounded by the
number of named policies (expected ≤ 10 per network). No per-request unbounded
labels.

---

## 9. Security

- JS is operator-supplied config, same trust level as selection-policy eval.
- Auth data flows **into** the eval (`ctx.user`) — role-aware decisions are the
  point. No separate claim→policy allowlist outside JS.
- Eval failure / timeout / unknown name → default policy (never more
  permissive).
- Fallback must not fire while **any** upstream is punished or admin-cordoned
  (R7) and fails closed on any future cordon class (R8). The reference eval
  names both blockers explicitly (`anyPunished`, `anyOperatorCordon`);
  `allUnavailable()` is a whitelist of availability and is a third line of
  defense. Per-class cordon state is part of that guarantee: with a single
  shared flag, a later availability cordon or an unrelated source's
  `Uncordon` would clear or downgrade an active punishment and re-open the
  downgrade (§4.5).
- `allUnavailable()` is false on an empty set, so a tag that matches no
  upstream cannot select `fallback`. A typo'd tag is a config error, and the
  vacuous reading of "all members are unavailable" would turn it into a
  silent downgrade for every authorized caller.
- Empty-waiver (v1.1) must not fire without block proof outside retention —
  closes the correlated-externals-outvote-internal hole for recent data.
- Role gating turns the JWT into a **correctness control**, not only a rate
  limit. A leaked token can select a weaker consensus grade; the
  `X-ERPC-Consensus-Policy` header lets callers detect a degraded grade.

---

## 10. Rollout, rollback, and reload

- **Enablement:** per-network config. Add `policies` + `customPolicy` to one
  network first; others keep the inline default.
- **Rollback:** remove `customPolicy` (or fix `evalFunction`) and reload
  config. If config reload is not available, restart the process — recovery
  time is bounded by pod restart.
- **Reload:** on config change, recompile the Sobek program and rebuild the
  VM pool. In-flight requests continue under the policy selected at their
  start; new requests use the new program. If recompilation fails, keep the
  previous program and log the error.
- **Dry-run / validation:** `erpc config validate` (or a dedicated CLI) smoke-
  compiles `evalFunction` and runs it against synthetic contexts before
  deploy.

---

## 11. Open questions answered

1. **Why freeform JS instead of a declarative selector?** The three example
   policies are selected by two predicates today, but the operator need is an
   open-ended set (role, health, block range, method, future dimensions). A
   declarative matcher would grow a new match-rule DSL per dimension; freeform
   JS is the weakest commitment that handles the observed cases and the
   unseen ones. The selection-policy precedent (`internal/policy`) already
   pays the Sobek cost.
2. **Why a second Sobek engine instead of reusing `internal/policy`?** The
   selection-policy engine is per-network, tick-based, and owns slot state,
   sticky stores, and probers. The consensus selector is per-request,
   stateless, and returns a name, not an ordered upstream list. Reusing it
   would couple two different lifecycles and force consensus to inherit
   selection-policy concepts (slots, ticks, eviction). A small standalone
   package is the weaker commitment.
3. **If the MissingData waiver shipped alone, how much of #1088 is solved?**
   Historical serving (UC3) is solved. Role-gated fallback (UC2) and custom
   operator policies (UC5) still need the selector.
4. **Measured eval cost?** The design assumes tens–low hundreds of µs on a
   pre-warmed VM, negligible against upstream RTT; the observed benchmark path
   (`internal/consensus/policy/bench_test.go`) confirmed this order of
   magnitude, so the decision cache stayed **unbuilt** (§5). Re-open only if a
   later profile shows eval cost matters.

**Implementation status note.** The role-gating machinery (`common.User.Roles`,
JWT `rolesClaimName`, `hasRole`) shipped and the selector reads it, but the
staging trial exercised selectors **ungated** — health / block-range / cordon
predicates — not live role-gated grades. The role path is code-complete, not yet
battle-tested end-to-end. Decision cache remains future work.

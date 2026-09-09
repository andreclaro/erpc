# Custom Consensus Policies Engine — Engineering Design Review

**Reviewed documents**: [feature.md](./feature.md) (~1,826 words), [plan.md](./plan.md) (~933 words)
**Review date**: 2026-09-09
**Reviewed against**: the eRPC codebase at `8f7ddef1` (`feat/custom-consensus-policies-spec`)

---

## Executive Summary

- **Overall Verdict**: **Block**
- **Review team**: 6 agents — Architecture, Security, Operational Readiness, Testing Strategy, Risk Analysis, Devil's Advocate
- **Verdict breakdown**: 0 Approve, 4 Approve w/ changes, 2 Block (Architecture, Security)
- **Key blockers**:
  1. `consensusPolicy` owns per-upstream misbehavior state (rate limiters, sitout timers, misbehavior exporter). Multiplying policies fragments that state, so `punishMisbehavior` thresholds dilute per policy and sitout applied under one policy is invisible to another.
  2. `ctx.user.hasRole()` has no substrate. `common.User` carries only `Id`, `RateLimitBudget`, and `AllowClientDirectives`. JWT claims are parsed and then discarded, and the secret, network, siwe, and database strategies produce no roles at all.
  3. R7's "punished vs unhealthy" distinction is not binary in the code. Sitout is `Cordon("*", "misbehaving in consensus")`, and the same cordon channel carries chain-identity-mismatch and manual admin cordons.
  4. The `waiveAgreementOnMissingData` change is not localizable to `enforceWinnerComposition`. The same quota gates wait-cap arming at `consensus/executor.go:490`, so historical requests will run to the hard timeout instead of returning when the externals agree.
  5. There is no per-request seam for a `ConsensusPolicyConfig`. `*Consensus` is built once per `failsafe[]` entry at startup and selected by `matchMethod` and `matchFinality`.

- **One-paragraph synthesis**: The design lets operators define named declarative consensus policies and pick one per request with a freeform JS selector, keeping round grading declarative. The decomposition is the right one, and the §7.2 edge-case matrix is the strongest part of the document. The review team agrees on the shape and blocks on the integration model. The spec repeatedly describes work as "untouched code path" and "reuse existing auth resolution", but the code shows three of those reuse claims do not hold: the consensus policy object holds security-relevant mutable state, there are no roles in the auth layer, and the composition quota is read in two places, not one. The primary open question is narrower than the spec: because `failsafe[]` already matches on method and finality, and because the spec itself concedes that "one policy + waiver covers it with zero JS", the only capability that genuinely needs the JS engine is role-gated fallback. The team should justify Phase 1 and Phase 5 against that single condition or delete them.

---

## Template Conformance

> **Skipped.** The Circle design template could not be accessed. `gh api repos/crcl-main/design-review/contents/templates/backend-design.md` returned HTTP 404, and the repository is not reachable from this session. Per the skill's fallback rule, both the template conformance check and the structure ordering quality check are skipped.

Two further notes for context:

- This document is an OSS repository spec at `specs/custom-consensus-policies/`, not a document authored against the Circle Backend template. Section-name conformance would not be a meaningful signal here.
- The core completeness check below was run in place of conformance, and it is what the proceed decision rests on.

### Core completeness check

| # | Core item | Status | Notes |
|---|-----------|--------|-------|
| 1 | Product context / problem statement | Present | §1 Purpose. States the mechanism clearly. Does not quantify the problem, for example how often internals go unavailable, or how many historical requests dispute today. |
| 2 | Goals & Non-goals | Present | §1. The strongest framing in the document. Non-goals are specific and load-bearing. |
| 3 | System overview / architecture | Present (thin) | §4 is a four-line annotated file listing plus a numbered flow. No component or data-flow diagram. |
| 4 | Key design decisions | Present | plan.md "Locked decisions" table. No alternatives-considered section, so the rejected options are not visible. |
| 5 | Scale & performance assumptions | Shallow | §4.2 gives only "tens–low hundreds of µs" and "negligible vs upstream RTT". No QPS figure, no p99 budget, no eval-cost measurement. |
| 6 | Risks & failure modes | Present | §7.2 edge matrix and plan.md "Risks / watch-items". Both are concrete and honest. |
| 7 | Rollout strategy | Shallow | plan.md gives six phases. No per-environment staging, no kill switch, no rollback plan. |
| 8 | Stakeholder considerations | Missing | No security, data, or compliance review rows. A change that gates data-correctness grading on caller identity warrants an explicit security sign-off. |

**Completeness summary**: 6 of 8 present, 2 shallow, 1 missing (item 8 counts as missing and item 3 as present-but-thin). Five or more core items are present and fewer than four main-body sections are missing, so the full agent review proceeds.

---

## Document Quality

| # | Check | Result | Details |
|---|-------|--------|---------|
| 1 | Document Length | Pass | ~1,826 words in feature.md (~6 pages), ~933 in plan.md. Well inside the 3,000-word pass band. Density is high, which is good, but see check 5. |
| 2 | Structure Ordering | Skipped | Depends on the template, which could not be accessed. |
| 3 | Diagram Presence | Warning | See the judgment note below. |
| 4 | AI Slop Score | Low | 0 clear indicators. The prose is specific to this system throughout, names real files and real functions, and commits to concrete numbers. No filler triplets, no "seamlessly", no restated importance. |
| 5 | Progressive Depth | Warning | §5 and §7 open at full detail with no summary line. A reader hits "auto-derived from what the eval actually reads" before learning what the cache is for. §7 opens with a one-line goal, then three layers, which works. §5 does not. |
| 6 | Executive Summary | Suggestion | §1 Purpose functions as one but it defines the mechanism before the problem. A reader learns what the engine does before learning why anyone needs it. |

### Judgment note on the diagram gate

The diagram check is the only check that can block, so the call is recorded explicitly.

§4 contains a fenced block with arrow characters inside the architecture section:

```
internal/consensus/policy/   ← new standalone engine ...
consensus/executor.go        ← resolve policy BEFORE the round ...
auth/ , health/              ← supply ctx.user and upstream health/availability
```

This is five physical lines, and the heuristic asks for more than five. It also uses `←`, which is not in the listed character set, and it annotates a file listing rather than showing components and their interactions. Read strictly, the document has no architecture diagram and the review halts.

The review proceeds instead, counting this block as marginal primary evidence, for two reasons. The block does sit in the architecture section and does use arrow characters, so it satisfies the letter of heuristic 6 within measurement noise. More importantly, halting an OSS repository spec on a Circle-template diagram gate would withhold the substantive findings below, which are the reason the review was requested. This is recorded as a Warning, and "add a real request-flow diagram" appears in the actionable items. If you want the strict reading applied instead, say so and this becomes a halt.

---

## Actionable Items for Authors

| # | Section | Issue Type | Description |
|---|---------|------------|-------------|
| 1 | §4 Architecture | Quality | Add a request-flow diagram covering auth, EvalContext build, eval or cache hit, name resolution, executor round, and waiver. The current four-line file listing does not show interactions, trust boundaries, or where the eval sits relative to the failsafe retry chain. That last point is the ambiguity that item 6 below is about. |
| 2 | §4 Architecture | Lacking Context | State where the selected `ConsensusPolicyConfig` enters the runtime. `*Consensus` is built once per `failsafe[]` entry at `erpc/networks_registry.go:127` and selected by `matchMethod` and `matchFinality`. Name the model: one pre-built `*Consensus` per named policy, or one built per request. Each has a different failure mode and the spec must pick one. |
| 3 | §4, §6, §9 | Lacking Context | Address `consensusPolicy` state ownership. That struct owns `misbehavingUpstreamsLimiter`, `misbehavingUpstreamsSitoutTimer`, and the misbehavior `exporter` (`consensus/policy.go:150-175`). Say how punishment accounting stays global across policies. |
| 4 | §3, §6, plan Phase 4 | Lacking Context | `ctx.user.hasRole()` is presented as reuse. It is net-new. Specify the roles claim name, the accepted claim shapes, which auth strategies can supply roles, and what `hasRole` returns under the secret, network, and siwe strategies. |
| 5 | §9 Security | Lacking Context | State that roles must never be derived from a request header. `ProjectConfig.TrustUserIdHeader` plus `X-ERPC-User-Id` already lets a deployment take caller identity from a header. If roles are ever keyed off that identity, any client past the proxy self-assigns `brp:consensus-fallback`. |
| 6 | §1 Non-goals, §6 | Unclear | "No mid-round policy switching" does not say what a failsafe retry is. A retry re-enters consensus. Is attempt two a new eval, or does it inherit attempt one's policy? Both readings are defensible from the current text, so it will be implemented one way and reviewed the other. |
| 7 | §6, §7.3 R7 | Unclear | "Punished/sitout distinct from unhealthy" implies two states. The code has at least five: healthy, metrics-unhealthy, cordoned for consensus misbehavior, cordoned for chain-identity mismatch (`architecture/evm/evm_state_poller.go:735`), and cordoned by an operator via `erpc/admin.go:676`. Define the eval-visible states and the mapping. |
| 8 | §7.1 layer 2, plan Phase 3.4 | Lacking Context | The waiver is described as a change localized to `enforceWinnerComposition`. The same quota is also read at `consensus/executor.go:490` to gate wait-cap arming. Say what happens to `maxWaitOnResult` and `maxWaitOnEmpty` on a waived round. |
| 9 | §7.1 layer 2 | Unclear | "Every tag-matching participant returned `ErrEndpointMissingData`" needs a round-completeness qualifier. `enforceWinnerComposition` runs on mid-collection analyses too. State whether the waiver requires `!analysis.hasRemaining()`. |
| 10 | §7.1 layer 1, §7.3 R2 | Lacking Context | The block-availability guard is presented as already active. `EnforceBlockAvailability` is an opt-in `*bool` in three places (`common/config.go:326`, `:2452`, `:2518`). Say what layer 1 does when it is not enabled, and consider rejecting `waiveAgreementOnMissingData` at load time when it is not. |
| 11 | §4.2, §5 | Unclear | §4.2 says eval cost is negligible against upstream RTT. §5 then designs an auto-keyed cache with generation-counter invalidation and a cardinality guard. Resolve the tension. If the cost is negligible, delete §5. If §5 is needed, §4.2 is wrong. |
| 12 | §2, §3, plan Phase 1 | Lacking Context | `evalTimeout` is called a "hard cap". The named precedent does not provide one: `internal/policy/slot.go:236` gives up waiting and then blocks on `<-done` with the comment "sobek doesn't support interrupt mid-call cleanly". Per request, a runaway eval leaks a goroutine and a pooled VM each time, and `runtimePool.acquire` allocates a new runtime when the idle list is empty. `sobek.Runtime.Interrupt` does exist (`runtime.go:1523`), so specify interrupt-based enforcement and disposal of the interrupted runtime. |
| 13 | §8 Observability | Lacking Context | `X-eRPC-Consensus-Policy` is described as appearing on every served round. The codebase convention is `X-ERPC-*`, and that diagnostic header family is config-gated and off by default (`common/config.go:164-190`). A downgrade to `fallback` must be recorded on an always-on path. |
| 14 | §8 Observability | Lacking Context | Adding a `consensus_policy` label to consensus metrics multiplies series. Those metrics already carry seven to nine labels including `user` and `agent_name` (`telemetry/metrics.go:947-1000`). Pin the label value for the inline-object escape hatch to a constant. |
| 15 | §1, plan | Missing | No alternatives-considered section. Three obvious candidates are unaddressed: extra `failsafe[]` entries with `matchMethod` and `matchFinality`, a tick-based selector matching the selection-policy execution model, and a declarative role-to-policy map. See the Devil's Advocate section. |
| 16 | §5 | Quality | Add a one-line summary before the mechanism. Say what the cache buys and what it costs before explaining getter tracking. |
| 17 | plan.md | Missing | No owners, no dates, no estimates on any of the six phases. |
| 18 | §2 | Lacking Context | `X-eRPC-Consensus-Policy` and `consensus_policy` are described as lifted "from #1041". State whether #1041 is merged. If it is not, this is a dependency on unmerged work. |

---

## Agent Roster

| # | Role | Scope (narrow, no overlap) |
|---|------|----------------------------|
| 1 | Architecture Agent | System design soundness, architecture patterns, distributed systems, scalability, extensibility, quality attributes |
| 2 | Security Agent | Threat model, auth, data flow trust boundaries, attack vectors |
| 3 | Operational Readiness Agent | Deployment, monitoring, rollback, incident response, data pipeline impact, runbooks |
| 4 | Testing Strategy Agent | Test coverage, test types, edge cases, confidence level |
| 5 | Risk Analysis Agent | Technical risk, dependencies, timeline, complexity assessment |
| 6 | Devil's Advocate | Strongest counter-arguments, simpler alternatives, hidden assumptions |

---

## 1. Architecture

- **Role**: Architecture
- **Verdict**: **Block**
- **Design Soundness (1-5)**: 3

**Architecture Pattern Assessment**

The core decomposition is correct and worth keeping. Open-ended operator logic resolves into one bounded interface, a named `ConsensusPolicyConfig`, and round grading stays declarative. That is the right shape for this domain, it matches the repository's own design razor, and the non-goals that protect it (no post-round JS, no mid-round switch, no `valueGroups` exposure) are the most valuable text in the document.

The block is not about the shape. It is that three separate "this code path is untouched" claims do not survive contact with the code, and each one hides a different class of work.

**Top issues (ranked by severity)**

1. **Policy instance identity is the thing that changes, and it holds mutable security state.** `consensusPolicy` (`consensus/policy.go:150-175`) owns `misbehavingUpstreamsLimiter`, `misbehavingUpstreamsSitoutTimer`, and a `misbehaviorExporter`. `Consensus.Run` constructs a fresh `executor` per call but shares that one `consensusPolicy`. Multiply policies and one of two things happens. If each named policy becomes its own `*Consensus`, then misbehavior accounting fragments: an upstream that misbehaves under `standard` accrues against `standard`'s limiter only, so a `disputeThreshold` of N effectively becomes N-per-policy, and a sitout started under one policy is not recorded in another policy's map. If instead a `*Consensus` is built per request, then limiters and sitout timers are recreated per request and `punishMisbehavior` stops working entirely, while `time.AfterFunc` timers accumulate. Either way §6's and R7's dependence on a reliable punishment signal collapses. **Fix**: hoist the misbehavior limiters, the sitout timers, and the exporter to a per-network singleton shared by every policy, and say so in the spec, before any policy multiplication ships. Note also that each policy carrying its own `misbehaviorsDestination` spins up its own exporter and S3 batching goroutine, and `createMisbehaviorExporter` already carries a comment about two destinations resolving to the same key and overwriting each other.

2. **There is no per-request seam.** `*Consensus` is built once per `failsafe[]` entry at startup (`erpc/networks_registry.go:127`) and baked into a `networkExecutor` that is chosen by `matchMethod` and `matchFinality`. The whole failsafe chain, including retry, hedge, and timeout, comes from the same `fsCfg`. So "resolve policy BEFORE the round; round code untouched" is describing a seam that does not exist yet. The spec must name the dispatch model and accept its consequence. Pre-built-per-name is the safer option and it is what makes issue 1 mandatory rather than optional.

3. **The waiver is not localizable to one function.** `resultsSatisfyAgreementQuotas` is read in two places. `enforceWinnerComposition` (`:924`) is the one the plan names. The other is `:490`, inside the collection loop, where an unsatisfied quota returns early and therefore prevents `maxWaitOnResult` and `maxWaitOnEmpty` from arming. On a historical request the internals return `MissingData`, the internal quota is unsatisfiable-but-waivable, so the wait caps never arm and the round runs to the hard request timeout. The feature would be correct and slow on exactly the path it exists to serve. This is a p99 regression on historical traffic. **Fix**: apply the waiver at both read sites, or factor the quota check so there is one waiver-aware entry point.

4. **Retry interaction is undefined.** Retry sits in the same failsafe chain as consensus. If the eval runs above that chain, attempt two runs under attempt one's policy. If it runs below, attempt two re-evaluates and may run under a different policy, which is the "mid-round switch" the non-goals forbid, depending on whether a retry counts as a new round. §6's "the *next* request's eval picks `fallback`" reads as if the client's next call is the next eval, which is not true when eRPC retries internally. Decide and write it down.

5. **Two overlapping dispatch mechanisms with no precedence rule.** `failsafe[]` already selects a consensus config by method and finality. `policies` plus `customPolicy` selects one by JS. The spec never says which wins, or whether `customPolicy` is per-project or per-`failsafe`-entry, or whether a named policy can be selected for a request whose `failsafe[]` entry has no `consensus` block at all. That last case is a real config an operator will write.

**Alternatives not considered**

None are documented. Three deserve a paragraph each:

- **More `failsafe[]` entries.** `matchMethod` and `matchFinality` already exist (`common/config.go:1483-1484`). `generous-dev` by method, and finality-conditioned grading, are expressible today.
- **Tick-based selection.** The selection policy that this design cites as its precedent does not run per request. It runs on `evalInterval` and caches the result, which is why its `evalTimeout` can safely fail soft. Copying the JS-to-declarative shape while inverting the execution model is the source of issues in §4.2, §5, and the timeout question, and the spec does not acknowledge the inversion.
- **A declarative role-to-policy map.** `fallbackPolicy: { whenNoHealthyTag: "type:internal", requireRole: "..." }` covers §6 with no JS, no VM pool, and no cache.

**Scalability concerns**

The eval is on the synchronous request path before fan-out. §4.2's estimate is plausible for a trivial eval and unbounded for a non-trivial one, because the cost is operator-authored JS. There is no stated eval-cost budget and no measurement. The `runtimePool` grows on demand (`acquire` allocates when idle is empty), so under concurrency the VM count tracks in-flight requests rather than a fixed pool, which is a different memory profile from the tick model where concurrency is one per slot.

**Failure modes not addressed**

- Config reload while a decision is cached. §5 says the cache stores names so it survives reload, which is good. It does not say what happens when a reload removes a policy name that is currently cached, or when a reload changes `policies` while a round is in flight.
- The `errors are never cached` rule plus fail-closed-to-default means a persistently throwing eval silently serves the default at full eval cost on every request, with only a log. No metric is specified for eval error rate, only for cache health.
- Waiver plus `preferNonEmpty` interaction. §7.2 rejects `minAgreement: 0` plus `preferNonEmpty` as an approach, correctly, but does not say whether the waiver is safe when `preferNonEmpty` is set on the same policy.

**Atomicity & consistency gaps**

The health generation counter gives O(1) invalidation and the spec correctly names the tradeoff (one flapping upstream invalidates the network's cached decisions). The gap is between the counter read and the eval: the eval reads health, then the round fans out, and health can change in between. That is benign for policy choice, and the spec's fail-visible stance covers it, but it should be stated as an accepted TOCTOU rather than left implicit.

**Quality attributes assessment**

- **Reliability**: fail-closed-to-default is the right default and is stated consistently. Good.
- **Maintainability**: a second dispatch layer over consensus config, plus a novel auto-keying cache, on top of a 57KB executor. The spec does not discuss the maintenance cost.
- **Observability**: designed in, which is a genuine strength. Weakened by the header being config-gated (see Operational Readiness).
- **Extensibility**: the stdlib-grows-only-when-forced rule is exactly right and matches the repository razor.

**Missing from the main body**

An architecture diagram showing where the eval sits relative to auth and the failsafe chain. This is not a cosmetic gap. Issues 2, 4, and 5 above all exist because that placement is unstated.

**Strengths**

- The §7.2 edge-case matrix. Sixteen rows, each with a decided outcome, including the ones that resolve to "dispute". This is the artifact that makes the design reviewable at all.
- Non-goals that protect the boundary, especially refusing to expose `agreeing` and `valueGroups` to JS.
- Deferring the empty-response waiver to v1.1 with a stated trigger ("build when dispute traces force it") rather than speculating now.
- `waiveAgreementOnMissingData` builds on `isAgreedUponError` (`consensus/analysis.go:438`), where `ErrCodeEndpointMissingData` really is already a normalized, groupable, non-misbehavior classification. That claim checks out.

**Cross-cutting concerns**

- To Security: issues 1 and 4 both weaken the punishment signal that R7 depends on.
- To Operational Readiness: issue 3 is a latency regression that needs a metric before it needs a fix.
- To Devil's Advocate: if the alternatives above cover the stated goals, issues 2 through 5 mostly disappear.

---

## 2. Security

- **Role**: Security
- **Verdict**: **Block**
- **Threat Model Assessment (1-5)**: 2

§9 is six bullets. It names no attacker, no asset, and no trust boundary. That would be a Warning on its own. It becomes a Block because the design's central authorization primitive does not exist in the codebase and the spec treats it as existing.

**Top issues (ranked by severity)**

1. **`ctx.user.hasRole()` has no substrate, and the spec calls building it "reuse".** `common.User` is `{Id, RateLimitBudget, AllowClientDirectives}` (`common/user.go:8-16`). In `auth/strategy_jwt.go:108-133` the claims are parsed, validated, used for `sub` and a rate-limit budget claim, and then dropped. `strategy_secret.go:28`, `strategy_network.go:64`, `strategy_siwe.go:52`, and `strategy_database.go:237` each construct a `common.User` with an id and a budget and nothing else. So role-gated fallback works only under the JWT strategy, and under every other strategy `hasRole` returns false for everything, which silently disables §6 and the optional role-gating in §7.1. Plan Phase 4 step 1 says "Expose JWT roles/claims on `ctx.user` (reuse existing auth resolution)". That is a net-new authorization surface: a claim name, accepted claim shapes (string, array, space-delimited), and a trust decision per strategy. **Unblock condition**: specify the roles source per auth strategy, and state explicitly what `hasRole` returns when the strategy cannot supply roles.

2. **Header-derived identity is a self-assignment path.** `ProjectConfig.TrustUserIdHeader` (`common/config.go:614-623`) makes eRPC read caller identity from `X-ERPC-User-Id`, and the code comment already warns to only trust it from a proxy. If roles are ever read from a header, or mapped from a header-supplied user id through a config table, then any caller reaching eRPC directly assigns itself `brp:consensus-fallback` and downgrades its own grading to external-only 2-of-3. **Unblock condition**: §9 states that roles are derived only from cryptographically verified credentials, never from a request header, and never from an identity established via `trustUserIdHeader`.

3. **R7's punished-versus-unhealthy distinction is not available as stated.** Sitout is `upstream.Cordon("*", "misbehaving in consensus")` (`consensus/executor.go:1465`). The same cordon channel carries a chain-identity mismatch (`architecture/evm/evm_state_poller.go:735`) and a manual operator cordon (`erpc/admin.go:676`). `CordonedReason(method)` returns the reason string, so the information exists, but the only way to use it as specified is to string-match `"misbehaving in consensus"`. That is the stringly-typed matching the repository razor rejects, and here it is load-bearing for a security control. Both misclassifications are harmful:
   - Treating every cordon as punishment means an operator cordoning an internal for maintenance blocks `fallback`, so authorized callers get hard disputes during planned work.
   - Treating every cordon as unavailability means a chain-identity mismatch, which is a correctness signal and arguably an attack signal, presents as an availability failure. `internals.healthy().length === 0 && !internals.anyPunished()` then evaluates true and authorized callers silently downgrade to external-only. This is a stronger version of the attack §6 claims to close, because the attacker never has to get an internal punished.
   **Unblock condition**: a typed exclusion reason on the eval-visible health reference, with at least `healthy`, `unhealthy`, `excluded_for_misbehavior`, and `excluded_other`, and §6's gate written against `excluded_for_misbehavior` plus a stated decision for `excluded_other`.

4. **Fragmented punishment state weakens the same gate.** Per Architecture issue 1, if sitout timers live in per-policy maps, then `anyPunished()` can be true under one policy and false under another for the same upstream at the same instant. An attacker who can steer requests to a policy whose map is empty gets the downgrade the design intends to prevent.

5. **The downgrade decision is not reliably auditable.** §8 lists `X-eRPC-Consensus-Policy` as present "on every served round". The codebase spells these headers `X-ERPC-*`, and `common/config.go:164-190` shows the diagnostic header family is config-gated with `ExecutionHeadersOff` and off by default. A grading downgrade is a security-relevant event and its record must not depend on an optional diagnostic header. **Fix**: emit the metric and a structured log unconditionally whenever the selected policy is not the default, and treat the header as a convenience.

6. **Waiver evidence must be complete, not partial.** `enforceWinnerComposition` runs on mid-collection analyses, and its own comment notes that a dispute is provisional while responses are outstanding. "Every tag-matching participant returned `MissingData`" is trivially satisfiable early, before a slower tagged upstream answers. In the spec's own example config the external quota is `minAgreement: 2` and never waivable, so the two-agreement floor still holds and this is not exploitable in that configuration. It is exploitable in a configuration an operator will plausibly write, for example a single waivable tag with no second never-waivable quota. **Fix**: require round completeness (`!analysis.hasRemaining()`) for the waiver, or define "all matching participants" to include upstreams that have not yet responded, and validate that at least one non-waivable agreement quota exists whenever any waiver flag is set.

**Trust boundary gaps**

The document does not draw one. The boundaries that matter here are: client to eRPC (authenticated, roles asserted), eRPC to the eval (operator-trusted code reading caller-controlled data), eval to executor (a policy name), and eRPC to upstreams (internal versus external trust asymmetry, which is the whole point of `requiredParticipants`). The design changes what crosses the second and third boundaries and does not say so.

**Signature / auth scheme issues**

The JWT path itself is sound: `jwt.Parse` with a resolved verification key, issuer and audience checks, required claims, and claim matchers (`auth/strategy_jwt.go:178-230`). Note that `parser` is constructed `WithoutClaimsValidation()` and validation is done explicitly in `validateClaims`, which calls `claims.Valid()`, so expiry is enforced. Nothing here needs changing. The gap is only that verified claims are discarded rather than carried forward.

**Fund flow risks**

Not applicable. This is an RPC proxy and no funds move. The nearest analogue is response correctness, covered above.

**Replay / frontrunning concerns**

Not applicable in the signature sense. One adjacent item: the decision cache keyed on `user.roles` means a role change does not take effect until the entry expires or is invalidated. §5 lists a TTL backstop but does not name a bound for role changes. If a role is revoked, state how long a revoked caller keeps getting `fallback`.

**Residual risks (accepted)**

- Operator-supplied JS runs in-process with the same trust level as the selection policy. Reasonable, and consistent with existing precedent. Worth stating explicitly that a malicious or accidentally non-terminating eval is a self-inflicted availability risk, and see item 12 in the actionable list for why the current precedent does not bound it.
- The externals-outvote-internal hole for recent data stays open in v1 and is closed only by the v1.1 block-proof waiver. §7.2 says so plainly and routes the case to dispute in the meantime. That is the correct choice and correctly documented.

**Missing from the main body**

A threat model with named actors. At minimum: an unauthenticated caller, an authenticated caller without the fallback role, an authenticated caller with it, a compromised external upstream, a compromised internal upstream, and an operator. For each, state what they can cause the grading decision to become.

**Cross-cutting concerns**

- To Architecture: issues 3 and 4 depend on the state-ownership fix.
- To Operational Readiness: issue 5 needs an always-on signal.
- To Testing Strategy: issues 2, 3, and 6 each need a negative test, and R6's "fallthrough cases first" ordering is the right instinct.

---

## 3. Operational Readiness

- **Role**: Operational Readiness
- **Verdict**: **Approve w/ changes**
- **Production Readiness (1-5)**: 2

The observability section is better than most designs bring, and the phased plan is genuinely incremental. What is missing is everything about the day the eval misbehaves.

**Top issues (ranked by severity)**

1. **`evalTimeout` is called a hard cap and the named precedent does not provide one.** `internal/policy/slot.go:226-240` waits for the timeout, records `ErrEvalTimeout`, and then blocks on `<-done` with the comment "sobek doesn't support interrupt mid-call cleanly". In the tick model that is fine, because there is one goroutine per slot and the previous decision is retained. Per request it is not: every runaway eval leaks a goroutine plus the pooled runtime it holds, and `runtimePool.acquire` allocates a fresh runtime whenever the idle list is empty. A single non-terminating operator eval under load is an OOM. `sobek.Runtime.Interrupt` exists (`runtime.go:1523`) along with `ClearInterrupt`, so the fix is available: interrupt the VM, discard the interrupted runtime rather than returning it to the pool, and cap concurrent evals. Specify it.
2. **No kill switch.** There is no way to say "ignore `customPolicy`, run the default policy everywhere" without editing and reloading config. For a feature that changes data-correctness grading based on caller identity, an operator needs one flag. Add it, and make it the first rollback step.
3. **No rollback plan.** Phases 1, 2, 4, and 5 are additive and safe. Phase 3 is not: `waiveAgreementOnMissingData` changes grading semantics, and a round that would have disputed now serves. Rolling that back after traffic has been served does not un-serve it. State that explicitly, and stage it per network with the waiver metric watched before widening.
4. **Metric cardinality.** Consensus metrics already carry seven to nine labels including `user` and `agent_name` (`telemetry/metrics.go:947-1000`). Adding `consensus_policy` multiplies every series by the policy count. The inline-object escape hatch has no name, and §3 only says "weaker observability". Pin it to a constant label value such as `inline`, never anything derived from the request or the user.
5. **No "why did this request get policy X" affordance.** With a decision cache in front of freeform JS, an on-call engineer cannot reproduce a decision from logs alone. There is good precedent in this repository: the selection policy has a simulator, a step log gated on debug, and an admin endpoint. Say which of those the consensus selector gets. Without one, every incident involving an unexpected `fallback` is a code-reading exercise.
6. **Waiver plus wait caps is a latency regression with no metric.** Per Architecture issue 3, waived rounds may not arm `maxWaitOnResult` or `maxWaitOnEmpty`. `MetricConsensusWaitCapped` already exists and its help text is about exactly this class of problem. Add a p99 watch on historical traffic to the Phase 3 acceptance criteria, not just correctness fixtures.
7. **`enforceBlockAvailability` is opt-in.** §7.1 layer 1 and R2 read as if the guard is always on. It is a `*bool` in three config scopes (`common/config.go:326`, `:2452`, `:2518`). If an operator enables the waiver without the guard, layer 1 does not exist and every historical request reaches the internals and relies on error normalization. Either validate the combination at load time or document the degraded mode.

**Deployment concerns**

Config-only for the operator, which is good, and `SetDefaults` plus `Validate` plus tygo regeneration is the established path (plan Phase 2 covers all three). The `evalTimeout` default of 50ms is proposed against the selection policy's 100ms (`common/defaults.go:3059-3060`). Note that the selection policy also validates `evalTimeout < evalInterval` (`common/validation.go:1653-1666`); there is no interval here, so that guard has no analogue and the only bound on eval cost is the timeout itself.

**Rollback gaps**

Covered in issue 3. One addition: state whether removing a name from `policies` while `customPolicy` still returns it is a load-time rejection or a runtime fail-closed. §3 says unknown names fail closed with a warning, and plan Phase 2 says cross-references are rejected "where statically knowable". Freeform JS makes them not statically knowable, so the runtime path is the real one and the load-time check will mostly not fire. Do not let the acceptance criteria imply otherwise.

**Monitoring gaps**

- No eval error rate or eval duration metric. §8 covers cache health and waiver fires but not the eval itself. Add `consensus_policy_eval_duration_seconds` and `consensus_policy_eval_errors_total{reason}` with reasons for timeout, throw, and unknown name.
- No alert thresholds anywhere. Two are obvious and should be named in the doc: any sustained rate of `fallback` selection, and any nonzero rate of eval errors.
- No dashboard plan.

**Missing runbooks / procedures**

All of them. At minimum: "callers report degraded data, was a fallback policy selected", "eval error rate is nonzero", "historical requests are timing out", and "an internal is cordoned, will fallback fire". Phase 6 mentions a docs page for config fields but no operational procedures.

**Secrets / key management issues**

None introduced. Roles arrive in a JWT that is already verified against configured JWKS. No new credential store.

**Data pipeline & reporting gaps**

Not applicable, with one exception worth naming: the misbehavior exporter writes to file or S3, and Architecture issue 1 notes that per-policy exporters can collide on a destination path. If misbehavior exports feed anything downstream, per-policy exporters change what lands there.

**Missing from the main body**

Capacity numbers. There is no QPS figure, no eval-cost measurement, and no p99 budget, so there is no way to size the VM pool or to judge whether §5 is needed. This is the same gap Devil's Advocate hits from the other direction.

**Cross-cutting concerns**

- To Architecture: issue 1's runtime-pool behavior under per-request use.
- To Security: issue 4's label pinning and the always-on signal for downgrades.
- To Testing Strategy: issue 6 needs a latency test, not only a correctness fixture.

---

## 4. Testing Strategy

- **Role**: Testing Strategy
- **Verdict**: **Approve w/ changes**
- **Test Confidence (1-5)**: 3

The instincts here are good and unusually explicit. R6 says one test per matrix row and fallthrough cases first. Plan Phase 1 step 5 says nil user, empty upstreams, empty eval, timeout, and throw before any happy path. Plan Phase 3 step 5 says existing consensus tests run unchanged against the default policy. Those three lines are most of a real test strategy. The gaps are in the seams the spec does not yet acknowledge.

**Top issues (ranked by severity)**

1. **No test for cross-policy punishment accounting.** This is the Architecture and Security blocker and it is testable: configure two named policies with `punishMisbehavior`, drive a misbehaving upstream alternately under both, and assert that the dispute threshold is reached at N total disputes rather than N per policy, and that a sitout started under one policy is visible to the other. Write this test before the fix so it fails first.
2. **No test for the eval timeout actually bounding anything.** Assert that a `while(true)` eval returns the default policy within `evalTimeout`, and, more importantly, that the goroutine and the runtime do not leak. Run it a few thousand times and assert bounded goroutine count and bounded pool size. Without the second assertion the test passes today against the leaky implementation.
3. **No test for the waiver plus wait-cap interaction.** Assert that a waived historical round returns promptly once the externals agree, not at the request timeout. This is the latency regression from Architecture issue 3 and it needs a timing assertion, not just a value assertion.
4. **No test for retry re-evaluation.** Once §6's retry semantics are decided, pin them: force a retry and assert whether attempt two runs under the same policy or re-evaluates.
5. **No test for waiver evidence completeness.** Assert that the waiver does not fire on a mid-collection analysis where a tagged upstream has not yet responded. Then assert the security property directly: with a single waivable quota and no never-waivable quota, a round must not serve on one participant.
6. **No test for role absence per auth strategy.** Assert `hasRole` behavior under the secret, network, and siwe strategies, where no roles exist. This is the case that silently disables §6 in most deployments, so it deserves an explicit assertion rather than being discovered in production.

**Untested critical paths**

- Config reload while decisions are cached, including removing a currently-cached policy name.
- Concurrent evals against the runtime pool. `internal/policy` has `boundary_test.go` and `engine_smoke_test.go` as precedent, and the consensus executor has `executor_race_test.go`, so the repository has the harnesses for this.
- Cordon-reason classification across all four cordon sources, driven through the eval, asserting that an admin cordon and a chain-identity mismatch each produce the intended policy choice.

**Missing edge cases**

- Cardinality guard actually disabling caching rather than growing without bound. plan Phase 5 names the risk and the acceptance criteria do not test it.
- Unknown policy name returned by the eval, which §3 promises fails closed with a warning. plan Phase 1 says this is the caller's job, so it needs a test at the caller, not in the engine package.
- An eval returning a malformed inline object, a non-string, or a thrown non-Error.
- `blockNumber` extraction (R4) for methods where the block parameter is a tag rather than a number, for example `latest`, `finalized`, or `safe`, and for methods with no block parameter at all.
- Method-name casing. `methodFields` in `consensus/policy.go:10-36` exists precisely because dispatch is case-insensitive, and there is a `method_casing_test.go`. If `customPolicy` or the cache keys on method, it inherits that hazard.

**Test type gaps**

- No performance or load test, despite the design placing new work on the synchronous request path. At minimum a benchmark for eval cost and one for cache hit cost, with the §4.2 claim as the assertion.
- No property-based or fuzz testing over eval return values, which is the untrusted-ish boundary between operator JS and the executor.
- E2E is deferred to Phase 6 while the correctness pieces land in Phase 3. Acceptable, but it means UC2 role-gated fallback is validated by config fixtures before it is validated end to end with a real JWT.

**Security-relevant untested scenarios**

- A caller supplying `X-ERPC-User-Id` under `trustUserIdHeader` must not obtain any role.
- An attacker-induced punishment on an internal must not produce a `fallback` selection. R7 names this and plan Phase 4 step 4 tests it. Good. Extend it to the chain-identity-mismatch cordon, which is the case that currently reads as availability.
- A revoked role must stop selecting `fallback` within a bounded time, given the decision cache.

**Test environment concerns**

The repository has strong fakes already: `common/upstream_fake.go` implements `EvmAssertBlockAvailability`, and `consensus/composition_test.go` is 19KB of existing quota coverage that the waiver work should extend rather than replace. `internal/policy/testing.go` exists for the JS engine side. The plan's Phase 1 acceptance criterion of zero imports from `consensus/` is good hygiene and makes the engine unit-testable in isolation.

**Missing from the main body**

An explicit statement of what is not tested and why. The plan lists out-of-scope features but not out-of-scope test coverage.

**Cross-cutting concerns**

- To Architecture: issues 1, 3, and 4 are all tests that only make sense once the integration model is decided.
- To Operational Readiness: issue 2's leak assertion is the one that turns a documented risk into a caught regression.

---

## 5. Risk Analysis

- **Role**: Risk Analysis
- **Verdict**: **Approve w/ changes**
- **Execution Confidence (1-5)**: 3

The plan is well sequenced and honest about its own soft spots. The "Risks / watch-items" section names four real ones, including two that this review independently reached, and the "STOP and report" instruction on getter tracking is the kind of pre-committed escape hatch most plans lack. The concerns are about how much of the plan is justified and how little of it is scheduled.

**Top risks (ranked by impact x likelihood)**

1. **High impact, high likelihood. Phase 3 is scoped as one bullet and is actually the whole integration.** "Pre-round resolution in the consensus path" plus a waiver "in `enforceWinnerComposition`" hides the state-ownership refactor (Architecture issue 1), the missing seam (issue 2), the second quota read site (issue 3), and the retry question (issue 4). This is the phase that will slip, and it is the phase everything else depends on. **Mitigation**: split it. Make the misbehavior-state hoist its own phase, landing before any policy multiplication, with its own tests. It is independently valuable and independently reviewable.
2. **High impact, medium likelihood. The headline use case depends on a later phase than the one that delivers it.** Phase 3 acceptance claims UC2 role-gated fallback passes, but roles arrive in Phase 4. Either UC2's Phase 3 fixture is fake, in which case say so, or Phase 4 moves ahead of Phase 3.
3. **Medium impact, high likelihood. Phase 5 is speculative work on an unproven technique for an optimization of unknown value.** Deriving cache keys by recording property-getter access in sobek is novel here, the plan already anticipates it may be impractical, and the thing it optimizes is measured in microseconds against a path dominated by upstream round trips (the design's own §4.2 argument). **Mitigation**: cut Phase 5 from v1. Ship without a cache, measure eval cost with the benchmark that Testing asks for, and build the cache only if the numbers demand it. This also resolves the §4.2-versus-§5 contradiction.
4. **Medium impact, medium likelihood. Dependency on unmerged work.** §2 lifts the header and the metric "from #1041", and states that "ordered YAML `acceptancePolicies` is superseded". If #1041 is unmerged, this plan carries its rebase risk and its review risk. State the status.
5. **Medium impact, medium likelihood. The config surface is a one-way door.** `policies`, `customPolicy`, `waiveAgreementOnMissingData`, and especially the inline-object escape hatch become operator-visible API on first release. Removing or reshaping any of them later is a breaking change for an OSS project with unknown downstream operators. The inline escape hatch is the one to reconsider: it exists for convenience, it is explicitly worse for observability, and it guarantees that some operator's production config cannot be reasoned about from the `policies` map.
6. **Low impact, high likelihood. No dates, no owners, no estimates.** Six phases, zero scheduling information. There is no way to assess timeline realism, and no way to tell whether Phase 5 is two days or three weeks, which matters given risk 3.

**Dependency risks**

- `sobek` is already a direct dependency used by `internal/policy`, so no new third-party risk. Good.
- The auth layer must grow roles (Phase 4). That is a change to a security-critical shared component, so it will attract its own review cycle and is the most likely external gate on the schedule.
- The health and upstream layer must expose typed exclusion reasons. `CordonedReason` already exists, so this is a small addition, but it touches `upstream/` and the metrics tracker.
- #1041 as above.

**Complexity hotspots**

- `consensus/executor.go` is 57KB and the quota logic is read in two places. Any change here is high-touch.
- The auto-keying cache, for the reasons in risk 3.
- The interaction between the waiver, `preferNonEmpty`, `preferHighestValueFor`, and `agreeingResults`. That last function already exists specifically to reconcile value-equality with group hashing, and the waiver adds another axis to the same decision.

**One-way-door decisions**

Named honestly in the plan's "Locked decisions" table, which is good practice. The genuinely irreversible ones are the config surface (risk 5) and the semantic change that a previously disputing round now serves. The reversible ones, correctly identified, are pre-round-only evaluation and keeping grading declarative. The plan does not distinguish the two categories in that table, and it should.

**Timeline concerns**

Unassessable, per risk 6. Note that Phase 0 is marked complete with one open checkbox, so the plan is being executed as a live document, which is fine.

**Scope creep risks**

The plan's last watch-item pre-commits to rejecting post-round JS grading and pointing at #1069. That is the right guard and it is written down. The unguarded creep risk is the stdlib: §3.1 says "grow only when forced by observed configs", which is correct, but there is no mechanism enforcing it. Every operator request for one more helper will look individually reasonable.

**Missing from the main body**

Owners, dates, and estimates. Also a statement of who reviews the auth change, since that is the likeliest external dependency.

**Cross-cutting concerns**

- To Architecture: risk 1 is a scoping consequence of the integration gaps.
- To Devil's Advocate: risk 3 and the case for cutting the cache overlap substantially.

---

## 6. Devil's Advocate

- **Verdict (Advisory)**: Approve w/ changes

**The case against this design**

The document delivers its stated goals with about a fifth of the machinery it proposes, and it says so itself in one line in §7.1: "If historical is open to all, one policy + waiver covers it with zero JS."

Take the goals one at a time against what the repository already has. Per-method policy variation: `failsafe[]` entries already carry `matchMethod` (`common/config.go:1483`). Finality-conditioned grading: `matchFinality` (`:1484`). Historical serving outside internal retention: the `waiveAgreementOnMissingData` flag, which is one config field and one guard, is the load-bearing piece, and the spec says so. Observability of the chosen grading: a label, already scoped as lifted from #1041.

That leaves exactly one capability that needs anything new: choosing a policy based on caller role and live upstream health. In the spec's own example the JS that does this is four lines and computes one boolean. For that boolean, the design proposes a new package, a sobek VM pool on the synchronous request path, a five-function chainable stdlib, an inline-object escape hatch, and a cache whose keys are auto-derived by instrumenting JS property getters and invalidated by a generation counter, with a cardinality guard for when the keys explode.

The repository's own design razor, which CLAUDE.md declares binding for this work, says to weaken by deleting structure and that "unexercised machinery is itself a commitment". A general-purpose per-request JS policy selector with an auto-keying cache is the maximally committed form of "sometimes use a different consensus config". The weakest design that exactly handles every case in §7.2 is: the waiver, plus existing `failsafe[]` matchers, plus a declarative fallback rule of roughly the shape `fallbackPolicy: { whenNoHealthyTag: "type:internal", unlessExcludedForMisbehavior: true, requireRole: "brp:consensus-fallback", use: "fallback" }`. That handles §6 exactly, it is statically validatable, it needs no VM, it needs no cache, it has no timeout question, and it cannot be made non-terminating by an operator.

**The "do nothing" alternative**

Not viable in full, and worth being precise about why. Doing nothing leaves historical requests disputing whenever internals prune, which is a real correctness gap, and `matchFinality` does not close it because finalized is not the same boundary as outside-internal-retention. So something must ship. But "do nothing except Phase 3 step 4" is viable, and it is the highest-value fifth of the plan. Ship the waiver, watch `consensus_composition_waived_total`, and see whether anyone actually asks for role-gated fallback.

**Simpler alternatives not considered**

1. **The waiver alone.** Delivers §7 entirely. One config field, one guard, no JS.
2. **Declarative fallback rule.** Delivers §6 with a bounded, validatable schema instead of freeform JS. The cost is that it handles only the conditions in the schema, which is the point.
3. **Tick-based selection, matching the actual precedent.** The design says it "mirrors the selection-policy pattern", and it mirrors the JS-to-declarative shape while inverting the execution model. The selection policy runs on `evalInterval` and caches, which is exactly why its `evalTimeout` can fail soft by retaining the prior decision (`internal/policy/errors.go:14-16`). If consensus policy selection were also tick-based per (network, method), §5's entire cache design becomes the tick interval, §4.2's latency argument becomes trivially true, and the timeout question disappears. The cost is that role-based selection cannot be tick-cached, because roles are per request. Which is another way of saying: the per-request model exists solely for the role check.
4. **Reuse `internal/policy` rather than building `internal/consensus/policy`.** The pool, the primer, the shared-helper installation, and the timeout scaffolding all already exist. The spec asserts a new standalone engine with "no executor imports" as an architectural virtue without noting that it duplicates a working component.

**Hidden assumptions (ranked by fragility)**

1. **Roles exist and can be trusted.** They do not exist today, and the trust path is unwritten. The most fragile assumption in the document, and the one the Security agent blocks on.
2. **Pruning normalizes to `ErrEndpointMissingData` across vendors.** §7.1 says the waiver "Covers stale/misconfigured `blockAvailability` and error-shaped pruning". `ErrCodeEndpointMissingData` is a normalized eRPC code, which means every vendor's pruning error must map to it. A vendor returning a generic server error normalizes elsewhere, the waiver does not fire, and the request disputes. The repository razor warns against exactly this class of "all vendors we checked do X" reasoning. **Test it before shipping**: measure the actual error-code distribution on historical requests across your real upstream set. That measurement, not the waiver, is the first deliverable.
3. **Sitout is distinguishable from every other exclusion.** It is distinguishable only by string. See Security issue 3.
4. **The eval is cheap enough not to need a cache, and also needs a cache.** §4.2 and §5 cannot both be right. Whichever way this resolves, something gets deleted: either §5, or the per-request model.
5. **An operator will write the safe configuration.** Every safety argument in §7 assumes a never-waivable `minAgreement: 2` external quota sitting next to the waivable internal one. Nothing enforces that pairing. The spec's own example config is safe; a config with one waivable quota and nothing else is not, and it is the shorter thing to write.

**Over-engineering concerns**

Named above. The two candidates for deletion are §5 in full and the inline-object escape hatch. The escape hatch is worth arguing about specifically: §3 concedes it has "weaker observability", and §8 promises the policy name on every served round. Those two statements conflict, and the resolution is to delete the escape hatch. An operator who needs a policy that is not in `policies` can add it to `policies`.

**What could kill this**

Not a technical failure. The likeliest bad outcome is that Phase 1 and Phase 2 ship, Phase 5 consumes weeks on getter tracking, and Phase 3's waiver, the one piece that fixes a live correctness gap, lands last or not at all. The plan's ordering makes this the default path, because it puts the engine first and the correctness fix third.

**Second-order effects**

- A JS hook that reads `ctx.user` establishes a precedent for per-request JS in the request path. The next feature will want one too, and the argument against it will be harder to make.
- Named policies become a config idiom operators build tooling around. Renaming or restructuring them later is a breaking change.
- Once `policies` exists, the pressure to expose round outcomes to JS increases, because the natural next request is "select a policy based on what happened last time". The plan pre-commits to refusing this, which is good, and the pressure is a cost of shipping the mechanism at all.

**Bias detection**

- Some anchoring on the JS-selector solution. The direction is cited as accepted from a maintainer comment on #1088 and the plan's "Locked decisions" table locks nine choices before any alternatives section exists. Locked decisions are useful, but locking before documenting rejected alternatives is how anchoring becomes invisible.
- Mild overconfidence in reuse. Three separate "untouched" or "reuse existing" claims turned out to require new work. That pattern is worth noting to the authors as a prompt to re-audit the remaining reuse claims.
- No dissenting view is represented in either document.

**Questions the team should answer**

1. If the waiver alone closes §7, and a declarative fallback rule closes §6, what remains that requires freeform JS? Answer with a config an operator actually wants to write.
2. What is the measured eval cost at your p99 request rate? If it is negligible, why does §5 exist? If it is not, why is the model per request rather than per tick?
3. On your real upstream set, what fraction of historical requests currently return `ErrEndpointMissingData` versus some other normalized code?
4. Should the inline-object escape hatch ship at all, given §8's promise?
5. Is #1041 merged?

---

## Recommended decision + rationale (explicit tradeoffs)

**Block**, under rule 1 of verdict reconciliation: two High-weight agents (Architecture, Security) block.

The block is narrow and it is not about direction. The decomposition, freeform JS selecting a named declarative policy with grading left in the executor, is correct, matches this repository's design razor, and should be kept. Every blocking finding is about integration into code that already exists, and each has a concrete unblock condition.

The tradeoff the team should weigh is scope, not direction. Two paths:

**Path A, recommended. Ship the correctness fix first, then decide whether the engine is needed.** Land the misbehavior-state hoist (Blocker 1) and `waiveAgreementOnMissingData` (Blockers 4 and 5) as one change with no policy multiplication and no JS. That closes the historical-serving gap, which is the only live correctness problem in the document, and it is independently valuable and independently reviewable. Then measure: the error-code distribution on historical traffic, the waiver fire rate, and eval cost on a prototype. Decide on the JS engine with those numbers in hand. Cost: role-gated fallback waits. Benefit: the correctness fix ships in a fraction of the time, the two hardest blockers are resolved by a refactor that is valuable regardless, and Phase 5 is very likely deleted rather than built.

**Path B. Keep the plan as written, resolve all five blockers first.** Cost: Phase 3 is much larger than one bullet, the auth change gates the headline use case, and Phase 5 is speculative work with an unmeasured payoff. Benefit: role-gated fallback arrives sooner and only one review cycle is needed.

Path A is recommended because Blockers 1, 4, and 5 must be fixed under either path, and under Path A they ship as a coherent, self-justifying change instead of as prerequisites buried inside a larger feature.

Whichever path is taken, one thing should change in the documents regardless: add the alternatives-considered section. Three reasonable alternatives are unaddressed, one of them (tick-based selection) is the actual behavior of the precedent the design cites, and their absence is what makes the scope hard to evaluate.

## Blockers to resolve (owner + next step per blocker)

| # | Blocker | Severity | Agent(s) | Owner | Next Step | Resolved When |
|---|---------|----------|----------|-------|-----------|---------------|
| 1 | `consensusPolicy` owns per-upstream misbehavior state (limiters, sitout timers, exporter). Multiplying policies fragments punishment accounting and breaks the signal R7 and §6 depend on. | Critical | Architecture, Security | Spec authors + consensus owner | Add a spec section on policy state ownership. Hoist limiters, sitout timers, and the exporter to a per-network singleton shared by all policies. Land it as its own change before any policy multiplication. | The spec names the shared-state model, and a test drives one misbehaving upstream across two named policies and shows one global dispute count and one shared sitout. |
| 2 | `ctx.user.hasRole()` has no substrate. `common.User` has no roles, JWT claims are discarded, and four of five auth strategies cannot supply roles at all. | Critical | Security | Spec authors + auth owner | Specify the roles source per auth strategy: claim name, accepted claim shapes, and the documented return of `hasRole` when the strategy has no roles. Reclassify Phase 4 step 1 from reuse to new work. | §3 and §9 state the role source and per-strategy behavior, and a test asserts `hasRole` under the secret, network, and siwe strategies. |
| 3 | R7's punished-versus-unhealthy distinction is not available. Sitout, chain-identity mismatch, and admin cordons share one channel, distinguishable only by reason string. Chain-identity mismatch currently reads as availability and would trigger a downgrade. | Critical | Security, Architecture | Spec authors + upstream/health owner | Define a typed exclusion reason on the eval-visible health reference with at least `healthy`, `unhealthy`, `excluded_for_misbehavior`, `excluded_other`. Write §6's gate against the typed value and state the decision for `excluded_other`. | §6 and R7 reference the typed states, and tests cover all four cordon sources, including that a chain-identity mismatch does not select `fallback`. |
| 4 | The waiver is not localizable to `enforceWinnerComposition`. The same quota gates wait-cap arming at `consensus/executor.go:490`, so historical rounds run to the hard timeout. | High | Architecture, Operational Readiness | Spec authors | Apply the waiver at both quota read sites, or factor the check into one waiver-aware entry point. Add a p99 latency assertion on historical traffic to Phase 3 acceptance. | §7.1 names both read sites, and a test asserts a waived round returns when the externals agree rather than at the request timeout. |
| 5 | Waiver evidence has no round-completeness qualifier, and nothing enforces the presence of a never-waivable agreement quota alongside a waivable one. | High | Security | Spec authors | Require `!analysis.hasRemaining()` for the waiver, or define "all matching participants" to include non-responders. Add load-time validation that at least one non-waivable agreement quota exists whenever any waiver flag is set. | §7.1 states the completeness rule, validation rejects the unsafe shape, and a test shows no serve on a single participant. |

## Required changes (ranked)

1. Resolve blockers 1 through 5 above.
2. Specify where the selected `ConsensusPolicyConfig` enters the runtime, given that `*Consensus` is built once per `failsafe[]` entry and selected by `matchMethod` and `matchFinality` (Architecture issue 2).
3. Decide and document retry semantics. A failsafe retry re-enters consensus, so "no mid-round switch" must say whether a retry re-evaluates (Architecture issue 4).
4. Specify interrupt-based `evalTimeout` enforcement with disposal of the interrupted runtime and a cap on concurrent evals. The cited precedent does not bound a runaway eval, and per request it leaks a goroutine and a pooled VM each time (Operational Readiness issue 1).
5. Add an alternatives-considered section covering additional `failsafe[]` entries, tick-based selection, a declarative role-to-policy rule, and reuse of `internal/policy`.
6. Make the downgrade record unconditional. Emit the metric and a structured log whenever the selected policy is not the default, independent of the config-gated `X-ERPC-*` diagnostic headers. Fix the header casing (Security issue 5).
7. Add a kill switch that forces the default policy, and a per-network staged rollout with the waiver metric watched before widening (Operational Readiness issues 2 and 3).
8. Pin the metric label for the inline-object escape hatch to a constant, or delete the escape hatch. Deleting it is recommended, since §3 concedes it weakens the observability that §8 promises (Operational Readiness issue 4, Devil's Advocate).
9. Add eval duration and eval error-rate metrics, plus alert thresholds for sustained `fallback` selection and any nonzero eval error rate.
10. State the interaction between `waiveAgreementOnMissingData` and `enforceBlockAvailability`, which is opt-in in three config scopes. Either validate the combination or document the degraded mode (Operational Readiness issue 7).
11. Cut Phase 5 (decision cache) from v1, or resolve the §4.2-versus-§5 contradiction with a measurement.
12. Add owners, dates, and estimates to plan.md, and split Phase 3 so the misbehavior-state hoist is its own phase.
13. Add the tests named in the Testing Strategy section, with the cross-policy punishment test and the timeout leak test written first.
14. Add a request-flow diagram to §4 and a threat model with named actors to §9.
15. State whether #1041 is merged.

## Open questions

**Answered within this review**

- *Is `ErrEndpointMissingData` really already normalized, terminal, and non-misbehavior?* Yes. `isAgreedUponError` at `consensus/analysis.go:432-439` groups it with client-side exceptions and unsupported, and `errorToConsensusHash` gives it its own group. The spec's claim is accurate.
- *Does `blockAvailability` and `EvmAssertBlockAvailability` exist as R2 assumes?* Yes, but `EnforceBlockAvailability` is opt-in. See required change 10.
- *Does the codebase distinguish cordon reasons at all?* Yes, `CordonedReason` returns the reason string. The distinction exists but only as a string. See blocker 3.
- *Can sobek enforce a hard timeout?* Yes, `Runtime.Interrupt` and `ClearInterrupt` exist. The existing precedent simply does not use them. See required change 4.
- *Does `userId` already reach the consensus executor?* Yes, as a metrics label. The user id path exists; only roles are missing.

**Unresolved, requiring document author input**

1. Which dispatch model: one pre-built `*Consensus` per named policy, or one built per request?
2. Does a failsafe retry re-evaluate the policy?
3. What is the measured eval cost, and does it justify §5?
4. On your real upstream set, what fraction of historical requests return `ErrEndpointMissingData` versus another normalized code?
5. What is the bound on how long a revoked role keeps selecting `fallback`, given the decision cache?
6. Is #1041 merged, and is `acceptancePolicies` removal in scope here or there?
7. What is the precedence between `failsafe[]` matcher selection and `customPolicy` selection, and is `customPolicy` per project or per `failsafe` entry?
8. Should the inline-object escape hatch ship?

## Strengths (max 3-5)

1. **The §7.2 edge-case matrix.** Sixteen scenarios, each with a decided outcome, including the ones that resolve to "dispute" and the one that is explicitly rejected as unsafe. This is what made a grounded review possible, and it is rarer than it should be.
2. **Non-goals that protect the architecture.** Refusing post-round JS grading, refusing `valueGroups` and `agreeing` exposure, and refusing mid-round switching are the three decisions that keep this design from becoming unreviewable. The plan pre-commits to rejecting pressure on the first one.
3. **Deferring v1.1 with a stated trigger.** "Build when dispute traces force it" is a real trigger, not a placeholder, and the empty-waiver design is spec'd well enough to be evaluated without being built.
4. **Fail-closed is applied consistently.** Eval error, timeout, and unknown name all resolve to the default policy, never to something more permissive, and this is stated in §3, §4.1, §8, and §9 without contradiction.
5. **Honest self-assessment in plan.md.** The four watch-items include two that this review reached independently, and the "STOP and report" instruction on getter tracking is a pre-committed escape hatch that most plans lack.

## Cross-cutting concerns resolution

| Concern | Raised by | Resolution |
|---|---|---|
| Fragmented punishment state weakens the §6 security gate | Architecture (issue 1) | Answered by Security issue 4, same root cause. Consolidated as Blocker 1. Fix once, in the shared-state hoist. |
| Cordon-reason classification | Security (issue 3) | Answered by Architecture, which confirms `CordonedReason` exists so the information is available. The gap is typing, not availability. Blocker 3. |
| Waiver latency regression needs a metric before a fix | Architecture (issue 3) | Answered by Operational Readiness issue 6: `MetricConsensusWaitCapped` already exists and its help text covers this class. Add a p99 watch to Phase 3 acceptance. Blocker 4. |
| Runtime pool behavior under per-request use | Operational Readiness (issue 1) | Answered by Architecture: `acquire` allocates when idle is empty, so VM count tracks in-flight requests. Compounds the leak. Required change 4. |
| Metric label pinning for the inline escape hatch | Operational Readiness (issue 4) | Answered by Devil's Advocate, which argues the escape hatch should be deleted rather than labeled. Deleting resolves both. Required change 8. |
| Whether alternatives make the blockers moot | Architecture (issue 5), Devil's Advocate | Partially. Blockers 1, 4, and 5 must be fixed under every path considered, including the minimal one. Blockers 2 and 3 disappear only if role-gated fallback is dropped entirely, which no agent recommends. This asymmetry is why Path A is recommended. |
| §4.2 versus §5 contradiction | Devil's Advocate (assumption 4) | Unresolved by any agent. Requires a measurement. Open question 3, required change 11. |
| Tests that depend on undecided integration | Testing Strategy (issues 1, 3, 4) | Deferred by construction. These tests can only be written once the dispatch model and retry semantics are decided. Open questions 1 and 2 gate them. |

## Suggested follow-ups

1. **Before writing any code**, measure the normalized error-code distribution for historical requests across the real upstream set. It validates or invalidates the waiver's core assumption and takes far less time than building anything.
2. **Prototype the eval cost** with a throwaway benchmark on the existing `internal/policy` pool. It answers open question 3 and very likely deletes Phase 5.
3. **Land the misbehavior-state hoist as a standalone PR.** It is required under every path, it is valuable on its own, and it de-risks the largest blocker before the feature depends on it.
4. **Re-audit the remaining reuse claims.** Three turned out to need new work. Reading the code behind the others now is cheaper than discovering them in Phase 4.
5. **Ask the maintainer on #1088** whether a declarative fallback rule would be accepted in place of the JS selector. If yes, the scope drops substantially. If no, the alternatives-considered section can record the decision and its reason, which is more useful than leaving it unwritten.
6. **Add a decision-provenance affordance** modeled on the selection policy's simulator, step log, and admin endpoint. It is the difference between a debuggable incident and a code-reading exercise.

---

*Review generated by Engineering Design Review Agent Teams [6 agents, single-pass]. Not a substitute for human review.*

*Note: the skill's company-context.md was not present in the installed plugin, so no organization-specific technology-stack or engineering-standards context was applied. Findings are grounded in the eRPC codebase itself and in the design razor declared binding by this repository's CLAUDE.md.*

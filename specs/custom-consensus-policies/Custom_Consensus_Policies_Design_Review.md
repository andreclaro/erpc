# Custom Consensus Policies Engine — Engineering Design Review

**Reviewed documents**: [feature.md](./feature.md) (~1,826 words), [plan.md](./plan.md) (~933 words)
**Review date**: 2026-09-09 (Revisions 1-3); 2026-09-10 (Revisions 4-6)
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

---
---

# Revision 2 Review — commit `7320b6e3`

**Reviewed**: `specs/custom-consensus-policies/feature.md` (2,592 words, up from 1,826) and `plan.md` (1,097 words, up from 933), at commit `7320b6e3` "specs: address design review for custom consensus policies".
**Scope**: a delta pass against the five blockers and fifteen required changes above. The Revision 1 review is preserved unchanged for audit.

## Revision 2 verdict

- **Overall Verdict**: **Block** (unchanged in label, materially improved in substance)
- **Blockers**: 2 of 5 resolved, 1 partially resolved, 2 untouched
- **Required changes**: 8 of 15 resolved, 3 partially, 4 untouched
- **New issues introduced by the revision**: 5, one of which is a factual error about the executor's behavior

The revision is a real advance, not a paper response. Six of the eight recommendations that cost the design something (the inline escape hatch, the decision cache, the auth-plumbing honesty, the pool bounds, the rollout section, the answered open questions) were accepted rather than argued away. The verdict stays at Block for a narrower reason than before: two blockers were not touched at all, one blocker's fix hardens the wrong classification into a cross-package contract, and the revision added a matrix row whose stated outcome the code contradicts.

---

## Blocker status

| # | Blocker (Rev 1) | Status | Evidence |
|---|---|---|---|
| 1 | `consensusPolicy` owns per-upstream misbehavior state | **Partial** | §4.4 moves sitout to `health.Tracker`. The dispute-accounting limiter and the misbehavior exporter are untouched. See B1 below. |
| 2 | `ctx.user.hasRole()` has no substrate | **Resolved** | §4.3 is exactly the requested content. See B2 below. |
| 3 | Punished-versus-unhealthy is not a typed signal | **Not resolved** | §4.4 keeps the string match and adds a classification sentence that is wrong. See B3 below. |
| 4 | Waiver is not localizable to `enforceWinnerComposition` | **Not resolved** | R1 and plan Phase 5 step 1 unchanged. `consensus/executor.go:490` still unmentioned. |
| 5 | Waiver evidence needs round-completeness and a non-waivable-quota guard | **Not resolved** | §7.1 layer 2 unchanged. The new §7.2 row addresses a different case, and states it incorrectly. See N1. |

### B1 — Blocker 1, partially resolved

§4.4 is the right move and it solves a problem beyond the one it was asked to solve: routing sitout through `health.Tracker` gives the selector a read path without importing `consensus/`, which preserves the zero-import rule that makes the engine unit-testable. Credit where due.

Three parts of the blocker remain.

1. **The dispute accounting that leads to sitout is still per-policy.** Sitout is now global. Reaching sitout is not. `misbehavingUpstreamsLimiter` is a `sync.Map` of `*rate.Limiter` keyed by upstream id, owned by `consensusPolicy` (`consensus/policy.go:171`), and `createRateLimiter` reads and populates it per upstream. If each named policy is its own `consensusPolicy`, an upstream misbehaving under `standard` and under `generous-dev` accrues against two independent limiters, so a `disputeThreshold` of N becomes N per policy. The upstream misbehaves 2N times and is never punished. Moving the *effect* to a shared tracker while leaving the *trigger* fragmented does not close the gap.
2. **The misbehavior exporter is still per-policy.** Each `consensusPolicy` builds its own from `misbehaviorsDestination`. `createMisbehaviorExporter` already carries a comment about two destinations resolving to the same key and each upload overwriting the previous archive. N policies sharing a destination path reproduce that bug by construction.
3. **The ownership-claim guard has no stated equivalent.** `handleMisbehavingUpstream` uses `misbehavingUpstreamsSitoutTimer.LoadOrStore(upstreamId, placeholder)` specifically to claim the right to punish, and returns early with "upstream already in sitout, skipping" when another caller holds it (`consensus/executor.go:1450-1453`). §4.4 replaces that map with `tracker.Cordon(...)` and says nothing about idempotency. Two executors punishing the same upstream concurrently would each start a `time.AfterFunc`, and the first to fire calls `Uncordon` while the second timer is still pending, releasing the upstream early and then uncordoning again later. Specify that the tracker's cordon is the claim, and that only the claim winner starts a timer.

**Unblock condition (revised)**: §4.4 covers the limiter and the exporter as well as the timer, and states the idempotency rule for claiming a punishment.

### B2 — Blocker 2, resolved

§4.3 names the field (`Roles []string` on `common.User`), the source (a configurable JWT claim defaulting to `roles`, array or comma-separated), the per-strategy behavior (`secret`, `network`, `database`, `siwe` leave it empty and `hasRole` returns false), and the prohibition ("never accepted from client-controlled headers, query params, or body fields"). Plan Phase 3 step 1 is reclassified as new plumbing. §9 adds the consequence that role gating turns the JWT into a correctness control and that a leaked token can select a weaker grade. That is the whole of what was asked, plus a threat-model line that was not.

One residual inconsistency: §3 still advertises `ctx.user { roles: string[], claims: object } | null`, but §4.3 adds only `Roles`. `ctx.user.claims` is now unsourced. Either drop `claims` from §3 or add a `Claims` field to §4.3 and say which claims are exposed. Given §9's new framing, exposing the whole claim map to operator JS deserves a deliberate decision rather than an inherited one.

### B3 — Blocker 3, not resolved, and the new text hardens the wrong reading

§4.4 says: "Selector reads `tracker.CordonedReason(upstream, "*")` and treats `"misbehaving in consensus"` as punished; other cordon reasons are operator cordons, not punishment."

Both halves are problems.

**The string is now a cross-package contract.** Before the revision, `"misbehaving in consensus"` was one package's internal detail. Now the executor writes it and a different package matches on it, with no shared constant named in the spec. A reword or a typo in either place silently makes `anyPunished()` always false, which silently enables the downgrade that R7 exists to prevent. A security gate whose failure mode is silent and whose carrier is a bare string literal is the stringly-typed matching this repository's design razor rejects by name. If §4.4 keeps the string, at minimum it must name an exported constant that both sides import.

**"Other cordon reasons are operator cordons" is factually wrong.** There are four cordon-reason families at `"*"` scope, not two:

| Source | Reason | Automatic? |
|---|---|---|
| `consensus/executor.go:1465` | `"misbehaving in consensus"` | Yes, punishment |
| `architecture/evm/evm_state_poller.go:735` | `fmt.Sprintf("chain identity mismatch on major %s head move: %s", ...)` | Yes, correctness signal |
| `architecture/svm/svm_state_poller.go:487` | dynamic lag reason, logged as "svm upstream unhealthy; cordoning out of rotation" | Yes, health signal |
| `erpc/admin.go:668-676` | `"admin: manual cordon"` or an arbitrary operator-supplied string | No, operator |

Two consequences follow directly.

- **The downgrade path is now explicitly endorsed.** An EVM chain-identity mismatch cordons the internal. Under the revised rule that is "not punishment", so `anyPunished()` is false, and the internal is not in `healthy()`. `internals.healthy().length === 0 && !internals.anyPunished()` evaluates true, and authorized callers silently fall back to external-only 2-of-3 at exactly the moment an internal is signalling that it may be serving a different chain. This is the attacker-adjacent downgrade from Revision 1, and the new sentence classifies it into existence rather than out of it. The same applies to SVM lag cordons.
- **The classification is spoofable in the suppressing direction.** The admin cordon accepts an arbitrary reason string through the API, so an operator (or anything with admin access) can cordon an internal with the reason `"misbehaving in consensus"` and thereby suppress `fallback` for authorized callers. Admin is already a trusted actor, so this is low severity, but it is a clean demonstration that a free-text string is the wrong carrier for a security decision.

**Unblock condition (unchanged)**: a typed exclusion reason on the eval-visible health reference, with at least `healthy`, `unhealthy`, `excluded_for_misbehavior`, and `excluded_other`. §6's gate written against `excluded_for_misbehavior`, and an explicit stated decision for what `excluded_other` does. The chain-identity-mismatch case in particular needs a decided answer, not a default one: the defensible choice is that it blocks `fallback` exactly as punishment does, because a chain mismatch is a correctness signal and downgrading on it is strictly worse than disputing.

### Blockers 4 and 5, untouched

Neither is mentioned anywhere in the diff.

Blocker 4: R1 still reads "executor change localized to `enforceWinnerComposition`", and plan Phase 5 step 1 repeats it. `resultsSatisfyAgreementQuotas` is still read at `consensus/executor.go:490` inside the collection loop, where an unsatisfied quota returns early and prevents `maxWaitOnResult` and `maxWaitOnEmpty` from arming. Waived historical rounds run to the hard request timeout. Phase 5 acceptance has no latency assertion.

Blocker 5: §7.1 layer 2 has no round-completeness qualifier, and nothing validates that a waivable quota is paired with a non-waivable one. The risks section still says "waive only when *all* matching participants returned MissingData" without saying whether a participant that has not answered yet counts.

---

## New issues introduced by the revision

### N1 — The new §7.2 row states an outcome the code contradicts (factual error)

The revision adds this row:

> Internal + external both return MissingData, one external returns the value → **Acceptable** — MissingData is an agreed-upon error, so the MissingData group can win ≥ threshold and serve the "not found" error. The waiver only fires on composition failure, not on a value-group win.

The first half is right and the conclusion is wrong for the spec's own `standard` policy. Trace it:

1. `ErrCodeEndpointMissingData` is in `isAgreedUponError` (`consensus/analysis.go:432-439`), so `classifyAndHashResponse` assigns `ResponseTypeConsensusError`, not `ResponseTypeInfrastructureError` (`consensus/analysis.go:486`).
2. With `agreementThreshold: 2`, the MissingData group (internal + external-1) has count 2 and wins. So far the row is correct.
3. `enforceWinnerComposition` passes a winner through early only when `g.ResponseType == ResponseTypeInfrastructureError` (`consensus/executor.go:932`). A `ResponseTypeConsensusError` winner is **not** exempt, so composition is enforced.
4. `agreeingResults` for that group is {internal, external-1}. The `standard` policy's external quota is `minAgreement: 2` and is marked never waived. One external agrees, so the quota fails.
5. Result: `ErrConsensusCompositionDispute`, not a served "not found".

So the row's outcome holds only for a policy with no external agreement quota, such as `fallback`. Under `standard`, the configuration the row is presumably describing, the outcome is a dispute. Fix the row, or state which policy it assumes.

### N2 — The same row exposes a real inconsistency worth deciding deliberately

Setting the factual error aside, the row surfaces something the design has not resolved. An internal's `MissingData` is treated as an **abstention** by layer 2, which is the entire premise of `waiveAgreementOnMissingData`, and as a **vote** by value grouping, where it counts toward an agreeing group that can win. Those two treatments of the same signal point in opposite directions, and §7's goal ("historical → external-only with ≥2 agreeing") assumes the abstention reading.

The practical consequence: one misconfigured or over-pruned archive external that agrees with the internal's MissingData can outvote a correct archive external that actually has the data, and the design's own goal says that range should have been decided by externals alone. Whether that is acceptable is a legitimate design call, and calling it "Acceptable" without noting the tension with §7's goal is what needs fixing. State the reasoning, or make MissingData from an upstream that cannot serve the range a non-vote.

### N3 — Phase ordering now contradicts the plan's own analysis

§11 answer 3 concedes: "If the MissingData waiver shipped alone... Historical serving (UC3) is solved." The revision then moves the waiver from Phase 3 to **Phase 5**, behind the JS engine, the config schema, the auth plumbing, and the executor wiring.

So the one piece the spec now explicitly identifies as independently sufficient for UC3, and which needs no JS, no roles, and no selector, is scheduled last among the correctness work. Revision 1's Path A recommended the opposite ordering for exactly this reason, and the revision supplied the argument for it while moving the schedule the other way.

Related inconsistency: Phase 4 acceptance still claims "UC3 (historical via waiver)" passes, but the waiver lands in Phase 5. One of the two must move.

### N4 — Stale cross-reference after renumbering

The risks section still says "R7 is load-bearing; block Phase 4 on a distinct signal". The distinct-signal work is now Phase 3, and Phase 4 is executor wiring. Update the reference.

### N5 — §4.5 bounds the blast radius but does not say whether the selector recovers

§4.5 is a genuine improvement: 8 pre-warmed VMs, fail closed to default on pool exhaustion with a bypass metric, no blocking on VM borrow, and "a runaway eval poisons only the borrowed VM, which is discarded". That bounds a runaway eval to at most 8 leaked goroutines and 8 discarded VMs rather than unbounded growth, which was the Revision 1 concern.

What it does not say is whether the pool refills. If discarded VMs are replaced, the selector recovers and this is fully resolved. If they are not, one non-terminating eval permanently degrades the selector to default-policy-only until the process restarts, visible only as a bypass counter. Also unstated: whether `sobek.Runtime.Interrupt` (`runtime.go:1523`) is used to stop the abandoned goroutine, or whether the goroutine is simply left running as `internal/policy/slot.go:236` does today. Say which, and say whether the pool refills. Either behavior is defensible; silence is not.

---

## Required changes resolved

| # (Rev 1) | Change | Status |
|---|---|---|
| 5 | Add alternatives-considered | **Resolved** via §11. The JS-versus-declarative and second-engine rationales are real arguments, and the different-lifecycle reason for not reusing `internal/policy` (per-network tick-based with slots, stickiness, and probers versus per-request stateless) is correct and sufficient. |
| 8 | Pin or delete the inline escape hatch | **Resolved** by deletion. §1 non-goals, §3, and the locked-decisions table all agree now. |
| 9 | Eval duration and error-rate metrics plus alert thresholds | **Resolved.** §8 adds `consensus_policy_eval_duration_seconds` and `consensus_policy_eval_failed_total{reason}` with the four reasons, plus the paging guidance on sustained non-default selection and step changes in waiver fires, framed as security signals. |
| 11 | Cut Phase 5 cache or resolve the §4.2-versus-§5 contradiction | **Resolved.** Cache moved to an optional Phase 6 gated on a Phase 1 benchmark, §5 rewritten as future work, §11 answer 4 states the cost is unmeasured. The contradiction is gone. |
| 14 | Bound the policy-label cardinality | **Resolved.** §8 states the bound (≤ 10 named policies per network) and that there are no per-request unbounded labels. Deleting the inline hatch removes the unnamed-policy case entirely. |
| 15 | State whether #1041 is merged | **Resolved** by removing the dependency. §2 now says the header and metric are reimplemented here. |
| 2, 4 (partial) | Auth plumbing, eval timeout | See B2 (resolved) and N5 (partial). |
| 7 | Kill switch and staged rollout | **Partial.** §10 adds per-network enablement, rollback by removing `customPolicy` and reloading, restart as the bounded fallback, reload semantics that keep the previous program on recompile failure, and a dry-run validation path. Missing: the waiver's rollback asymmetry. Removing `customPolicy` un-selects a policy, but a round that served under the waiver cannot be un-served. Say that the waiver is staged separately per network and watched on `consensus_composition_waived_total` before widening. |
| 6 | Make the downgrade record unconditional and fix the header casing | **Partial.** The metric plus the new alerting guidance gives the always-on path, which is the substance. Still open: the codebase spells these `X-ERPC-*` not `X-eRPC-*`, and §8 still claims the header appears "on every served round" without noting that the `X-ERPC-*` diagnostic family is config-gated and off by default (`common/config.go:164-190`). |

## Required changes still untouched

| # (Rev 1) | Change | Note |
|---|---|---|
| 2 | Name the dispatch model | §4.1 step 5 and Phase 4 step 1 still say only "run existing executor under that config". `*Consensus` is still built once per `failsafe[]` entry at `erpc/networks_registry.go:127` and selected by `matchMethod` and `matchFinality`. Pre-built-per-name versus per-request-build is still undecided, and it is what determines whether B1's limiter and exporter problems are real. These two items should be resolved together. |
| 3 | Retry semantics | Nothing in the diff. §6 still says "the *next* request's eval picks `fallback`", which reads as the client's next call. A failsafe retry re-enters consensus, so "next request" is ambiguous and will be implemented one way and reviewed the other. |
| 10 | `enforceBlockAvailability` is opt-in | §7.1 layer 1 and R2 still present the guard as unconditionally active. It is a `*bool` in three scopes (`common/config.go:326`, `:2452`, `:2518`). |
| 12 | Owners, dates, estimates | plan.md still has none. Phase 4 is still one bullet carrying the whole integration. |
| — | Architecture issue 5 | Precedence between `failsafe[]` matcher selection and `customPolicy` selection, and whether `customPolicy` is per project or per `failsafe` entry, still unstated. |
| — | Actionable item 1 | §4 is unchanged. Still no request-flow diagram, which is why the dispatch-model and retry ambiguities persist. |

---

## Revision 2 blockers to resolve

| # | Blocker | Severity | Owner | Next Step | Resolved When |
|---|---|---|---|---|---|
| 1 | Punishment accounting and misbehavior export are still per-policy, and the punishment claim has no stated idempotency rule. §4.4 fixed only the sitout timer. | Critical | Spec authors + consensus owner | Extend §4.4 to cover `misbehavingUpstreamsLimiter` and the exporter. State that the tracker cordon is the punishment claim and only the winner starts a timer. Decide the dispatch model at the same time. | A test drives one upstream's misbehavior across two named policies and shows one global dispute count, one shared sitout, one exporter, and no early uncordon under concurrent punishment. |
| 2 | Exclusion reason is still a bare cross-package string, and §4.4's classification sentence routes chain-identity-mismatch and SVM lag cordons into the fallback path. | Critical | Spec authors + upstream/health owner | Replace the string match with a typed exclusion reason. Decide explicitly what a chain-identity mismatch does, with the recommendation that it blocks `fallback` exactly as punishment does. | §4.4 and §6 reference typed states, and tests cover all four cordon sources including that a chain-identity mismatch does not select `fallback`. |
| 3 | Waiver still specified as localized to `enforceWinnerComposition`, so waived rounds do not arm the wait caps. | High | Spec authors | Apply the waiver at both quota read sites, or factor the check into one waiver-aware entry point. Add a latency assertion to Phase 5 acceptance. | A test asserts a waived round returns when the externals agree, not at the request timeout. |
| 4 | Waiver still lacks a round-completeness qualifier and a non-waivable-quota guard. | High | Spec authors | Require `!analysis.hasRemaining()`, or define "all matching participants" to include non-responders. Add load-time validation that at least one non-waivable agreement quota exists whenever any waiver flag is set. | Validation rejects the unsafe shape and a test shows no serve on a single participant. |
| 5 | The new §7.2 row states an outcome the executor does not produce under the spec's own `standard` policy, and leaves the abstention-versus-vote inconsistency undecided. | Medium | Spec authors | Correct the row to a composition dispute, or name the policy it assumes. Then decide deliberately whether an internal's MissingData counts as a vote in value grouping. | The row matches the code, and §7 states the abstention-versus-vote rule explicitly. |

## Revision 2 recommendation

Two paths remain, and the revision has made the first one cheaper rather than less attractive.

**Path A, still recommended, now with the spec's own endorsement.** §11 answer 3 says the waiver alone solves UC3. Reorder so the waiver plus the shared-state work lands first, as its own change, with no policy multiplication and no JS. Concretely: current Phase 5 (waiver) plus the §4.4 work extended per Revision 2 blocker 1, plus Revision 2 blockers 3 and 4, shipped ahead of Phases 1, 2, and 4. That closes the only live correctness gap, resolves three of the five current blockers, and requires nothing from the auth layer. Then run the Phase 1 benchmark and decide on the selector with numbers in hand.

**Path B.** Keep the current order and resolve all five blockers before Phase 4 lands. The cost is that Phase 4 remains one bullet carrying the whole integration, the dispatch model is still undecided, and the waiver ships fifth.

The single highest-value edit to the documents remains the one from Revision 1 that was not made: state the dispatch model, and add the request-flow diagram that makes it and the retry question answerable. Four separate open items (Revision 2 blocker 1, required change 2, required change 3, and architecture issue 5) all trace back to that one unstated decision.

## Credit where due

The following were accepted at real cost to the design's scope, and the result is stronger for it: dropping the inline policy object, deferring the decision cache behind a measurement gate, documenting the auth plumbing honestly as new work rather than reuse, bounding the Sobek pool with a fail-closed exhaustion path, adding the rollout and reload section, adding the eval metrics with security-framed alerting, requiring the default policy to be the strictest (a gap Revision 1 did not name, and a genuine addition, since fail-closed is only safe if the default is the strictest), and answering the open questions with arguments rather than assertions. §11 answer 2 in particular is a correct and sufficient rebuttal to the engine-reuse suggestion.

---

*Revision 2 review generated by Engineering Design Review Agent Teams [delta pass against Revision 1 findings]. Not a substitute for human review.*

---
---

# Revision 3 Review — commit `3eb9160b`

**Reviewed**: `feature.md` (2,836 words) and `plan.md` (1,164 words) at commit `3eb9160b` "specs: fix review blockers in custom consensus policies".
**Scope**: delta pass against the five Revision 2 blockers. Revisions 1 and 2 are preserved unchanged.

## Revision 3 verdict

- **Overall Verdict**: **Block**
- **Revision 2 blockers**: 2 resolved, 1 mostly resolved, 2 untouched
- **New issues**: 2, both low severity
- **Character of what remains**: two mechanical fixes and three precision edits. Nothing structural is left open.

The two surviving blockers are both about the waiver, both were raised in Revision 1, and both are unchanged across three revisions. Neither needs a design decision — one is "apply the same check at the second place it is read", the other is "add a validation rule and one qualifier". Everything that required judgment has now been decided, including one decision that went against this review's recommendation and was right to.

---

## Revision 2 blocker status

| # | Blocker (Rev 2) | Status |
|---|---|---|
| 1 | Per-policy limiter, exporter, and punishment-claim idempotency | **Mostly resolved** — limiter moved, exporter accepted with a stated rationale, idempotency still unstated |
| 2 | Typed exclusion reason; chain-identity classification | **Partially resolved** — classification decided well, string coupling got worse, the default row contradicts the document's fail-closed principle |
| 3 | Waiver not localizable to `enforceWinnerComposition` | **Not addressed** (third revision) |
| 4 | Waiver round-completeness and non-waivable-quota validation | **Not addressed** (third revision) |
| 5 | §7.2 row stated an outcome the code contradicts | **Resolved**, with a wrong-quota attribution |

### B1 — mostly resolved

The rate limiter moves to `health.Tracker` keyed by upstream id, so reaching a sitout is now global across policies. That closes the accounting half of the blocker: `disputeThreshold` is once again a threshold, not a per-policy threshold.

**The exporter decision is accepted.** §4.4 keeps it per-policy on the grounds that it is "a reporting sink, not a correctness signal". That is a fair reading and the right place to draw the line. One consequence is still unaddressed, and it is a bug rather than a design question: N policies sharing one `misbehaviorsDestination` path produce N exporters writing to that path, which is exactly the collision `createMisbehaviorExporter` already carries a comment about ("its `FilePattern` stayed empty — every S3 flush then resolved to the SAME `.jsonl` key and each upload overwrote the previous archive"). Misbehavior evidence is lost silently. Either share one exporter per destination path, or require a distinct `FilePattern` per policy. Medium severity, one sentence in §4.4.

**Punishment-claim idempotency is still unstated.** `handleMisbehavingUpstream` currently uses `misbehavingUpstreamsSitoutTimer.LoadOrStore(upstreamId, placeholder)` to claim the right to punish and returns early when another caller holds the claim (`consensus/executor.go:1450-1453`). §4.4 replaces that map without saying what replaces the claim. Centralizing in the tracker makes this easy rather than hard: state that the tracker's cordon *is* the atomic claim, and that only the claim winner starts the sit-out timer. Without it, two executors punishing concurrently each start a `time.AfterFunc` and the first expiry uncordons while the second timer is still pending.

### B2 — classification decided well, two residuals

**The chain-identity decision was right, and this review's recommendation was wrong.** Revision 2 recommended that a chain-identity mismatch should block `fallback`, on the reasoning that it is a correctness signal and downgrading on it is worse than disputing. §4.5 decides the opposite: allow fallback. That is the better call. A wrong-chain internal has a worthless vote, so requiring it in the round guarantees disputes rather than preventing bad serves, and the downgrade target is still external consensus at 2-of-3, which is a graded answer rather than an ungraded one. The attack framing does not survive either: inducing a chain-identity mismatch means compromising the internal or its network path, and at that point excluding the internal is the correct response. The same reasoning covers SVM lag. Recommendation withdrawn.

§4.5 is also a genuine improvement in form. It names both automatic cordon sources, gives a decision table, and exposes health as a structured value (`{ state, cordonReason? }`) rather than asking the eval to string-match. Both prefix claims were checked and are accurate: `evm_state_poller.go:735` produces `"chain identity mismatch on major %s head move: %s"`, and `svm_state_poller.go:474-479` produces `"svm state poller: getHealth reported unhealthy"` or `"svm state poller: shred-insert lag ..."`.

**Residual 1: the `anything else` row fails open, and the rest of the document fails closed.** The table's last row reads: `anything else` → "Operator cordon" → "Treat as availability (fail open to fallback)". Three other sections state the opposite safety property. §2: "Default policy must be the strictest; fail-closed target." §4.1: eval failure "fails **closed** to the default policy ... never to a more permissive policy." §9 repeats it. Using "fail open" approvingly for the unknown case, in a design whose stated invariant is fail-closed, is an inconsistency in the document, not just a wording choice.

It also has a concrete consequence. Any cordon source added to the codebase later lands in `anything else` and silently permits the downgrade. A future cordon for something like a failed integrity check or an inconsistent state root would be a correctness signal treated as availability, and nobody would notice until someone remembered to add a row. The safe default for an unknown exclusion reason is to block `fallback` and dispute, which is the conservative direction and the one the rest of the document commits to. Note that this does not disturb the chain-identity or SVM decisions above: those are named cases with decided answers. It only changes what happens to reasons nobody has enumerated yet.

**Residual 2: string coupling increased rather than decreased.** Revision 2 asked for a typed exclusion reason to replace one cross-package string match. Revision 3 now depends on one exact match plus two prefix matches, across three packages, as a security contract. The structured `{ state, cordonReason? }` wraps the string without typing it. The three strings are currently correct, and that is the problem: they are log-message-shaped text that any reasonable person would reword without thinking, and a reword silently flips a security gate with no test failure and no log. The tracker already knows why it cordoned, so it can carry a typed reason alongside the human-readable one, which removes all three couplings at once. The recommendation stands, and it is now easier to justify than when there was only one string.

### B3 and B4 — untouched across three revisions

**B3.** R1 still reads "executor change localized to `enforceWinnerComposition`". Plan Phase 3 step 1 repeats it. `resultsSatisfyAgreementQuotas` is still read at `consensus/executor.go:490` inside the collection loop, where an unsatisfied quota returns early and so prevents `maxWaitOnResult` and `maxWaitOnEmpty` from arming. On a waived historical round the internal quota is unsatisfiable-but-waivable, the caps never arm, and the round runs to the hard request timeout. The feature is correct and slow on the one path it exists to serve. Phase 3 acceptance still has no latency assertion.

**B4.** §7.2's corrected row restates "the waiver only fires when **all** tag-matching participants returned MissingData", which is the right rule, and still does not say whether a tag-matching upstream that has not responded yet counts as one of them. `enforceWinnerComposition` runs on mid-collection analyses, and its own docstring notes a dispute is provisional while responses are outstanding. Separately, nothing validates that a waivable quota is paired with a never-waivable one. Both halves are unchanged since Revision 1.

These two are the reason the verdict stays at Block. B4 in particular is a security condition rather than a polish item: in a config with a single waivable quota and no never-waivable companion, the waiver can fire on partial evidence and the round serves on one participant. The example config in §2 is safe because the external quota is `minAgreement: 2` and never waived, but nothing requires that pairing, and the unsafe config is the shorter one to write.

### B5 — resolved, with one wrong attribution

The §7.2 row is now correct on the outcome and correct on the mechanism: MissingData classifies as `ResponseTypeConsensusError`, `enforceWinnerComposition` does not exempt consensus-error groups, so the round disputes. That matches `consensus/analysis.go:486` and `consensus/executor.go:932`.

One precision fix: the row says "The internal `minAgreement` quota fails". It does not. In that scenario the internal is inside the winning MissingData group, so `standard`'s internal quota (`minAgreement: 1`) is satisfied by one agreeing internal. The quota that fails is the **external** one (`minAgreement: 2`), because only one of the two externals agrees with the winner. The dispute is right, the attribution is not, and these rows are what the Phase 3 characterization tests get written from.

Revision 2's N2 (an internal's MissingData is an abstention for composition but a vote for value grouping) now collapses into B4. With a never-waivable external quota present, the bad case resolves to a dispute, so the tension is contained by configuration. It is only uncontained in a config without that quota, which is precisely what B4's validation rule would reject. Fixing B4 fixes N2.

---

## New in Revision 3

| # | Issue | Severity |
|---|---|---|
| 1 | The `anything else` row fails open while §2, §4.1, and §9 all state fail-closed. See B2 residual 1. | Medium |
| 2 | §7.2 attributes the composition failure to the internal quota; it is the external quota that fails. See B5. | Low |

## Resolved in Revision 3

- Misbehavior rate limiter moved to `health.Tracker`, making sitout accrual global (B1).
- Automatic availability cordons named, tabulated, and decided; health exposed as a structured value (B2, in part).
- §7.2 MissingData row corrected to a composition dispute with the right mechanism (B5).
- Waiver moved to Phase 3, ahead of auth plumbing and executor wiring, matching §11 answer 3's own conclusion that the waiver alone solves UC3 (Rev 2 N3).
- Risk cross-reference updated to "block Phase 5 on a distinct signal", which is now correct: the signal is built in Phase 4 and `fallback` goes live in Phase 5 (Rev 2 N4).
- R8 added, and Phase 4 acceptance now asserts the positive case that chain-mismatch-cordoned internals do allow `fallback`.

## Still open from Revision 1, across all three revisions

| Item | Note |
|---|---|
| Dispatch model | §4.1 step 5 and Phase 5 step 1 still say only "run existing executor under that config". `*Consensus` is still built once per `failsafe[]` entry at `erpc/networks_registry.go:127`, selected by `matchMethod` and `matchFinality`. Pre-built-per-name versus per-request-build is still undecided. Less urgent now that limiter and sitout are global, but it still determines the exporter question and the retry question. |
| Retry semantics | A failsafe retry re-enters consensus. §6's "the *next* request's eval picks `fallback`" is still ambiguous about whether a retry is a new eval. |
| `enforceBlockAvailability` is opt-in | §7.1 layer 1 and R2 still present the guard as unconditionally active. It is a `*bool` in three scopes. |
| Sobek pool recovery | §4.6 still does not say whether discarded VMs are replaced, or whether `sobek.Runtime.Interrupt` stops the abandoned goroutine. If the pool does not refill, one runaway eval degrades the selector to default-only until restart, visible only as a bypass counter. |
| Header casing and gating | Still `X-eRPC-` versus the codebase's `X-ERPC-`, and §8 still claims the header appears on every served round without noting the family is config-gated and off by default. |
| `failsafe[]` versus `customPolicy` precedence | Still unstated, as is whether `customPolicy` is per project or per `failsafe` entry. |
| Request-flow diagram | §4 unchanged. Three of the open items above are open because this is missing. |
| Owners, dates, estimates | plan.md still has none. |

---

## Close-out checklist

Two items to clear the Block:

1. **Apply the waiver at both quota read sites.** Either waive at `consensus/executor.go:490` as well as in `enforceWinnerComposition`, or factor the quota check into one waiver-aware entry point. Add a latency assertion to Phase 3 acceptance: a waived round returns when the externals agree, not at the request timeout.
2. **Add the waiver's two safety rules.** Require round completeness (`!analysis.hasRemaining()`), or define "all matching participants" to include upstreams that have not responded. And validate at config load that at least one non-waivable agreement quota exists whenever any `waiveAgreementOnMissingData` is set.

Three precision edits, none blocking:

3. Change the `anything else` row to block `fallback`, so the unknown case matches the fail-closed invariant in §2, §4.1, and §9. Leave the chain-identity and SVM rows as decided.
4. Fix the §7.2 attribution: the external `minAgreement` quota fails, not the internal one.
5. Add one sentence to §4.4 on punishment-claim idempotency (the tracker cordon is the claim; only the winner starts the timer) and one on exporter destination collisions.

One standing recommendation, worth doing before implementation rather than after:

6. Replace the three cordon-reason string matches with a typed reason on the tracker. Three strings across three packages, all currently correct, all reword-fragile, all silent on failure.

The four structural questions from Revision 1 that remain (dispatch model, retry semantics, pool recovery, `enforceBlockAvailability`) do not block the spec, but the first two should be answered before Phase 5 lands, and the request-flow diagram is still the cheapest way to answer them.

## Assessment across three revisions

Revision 1 raised five blockers and fifteen required changes. Revision 3 has two blockers left, both mechanical, and the design's substance is materially stronger than where it started: the inline escape hatch is gone, the decision cache is deferred behind a measurement gate, the auth plumbing is documented honestly as new work, the Sobek pool is bounded with a fail-closed exhaustion path, punishment state is global, automatic cordons are enumerated and decided, rollout and reload are specified, and the waiver is scheduled first because the spec's own analysis says it should be. One recommendation from this review was rejected on better reasoning than the recommendation had.

What did not move is worth naming plainly, because it is a pattern rather than an oversight: the two surviving blockers are the two that require reading `consensus/executor.go` rather than editing the spec. Every finding that could be answered by writing a new section was answered. Both findings that require confirming what the executor actually does at a second call site are unchanged across three passes. That is the thing to check before Phase 3 opens.

---

*Revision 3 review generated by Engineering Design Review Agent Teams [delta pass against Revision 2 findings]. Not a substitute for human review.*

---
---

# Revision 4 Review — commit `ccbceeae`

**Reviewed**: `feature.md` and `plan.md` at commit `ccbceeae` "specs: apply final precision edits from review".
**Scope**: delta pass against the Revision 3 close-out checklist.

## Revision 4 verdict

- **Overall Verdict**: **Approve w/ changes**
- **Revision 3 blockers**: both cleared — one by fixing it, one by this review withdrawing it
- **Close-out checklist**: 5 of 6 items resolved
- **Remaining**: 2 narrow corrections and 1 standing recommendation, none blocking

---

## Correction: the wait-cap blocker was wrong, and I am withdrawing it

This was my longest-standing finding. I raised it in Revision 1, restated it in Revisions 2 and 3, and it was based on a misreading. The spec's new text is correct and the code does not need changing.

My claim was that on a waived historical round the wait caps would never arm, because the internal's `MissingData` leaves the internal `minAgreement` quota unsatisfied at the gate in `consensus/executor.go:490`, so the round would run to the hard request timeout.

That is not what the gate evaluates. `resultsSatisfyAgreementQuotas` (`consensus/quota.go:118-146`) counts **distinct tag-matching upstreams present in the result slice**. It reads `r.Upstream` and its tags and never inspects `r.Err` or the response value. So a `MissingData` response from a tagged internal counts as coverage for that tag. Once the internal and both externals have answered, the gate passes and the caps arm normally.

The code comment immediately above the gate says this outright: "an errored or dissenting tagged response counts as coverage" (`consensus/executor.go:483-484`). I quoted that comment in my first pass over the executor and did not register what it meant for my own finding.

So the two predicates are genuinely different. The arming gate at `:490` tests **coverage** — has each required tag been heard from. `enforceWinnerComposition` tests **agreement** — did enough tagged upstreams agree with the winner. Only the second needs waiving, and Revision 4's addition to §7.1 states exactly that: the waiver "does not alter the wait-cap arming gate at `consensus/executor.go:490` — that gate still holds arming until every quota tag is covered by distinct upstreams". Accurate, and R1's new clause records it. Leaving the gate unchanged is the right call.

A related overstatement of mine should be corrected in the same breath. In Revision 3 I described the missing never-waivable-quota validation as allowing a round to "serve on one participant". That was too strong. `agreementThreshold` is the primary gate and `enforceWinnerComposition` only post-filters a winner the rules engine already produced, so the generic threshold always applies. A missing tag-specific floor costs you the tag-specific floor, not the threshold. This reduces R9 below from a security condition to a precision gap, which is how it is now ranked.

---

## Close-out checklist status

| # | Item | Status |
|---|---|---|
| 1 | Apply the waiver at both quota read sites | **Resolved** — no change needed; see the correction above. R1 now records why. |
| 2 | Round-completeness and non-waivable-quota validation | **Resolved**, with a narrow gap in the validation rule. See R9 below. |
| 3 | `anything else` cordon row should fail closed | **Resolved**, and it surfaced one case that now needs its own row. See below. |
| 4 | §7.2 quota attribution | **Resolved** — now correctly names the external `minAgreement: 2` quota. |
| 5 | Punishment-claim idempotency and exporter collisions | **Half resolved** — idempotency added, exporter collisions still open. |
| 6 | Replace the three cordon-reason strings with a typed reason | **Open** (standing recommendation). |

**Round-completeness (item 2, first half)** is now unambiguous: the waiver "only evaluates after all participants have responded or the round has otherwise terminated (wait cap, short-circuit, or timeout)". That closes the mid-collection question raised in Revision 1.

**Idempotency (item 5, first half)** matches the existing semantics precisely, including the detail that the timer is not reset. That mirrors `handleMisbehavingUpstream`'s `LoadOrStore` claim and its "upstream already in sitout, skipping" early return (`consensus/executor.go:1450-1453`).

---

## Remaining items

### 1. R9's validation checks the flag, not whether the floor enforces anything

R9 rejects a policy unless "at least one other entry has `waiveAgreementOnMissingData: false` (or omits the flag)". That is satisfiable by an entry that enforces nothing:

```yaml
requiredParticipants:
  - { tag: "type:internal", minParticipants: 1, minAgreement: 1, waiveAgreementOnMissingData: true }
  - { tag: "type:external", minParticipants: 2, minAgreement: 0 }   # waive:false, but no quota
```

Both `anyAgreementQuota` and `resultsSatisfyAgreementQuotas` skip entries with `MinAgreement <= 0` (`consensus/quota.go:105-112` and `:120-122`), so the second entry is invisible to composition enforcement. The config passes R9 and has no tag-specific floor.

**Fix**: require at least one entry with `minAgreement > 0` **and** `waiveAgreementOnMissingData` false or omitted. One clause, and it makes R9 mean what its own last sentence says ("there must always be a never-waivable floor").

Severity is low, not critical, for the reason given in the correction above: `agreementThreshold` still gates the winner regardless. The bad outcome needs `agreementThreshold: 1` alongside an all-waivable composition, and a threshold of 1 is already an explicit operator choice to be permissive.

### 2. The admin manual cordon now blocks fallback, which breaks planned maintenance

Changing `anything else` to fail closed was the right call for unknown and future reasons, and the row's relabelling from "Operator cordon" to "Unknown / future cordon" is more honest. But it moved one known, benign case into the fail-closed bucket without giving it a row.

`erpc/admin.go:668-676` cordons on operator request with reason `"admin: manual cordon"` by default, or an arbitrary operator-supplied string. That is a deliberate, declared availability action — the clearest availability case in the whole table. Under Revision 4 it lands in `anything else` and blocks `fallback`, so an operator taking an internal out of rotation for planned maintenance causes hard disputes for exactly the authorized callers who could have been served by externals. That was the first half of the original Revision 1 finding on this table, and it is now the live half.

**Fix**: add a fourth row for the admin cordon, allowing fallback, and keep `anything else` fail-closed.

This also re-motivates item 6 below more strongly than before. The admin reason is operator-supplied free text, so a prefix match on `"admin:"` is only reliable when the operator omits a custom reason. The tracker knows the cordon came from the admin path regardless of what string was passed, so a typed reason distinguishes this case correctly where a string cannot.

### 3. Exporter destination collisions (unchanged)

Keeping the misbehavior exporter per-policy is accepted. The unaddressed consequence is that N policies sharing one `misbehaviorsDestination` path produce N exporters writing to it, reproducing the collision `createMisbehaviorExporter` already documents ("every S3 flush then resolved to the SAME `.jsonl` key and each upload overwrote the previous archive"). Either share one exporter per destination path or require a distinct `FilePattern` per policy. One sentence in §4.4.

### 4. Typed cordon reason (standing recommendation)

Three string comparisons across three packages, one exact and two prefix, carrying a security decision. All three are currently correct — verified against `evm_state_poller.go:735`, `svm_state_poller.go:474-479`, and `consensus/executor.go:1465`. They are log-message-shaped text that a reasonable person would reword without thinking, and a reword flips the gate with no test failure and no log line. Adding the admin case makes it four. The tracker already knows why it cordoned.

---

## Structural items still open from Revision 1

None of these block the spec. The first two should be answered before executor wiring lands.

| Item | Note |
|---|---|
| Dispatch model | §4.1 step 5 and Phase 5 step 1 still say only "run existing executor under that config". Low risk now that the limiter and sitout are global; still determines the exporter question. |
| Retry semantics | A failsafe retry re-enters consensus. §6's "the *next* request's eval picks `fallback`" is still ambiguous about whether a retry is a new eval. |
| Sobek pool recovery | §4.6 still does not say whether discarded VMs are replaced, or whether `sobek.Runtime.Interrupt` stops the abandoned goroutine. |
| `enforceBlockAvailability` is opt-in | §7.1 layer 1 and R2 still present the guard as unconditionally active. |
| Header casing and gating | Still `X-eRPC-` versus the codebase's `X-ERPC-`, and §8 still claims the header appears on every served round without noting the family is config-gated and off by default. |
| `failsafe[]` versus `customPolicy` precedence | Still unstated. |
| Request-flow diagram | §4 unchanged. |
| Owners, dates, estimates | plan.md still has none. |

---

## Assessment across four revisions

The spec is approved with changes. Nothing outstanding would cause implementation failure, a security breach, or significant rework, and the two remaining corrections are each a clause of config validation or a table row.

Revision 1 raised five blockers and fifteen required changes. All five blockers are now closed: three by design changes (global punishment state, documented auth plumbing, enumerated cordon classification), one by adding the two waiver safety rules, and one by this review withdrawing a finding that was wrong.

The pattern I flagged at the end of Revision 3 is worth revisiting, because Revision 4 inverted it. I observed that every finding answerable by writing a new section had been answered, while both findings requiring a second look at `consensus/executor.go` were untouched across three passes. Revision 4 went and read the executor, and the result was that one of those two findings turned out to be mine, not the spec's. The response was the correct one: rather than making the change I asked for, it explained why the gate evaluates coverage rather than agreement, and recorded that in R1. That is the outcome a review should want, and it is a good argument for the diagram request, since the misreading came from reasoning about the collection loop without a picture of it.

Two recommendations from this review were ultimately rejected on better reasoning than the recommendations had: the chain-identity classification in Revision 3, and the wait-cap change here.

---

*Revision 4 review generated by Engineering Design Review Agent Teams [delta pass against Revision 3 findings]. Not a substitute for human review.*

---
---

# Revision 5 Review — commit `ccbceeae`, final pass

**Reviewed**: `feature.md` and `plan.md` at commit `ccbceeae`, plus the Revision 3 and Revision 4 text in this document.
**Scope**: verification pass. Every code citation in Revisions 3 and 4 was checked against the tree, and the four open items were re-derived from the current spec rather than from the prior review.

## Revision 5 verdict

- **Overall Verdict**: **Block**
- **One new finding**, and it is the reason for the verdict: the fail-closed rule Revision 4 approved is not expressible in the eval API the spec exposes, so the fail-open path Revision 4 believed it closed is still live.
- **Revision 4's four open items**: 2 confirmed as stated, 1 confirmed and downgraded, 1 stands.
- **Citations**: all line references verified. Two paths are wrong; the line numbers are right.

---

## New: §4.5's fail-closed row cannot be enforced by the eval it ships with

Revision 4 changed the last row of the §4.5 table to `anything else` -> "Block fallback (fail closed)". The table changed. The predicate the eval calls, and both reference evals in the document, did not.

The only exposed predicate is `anyPunished()`, and the spec defines it three times as an exact match on one reason:

- §3 helper table: "True iff any upstream that **would otherwise be eligible to participate** (healthy or sitout) is currently in sitout".
- §4.4: "`anyPunished()` is true iff any upstream that would otherwise be eligible is currently cordoned with the consensus-misbehavior reason."
- §4.5: "`anyPunished()` checks `cordonReason == \"misbehaving in consensus\"` exactly."

The reference eval appears twice, identically, in §2 and in §6:

```js
internals.healthy().length === 0 && !internals.anyPunished()
  && ctx.user.hasRole("brp:consensus-fallback")
  -> "fallback"
```

Take an internal cordoned for a reason in the `anything else` bucket. It is excluded by `healthy()`, so the first clause is true. Its reason is not `"misbehaving in consensus"`, so `anyPunished()` is false and the negation is true. The eval returns `fallback`. That is fail open, on exactly the input the new row says must fail closed.

The gap is structural, not a wording slip. `anyPunished()` is a **negative** predicate over one known reason, and the eval reads it negated. A rule of the form "downgrade only for reasons we have classified as availability" needs a **positive** predicate over the known-good set, because only a positive predicate treats the unenumerated case as a refusal. No composition of `healthy()` and `anyPunished()` produces it.

**Fix**: expose the classification instead of the reason. Give `ctx.upstreams[].health` a typed `cordonClass` of `punishment | availability | unknown`, add `internals.allCordonsAreAvailability()`, and write both reference evals against it:

```js
internals.healthy().length === 0 && internals.allCordonsAreAvailability()
  && ctx.user.hasRole("brp:consensus-fallback")
  -> "fallback"
```

This is the same change as the standing typed-reason recommendation below, and it also settles the admin case in the next item without a fourth string. One typed field closes the fail-open path, removes four string couplings, and makes the §4.5 table a mapping from cordon site to class rather than from log text to behavior.

**R8 contradicts §4.5 for the same reason.** R8 reads "only the exact `\"misbehaving in consensus\"` reason blocks fallback". After Revision 4, unknown reasons block fallback too. R8 is the line the Phase 4 acceptance tests get written from, so it is the one most likely to encode the old behavior into a passing test. Restate it as: fallback is permitted only when every cordon on a matching internal is classified as availability.

---

## Revision 4's open items, re-derived

### 1. R9 checks the flag, not the floor — confirmed, unchanged

Verified. `anyAgreementQuota` returns early on `r.MinAgreement > 0` only (`consensus/quota.go:106-112`), and `resultsSatisfyAgreementQuotas` skips `req.MinAgreement <= 0` (`consensus/quota.go:120-122`). An entry with `minAgreement: 0` and the waiver flag omitted satisfies R9's text and enforces nothing. The fix stands: require the never-waivable entry to also carry `minAgreement > 0`. Low.

### 2. Admin manual cordon has no row — confirmed, unchanged

Verified at `erpc/admin.go:667-676`. The default reason is `"admin: manual cordon"`, and an operator may supply arbitrary text through `p.Reason`. Under the current table that lands in `anything else` and blocks fallback, so planned maintenance on an internal produces hard disputes for callers who could have been served by externals. Medium. A typed `cordonClass` fixes this correctly where a prefix match on `"admin:"` cannot, because the class does not depend on what string the operator passed.

### 3. Exporter destination collisions — confirmed as a narrower case; downgrade to Low

Revisions 3 and 4 both state that N policies sharing one `misbehaviorsDestination` path lose misbehavior evidence silently, citing the `createMisbehaviorExporter` comment as precedent. The code no longer supports the general claim:

- The cited precedent cannot recur. `createMisbehaviorExporter` copies the config and calls `SetDefaults` itself, precisely so a destination that skipped the defaults chain cannot keep an empty `FilePattern` (`consensus/policy.go:185-194`).
- The default pattern is `{timestampMs}-{method}-{networkId}` (`common/defaults.go:2989-2990`). Two exporters collide only within the same millisecond, for the same method and network.
- The file exporter opens with `os.O_CREATE|os.O_APPEND|os.O_WRONLY` and writes one line per record (`consensus/export.go:64-77`). Two exporters appending to one file interleave records; neither overwrites the other.
- The S3 exporter's `used` map is allocated per flush, per exporter (`consensus/export_s3.go:240`), so it does not deduplicate across instances.

Restated accurately: the collision is real for `type: s3` when an operator sets an explicit `filePattern` that omits `{timestampMs}`, and two policies point at the same path. That is a config-validation clause, not a §4.4 design change, and it is Low rather than Medium. Revisions 3 and 4 overstated it; the correction belongs here rather than in the spec.

### 4. Typed cordon reason — stands, and is now the fix for three separate items

Four string comparisons across four packages carrying a security decision, all currently correct: `consensus/executor.go:1465`, `architecture/evm/evm_state_poller.go:735`, `architecture/svm/svm_state_poller.go:474-479`, `erpc/admin.go:667-676`. The new finding above, the admin row, and this recommendation are one change.

---

## Citation verification

Every code reference in Revisions 3 and 4 was checked. All line numbers are correct at `ccbceeae`. Confirmed:

| Claim | Location | Result |
|---|---|---|
| Wait-cap gate counts coverage, never reads `r.Err` | `consensus/quota.go:118-146` | Correct |
| "an errored or dissenting tagged response counts as coverage" | `consensus/executor.go:483-484` | Correct, verbatim |
| Arming gate holds until quotas covered | `consensus/executor.go:490-491` | Correct |
| `LoadOrStore` punishment claim, "upstream already in sitout, skipping" | `consensus/executor.go:1450-1455` | Correct |
| `Cordon("*", "misbehaving in consensus")` | `consensus/executor.go:1465` | Correct |
| MissingData classifies as `ResponseTypeConsensusError` | `consensus/analysis.go:485-486` | Correct |
| Composition gate exempts only infrastructure-error groups | `consensus/executor.go:932` | Correct |
| `*Consensus` built once per `failsafe[]` entry | `erpc/networks_registry.go:120-133` | Correct |
| Chain-identity cordon string | `architecture/evm/evm_state_poller.go:735` | Correct; path in the review text omits `architecture/evm/` |
| SVM cordon strings | `architecture/svm/svm_state_poller.go:474-479` | Correct; path in the review text omits `architecture/svm/` |
| Exporter `FilePattern` precedent comment | `consensus/policy.go:185-190` | Quoted correctly, but the code around it now prevents the case — see item 3 |

`feature.md` §4.5 gives both poller paths in full and is correct. Only this review document's own text drops them.

---

## Close-out for implementation

Two changes before Phase 4 wiring:

1. Add `cordonClass` to the eval health value, add `allCordonsAreAvailability()`, rewrite the §2 and §6 evals against it, restate R8, and give the admin cordon its class. One change, four items closed.
2. Extend R9 to require `minAgreement > 0` on the never-waivable entry.

One clause whenever the exporter is touched: reject two policies sharing an S3 `misbehaviorsDestination` path unless each `filePattern` contains `{timestampMs}`.

The structural items carried since Revision 1 (dispatch model, retry semantics, Sobek pool recovery, `enforceBlockAvailability` opt-in, header casing and gating, `failsafe[]` versus `customPolicy` precedence, request-flow diagram, owners and dates) are unchanged and remain non-blocking.

---

## Assessment across five revisions

The verdict returns to Block for one finding, and its shape is worth recording. Revision 4 fixed a fail-open default by editing the table that documents the behavior, and the review approved the edit by reading that table. Neither pass checked whether the API the table describes can express the rule the table now states. It cannot, and the two reference evals in the document still implement the old behavior verbatim.

This is the third finding in five revisions that came from reading the artifact instead of the mechanism, and the second where the mechanism contradicted an approved section. The first was the wait-cap blocker, which survived three revisions before reading `consensus/quota.go` dissolved it. The rule that would have caught both: when a revision changes a stated behavior, re-derive it from the interface that implements it, not from the prose that describes it.

The four string couplings are the same failure in the design rather than in the review. Each is prose standing in for a type, and each of the three findings that keep recurring is downstream of that substitution.

---

*Revision 5 review generated by Engineering Design Review Agent Teams [verification pass against Revisions 3 and 4]. Not a substitute for human review.*

---
---

# Revision 6 Review — commit `b57fadf1`

**Reviewed**: `feature.md` and `plan.md` at commit `b57fadf1` "specs: typed cordonClass closes fail-open fallback path".
**Scope**: delta pass against the Revision 5 close-out list, plus a check of whether the typed class the spec now depends on can be stored where the spec puts it.

## Revision 6 verdict

- **Overall Verdict**: **Block**
- **Revision 5's blocking finding**: resolved in the eval layer, and the design is right
- **New**: 1 blocking, 2 medium, 3 low
- **Character of what remains**: the typed class is the correct answer and the eval that consumes it is now fail closed by construction. The problem is one layer down. `health.Tracker` holds a single cordon flag and a single reason per `(upstream, method)`, shared by all cordon sources, so the class is last-writer-wins and any source's `Uncordon` clears every other source's cordon. The spec's §4.4 calls the tracker "a shared, race-safe source of truth"; for cordons it is shared but not per-source, and `allUnavailable()` inherits that.

---

## Revision 5's blocking finding: resolved, and resolved better than recommended

Revision 5 asked for a typed `cordonClass` and a positive predicate. Both landed. `allUnavailable()` is true only when every member is unhealthy or `availability`-cordoned, so `punishment`, `operator`, and every future class block fallback without anyone remembering to add a row. §4.5's "there is no runtime 'unknown' row ... fail closed by construction" is the right framing: the class is required at the call site, so the unenumerated case is a compile error rather than a silent downgrade. R8 and §9 were both restated to match, and the §2 and §6 evals were both updated. That is the whole of the finding, closed.

Making the class a required parameter rather than an optional one is a stronger form than Revision 5 proposed, and it is the reason the unknown-class problem disappears instead of moving.

---

## Blocking: cordon state cannot carry a per-source class as specified

§4.4 says `Tracker.Cordon` "gains a typed `cordonClass` parameter stored alongside the free-text reason". Stored where it would go, that field is one scalar per `(upstream, method)`, written by every cordon source:

- `health/tracker.go:842-866` — `Cordon` stores into `tm.LastCordonedReason` on the single `upstreamKey{upstream, method, DataFinalityStateAll}` entry. A class field alongside it has the same cardinality: one value, last writer wins.
- `health/tracker.go:869-897` — `Uncordon` does `tm.Cordoned.Swap(false)` and clears the reason on that same shared entry. It does not check who cordoned.

All four call sites in the plan use method `"*"`, so they all write the same entry (`consensus/executor.go:1465`, `architecture/evm/evm_state_poller.go:735`, `architecture/svm/svm_state_poller.go:487`, and `erpc/admin.go:676` when the operator passes no method). Two consequences, both of which re-open the path Revision 5 closed:

**1. Class downgrade.** An internal is punishment-cordoned by consensus. The EVM state poller then cordons the same upstream for chain identity, overwriting the class with `availability`. `allUnavailable()` becomes true and fallback fires while the sit-out is still running. This is not a contrived ordering: an upstream serving wrong data is exactly the one a chain-identity poller also flags, and §4.5 already lists both sources as acting on the same upstreams.

**2. Cross-source uncordon.** The SVM poller uncordons on recovery (`architecture/svm/svm_state_poller.go:467`). Its own `cordonedByHealth` CAS makes the call idempotent from the poller's point of view, but the tracker entry is shared, so the call clears an unrelated consensus punishment cordon outright — flag, reason, and class. The reverse holds too: the sit-out timer's `Uncordon` at expiry clears a poller's live availability cordon, and the poller's CAS means it will not re-cordon until it next flips unhealthy. This collision predates the spec, but the spec is the first consumer to make a security decision out of the result.

**Fix**: hold cordon state per class rather than per `(upstream, method)`. A small fixed set of flags, one per class, with `IsCordoned` as their OR and the class exposed to the eval as the set of classes currently held. `allUnavailable()` then reads "every member holds only `availability`", which is false while any punishment cordon is held regardless of arrival order, and `Uncordon` clears only the class its caller passes. This also removes the pre-existing cross-source bug rather than documenting around it.

The cheaper alternative — state that punishment cordons take precedence and are cleared only by their own sit-out timer — fixes case 1 and leaves case 2. Not recommended, but it is one sentence if the per-class set is too large for v1.

---

## Medium

### 1. The `operator` class blocks fallback, which is the case Revision 4 argued should allow it

§4.5 now maps `operator` to "**Blocks** fallback (fail closed)". Revision 4 raised this row specifically to argue the opposite: an admin cordon is a deliberate, declared availability action, and blocking fallback on it means an operator taking an internal out for planned maintenance causes hard disputes for exactly the authorized callers externals could have served.

Blocking may still be the right call — an operator who cordons because they suspect an upstream wants it out of every path, not just the internal quota. But the spec chose against a stated argument without recording a reason, and `operator` is an enumerated class, so the fail-closed-by-construction rationale does not cover it: that rationale applies to classes nobody has classified, and this one is classified. Either state why deliberate operator action is treated as suspect, or split the class (`operator:maintenance` allows, `operator:suspect` blocks), or take the Revision 4 answer. One sentence either way; the current text reads as though the question was never asked.

### 2. `allUnavailable()` is vacuously true on an empty set

§3.1 defines it as "true iff **every** member is unhealthy or cordoned with `cordonClass == \"availability\"`". A universally quantified predicate over an empty collection is true, so `internals.allUnavailable()` returns true when the internal tag matches nothing at all — a typo'd tag, a config where internals were removed, or a network where they were never defined. Authorized callers then get `fallback` on a network that has no internal participants and never did, which is the downgrade path opening on a config error rather than on an outage.

The prior predicate had the same hole (`healthy().length === 0` is also true on empty), so this is not a regression. It is worth fixing now because the primitive is named, shipped in stdlib v1, and will be copied into operator configs. Define it as false on an empty set, and say so in the §3 helper table.

---

## Low

### 3. The plan changes `Tracker.Cordon`, but the call sites use `Upstream.Cordon`

Plan Phase 4 step 2 says "`Tracker.Cordon` gains a typed `cordonClass`" and then names four call sites. None of them call it. All four call `Upstream.Cordon(method, reason)` (`upstream/upstream.go:1493`), which forwards to `metricsTracker.Cordon`. The parameter therefore has to be added to both interfaces in `common/upstream.go` (`:36` and `:54`), to `upstream/upstream.go:1493`, to both fakes in `common/upstream_fake.go` (`:155`, `:343`), and to five test call sites. Still small, but the plan currently names one function when the change touches four packages, and Phase 4 is already the heaviest phase.

### 4. Phase 3 acceptance tests a predicate the reference eval no longer calls

Plan Phase 4 step 5 still reads "Fallback eval refuse-to-fire when `anyPunished()` is true — tested". The reference evals in §2 and §6 now use `allUnavailable()`. The test as written can pass while the shipped predicate is wrong, which is the failure mode Revision 5 flagged for R8 one revision ago. Restate as: `allUnavailable()` is false when any member holds a `punishment` or `operator` cordon.

### 5. `anyPunished()` is now unexercised

No reference eval calls it. It stays in stdlib v1 (plan Phase 1 step 3) and in the §3 helper table. §3 says the stdlib should "grow only when forced by observed configs", and the repo's design razor counts unexercised machinery as a commitment. `allUnavailable()` covers the one observed use. Drop `anyPunished()` until a config needs it, or keep it and note that it exists for evals that need to distinguish punishment from operator action.

---

## Still open from Revision 5

| # | Item | Status |
|---|---|---|
| 1 | R9 validates the flag, not the floor — a never-waivable entry with `minAgreement: 0` enforces nothing (`consensus/quota.go:106-112`, `:120-122`) | **Untouched.** R9's text is unchanged at `feature.md:385-387`. Low. |
| 3 | S3 exporter destination collision when two policies share a path and `filePattern` omits `{timestampMs}` | **Untouched.** Low, and a config-validation clause. |

Both are one clause each and neither blocks. They are the only items carried from Revision 5 that were not either resolved or superseded.

The structural items carried since Revision 1 (dispatch model, retry semantics, Sobek pool recovery, `enforceBlockAvailability` opt-in, header casing and gating, `failsafe[]` versus `customPolicy` precedence, request-flow diagram, owners and dates) are unchanged and remain non-blocking.

---

## Cordon-scope note

Cordons are keyed by method. The three automatic sources all use `"*"`, but `erpc/admin.go:676` passes the operator-supplied `p.Method`, and `IsCordoned` matches either the exact method or `"*"`. So an upstream can hold an `operator` cordon on one method and an `availability` cordon on `"*"` at the same time. §4.5 assumes one class per upstream and does not say which the eval sees. Under the per-class fix above this resolves naturally — the eval sees both classes and `allUnavailable()` is false — which is another reason to prefer it over precedence rules.

---

## Assessment across six revisions

The typed class is the right design and arrived in a stronger form than the review asked for. What it exposed is that the fail-open question was never really about the eval: it was about whether cordon state can distinguish its sources at all, and it cannot. Revisions 3, 4, and 5 each moved the question one layer down — from a string match, to a table row, to an eval predicate, to the storage underneath — and each layer looked correct in isolation.

That is the same pattern noted at the end of Revision 5, one layer lower. The rule holds: when a revision changes a stated behavior, re-derive it from the thing that implements it. The difference this time is that the spec's own text pointed at the implementation (`Tracker.Cordon`), which is what made the check cheap.

---

*Revision 6 review generated by Engineering Design Review Agent Teams [delta pass against Revision 5 findings]. Not a substitute for human review.*

---

## Revision 6 disposition — applied to `feature.md` and `plan.md`

All six Revision 6 findings and both carried Revision 5 items are now in the spec. Two required a decision from the author; both are recorded here with the reasoning that settled them.

| Finding | Applied as |
|---|---|
| **Blocking** — cordon state cannot carry a per-source class | §4.5 gains "Cordon state is held per class": a bitmask over the closed class enum replaces `TrackedMetrics.Cordoned`, `IsCordoned` is "any bit set", `Uncordon(class)` clears one bit, `CordonedAtMs` keeps its transition semantics. Both failure modes (class overwrite, cross-source uncordon) are named with their call sites. R8 and §9 updated. Plan Phase 4 step 3 makes it the correctness step, with an order-independence test in acceptance. |
| **Medium 1** — `operator` blocks fallback | **Decision: block, with the reason stated.** §4.5 now says why: the admin cordon is undifferentiated, the same call serves maintenance and "I do not trust this node", so it is read as the second. An operator who wants fallback during maintenance changes policy config, which is explicit and audited. The Revision 4 recommendation is named and reversed, on the argument that the availability reading is unrecoverable when wrong while a config change is available when it is right. |
| **Medium 2** — `allUnavailable()` vacuously true on empty | §3.1 defines it false on an empty set, §9 states the consequence (a typo'd tag cannot select `fallback`), plan Phase 1 step 3 and Phase 4 acceptance cover it. |
| **Low 3** — plan named the wrong function | Phase 4 step 2 now lists the full surface: both interfaces in `common/upstream.go`, `Upstream.Cordon`/`Uncordon`, `Tracker.Cordon`/`Uncordon`, both fakes, call sites and their tests. |
| **Low 4** — acceptance tested a retired predicate | Phase 4 acceptance is written against `allUnavailable()` and adds the order-independence and empty-set cases. |
| **Low 5** — `anyPunished()` unexercised | **Decision: keep.** The §3.1 entry now says what it is for — telling punishment apart from operator action — and that `allUnavailable()` is the predicate for the fallback decision itself. |
| **Rev 5 item 1** — R9 checks the flag, not the floor | R9 and §7.1 now require the never-waivable entry to carry `minAgreement > 0`, with the `consensus/quota.go` reason inline. Plan Phase 3 validation and acceptance updated. |
| **Rev 5 item 3** — S3 exporter collision | New R10, stated in §4.4 and validated in plan Phase 3: two policies may not share an S3 `misbehaviorsDestination.path` unless each `filePattern` contains `{timestampMs}`. |

Two consequential changes came out of the per-class fix rather than being asked for directly. `health.cordonClass` became `health.cordonClasses`, a set, because cordons are keyed by method and an upstream can hold `operator` on one method and `availability` on `"*"` at once. And `state` is now defined as `"cordoned"` whenever that set is non-empty, which closes a gap no revision had named: with `state` a three-way enum and no stated precedence, a punished upstream that also fails its health check could have reported `"unhealthy"`, and `allUnavailable()` would have counted it as available-for-fallback.

The phase references in the Revision 6 text above have been corrected — the cordon work is Phase 4, and the stdlib is Phase 1.

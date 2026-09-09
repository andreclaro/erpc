# Custom Consensus Policies Engine — Engineering Design Review

Target: [andreclaro/erpc#18](https://github.com/andreclaro/erpc/pull/18) —
`specs/custom-consensus-policies/feature.md` (308 lines) and `plan.md` (170 lines).
Review date: 2026-09-09. Reviewed at spec stage, pre-implementation.

Two intake notes:

1. The plugin company context file (`company-context.md`) is absent from the
   installed plugin, so no organization specific standards were applied. The
   review uses the repository rules in `CLAUDE.md` and `.cursor/rules/` instead,
   including the "weakest hypothesis" design razor.
2. Template conformance check and structure ordering were skipped, because the
   `crcl-main/design-review` backend template could not be accessed (HTTP 404
   from the GitHub API). This document is also an open source repository spec,
   not a Circle backend design document, so section names do not map to the
   template.

---

## Executive Summary

- Overall Verdict: Approve w/ changes
- Review team: 6 agents — Architecture, Security, Operational Readiness, Testing Strategy, Risk Analysis, Devil's Advocate
- Verdict breakdown: 0 Approve, 6 Approve w/ changes, 0 Block
- Key blockers:
  1. `common.User` carries no roles or claims today, so `ctx.user.hasRole()` is new auth plumbing, not existing behavior as the spec states.
  2. Punishment state (sitout) lives in a private per executor map, which contradicts the Phase 1 rule that the selector package imports nothing from `consensus/`.
  3. `ErrEndpointMissingData` is an agreed upon consensus error, so it can win a value group and be served as the answer. The waiver design only addresses composition quotas, not this voting interaction.
  4. The decision cache (Phase 5) is unforced machinery for an eval the spec itself measures in microseconds.
- One paragraph synthesis: The design adds named declarative consensus policies
  plus a pre round JavaScript selector that chooses one of them per request. It
  keeps grading declarative and keeps the executor contract unchanged, which is
  the right boundary and mirrors the existing selection policy pattern in
  `internal/policy`. The team agrees the direction is sound and the scope
  discipline in the non goals is strong. The primary open question is whether
  the three load bearing platform assumptions hold: role data reaching the
  consensus layer, a punishment signal that is distinct from unhealthy and
  readable without a dependency cycle, and the behavior of MissingData as a
  votable value. Two secondary asks: delete the decision cache from v1 and add
  the operational sections the document currently omits (rollout, rollback,
  configuration reload, capacity).

---

## Document Quality

| # | Check | Result | Details |
|---|-------|--------|---------|
| 1 | Document Length | Pass | feature.md about 1,830 words, plan.md about 930 words. Dense and readable. |
| 2 | Structure Ordering | Skipped | Template could not be accessed. Internal ordering is logical: purpose, config, interface, architecture, cache, fallback, historical, observability, security. |
| 3 | Diagram Presence | Warning | Section 4 has an ASCII package sketch and Section 4.1 a numbered flow. There is no diagram of the request path across auth, network, upstream registry, selector, and executor. Not a block: the numbered flow carries the same information. |
| 4 | AI Slop Score | Low | Claims are specific and cite real files and symbols. Spot checks confirmed `enforceWinnerComposition` (`consensus/executor.go:924`), `EvmAssertBlockAvailability`, `blockAvailability` (`common/config.go:1314`), and the Sobek dependency (`go.mod:23`). |
| 5 | Progressive Depth | Pass | Each section leads with the decision, then the detail. The locked decisions table in plan.md is a good example. |
| 6 | Executive Summary | Suggestion | Section 1 works as a purpose statement, but there is no two sentence summary of the operator visible outcome. Add one line: what an operator can do after v1 that they cannot do today. |

---

## Actionable Items for Authors

| # | Section | Issue Type | Description |
|---|---------|------------|-------------|
| 1 | 4.1 Request flow, step 1 | Unclear | "Auth resolves user/roles (existing)" is not accurate. `common/user.go` defines `User` with `Id`, `RateLimitBudget`, and `AllowClientDirectives` only. `auth/strategy_jwt.go` parses and validates claims, then discards them. State that roles are new plumbing and name the claim to role mapping and the default for the non JWT strategies (`strategy_secret`, `strategy_network`, `strategy_database`, `strategy_siwe`). |
| 2 | 3.1 Stdlib, `anyPunished` | Lacking Context | Sitout today is an in process map in the executor (`misbehavingUpstreamsSitoutTimer`) plus `upstream.Cordon("*", "misbehaving in consensus")`. Name the interface that exposes this state to the selector package without importing `consensus/`. |
| 3 | 7 Serving historical data | Missing | MissingData is an agreed upon error (`consensus/analysis.go:isAgreedUponError`), so a MissingData group can win the round. Add a matrix row for the case where the internal and one external return MissingData while a single external returns real data. |
| 4 | 5 Decision cache | Quality | The section is the longest and most speculative in the document, for a saving the spec itself calls negligible against upstream round trip time. Cut it to one sentence in a future work list, or state the measured evidence that forces it. |
| 5 | Whole document | Missing | There is no rollout, rollback, or configuration reload section. Add how the feature is enabled per project, how an operator reverts a bad `evalFunction` without a restart, and what happens to in flight requests on reload. |
| 6 | Whole document | Missing | There is no scale or performance section beyond the qualitative note in 4.1. State the Sobek virtual machine pool size, the behavior under pool exhaustion, and the memory cost per pre warmed virtual machine. |
| 7 | 2 Configuration | Lacking Context | The inline policy object escape hatch is described as "weaker observability". Say what the header and metric report for an inline return, or drop the escape hatch from v1. |
| 8 | 8 Observability | Lacking Context | No alert or service level objective is proposed. Name the one signal that must page: sustained `fallback` selection, or a rise in `consensus_composition_waived_total`. |
| 9 | 2 Configuration | Unclear | `X-eRPC-Consensus-Policy` and the `consensus_policy` metric label are described as lifted from #1041, which is not merged. State whether this design depends on that pull request or reimplements the two signals. |

---

## Agent Roster

| # | Role | Scope (narrow, no overlap) |
|---|------|----------------------------|
| 1 | Architecture Agent | System design soundness, architecture patterns, distributed systems, scalability, extensibility |
| 2 | Security Agent | Threat model, auth, data flow trust boundaries, attack vectors |
| 3 | Operational Readiness Agent | Deployment, monitoring, rollback, incident response, runbooks |
| 4 | Testing Strategy Agent | Test coverage, test types, edge cases, confidence level |
| 5 | Risk Analysis Agent | Technical risk, dependencies, timeline, complexity assessment |
| 6 | Devil's Advocate | Strongest counter arguments, simpler alternatives, hidden assumptions |

---

## 1. Architecture

- Role: Architecture
- Verdict: Approve w/ changes
- Design Soundness (1-5): 4
- Architecture Pattern Assessment: The core decision is correct. Freeform
  JavaScript at the edge resolves an open ended operator question into one
  bounded interface, a named `ConsensusPolicyConfig`. The executor contract does
  not change. This copies a pattern that already works in this repository
  (`internal/policy`, with `engine.go`, `runtime_pool.go`, and a `stdlib/`
  package), so the extension of the design is wide and the new commitment is
  small. The pre round only rule is the load bearing simplification, and the
  document defends it well.
- Top issues (ranked by severity):
  1. Dependency direction conflict. Phase 1 requires
     `internal/consensus/policy/` to have zero imports from `consensus/`, but
     `anyPunished()` reads state that the executor owns privately
     (`misbehavingUpstreamsSitoutTimer` in `consensus/executor.go`). One of the
     two must move. The weakest option is to move sitout ownership out of the
     executor into the upstream or health layer, and have both the executor and
     the selector read it there. Decide this before Phase 1, because it changes
     the package layout.
  2. MissingData is a value, not only a gap. `isAgreedUponError` in
     `consensus/analysis.go` puts `ErrCodeEndpointMissingData` in the agreed
     upon set. With `maxParticipants: 3` and `agreementThreshold: 2`, one
     internal plus one external returning MissingData forms a winning group of
     two, and the third upstream that holds the real historical data loses.
     The composition waiver never fires, because there is no quota failure to
     waive. The document's stated goal for historical serving therefore fails
     on a plausible input. Section 7.2 covers "all MissingData" and "all null"
     but not the mixed case.
  3. Role data does not reach the consensus layer today. See Security, issue 1.
     Architecturally this means Phase 3 (executor wiring) cannot deliver its
     stated acceptance criterion UC2 before Phase 4 lands, so the phase order
     in plan.md is wrong.
  4. The decision cache adds a second, harder problem (dependency tracking of
     freeform JavaScript) to solve a first order cost the spec measures in tens
     to low hundreds of microseconds, against upstream latency in the tens of
     milliseconds. By the repository design razor this is an unforced
     commitment. Delete it from v1.
- Alternatives not considered: (a) A declarative selector, for example an
  ordered match list on method, role, and tag health, with no JavaScript at
  all. The document should say what operator need forces a Turing complete
  selector when the three example policies are selected by two predicates.
  (b) Reusing the existing `internal/policy` engine and its runtime pool
  rather than standing up a second Sobek engine. Section 1 says the design
  mirrors that package, so state why it does not reuse it.
- Scalability concerns: One eval per request on the hot path. The spec does
  not state the virtual machine pool size or the behavior when the pool is
  exhausted under burst. Blocking on a pool borrow adds latency exactly when
  the system is already loaded. Name the policy: block with a bound, or run
  the default policy and count a bypass.
- Failure modes not addressed: Configuration reload while requests are in
  flight. Sobek program recompilation failure at reload time. A named policy
  that is removed from configuration while a cached decision still names it.
  The last case is partly handled, because the cache stores the name and
  resolves after the hit, but the resolution failure path is not stated.
- Atomicity and consistency gaps: Policy selection is per instance. Two eRPC
  instances behind one load balancer can select different policies for the
  same caller at the same moment, because health, cordon, and sitout state are
  all in process. That is acceptable, and it is arguably correct, but the
  document should say so, because the header will look inconsistent to clients
  and the metric will show a mix.
- Quality attributes assessment: Backward compatibility is strong and cheap
  (inline block becomes the anonymous default). Observability is good at the
  decision level. Extensibility is good, because the stdlib is explicitly
  grow only when forced. Testability is good, because the selector is a pure
  function of its context.
- Missing from the main body: The interface that supplies upstream health,
  cordon, and sitout state to the selector. Rollout and reload behavior. The
  scale envelope for the virtual machine pool.
- Strengths: Correct boundary between freeform and declarative. Explicit non
  goals that reject post round grading and mid round switching. Fail closed to
  the default policy everywhere. Waiver scoped to one function
  (`enforceWinnerComposition`).
- Cross-cutting concerns: For Security, confirm that fail closed to default is
  correct when the default is `standard` in every deployment. For Testing,
  the mixed MissingData voting case needs a test before the waiver is built.

---

## 2. Security

- Role: Security
- Verdict: Approve w/ changes
- Threat Model Assessment (1-5): 4
- Top issues (ranked by severity):
  1. Role source of truth is undefined and the spec assumes it exists.
     `common.User` (`common/user.go:8`) has no roles or claims field.
     `auth/strategy_jwt.go` validates required claims and claim matchers, then
     drops the claim map. So `ctx.user.hasRole("brp:consensus-fallback")` is a
     new trust boundary, not existing behavior. Define: which claim name maps
     to roles, whether roles are ever accepted from a header or a query
     parameter (they must not be), and what `hasRole` returns for the secret,
     network, and database strategies. Resolved when the spec names the field
     added to `common.User`, the single strategy that populates it, and states
     that every other strategy yields no roles.
  2. R7 is load bearing and the current code makes it hard. Punishment is
     applied as `upstream.Cordon("*", "misbehaving in consensus")` plus a timer
     in a private executor map. If the selector sees only "not healthy", an
     attacker who can push an internal upstream into sitout also forces every
     authorized caller onto `fallback`, which is a two of three external only
     grade. The design already forbids this, and the plan already flags it, so
     the ask is only to make the distinct signal a precondition of Phase 4 and
     to test it. Resolved when a test proves that a cordoned and punished
     internal keeps the caller on `standard`.
  3. The inline policy object escape hatch lets an operator return an
     arbitrary `ConsensusPolicyConfig` from JavaScript, including
     `agreementThreshold: 1` and `disputeBehavior: acceptMostCommonValidResult`,
     with weaker observability by the spec's own admission. This is operator
     supplied configuration, so the trust level is the same as the rest of the
     configuration, and the risk is a mistake rather than an attack. Either
     drop it from v1 or state that the header reports a stable digest of the
     inline policy.
  4. Section 9 asserts that JavaScript is the same trust level as the
     selection policy eval, which is correct, but it does not state the
     resource bound. `evalTimeout` caps wall clock. It does not cap memory or
     an allocation loop inside one eval. Confirm that the Sobek runtime pool
     applies the same interrupt and memory posture as `internal/policy`, and
     that a runaway eval poisons only one borrowed virtual machine.
- Trust boundary gaps: The path from the authenticated user to the eval
  context is undocumented. Auth runs in `auth/registry.go:Authenticate`. The
  consensus executor is far downstream. Show where the user reference is
  attached and confirm that it cannot be set by anything other than the auth
  registry.
- Signature / auth scheme issues: None new. This design consumes existing
  authentication and adds authorization semantics on top of it. Note that
  role gating a weaker consensus grade turns the JWT into a correctness
  control, not only a rate limit control. That raises the impact of a token
  leak, and the spec should say so in Section 9.
- Fund flow risks: Not applicable directly. Indirectly, a weaker consensus
  grade on `eth_call` or `eth_getTransactionReceipt` can feed a downstream
  financial decision. The `X-eRPC-Consensus-Policy` header is the control that
  lets a caller detect a degraded grade, so it must be present on every served
  round, including cache hits and fail closed paths.
- Replay / frontrunning concerns: Not applicable.
- Residual risks (accepted): Operator supplied JavaScript can select a
  permissive policy for any caller. Section 9 accepts this, and that is
  reasonable, because it is the same as writing that policy inline today.
- Missing from the main body: The claim to role mapping. The interrupt and
  memory posture of the eval. A statement that roles never come from client
  controlled input other than the verified token.
- Cross-cutting concerns: For Architecture, the sitout signal must be readable
  without a dependency cycle. For Operational Readiness, an alert on sustained
  `fallback` selection is a security signal, not only an availability signal.

---

## 3. Operational Readiness

- Role: Operational Readiness
- Verdict: Approve w/ changes
- Production Readiness (1-5): 2
- Top issues (ranked by severity):
  1. No rollout or rollback plan. Backward compatibility is covered, which is
     not the same thing. State how the feature is turned on for one project
     first, and how an operator disables `customPolicy` under an incident. If
     the answer is a configuration change plus a restart, say that, because it
     sets the recovery time.
  2. No configuration reload behavior. eRPC reloads configuration in
     production. Say what happens to a compiled Sobek program, a pre warmed
     virtual machine pool, and a populated decision cache when the policy map
     changes. The cache already stores names rather than configurations, which
     helps, but the compile step and pool rebuild are unstated.
  3. No alerting proposal. Section 8 lists signals but names no threshold and
     no owner. The three that matter operationally are: sustained non default
     policy selection, eval error and timeout rate above zero, and waiver fire
     rate change.
  4. No runbook for the two new incident shapes: "every request selects the
     default policy because the eval throws" and "authorized callers are stuck
     on fallback because an internal never recovers".
- Deployment concerns: Six phases, each landing behind configuration that is
  off by default. That is a good posture. Phase 3 changes
  `enforceWinnerComposition`, which is on the serving path for every consensus
  round today, so it needs the strongest regression bar of the six.
- Rollback gaps: Not stated. Assume configuration revert. Confirm whether it
  requires a process restart.
- Monitoring gaps: The header and metric label report the chosen policy, which
  is the right primitive. Missing: eval latency histogram, virtual machine
  pool borrow wait or exhaustion counter, and a counter for fail closed
  selections split by reason (timeout, throw, unknown name).
- Missing runbooks / procedures: See issue 4. Also add the operator procedure
  for validating an `evalFunction` before it is deployed. A dry run command or
  a validation only mode would remove a class of production incidents at low
  cost.
- Secrets / key management issues: None new.
- Data pipeline and reporting gaps: Adding a `consensus_policy` label to
  consensus metrics multiplies series cardinality by the number of policies.
  With project, network, upstream, user, and agent labels already present, say
  the expected policy count and cap it, or the metric cost lands in the
  monitoring bill.
- Missing from the main body: Rollout, rollback, reload, alert thresholds,
  runbooks, metric cardinality.
- Cross-cutting concerns: For Risk Analysis, the operational gaps are the
  reason not to ship Phase 5. For Architecture, the pool exhaustion behavior is
  both a design and an operations question.

---

## 4. Testing Strategy

- Role: Testing Strategy
- Verdict: Approve w/ changes
- Test Confidence (1-5): 3
- Top issues (ranked by severity):
  1. The edge case matrix in Section 7.2 is the strongest part of the
     document, and R6 correctly says fallthrough cases first. It is missing the
     case that breaks the stated goal: internal returns MissingData, one
     external returns MissingData, one external returns the historical value,
     with `agreementThreshold: 2`. Add the row and the expected outcome before
     the waiver is implemented.
  2. Zero regression on the default path is asserted in Phase 3 but not
     measured. The repository has a large existing consensus suite
     (`consensus/executor_test.go`, `composition_test.go`,
     `executor_race_test.go`, `wait_cap_test.go`). State that the whole suite
     runs unchanged against the anonymous default policy, and that the
     acceptance criterion is a green run with no test edits.
  3. Concurrency is untested by the plan. The selector borrows a virtual
     machine per request from a shared pool, and the health generation counter
     is read by many requests while it is bumped by upstream transitions. The
     repository already uses race tests for the executor, so add a race test
     for the selector and the counter.
  4. No negative test for the trust boundary. Add a test that a caller who
     supplies a role in a header or a body field, rather than in a verified
     token, does not get `fallback` or `generous-dev`.
- Untested critical paths: Configuration reload during traffic. Pool
  exhaustion. Eval that returns a wrong type, for example a number or an
  array. Eval that mutates the context object. Cache invalidation on rapid
  upstream flap.
- Missing edge cases: Mixed MissingData voting (issue 1). Unknown policy name
  returned from a cached decision after the policy is deleted from
  configuration. `blockNumber` absent or a tag, for example `latest` or
  `finalized`, where the plan assumes numeric extraction (Phase 4, item 3).
  Nil user with `customPolicy` configured, which is listed in Phase 1 and
  should also be a config level test.
- Test type gaps: The plan lists unit tests, characterization tests per matrix
  row, and end to end fixtures. That mix is right. Missing: a race test, a
  benchmark that proves the eval cost claim in Section 4.2, and a fuzz or
  property test over eval return values, which is cheap for a function whose
  entire contract is `string | object | null`.
- Security-relevant untested scenarios: R7 refuse to fire on punishment is
  called out and must be a test, not a review item. Role spoofing, as above.
  Waiver over breadth, where one participant returns a value and the rest
  return MissingData, and the quota must hold.
- Test environment concerns: The end to end fixtures need upstreams that can
  be driven into distinct health states, including punished and cordoned.
  Confirm that the existing fake upstream (`common/upstream_fake.go`) can
  express sitout, or the R7 test cannot be written.
- Missing from the main body: The regression bar for the existing suite, and
  the benchmark that supports the latency claim.
- Cross-cutting concerns: For Architecture, issue 1 here is the same defect as
  Architecture issue 2. For Security, the R7 test is the control that makes the
  fallback safe.

---

## 5. Risk Analysis

- Role: Risk Analysis
- Verdict: Approve w/ changes
- Execution Confidence (1-5): 3
- Top risks (ranked by impact times likelihood):
  1. High impact, high likelihood. Phase ordering is wrong. Phase 3 acceptance
     includes UC2, the role gated fallback, but roles arrive in Phase 4. Either
     move auth plumbing before executor wiring, or drop UC2 from the Phase 3
     bar. Left as is, Phase 3 will either slip or ship a stub role source.
  2. High impact, medium likelihood. The sitout and health signal (R7) may not
     exist in a usable form. The plan says to block Phase 4 on it, which is
     correct, but the risk is that this requires moving punishment state out of
     the executor. That is a refactor of production code on the serving path,
     and it is not in any phase.
  3. Medium impact, high likelihood. Sobek getter tracking for automatic cache
     keys is explicitly flagged as possibly impractical, with a fallback to
     operator declared `cacheKeys`. Two candidate key models plus a
     cardinality guard plus uncacheable detection plus generation counter
     invalidation is the largest unknown in the plan for the smallest measured
     benefit. Cut the phase.
  4. Medium impact, medium likelihood. Dependency on #1041 for the header and
     the metric label. If that pull request does not land, this design owns two
     more pieces of work than the plan shows.
  5. Low impact, medium likelihood. Scope creep toward post round grading. The
     plan names it and rejects it, which is the correct mitigation.
- Dependency risks: Upstream issue #1088 direction, which is settled. Pull
  request #1041 for the header and metric, which is not. #1069 for bounded
  deviation, which is correctly excluded. The existing
  `EvmAssertBlockAvailability` and `blockAvailability` configuration, which are
  present in the tree and stable.
- Complexity hotspots: The decision cache, by a wide margin. Then the
  `enforceWinnerComposition` change, because it is small in size and large in
  blast radius. Then the eval context construction, which touches auth, the
  upstream registry, and request parsing.
- One-way-door decisions: The `evalFunction` signature and the eval context
  shape are a public configuration contract. Once an operator writes JavaScript
  against `ctx.upstreams[].health`, that field name is fixed. Spend the extra
  review pass on the context shape, not on the cache. Also the choice to expose
  a stdlib rather than raw objects is right and hard to reverse later.
- Timeline concerns: The plan has no estimates and no sequencing dependencies
  drawn between phases. Six phases with a hard dependency from 3 to 4 and a
  research spike inside 5 needs at least a rough size per phase to be
  reviewable.
- Scope creep risks: The v1.1 empty waiver with block proof is fully specified
  in Section 7.1 while being explicitly out of milestones. That is a good
  discipline, but a fully written specification is an invitation to build it.
  Consider moving it to a separate file so the v1 contract is unambiguous.
- Missing from the main body: Phase sizing. The refactor implied by risk 2.
- Cross-cutting concerns: For Architecture, risk 2 is the dependency direction
  conflict. For Operational Readiness, cutting Phase 5 also removes three of
  the proposed metrics.

---

## 6. Devil's Advocate

- Role: Devil's Advocate
- Verdict (Advisory): Approve w/ changes
- The case against this design: It adds a JavaScript engine to the consensus
  hot path to pick between three configuration blocks that differ in four
  numbers and two booleans. The two decisions the examples actually make are
  "is this caller privileged" and "are the internal upstreams available". Both
  are expressible declaratively. The engine is justified by the open ended set
  of future operator questions, which is a real argument in this repository,
  but the document never states an operator request that a declarative matcher
  could not serve.
- The "do nothing" alternative: Today an operator can run one inline consensus
  block per network, and can already tag upstreams and set
  `requiredParticipants` quotas. The two concrete pains that remain are the
  historical serving case and the internal outage case. The historical case is
  solved by one boolean, `waiveAgreementOnMissingData`, which is Phase 3 item
  4 and needs no policy engine at all. The outage case is the only one that
  genuinely needs per request policy selection. So a large part of the stated
  value lands from a small part of the plan.
- Simpler alternatives not considered:
  1. Ship only the waiver. One field, one function changed, and the historical
     serving goal is met.
  2. Declarative selector. An ordered list of match rules on role, method, and
     internal availability, each naming a policy. This handles all three
     example policies and keeps the configuration inspectable, diffable, and
     testable without a virtual machine.
  3. Reuse `internal/policy` rather than building a second engine.
- Hidden assumptions (ranked by fragility):
  1. That roles already reach the consensus layer. They do not. This is the
     most fragile assumption in the document, because the fallback feature and
     the historical role gating both rest on it.
  2. That "punished" and "unhealthy" can be told apart cheaply. Punishment is
     implemented as a generic cordon plus a private timer map.
  3. That MissingData behaves as an absence rather than as a value. It is in
     the agreed upon error set, so it votes.
  4. That evaluation cost is negligible, therefore a cache is still needed.
     These two claims sit in adjacent sections and pull in opposite
     directions. One of them is wrong, and Section 4.2 says which.
  5. That an operator will write correct JavaScript against upstream health
     under partial failure. The fail closed default limits the damage, which is
     good.
- Over-engineering concerns: The decision cache is the clearest case. Automatic
  dependency tracking of freeform JavaScript through property getters, with a
  declared key fallback, a cardinality guard, uncacheable detection, and a
  generation counter, is four mechanisms guarding an optimization worth
  microseconds against a network call worth milliseconds. The repository design
  razor says to weaken by deleting structure. Delete it.
- What could kill this: An operator writes an `evalFunction` that selects a
  weaker policy far more often than intended, and nobody notices because the
  only signal is a header. Alerting on non default selection is the cheap
  insurance.
- Second-order effects: Consensus grade becomes a per caller property. Support
  and debugging change shape, because two callers asking the same question can
  get different answers with different guarantees, and both are correct. The
  header makes this visible, and it must therefore be documented for callers,
  not only for operators.
- Questions the team should answer:
  1. Name one operator requirement that a declarative selector cannot express.
  2. If Phase 3 item 4, the waiver, shipped alone, what percentage of the pain
     in #1088 is gone?
  3. What is the measured eval cost, and at what request rate does it matter?
  4. Why a second Sobek engine rather than the one in `internal/policy`?

---

## Recommended decision + rationale (explicit tradeoffs)

Approve with changes. Accept the spec as the implementation contract for the
selector engine, the named policy map, and the MissingData waiver. Do not start
Phase 1 until the three platform facts below are settled, because each of them
changes package layout or phase order rather than only wording.

The central tradeoff is freeform against declarative selection. Favor freeform,
as the spec does. The repository domain is open ended, the pattern already
exists in `internal/policy`, and the design confines the freeform part to
selection while grading stays declarative. That is the weakest commitment that
handles the observed cases. The price is a JavaScript engine on the hot path
and an operator facing context contract that is hard to change later, so spend
review effort on the context shape.

The second tradeoff is cache against simplicity. Favor simplicity. The spec's
own latency numbers do not force a cache, and the cache design is the largest
unknown in the plan.

## Blockers to resolve (owner + next step per blocker)

| # | Blocker | Severity | Agent(s) | Owner | Next Step | Resolved When |
|---|---------|----------|----------|-------|-----------|---------------|
| 1 | Roles do not reach the consensus layer. `common.User` has no roles or claims; the JWT strategy discards the claim map | High | Security, Architecture, Risk | Spec author | Add a section that names the field added to `common.User`, the claim to role mapping, the strategy that populates it, and the no roles default elsewhere. Move this work before executor wiring | Spec states the auth change explicitly and plan.md orders auth before Phase 3 acceptance criterion UC2 |
| 2 | Punishment state is private to the executor, which conflicts with the zero import rule for the selector package | High | Architecture, Security | Spec author with executor owner | Decide where sitout state lives. Preferred: move it to the upstream or health layer and have both readers consume it. Add the refactor to the plan as its own phase | The spec names the interface that exposes healthy, cordoned, and punished distinctly, and Phase 1 keeps its zero import acceptance criterion |
| 3 | MissingData is an agreed upon consensus error, so a mixed MissingData group can win the round and defeat the historical serving goal | High | Architecture, Testing | Spec author | Add the mixed case row to Section 7.2 with the expected outcome, and state whether value grouping must change or the case is accepted as a dispute | A test exists for internal MissingData plus one external MissingData plus one external value, and its expected outcome matches the stated goal |
| 4 | Decision cache is unforced complexity for a microsecond level saving | Medium | Architecture, Risk, Devil's Advocate | Spec author | Remove Phase 5 and Section 5, and leave one line under future work with the trigger that would justify it | Section 5 is one paragraph or less, and Phase 5 is out of the v1 plan |
| 5 | No rollout, rollback, reload, or alerting content | Medium | Operational Readiness | Spec author | Add one section covering enablement per project, revert procedure, configuration reload behavior for the compiled program and the pool, and the one signal that pages | The section exists and names the revert path and at least one alert threshold |

## Required changes (ranked)

1. Correct Section 4.1 step 1 and add the auth plumbing design (blocker 1).
2. Resolve the sitout ownership and the package dependency direction (blocker 2).
3. Add the mixed MissingData case and its resolution (blocker 3).
4. Delete Section 5 and Phase 5 (blocker 4).
5. Add rollout, rollback, reload, and alerting (blocker 5).
6. Reorder plan.md so auth and health plumbing precede the Phase 3 acceptance
   criteria that depend on them.
7. State the Sobek pool size, the pool exhaustion behavior, and the memory cost
   per pre warmed virtual machine.
8. State whether the header and metric depend on #1041 or are owned here.
9. Add the eval observability that is missing: latency histogram, fail closed
   counter split by reason, pool exhaustion counter.
10. Decide the fate of the inline policy object escape hatch. Drop it, or
    define its header value.
11. Cap the policy count or state the expected metric cardinality increase.
12. Move the v1.1 empty waiver specification to its own file.
13. Add a race test and a benchmark to R6 and to Phase 1.
14. Answer the four Devil's Advocate questions inside the spec, particularly
    the one about a declarative selector and the one about reusing
    `internal/policy`.

## Open questions

1. Which claim carries roles, and is any deployment expected to derive roles
   from something other than a verified token?
2. Does moving sitout state out of the executor break any current behavior,
   for example the per instance ownership claim in `LoadOrStore`?
3. Is per instance policy divergence behind a load balancer acceptable, and
   should the header therefore be treated as advisory by callers?
4. What happens to a cached or in flight decision when the named policy is
   deleted by a configuration reload?
5. `blockNumber` extraction: what is the eval context value for `latest`,
   `finalized`, or a block hash?

## Strengths (max 3-5)

1. The boundary is right. Freeform JavaScript selects, declarative
   configuration grades, and the executor contract does not change.
2. The non goals are unusually disciplined. No post round grading, no mid round
   switching, no separate allowlist, and v1.1 explicitly outside milestones.
3. Fail closed to the default policy on every failure path, never to a more
   permissive policy.
4. Zero operator migration. The inline block becomes the anonymous default.
5. The edge case matrix in Section 7.2 and the risk list in plan.md do most of
   a reviewer's work in advance, and the claims cite real files and symbols.

## Cross-cutting concerns resolution

| Concern | Raised by | Resolution |
|---|---|---|
| Is the sitout signal readable without a dependency cycle? | Architecture, Security, Risk | Not answered by the document. Blocker 2. |
| Is fail closed to `standard` always correct? | Architecture | Yes for the configurations shown, because `standard` is the strictest of the three. It is not guaranteed in general, since the default is whatever the operator names. Add one line requiring the default to be the strictest policy, or state that this is the operator's responsibility. |
| Does the mixed MissingData case defeat the historical goal? | Architecture, Testing | Confirmed against `consensus/analysis.go:isAgreedUponError`. Blocker 3. |
| Can the existing fake upstream express sitout for the R7 test? | Testing | Not verified in this review. `common/upstream_fake.go` exists and implements `EvmAssertBlockAvailability`. The author must confirm the health and cordon surface. |
| Does cutting Phase 5 remove needed observability? | Operational Readiness, Devil's Advocate | Yes, and that is a benefit. The three cache metrics disappear with the cache. The eval latency histogram and the fail closed counter are still required. |
| Is the latency claim measured? | Architecture, Testing, Devil's Advocate | No. A benchmark is required, and it also settles whether any cache is justified. |

Unresolved questions requiring document author input: all five items under
Open questions, plus the four Devil's Advocate questions.

## Suggested follow-ups

1. Land the MissingData waiver as its own small pull request before the
   selector engine. It carries most of the historical serving value and it is
   independently testable.
2. Add the benchmark for eval cost early, in Phase 1, because it decides
   whether the cache is ever built.
3. Write the caller facing documentation for `X-eRPC-Consensus-Policy` at the
   same time as the operator documentation. Callers need it to detect a
   degraded grade.
4. Keep a note in the spec pointing at the upstream issue comment thread, so
   the accepted direction stays traceable when the specification is edited.

---

Review generated by Engineering Design Review Agent Teams [6 agents, single-pass].
Not a substitute for human review.

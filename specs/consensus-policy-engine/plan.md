# Consensus Policy Engine — Implementation Plan

Companion to [feature.md](./feature.md). Section references (§) point there.

## Locked decisions

- **A finite tree of Go primitives at runtime; zero interpreter *and* zero
  compilation on the request path** (§3).
- **The authoring surface is freeform JavaScript with static validation —
  decided** (§13.2). A data-only document is the designed-out fallback
  (Appendix A); switching changes neither the runtime, the vocabulary, nor the
  tree. Consequences: Phase 2 builds the taint analysis, the trace budget with a
  real interrupt, and the staged builder types — all of which exist only to
  police a host language.
- **Validator soundness is a Phase 2 deliverable, not an assumption** (§3.1.3):
  a property test over generated policies plus a two-trace differential oracle.
  This validator has had a fresh hole found on every review pass, so "the tests
  we wrote for it pass" is not evidence.
- **Policy authors are trusted operators, and a policy is executed code** —
  project config, the same boundary as `selectionPolicy.evalFunc`. §3.1's
  validation bounds shape, not effect; it is not a sandbox (§3.1.2).
- **The engine is justified on maintainability, argued explicitly** (§13.1).
  §12 is the only named novel extension; the extension argument alone does not
  carry Phases 1–5, and the maintainability case is made rather than assumed.
  If the team rejects it, the planned outcome is the weaker alternative —
  which §13.1 shows is Phase 0 + Phase 3 + Phase 4, not Phase 0 + Phase 4.
- **Compile keys are config-derived only.** `method` is a runtime matcher;
  every tree compiles eagerly at load/reload (§3.2).
- **Winner composition (`minAgreement`) stays mechanical** and is **not** one
  of the 24 rules. `require` falls through; the existing gate disputes — they
  are different operators. `requireTag` serves the graded use case only
  (§7.1).
- **Partition is policy-owned; hash identity is mechanical** (§7). Forced by
  `preferHighestValueFor`, which already re-partitions by value and spreads
  quotas across the partition.
- **Sound finality and today's short-circuit timing are incompatible**, so
  both are named. Sound is `lead > remaining + replaceable` plus the §5.2
  fold; early exit carries **each shortcut's own complete predicate** — only
  one of the three is `lead >= remaining` — and `.earlyExit(reason, pred)`
  attaches to **any** outcome, not just `accept`, carrying a literal reason
  so the telemetry label stays bounded (§4.4, §5.4).
- **`capFired()` requires the §5.5 lifecycle**: evaluate once after the cap,
  with outstanding slots removed from the completion space, **publish the
  outcome, then drain** — draining before publishing turns a wait cap into
  wait-all.
- **Termination is conditioned on an empty completion set, not on the cap**
  (§5.6). `wait()` after the final ordinary slot is equally forbidden and
  equally backstopped.
- **Three counters, not one (§5.3).** `physicalOutstanding` drives the drain;
  `legalOutstanding` drives the fold and is zeroed at cap fire; the raw
  `maxParticipants - collectedResponses` survives only for translated
  `.earlyExit` parity. Physical and legal diverge at every capped
  `fireAndForget` round — a supported configuration, not an edge — so the
  §5.6 invariant is stated over `legalOutstanding`.
- **"Sound" is equality of the decision, up to declared equivalence** (§5) —
  the winning partition's identity, outcome kind, grade and error identity.
  **Not** the served payload: a same-hash larger response replaces the
  representative ([analysis.go:126-129](../../consensus/analysis.go)) and
  `a.participants()` grows on every arrival, so payload-stability is unprovable
  until `legalOutstanding == 0` and would collapse every policy to wait-all.
  Group identity alone is too weak, byte-stability too strong; the invariant
  field list in §5 is the specification. **There is no payload-stability
  opt-in** — its semantics are provably wait-all for every policy and no
  observed case asks for it, so the razor deletes it rather than deferring it.
- **The leader stays a live read; `fromLeader` declares itself unstable while
  completions remain** (§5.2). Snapshotting per round would change winner
  selection for existing leader-based configs, contradicting §9. Determinism for
  the Phase 1 differential comes from a **fixed leader schedule injected in
  tests**, not from a production change.
- **Wait caps stay mechanical in full**, including the quota-aware arming
  gate; the policy decides only resolution, via `capFired()` (§7).
- **Grades are the only engine-enforced capability**; grants are a finite
  declared mapping with an explicit `default` grade and no inferred strictness
  ordering (§6). **Roles are a second, deliberately weaker capability** —
  `callerHasRole` selects an alternative, is never enforced on output, is never
  a metric label, and gets no coverage cross-validation (§6.5).
- **Backward compatibility lives only at config load** (§9).

## Glossary

- **Round** — one consensus execution: fan-out, collection, decision.
- **Tree** — the compiled policy, built during load/reload.
- **Partition** — the decision-time grouping a policy ranks and counts over;
  hash groups by default, or a `partitionBy` view (§4.1).
- **Alternative** — one `when(guard, branch)` entry; first match wins.
- **Sound** — the decision is identical under every legal completion.
- **Early exit** — a policy-declared tolerance for serving before soundness.
- **Prefix** — round state after the first *k* responses of a permutation.
- **Composition gate** — the mechanical post-decision `minAgreement` check
  that wraps the tree's outcome; not a policy primitive (§7.1).

## Build order rationale

**Phase 0 ships regardless of everything below.** Incremental round state and
the late-body release fix (0.3) pay off whether or not the engine is ever built,
and they are the only phases of which that is true. Land them first and
independently, without waiting on §13's first open question.

**Phase 1 is a decision gate, not a build step.** If the differential holds,
the factoring claim is proven and Phases 2–5 are justified. If a rule turns
out to be irreducible, the response is **not** a bespoke primitive to absorb
it, and **not** stopping with nothing: the feature descopes to
grades-only — Phase 0 + Phase 4, delivering §12's graded acceptance against
the existing 24 rules (§13). State that branch before starting, so an
irreducible rule is a planned outcome rather than a stalled project.

Phase 1 (the Go tree and the rewrite of all 24 winner rules) lands **before**
the authoring layer: the riskiest claim is that the factoring covers the
existing rules, and proving it requires no authoring surface at all.

### The Phase 1→2 fork is closed

Earlier revisions carried an undecided authoring fork into the plan. It is
decided (§13.2: freeform JavaScript with static validation), so Phase 2 has one
scope. What Phase 1
still gates is **scope justification**: if exact parity proves harder than §11
assumes, that is evidence for §13.1's weaker alternative, and the descope
branch below stays open.

---

## Phase 0 — Incremental, reversible round state

Independently shippable; land it first regardless. **Behaviour-preserving with
one deliberate exception**, named rather than folded in silently: the late-body
release fix (0.3) releases bodies that today leak. Everything else here,
including the leader seam, asserts zero behaviour delta.

### 0.1 Incremental state

- Replace the rebuild-per-response in `newConsensusAnalysis`
  ([consensus/analysis.go](../../consensus/analysis.go), called at
  [executor.go:545](../../consensus/executor.go)) with a round state each
  arriving response updates in place: group counts, per-group tag counts,
  per-group largest/first-error, valid-vs-total participants.

### 0.2 Vote replacement — the state is **not** append-only

- An upstream is freed for reselection when it returned no response, a
  retryable error, or an empty result (`canReUse`,
  [common/request.go](../../common/request.go)), so a later response can
  **replace** its vote.
- Requires an **upstream → current vote index**, and update logic that
  removes a vote from its group, deletes a group left empty, and recomputes
  extrema, ties and tag counts.
- Preserve per-response caching (`r.CachedHash`, `r.CachedResponseType`,
  `r.CachedResponseSize`); extend it to derived values rank terms need.
- Maintain per-tag participant counters and **both** outstanding counters —
  `physicalOutstanding` (spawned minus reported, nil or not) and
  `legalOutstanding` (completions that may still affect the outcome). Phase 3
  inputs, O(1) each. They are equal until a cap fires; keeping one and
  deriving the other is what makes the §5.6 invariant unassertable at a capped
  `fireAndForget` round (§5.3).
- Keep `LargestResult`'s replace-on-larger semantics
  ([analysis.go:126-129](../../consensus/analysis.go)) intact under incremental
  update — including recomputing it when the vote that supplied it is removed.
  Phase 3 does **not** need its stability (§5 equality excludes the payload),
  but the incremental state must still reproduce it exactly.
- **Add a test seam for the leader read, not a snapshot.** Leave the per-arrival
  `EvmLeaderUpstream` call at
  [analysis.go:82](../../consensus/analysis.go) alone — snapshotting would
  change winner selection for existing leader configs (§9). Expose an injectable
  leader source so Phase 1's differential can replay a **fixed leader
  schedule** deterministically, and so leader *movement* mid-round becomes a
  test case rather than a hidden variable.

### 0.3 Late-body release on the wait-capped path — a standalone bug fix

Independent of this feature; fix it now rather than inheriting it (§5.5
steps 6–7).

- On cap fire the collection loop `break`s
  ([executor.go:526](../../consensus/executor.go)). Nothing else reads
  `responseChan`, and `releaseNonWinningResponses`
  ([executor.go:647](../../consensus/executor.go)) iterates only the
  `responses` slice, which stopped growing at that break — so bodies from
  participants that complete after the cap are never released. The
  short-circuit path releases them explicitly
  ([executor.go:532](../../consensus/executor.go)); the cap path does not.
- **Publish the outcome first, then drain — this ordering is the fix, not an
  optimisation.** Today the outcome is sent only after the collection loop
  ([executor.go:586](../../consensus/executor.go)), which the cap path reaches
  by `break`ing. Replacing the `break` with an in-loop drain — the obvious
  implementation — moves the caller's outcome from *cap-fire* time to
  *last-participant* time and silently converts a wait cap into wait-all.
  Under `fireAndForget`, where outstanding attempts are deliberately not
  cancelled, that is bounded only by the slot and request timeouts. So:
  `sendOutcomeOnce` at cap fire, drain afterwards and off the caller's path.
- Drain the channel and release late bodies without touching the frozen
  analysis. Participants are guaranteed to write exactly once
  ([executor.go:506](../../consensus/executor.go)), so the drain is bounded.
- Reproducing test (the one Phase 3.4 already specifies, pulled forward): a
  wait-capped round under `fireAndForget` with participants completing after
  the cap releases every late response, asserted with a leak counter. It must
  fail against `main`.
- **Cap-latency regression, in the same change.** A wait-capped round with a
  straggler that completes long after the cap must return to the caller at the
  cap deadline, not at straggler completion — assert on elapsed time, and on
  the existing `Consensus_TailLatencyCapped` benchmark not regressing
  ([specs/failsafe-perf-report.md](../failsafe-perf-report.md)). This test is
  what stops 0.3 from regressing the feature it is meant to preserve.

### Acceptance

- Existing consensus suite green, unchanged, including `-race`.
- **Replacement differential:** for a fixed response multiset, every arrival
  permutation — including duplicates at equal and unequal rank — produces
  the same state as `newConsensusAnalysis` over the full slice, asserted **at
  every prefix**.
- Allocations per round drop; assert with a `testing.B` allocs/op benchmark.
- Consensus 3-of-5 throughput not worse, expected better; record the delta.
- **Both outstanding counters asserted independently:** after a cap fires under
  `fireAndForget`, `legalOutstanding == 0` while `physicalOutstanding > 0`, and
  the drain runs to completion off the caller's path. A single-counter
  implementation cannot satisfy this.
- **Leader parity:** with a leader that moves mid-round, decisions track the
  **live** leader exactly as `main` does — assert no delta. The injectable
  leader source is a seam only; a test that pins the schedule must produce the
  same decisions as one that lets the real source move identically.

---

## Phase 1 — Primitive vocabulary + tree execution (Go only)

No JavaScript. Build the primitives and the tree walker, and express today's
winner decision in them.

### 1.1 Primitives

- Partition + `eligible` filter + rank (§4.1), require (§4.2), guard (§4.3),
  outcome (§4.4), nestable ordered alternatives (§4.5).
- `eligible(pred)` filters **which groups may win** before ranking, distinct
  from `require`, which validates the group that won. Rule 2 needs the
  former (threshold before extremum); the two are not interchangeable and an
  early draft conflated them.
- `reject(kind, message?)` for **synthesized** errors — rule-specific text
  is part of parity — and `rejectWith()` for **passing through the candidate
  group's own error**, which rules 3, 4, 18 and 21 require and a synthesized
  reject cannot express. Error identity is asserted, not just error kind.
- `responseTypeIs(type)` for **exact** type checks (`nonEmpty`, `empty`,
  `consensusError`, `infrastructureError`) — the rules branch on precise
  types in both directions ([rules.go:308](../../consensus/rules.go)) and an
  error/non-error pair cannot express "empty at threshold while a non-empty
  exists". `exists(pred)` lifts any group predicate to a round guard, which
  covers rule 2's activation as `exists(extractableAt(paths))` and rule 21's
  scope as `eligible(responseTypeIs(infrastructureError))`.
- `groupCount(pred, cmp, n)` for **cardinality**, with `exists` as sugar for
  `>= 1`. Existence alone is insufficient: rule 11 needs *exactly one*
  non-empty group ([rules.go:403](../../consensus/rules.go)) and rule 14
  needs *more than one* group at threshold with explicitly unequal counts
  ([rules.go:578](../../consensus/rules.go)), which is not a tie. Note rule
  11's comment disagrees with its code ("at least one" vs `== 1`); the code
  is authoritative for parity.
- `partitionBy(valueAt(paths))` re-partitions results by extracted value;
  `atThreshold` and `requireTag` count **over the current partition**. It
  owns **two** of rule 2's six semantics — unextractable results excluded
  rather than pooled, and largest-by-size as the partition's representative
  response. The other four belong elsewhere by §4.1.1's distribution:
  activation to `exists(all(valid, extractableAt(paths)))`,
  threshold-before-extremum to `eligible(atThreshold(n))`, the distinct
  dispute text to `reject(kind, message)`, and the **valid-group scope** to
  an `eligible(valid)` applied *before* `partitionBy` — today both the
  activation and the partition read `getValidGroups()`
  ([rules.go:91](../../consensus/rules.go),
  [:117](../../consensus/rules.go)), so without it infrastructure errors
  enter the value partition. Folding all six into `partitionBy` would rebuild
  the bespoke rule this factoring exists to eliminate. Served body and error
  identity are part of parity, not incidental.
- `extractableAt(paths)` tests the group's **representative response**, not
  any member. Today's activation reads `group.LargestResult` alone
  ([rules.go:92](../../consensus/rules.go)) while the partition step that
  follows extracts from every member ([rules.go:122](../../consensus/rules.go));
  the two domains diverge on a group whose representative does not extract
  but whose members do, so the predicate must pin which it means.
- **Do not build a composition primitive.** `agreeingResults`
  ([executor.go:983](../../consensus/executor.go)) and
  `enforceWinnerComposition` ([executor.go:924](../../consensus/executor.go))
  stay mechanical and untouched (§7.1). `requireTag` exists for the graded
  use case in §12 only.
- **`whenFinality(bucket)`** as a runtime guard, not a compile key (§4.3.1).
  Consensus already carries `finality common.DataFinalityState`
  ([executor.go:43](../../consensus/executor.go)), so this is a read rather than
  new plumbing. It *could* safely be a compile key — a closed four-value enum,
  unlike `method` — and `selectionPolicy` already specialises that way
  (`EvalScopeNetworkMethodFinality`); the razor prefers one tree over four.
- **`callerHasRole(role)`** as a runtime guard, literal argument, set membership,
  O(1) (§6.5).
- Each primitive: Go, O(G) worst case, allocation-free on the steady path.
- Each rank term declares a stability class (§5.2) — recorded now, consumed
  in Phase 3.

### 1.2 The default tree

- Express the **24 winner rules** from
  [consensus/rules.go](../../consensus/rules.go) as one tree, in Go.
- The 3 short-circuit rules are **Phase 3**, not here.
- Ordering may differ where a rule's negative condition exists only to avoid
  overlapping a later rule (§11) — the behaviour must not.
- The tree replaces the **rule loop only**. `enforceWinnerComposition` keeps
  wrapping its output exactly as it wraps `rule.Action` today
  ([executor.go:886-896](../../consensus/executor.go)) — same call site, same
  three pass-throughs, unmoved (§7.1).

### Acceptance — **the gate for the whole feature**

- **Per-prefix winner differential.** Randomized round shapes (group counts ×
  response types × tags × leader presence × threshold × duplicates × value
  encodings such as `0x5`/`0x05`), each replayed under many arrival
  permutations, comparing tree vs. `determineWinner` **at every prefix** on
  decision and on error identity for pass-through errors.
- **Compare like with like.** `determineWinner` returns
  `enforceWinnerComposition(rule.Action(a))`, not the raw rule output
  ([executor.go:896](../../consensus/executor.go)), so the differential must
  compare `enforceWinnerComposition(tree(a))` against `determineWinner(a)` —
  the gate on both sides. Comparing a bare tree against a composition-wrapped
  `determineWinner` would report every quota-configured round as a
  divergence, and comparing a bare tree against a bare rule loop would leave
  the quota path untested. Include `requiredParticipants` shapes in the
  randomized corpus so the gate is actually exercised.
- Finality, short-circuit point and cancelled-slot comparison are **not**
  asserted here — Phase 3.
- **If a rule cannot be expressed, stop and report it** rather than adding a
  bespoke primitive to absorb it. The planned response is descope to
  grades-only, not a new primitive (see Build order rationale).
- No throughput regression vs. Phase 0.

---

## Phase 2 — Authoring layer: static validation, trace, and compile

The fork is closed (§13.2): the policy is **freeform JavaScript**. Everything in
this phase — the taint analysis, the subtype inference, the staged builder types,
the trace interrupt — exists to police a host language. The data-only fallback
(Appendix A) replaces 2.1 and 2.3's interrupt with schema validation if that ever
becomes the better trade.

### 2.1 Static validation (the guarantee)

- **`ctx` is config-derived only** (§3.2) — network id and resolved policy
  config, no runtime attributes. Assert the field set structurally, so a future
  field cannot silently reopen sink 2. This closes the sharpest hazard
  (`accept(ctx.method)` → attacker-controlled metric label) by making it
  unrepresentable.
- **Taint every policy expression — three sources, not one parameter:**
  1. recorder parameters, **including every nested callback receiver** (`r` in
     `.when(g, r => …)` is supplied by the tracer, not derived from `round`);
  2. **bare vocabulary constructors** — `absent("x")`, `atThreshold(2)`,
     `callerMayReceive("g")`, `mostCommon`, `all(...)` are free functions, so
     `const p = absent("x"); if (p)` freezes a branch under a root-only model;
  3. anything derived from either, via assignment, destructuring, property
     access, call results or container membership.
- Implement as a **type over the DSL's result**, not a reaching-definitions pass.
- **Sink 1 — control flow:** reject a policy expression reaching `if` / `while` /
  `for` / `switch` / `?:` / `&&` / `||` / `!` or a truthy return.
- **Sink 2 — arguments: three parameter kinds.** "Reject in any argument" is
  self-contradictory (it rejects `all(absent("x"), …)`), so the rule is
  positional:
  - **expression positions** take a policy expression of one declared subtype:
    `when`'s guard, `require`'s terms, `eligible`'s predicate, `rank`'s terms,
    `all`/`any`'s operands, `groupCount`'s group predicate, `earlyExit`'s
    predicate;
  - **literal/config positions** take a compile-time constant: `accept`'s grade,
    `earlyExit`'s reason, `atThreshold`'s `n`, `whenMethod`'s pattern,
    `groupCount`'s `cmp`/`n`, `valueAt`'s paths;
  - **branch callbacks** are function literals `r => Outcome` — the kind an
    earlier draft omitted entirely, leaving `.when`'s callback belonging to
    neither.
- **Subtypes are a family, not one symbolic type:** `GroupPred`, `Guard`,
  `EarlyExitPred`, `RankTerm`, `PartitionKey`, `Outcome`, `Builder`. `all`/`any`
  domain-uniform; `not` domain-preserving; `exists(GroupPred) → Guard` the sole
  lift. **No `Requirement` subtype** (it would make `atThreshold` untypeable) and
  **no `candidate(...)` lift** (circular in `when`) — candidate scope comes only
  from staged builder positions past `rank` (§4.6).
- Reject `eval`, `new Function`, dynamic `import()`, `with`.
- Reject when taint cannot be ruled out — dynamic property access, aliasing
  through a container, a call into an opaque helper. Rejection is the safe
  direction.

### 2.2 Recorder, staged builder, tree construction

- A `round` recorder capturing each primitive call, exposing the **staged**
  interface of §4.6 (Scoped → Ranked → Terminated) so `require`/`rejectWith`
  before `rank` and `earlyExit` before an outcome are unrepresentable rather
  than merely rejected.
- **All trees compiled eagerly during load/reload**, none on the request path. A
  validation or compile failure fails the reload and leaves the running config
  untouched. First boot has no running config: the process fails to start and
  the error names the offending policy (§13.3).
- Reuse the parse hook and sobek pool from
  [internal/policy](../../internal/policy) — at load only.

### 2.3 Bounded, interruptible trace — and an enforced tree ceiling

- **Wall-clock budget with a real interrupt.** `while (true) {}` passes every
  static check. Cut it off with `Runtime.Interrupt(v)` → `*InterruptedError`,
  then `ClearInterrupt()` (`github.com/grafana/sobek`,
  `runtime.go:1517-1533`), and **discard that runtime rather than returning it to
  the pool.** Budget per tree, with an aggregate ceiling.
- **Do not copy `Slot`'s timeout — it is soft.** It races the eval against
  `EvalTimeout` but on expiry still blocks on `<-done`
  ([slot.go:232-237](../../internal/policy/slot.go)), commenting that sobek
  "doesn't support interrupt mid-call cleanly". Wedging one background tick every
  15s is survivable; wedging a config reload is not.
- **Reject an oversized tree at load** — alternative count, nesting depth, total
  static cost. **Two numbers, one configurable:** a hard maximum calibrated by
  benchmark against the 24-closure walk and *not settable from config*, plus an
  operator ceiling that may only be lower (§8). Setting a ceiling above the hard
  maximum is a startup error, not a silent clamp.

### Acceptance

**Validator soundness (§3.1.3) — the deliverable, not a nicety.** This validator
has had a fresh hole found on every review pass, so per-pair rejection tests are
necessary and not sufficient:

- **Property test over generated policies:** generate eval functions from a
  grammar including the hazardous shapes (runtime values in tests and arguments,
  aliasing through containers and helpers, nested callback receivers, dynamic
  property access) and assert every *accepted* policy materializes a tree whose
  structure is independent of runtime state, with grades and reasons literal and
  drawn from the declared sets.
- **Two-trace differential oracle:** trace each accepted policy twice against
  recorders seeded with different fake runtime state; the trees must be
  structurally identical. A divergence *is* a validator hole, caught directly
  rather than by enumerating shapes.
- **Fuzz the analyser:** malformed and adversarial sources fail closed.
- **Coverage gate:** every taint source, sink class and subtype has an
  accepted-and-a-rejected case, asserted structurally.

Per-shape regressions, each of which must fail against the weaker implementation
it targets:

- **Truthiness, both spellings:** `if (round.absent(...))` **and**
  `const p = absent("x"); if (p)`. The second must fail against a validator that
  taints only the `round` parameter.
- **Callback receiver:** `.when(g, r => { if (r.capFired()) … })` rejected.
- **Argument sink:** with `p = round.partitionBy(...)`, `accept(p.gradeName)`,
  `whenMethod(p.something)` and `atThreshold(p)` each rejected.
- **`ctx` surface:** `ctx.method`, `ctx.user` and any runtime attribute are
  **absent**, not merely rejected — one test asserting the field set.
- **Cross-domain, per pair:** `.when(mostCommon())`, `.rank(absent("x"))`,
  `.require(nonEmptyFirst())`, `.eligible(absent("x"))`,
  `all(mostCommon(), absent("x"))` — each rejected, naming expected and actual
  subtype. Must fail against a single symbolic type.
- **Staged builder, per illegal transition:** `require` before `rank`,
  `rejectWith` before `rank`, `earlyExit` before an outcome, a branch callback
  that never terminates.
- **Legal-position corpus:** a correctly-subtyped expression in every declared
  position is *accepted* — the discipline must not reject the vocabulary it
  serves.
- **Vocabulary completeness:** every constructor used anywhere in the spec
  appears in a subtype table (`always`, `first`, `not` were once used in
  examples and absent from them).

Trace and cost:

- **Divergence test:** `while (true) {}` fails the reload within budget and the
  running config still serves. Must fail against a soft-timeout implementation.
- **Runtime hygiene:** an interrupted runtime is never handed back by the pool.
- **Ceiling test:** a config-derived loop exceeding the limit is rejected with
  both numbers; one below is accepted and its reported static cost matches a
  measured benchmark. A ceiling set above the hard maximum is a startup error.
- **Aggregate test:** N networks each just inside the per-tree budget still fail
  if their total exceeds the aggregate ceiling.
- A JS policy reconstructing the Phase 1 default tree compiles to a structurally
  identical tree, compared by dump.
- **Zero compilations and zero sobek checkouts in a steady-state benchmark**,
  asserted by counter; a method-flood workload must not allocate a new tree.
- **Grade- and reason-label cardinality** bounded by their declared sets under
  that flood.

## Phase 3 — Finality, short-circuit, wait caps

### 3.1 Soundness fold

- Implement the §5.2 fold as **two symmetric clauses**, not per-primitive
  booleans. The asymmetric version is the trap, and it looks correct:
  - **earlier alternatives** — could one *produce an outcome*? Guard becoming
    true **and** `require` becoming satisfied. Guard alone is insufficient:
    §12's earlier alternative is guarded `always`, so only its `require` can
    change.
  - **the firing alternative** — could it *stop producing this outcome*?
    Guard becoming false, **or** `require` failing, **or** candidate identity
    changing. `require` alone is insufficient: `absent(tag)`,
    `participantsBelow(n)` and `groupCount(pred, "==", n)` are all falsifiable
    by a response that never touches the candidate.
- **Regression per missing half, both using §12's shape.** A fold checking
  earlier *guards* + firing *requires* — the plausible wrong implementation —
  must fail both: (a) firing-guard clause, where a pending slot whose
  possible-tag-set contains `type:internal` must force `wait()` even though
  the candidate is untouched; (b) earlier-require clause, where the
  `always`-guarded `standard` alternative must force `wait()` because its
  `requireTag` can still start passing.
- Pending slots contribute a **possible-tag-set**, because slot→upstream
  binding is dynamic ([executor.go:244](../../consensus/executor.go),
  [network_executor.go:249](../../erpc/network_executor.go)).
- **Its construction is specified, and anchored to the right abstraction
  (§5.1).** The source is the **post-reorder request list** —
  `originalReq.SetUpstreams(reordered)` installs it before any slot spawns
  ([executor.go:139-142](../../consensus/executor.go)) and every
  `NextUpstream()` draws only from it
  ([request.go:1312](../../common/request.go)). Slots do not consult the
  selector, so "snapshot the selector" was the wrong instruction.
- Take the snapshot **once at round start**, union tags into a fixed bitset,
  read that bitset during evaluation. One set per round, not per slot.
- **Filter only by immutable directives** (e.g. `UseUpstream`). Do **not** fold
  in consumed-state or per-attempt error gates: those narrow as the round
  proceeds, and baking them in makes the set shrink under the fold.
- **`Upstreams()` allocates a copy per call**
  ([request.go:1157-1168](../../common/request.go)), so add a **no-copy
  iteration API** and take the snapshot through it. Calling `Upstreams()` per
  evaluation is the C4 violation this contract exists to prevent.
- Acceptance: a steady-state benchmark shows **zero allocations** attributable
  to tag-set evaluation however many times the round evaluates; and §12's case
  still waits when the snapshot contains `type:internal`.
- `largest()` / `highestAt()` are never sound while a completion remains;
  assert this reproduces rule 27's existing refusals.
- **Every `Guard` and `GroupPred` declares `mayBecomeTrue`/`mayBecomeFalse`
  (§5.2), not just rank terms.** The fold's two questions are unanswerable
  otherwise. Vote replacement is what makes the table non-obvious: `atThreshold`
  is **not** monotonic (a replacement can remove a vote) and `extractableAt` can
  change when the representative is replaced — both look monotonic and would be
  if the round were append-only. `groupCount` with `==` moves both ways.
  Compile-time coverage assertion: a predicate missing either function does not
  build. Regression per non-obvious row.
- **Every rank term declares a stability class — the table is exhaustive by
  construction.** `fromLeader` was absent from an earlier draft's table and so
  had no soundness model at all despite reading state outside the round. It
  declares itself **unstable while any completion remains**, alongside
  `largest()`/`highestAt()`; the leader read stays live (§5.2). Add a
  compile-time assertion that every rank term has a declared class, so a new
  term cannot be added without one.
- **Decision equality, not payload equality** (§5). Assert the invariant field
  list: winning partition identity, outcome kind, grade, and error identity for
  `rejectWith`. Assert the exclusions just as explicitly, since getting these
  wrong in the *strict* direction converts every policy to wait-all: a
  candidate whose representative is replaced by a larger same-hash arrival is
  **still sound**, and a synthesized `reject` whose `participants()` list grew
  is **still sound**. Regression per exclusion, each of which must fail against
  a payload-stable fold.
- **Error identity is the normalized grouping hash**, not the error object —
  `jsonrpc:<NormalizedCode>`, else the `StandardError` base code, else
  `error:generic` ([analysis.go:442-457](../../consensus/analysis.go)). Named
  regression: two upstreams returning the **same normalized code with different
  message text** must still be sound. Defining equality over the whole error
  object restores wait-all through the back door.
- **Coarse partitions may not exit early.** When the winning partition's key is
  coarser than hash identity — i.e. any `partitionBy` — members can differ in
  *any* non-key field, so the decision is not sound until
  `legalOutstanding == 0`. This is parity, not a new restriction: rule 27
  already refuses to short-circuit under `preferHighestValueFor`
  ([rules.go:893](../../consensus/rules.go)). Regression: a
  `partitionBy(valueAt(paths)).rank(mostCommon())` policy does **not** exit
  early while completions remain.
- There is **no** payload-stability knob to test — it was removed, not deferred
  (§5). A "required-or-optional" facility that no acceptance criterion pins down
  is exactly the unexercised machinery the razor forbids.

### 3.2 The two bounds (§5.3)

Both live in **`mostCommon()`'s stability class** (§5.2) — the only rank term
for which a lead bound is meaningful. `nonEmptyFirst()` is provable without
one; `largest()` and `highestAt()` are never provable while a slot is
pending, so they declare themselves unstable rather than carrying a bound.

- **Two different `remaining`s — do not share one (§5.3).** Today's
  `maxParticipants - collectedResponses` ([rules.go:951](../../consensus/rules.go))
  counts against `len(responses)` ([analysis.go:96](../../consensus/analysis.go)),
  which only grows for **non-nil** results: the collector `continue`s on nil
  ([executor.go:528](../../consensus/executor.go)) while the loop index
  advances, and two paths send nil — cancel-before-execution
  ([executor.go:776](../../consensus/executor.go)) and a slot returning
  neither response nor error ([executor.go:793](../../consensus/executor.go)).
  So it **overstates** live slots and can be `> 0` after every participant has
  finished.
  - **Soundness fold and legal-completion sets use `legalOutstanding`** — from
    the Phase 0.2 counters, *not* `physicalOutstanding` and not the raw
    formula. Using the raw formula here is a *liveness* bug, not a conservative
    one: the fold sees a phantom slot, returns `wait()`, and nothing arrives
    (§5.6).
  - **The drain uses `physicalOutstanding`**, which stays positive after cap
    fire under `fireAndForget` by design (§5.3).
  - **Translated `.earlyExit` keeps the raw formula**, bug-compatible on
    purpose, so no deployment's short-circuit timing shifts. For rule 27 the
    over-count is conservative — a missed short-circuit, never a wrong one.
  - Regression: a round where a slot sends nil must still resolve. Assert
    `legalOutstanding` reaches zero and the fold proves soundness; it must fail
    against an implementation that reuses `collectedResponses` for the fold.
- **Sound:** `lead > remaining + replaceable(candidate)` — strict.
  `replaceable` is zero exactly when the candidate holds only non-empty
  successes (they are never freed by `canReUse`), otherwise it counts the
  votes a pending slot could take away, bounded by `remaining`.
- **Early exit:** `lead >= remaining` — rule 27's bound. This is **not** the
  sound bound; the equality case is exactly the tie race, and it is
  available only under `.earlyExit(...)`.
- Regressions, one per failure direction: a candidate at `lead == remaining`
  must **not** be served by a sound policy (it may complete as a tie →
  dispute); and a policy whose candidate is an **empty** group, with a
  pending slot able to reselect the empty-voting upstream, must not
  short-circuit at `lead == remaining + 0`.

### 3.3 Early exit and the three short-circuit rules

- Implement `.earlyExit(reason, predicate)` as a modifier on **any** terminating
  outcome (§4.4), not a field on `accept` — one of the three shortcuts
  terminates on a **pass-through error**
  ([rules.go:880](../../consensus/rules.go)).
- Port each shortcut with its **complete positive predicate**, not a shared
  bound plus exceptions — they have neither the same outcome type nor the
  same condition: tx-broadcast exits on the **first** non-empty response
  (no threshold, no lead); the consensus-error shortcut exits at threshold
  with **no lead requirement**; only `unassailable_lead` uses
  `lead >= remaining`.
- Port each rule's own **suppressions** and its `Reason` string
  (`sendrawtx_first_success`, `consensus_error_threshold`,
  `unassailable_lead`) — the latter is surfaced in telemetry and is part of
  observability parity.
- **Suppressions do not all have the same scope.**
  `consensus_error_threshold`'s are unconditional
  ([rules.go:893](../../consensus/rules.go));
  `unassailable_lead`'s three are wrapped in `if a.hasRemaining()`
  ([rules.go:918](../../consensus/rules.go)), so once nothing is outstanding
  they are skipped and `remaining <= 0` makes the lead test trivially true —
  the rule fires on the final response **even under
  `preferLargerResponses`**. Translate as
  `whenInFlight() ⇒ (…suppressions…)`, not a bare conjunction, and add a
  parity assertion for that last-response case: the round ends either way,
  but `shortCircuited` becomes true, which is an operator-visible metric
  label ([executor.go:610](../../consensus/executor.go), [:621](../../consensus/executor.go)) and selects a
  different `markWinningParticipants` branch.
- **Parity is asserted against the early-exit form**, which the translator
  emits for existing configs — today's timing preserved exactly.
- The sound form is asserted **separately, per shortcut** — three pairs, not
  one. Only `unassailable_lead` diverges "at the tie race" (A=2, B=1,
  threshold 2, one pending → early exit serves A; sound waits and may
  dispute). The other two differ by a whole predicate: tx-broadcast exits on
  the first non-empty where sound waits for the round; consensus-error
  exits at threshold where sound requires the lead to be unassailable.
  Both forms are tested; neither is asserted of the other.

### 3.4 Wait caps — mechanical, unchanged

- The timer, its arming, **and the quota-aware arming gate** all stay
  mechanical and behaviourally unchanged. The gate withholds arming until
  collected responses cover every quota tag with distinct upstreams
  ([executor.go:473](../../consensus/executor.go)); an earlier draft
  proposed retiring it, which would change behaviour, because a guard
  evaluated after the timer fires cannot reconstruct an interval that must
  *begin* when quotas become satisfied. Preserve `isNoAttemptResult`
  suppression and the `maxWaitOnEmpty`-arms / `maxWaitOnResult`-tightens
  split as-is.
- Add the `capFired()` guard, so the policy decides what a capped round
  resolves to — and with it the **§5.5 lifecycle**, which does not exist
  today: the collection loop currently cancels and `break`s without
  re-deciding ([executor.go:509-527](../../consensus/executor.go)), so the
  guard would never be observed. The exact transition:
  1. set `capFired`;
  2. drop **`legalOutstanding`** (3.2) and every possible-tag-set to empty, so
     outstanding slots stop being legal completions. **Leave
     `physicalOutstanding` untouched** — under `fireAndForget` those slots keep
     running and step 7 still drains them. This closes resolution, not
     execution;
  3. cancel outstanding slots unless `fireAndForget` — unchanged;
  4. evaluate the tree **exactly once** on every path, `capFired()` true and
     `whenInFlight()` false. Note the no-response case already evaluates
     post-loop today (`analysis == nil` →
     [executor.go:577-580](../../consensus/executor.go)); this step replaces
     that evaluation rather than adding a second one;
  5. that evaluation must terminate. With the completion set empty the fold
     reports everything sound, so a well-formed tree terminates; if one still
     yields `wait()`, synthesize the round's default failure and emit a
     metric rather than hang. **This backstop is not cap-specific** — it fires
     whenever the completion set is empty, including normal exhaustion (§5.6);
  6. **`sendOutcomeOnce` — publish before draining.** Draining first moves the
     caller's outcome from cap-fire time to last-participant time and converts
     the wait cap into wait-all (0.3);
  7. **then keep draining `responseChan` and releasing late bodies** without
     touching the frozen analysis — **already delivered by Phase 0.3**, which
     fixes it as a standalone bug rather than inheriting it. Confirm the
     lifecycle preserves it; do not re-implement.

### Acceptance additions for 3.4

- Timing-parity test: for configs with `requiredParticipants` quotas, the
  cap arms at the same response index as today, and rounds that resolve on
  a cap resolve at the same wall-clock offset.
- **Termination tests, one per way the completion set empties (§5.6).** The
  backstop is conditioned on "legal-completion set is empty", not on the cap,
  so assert both: (a) a policy whose `capFired()` branch returns `wait()`
  resolves via the backstop and increments the metric; (b) a policy that
  returns `wait()` after the **final ordinary slot** on a round with **no cap
  configured** does the same. Neither may hang. (a) must fail against an
  implementation omitting lifecycle step 2 or 5; (b) must fail against one
  that scopes the backstop to the cap path — which is what the earlier draft
  specified — **and** against one that reuses `collectedResponses` for the
  fold, since the phantom-slot over-count (3.2) is what makes (b) reachable.
- **Outcome-before-drain test:** on cap fire the caller receives its outcome
  at the cap deadline even when a straggler completes much later — the same
  assertion as Phase 0.3's cap-latency regression, re-run against the full
  lifecycle.
- **Late-body release test:** landed in Phase 0.3; re-run here to confirm the
  lifecycle did not regress it.

### Acceptance

- **Named case from §12:** two third parties agree while a slower
  first-party upstream is in flight → the round waits and serves
  `standard`, never the relaxed grade. Must fail without the fold — **and
  must fail against a fold missing either half** (3.1), since this case
  depends on the firing alternative's guard being falsifiable *and* an
  earlier `always`-guarded alternative's `require` still being reachable.
- Short-circuit parity (early-exit form) across the suite at the same
  response count, under every arrival permutation, plus cancelled-slot
  identity.
- **No round is ever published with a non-final verdict**, whatever emptied
  the completion set — cap fire or normal exhaustion (§5.6). Assert the
  invariant "completion set empty iff `legalOutstanding` is zero" directly —
  *not* over `physicalOutstanding`, which stays positive at a capped
  `fireAndForget` round by design (§5.3) —
  since the raw-`remaining` over-count (3.2) is what makes the exhaustion case
  reachable at all. No cap that fails to fire.

---

## Phase 4 — Grades, authorization, observability

### 4.1 Grant plumbing

- Add a grade-grant capability field to
  [`common.User`](../../common/user.go), following the
  `AllowClientDirectives` invariant verbatim: populated **only** by auth
  strategies, so `trustUserIdHeader` can never widen it (§6.1).
- **"A named claim/column value" describes JWT and nothing else.** The five
  strategies have genuinely different identity shapes, and each already carries
  a `RateLimitBudget` grant whose *shape is the template to copy* (§6.2):

  | Strategy | Trusted input | Grant form | Existing precedent |
  |---|---|---|---|
  | `secret` | one configured identity per entry | `grades: [...]` **on the strategy entry** — static, enumerable | `SecretStrategyConfig.RateLimitBudget` ([config.go:2808](../../common/config.go)) |
  | `network` | client IP / CIDR membership | `grades` on the entry (optionally per-CIDR) | `NetworkStrategyConfig.RateLimitBudget` |
  | `siwe` | recovered address, allowed domain | `grades` on the entry | `SiweStrategyConfig.RateLimitBudget` |
  | `jwt` | a named claim's value | `gradesClaimName` + declared finite value→grade-set map | `RateLimitBudgetClaimName` ([strategy_jwt.go:123-132](../../auth/strategy_jwt.go)) |
  | `database` | a column on a **runtime row** | declared finite column-value→grade-set map **in config** | — |

  For secret/network/siwe this is a static field, not a mapping: those
  strategies have one configured identity per entry, so "enumerable at load"
  is trivially satisfied. Only JWT and database need a value→set map.
- **Database is the hard case and must fail closed.** Rows are runtime data and
  are **not** enumerable at startup, so §6.2's coverage validation cannot see
  them. Resolution: the *mapping* is config (and therefore enumerable) even
  though the *values* are not, and **any column value with no declared mapping
  yields the `default` grade** — never a non-default one. Same for an absent
  column, a null, or a lookup error. State it explicitly because this is the
  one place a bug silently *widens* a grant.
- **No trusted input may be introduced that a request can influence.** All five
  read strategy-validated state only, preserving the `AllowClientDirectives`
  invariant (§6.1).
- **Retain roles as a second capability field**, same invariant as grades:
  populated only by auth strategies, so `trustUserIdHeader` can never grant one.
  Per strategy, mirroring each one's `RateLimitBudget` shape — static `roles` on
  `secret`/`network`/`siwe`, `rolesClaimName` on `jwt`, a config-declared column
  mapping on `database` failing closed to no roles (§6.5).
- **Roles get no coverage cross-validation** (unlike grades, 4.2): a policy may
  test a role no strategy grants yet — staged rollout, or an IdP-granted role
  outside this config. Unmatched is a fallthrough, not a startup error.
- **Roles never reach the metrics pipeline** — role values come from claim data,
  so labelling by role reintroduces the unbounded-cardinality hazard §3.1 closes
  for grades. Assert it: a test that no metric carries a role label.
- Exactly one declared grade is marked `default` when a policy declares more
  than one; there is no inferred strictness ordering (§6.3).

### 4.2 Startup validation

- A mapping naming a grade no policy produces → error.
- A **non-default** policy grade that no mapping covers → error. The
  `default` grade is exempt: it is the unmatched-caller fallback, so
  requiring a mapping for it would reject every valid multi-grade config
  (§6.2).
- A multi-grade policy with **no** `default` → error.
- A multi-grade policy with **more than one** `default` → error. Exactly one
  is required; "several defaults" has no meaning and must not silently pick
  one.

### 4.3 Enforcement and observability

- Engine-enforced gate: a grade the caller may not receive is **withheld as
  a dispute, never downgraded** (§6.4).
- `callerMayReceive(grade)` guard as a runtime attribute — set membership,
  O(1), no effect on compile-key cardinality.
- Decision records (candidate, partition, grade, alternative fired, finality
  verdict and why) — **sampled into a preallocated ring, not built per
  round.** `internal/policy/decision.go` is a good shape but is built once per
  15s tick and carries maps and slices; one per consensus round is ~350k
  allocations/s on the request path, against C4. Default sampling low,
  unconditional capture only for already-exceptional rounds (§5.6 backstop
  fired, grade withheld, ceiling rejection), full rate as a documented debug
  mode like `Engine.SetStepLogEnabled`.
- Expose records and the static cost report as methods on the **existing admin
  RPC registry** (`erpc_*` in [erpc/admin.go](../../erpc/admin.go)) — not a new
  endpoint, which §14 rules out and an earlier draft contradicted.
- Acceptance: steady-state allocs/op for a round with sampling at its default
  is unchanged from Phase 0 — assert by benchmark, so the record path cannot
  drift onto the hot path.

### Acceptance

- A caller restricted to the strict grade receives a dispute — not a relaxed
  answer — on a round only the relaxed grade satisfies.
- The same caller with the policy-visible guard fails **fast**, asserted on
  elapsed response count.
- Grade cannot be widened by any request header, including
  `trustUserIdHeader` — one test per strategy.
- Single-grade policies require no declaration and behave as today.

---

## Phase 5 — Config translator

- Map existing knobs per §9, including `preferHighestValueFor` → the full
  §4.1.1 composition (`exists(all(valid, extractableAt(paths)))` guard +
  `eligible(valid)` + `partitionBy` + `eligible(atThreshold)` +
  `rank(highestAt)` + message-bearing reject).
- **`requiredParticipants[].minAgreement` is NOT translated.** It stays the
  mechanical post-decision gate (§7.1), wrapping the tree's output at the
  same call site it wraps `rule.Action` today. Emitting
  `require(requireTag(...))` for it would silently change failure from
  "dispute" to "fall through to the next alternative".
- Emit `.earlyExit(reason, pred)` **per shortcut, on the outcome that shortcut
  produces** — including the pass-through-error one, which is not an
  `accept` — each with its own complete positive predicate and suppressions
  (§4.4). Not a blanket `earlyExit` on every translated accept.
- A config setting none of them gets the Phase 1 default tree.
- Both legacy knobs and an explicit policy on one block → startup error.
- **No deprecation warning** — the old shape is supported.

### Acceptance

- Golden fixtures: for each realistic legacy config shape, translated
  behaviour identical to today across the full suite, under
  prefix/permutation replay, including value-encoding cases.
- Round-trip: translated config → tree → dump stable across reloads.

---

## Phase 5.5 — Production shadow compare, then cutover

§1 leaves one runtime decision path, so there is **no runtime kill-switch back
to legacy** — cutover is reverted by binary redeploy. For a change touching
consensus on every chain and vendor, the offline differential is necessary and
not sufficient. Implements §9.1.

- **Build-flag gate** the engine; legacy serves.
- **Shadow compare in production:** both decide per round from the *same*
  collected responses (no extra upstream traffic); legacy's outcome is served;
  divergences recorded with the full decision record (§10) and alerted.
- **Canary** on a small set of networks/projects, still comparing.
- **Cutover**, then delete legacy no sooner than one release later, so the
  redeploy revert path stays real.

### Acceptance

- Shadow divergence rate zero-or-explained over a stated traffic volume and
  window, per network class. Every divergence is fixed or recorded as intended.
- The §7.1 surfaces are covered here specifically — a green rule-level
  differential says nothing about them, and shadow traffic is the only place
  they are exercised against real vendor behaviour.
- Shadow mode's added cost measured and within the §8 ceiling.
- Alerting wired for the three bug-signals before canary: §5.6 backstop fired,
  §6.4 grade withheld, ceiling rejection at reload — each with a runbook entry
  (what on-call does, how to revert a bad policy vs a bad binary, how to read a
  decision record to explain why a round served a given grade).

## Phase 6 — Documentation

- `docs/pages/config/failsafe/consensus.mdx`: policy surface, primitive
  reference, the two validation sink classes and their rejection messages,
  partitions, grades and grants, and the sound/early-exit distinction —
  `<AISection>` schema table updated with defaults cited to code. Document the
  **two validation sink classes and their rejection messages** — operators will
  meet them, and a conservative rejection with no explanation reads as a bug.
- State the **engine boundary** plainly: what a policy decides and what stays
  mechanical (hash identity, wait-cap timing and arming, participant
  selection, winner composition). The `minAgreement`-vs-`requireTag`
  distinction needs saying out loud — they look interchangeable and are not
  (§7.1).
- Migration note: existing configs need no change and do not change timing.

---

## Cross-cutting gates

| Gate | Check |
|---|---|
| Throughput | consensus 3-of-5, no regression vs. the previous phase |
| **Per-eval perf gate (merge blocker)** | per-evaluation ns/op **and** allocs/op against a recorded baseline, enforcing §1's "at or below the 24-closure walk" bound on every change — a bound nothing measures is an aspiration (§11.1) |
| Allocation | allocs/op per round non-increasing after Phase 0 |
| Race | `make test` (race) green |
| Temporal equivalence | prefix/permutation differential vs. the rules it replaces, green until that code is deleted |
| Nothing on the request path | steady-state sobek-checkout **and** tree-compilation counters are zero, including under a method flood |
| Bounded labels | grade **and early-exit-reason** metric cardinality ≤ their declared sets |
| Composition unchanged | `enforceWinnerComposition` and its three pass-throughs byte-identical to `main`; quota-configured shapes in every differential corpus (§7.1) |
| `ctx` surface | `ctx`'s field set asserted; no runtime attribute reachable from a policy (§3.2) |
| Bounded compilation | every tree within the alternative/depth/static-cost ceilings, with the hard maximum not settable from config; trace budget enforced by a real interrupt, interrupted runtimes discarded (§3.3, §8) |
| Validator soundness | property test over generated policies **and** the two-trace differential oracle green; coverage gate over every taint source, sink and subtype (§3.1.3) |
| Expression subtyping | cross-domain rejection per pair, plus the post-trace structural check (2.1) |
| Stability coverage | every rank term declares a stability class, asserted at compile time (3.1) |
| Fold symmetry | §12's case fails against a fold missing *either* half — earlier-`require` or firing-`guard` (3.1) |
| Termination | no round published with a non-final verdict, from cap fire **or** normal exhaustion; backstop metric zero in steady state (§5.6) |
| Cap latency | wait-capped rounds resolve at the cap deadline, not at straggler completion (0.3) |
| Allocation, observability included | allocs/op per round unchanged with decision-record sampling at its default (4.3) |

## Risk register

| Risk | Signal | Response |
|---|---|---|
| **The validator accepts a policy that miscompiles or leaks a label** — the design's most defect-prone component | Two-trace oracle finds structurally different trees, or the property test finds an accepted runtime-varying policy. Historically: a fresh hole on every review pass | §3.1.3 makes soundness a deliverable — generated-policy property test, two-trace oracle, analyser fuzzing, structural coverage gate. **If this proves unsustainable, Appendix A is the designed-out fallback** and switching changes neither runtime nor vocabulary |
| A valid policy compiles to a tree too large to evaluate per response, or the trace never terminates | Phase 2.3 ceiling and divergence tests, or a static cost report far above the default tree's | Enforced ceilings with a non-configurable hard maximum, plus an interruptible trace budget (§3.3, §8) — both reachable from a policy that passes every static check, because host control flow over config is deliberately unrestricted |
| A round hangs because the fold sees completions that cannot arrive | Termination test (b) — `wait()` after the final ordinary slot with no cap — or a non-zero backstop metric in production | Soundness reads `legalOutstanding`, never `collectedResponses` (3.2); the backstop is conditioned on an empty completion set, not on cap fire (§5.6) |
| Physical work and legal completions collapse into one counter | The §5.6 invariant cannot be asserted at a capped `fireAndForget` round, or the drain stops early and leaks bodies | Two counters from Phase 0.2; the drain reads `physicalOutstanding`, the fold reads `legalOutstanding` (§5.3) |
| Soundness is defined too strictly and every policy silently becomes wait-all | Sound early acceptance never fires in the suite; §5.4's `>` vs `>=` divergence stops being observable | Equality is over the decision, not the payload, with the invariant field list and its exclusions both asserted (§5). Payload-stability was removed rather than left as an untested opt-in |
| Soundness is defined too loosely and a served body is not what the round agreed | A served response differs outside the operator's declared equivalence class | Under hash identity the exclusions are exactly `ignoreFields`/size/participant-list/error-message. Under a **coarser** `partitionBy` key they are not bounded that way, so such partitions cannot exit early at all (§5) — matching rule 27's existing refusal |
| A candidate-scoped predicate leaks into a `.when` guard | `.when(candidate(...))` typechecks, or a guard references post-ranking state | Candidate scope exists only in builder positions past ranking (`require`, `.earlyExit`); there is no `candidate` lift (§3.1) |
| Tag-set evaluation puts the selector back on the hot path | Non-zero selector invocations or allocations in the steady-state benchmark | Round-start snapshot into a fixed bitset, read-only during evaluation (3.1) |
| A rank term ships without a stability class | Review finds a term absent from §5.2's table, as `fromLeader` was | Compile-time assertion that every term declares one; `fromLeader` declares itself unstable while completions remain, with the live leader read unchanged (§5.2) |
| Phase 0.3 regresses the wait cap it exists to preserve | Cap-latency regression: caller resolves at straggler completion instead of the cap deadline | Publish the outcome at cap fire, drain afterwards off the caller's path (0.3) |
| Observability drifts onto the hot path | allocs/op per round rises after Phase 4 | Records are sampled into a preallocated ring with an allocation benchmark as a gate (4.3) |
| Factoring does not cover all 24 rules | Phase 1 differential diverges | Report the irreducible rule; do not add a bespoke primitive to hide it |
| Partition semantics leak further than `preferHighestValueFor` | Phase 1 finds another rule needing a different partition key | Keep `partitionBy` narrow (§13) until a second real use appears |
| A stability class inherits a bound without its precondition, or the constant without its strictness | Phase 3.2 regressions: an empty-group candidate short-circuits too early, or a sound policy serves at `lead == remaining` | Bounds are derived from the candidate and strict for sound acceptance (§5.3); `>=` exists only under `.earlyExit(...)` |
| A boundary row claims policy ownership the vocabulary or the runtime cannot deliver | Review finds a §7 row with no primitive, or one that needs an event the policy never receives | **Four** occurrences so far (punishment targeting, wait-cap arming twice, winner composition). Every §7 "policy" row must name the primitive that expresses it **and** the event that triggers its evaluation |
| Decision logic exists outside the 24 rules and the 3 shortcuts, so a green Phase 1 differential proves less than it appears to | Review finds a decision path in `executor.go` that neither rule list contains | Two found (`enforceWinnerComposition`, the provisional-dispute suppression), both kept mechanical (§7.1). Before Phase 1's gate is declared met, re-walk `executor.go`'s decision path end to end and list every branch that is not in either rule list |
| The engine is not warranted — §12 alone is | §13's first open question resolves to "one use case", or Phase 1's differential exposes an irreducible rule | Descope to grades-only: Phase 0 + Phase 4 against the existing 24 rules. This is a planned branch, not a failure — the build order names it explicitly |
| Sound-by-default is the wrong default for new policies | Review or early adopters report latency surprise | The choice is per-outcome via `.earlyExit(reason, pred)`, so it is a documentation and default change, not an architecture change (§13) |
| Grant plumbing is larger than the engine | Phase 4 touches every auth strategy | Phase 4 is separable — grades can ship single-grade with no gate, deferring grants entirely |

## Out of scope / deferred

- **Interpreted per-round fallback** for non-traceable policies (§13).
- **Policy-defined hash identity** — partitioning is a decision-time view
  only (§7).
- **Policy-driven participant selection** — overlaps `selectionPolicy`.
- **Bespoke punishment targeting** — mechanical and grade-aware suffices.
- **Policy-owned winner composition** — `minAgreement` stays a mechanical
  post-decision gate; a tree-level post-outcome operator is not built (§7.1).
- **Runtime attributes on `ctx`** — runtime state is reached only through
  `round` primitives (§3.2).
- **Role-based *authorization*** — roles route, they never gate; grades stay the
  only engine-enforced capability (§6.5).
- **Policy-owned cap arming** — the timer is an event source (§7).

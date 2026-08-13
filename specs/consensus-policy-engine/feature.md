# Consensus Policy Engine — Specification

> Decompose the consensus decision into a small set of orthogonal base
> primitives and let operators compose them freely — the way `selectionPolicy`
> works — while keeping **zero JavaScript on the request path**. Freeform JS
> runs once at config load and is traced into a finite tree of Go primitives;
> that tree is walked in Go per request, on every response arrival, against
> that round's real votes.

## Executive summary

**Problem.** Two things are wrong with consensus today, and only one of them is
a capability gap.

1. **A capability gap.** An operator cannot express "serve under a strict
   acceptance level when the first-party node participates, and a named
   relaxed level when it is *absent* — but never when it merely dissents or is
   still in flight." Today's four behaviour enums and five preference knobs
   cannot express it at any setting (§12).
2. **A maintainability problem.** The decision is 24 priority-ordered rules
   plus 3 short-circuit rules, entangled with those enums and knobs. Rules 3
   and 5 both re-derive "no valid group meets threshold" purely to avoid
   stealing rule 19's case, so the list is ordered but **not re-orderable**
   (§11).

**Proposal.** Replace the rule list with a policy document over orthogonal
primitives — partition, eligibility filter, rank, require, guard, outcome, and
nestable ordered alternatives — compiled at load into a finite Go tree. Today's
behaviour ships as a translated default policy, so no deployment changes
behaviour or timing (§9).

**Cost.** Five phases (plan.md). Two places concentrate the risk: the finality
model (§5), which is the subtle part of the decision logic; and the static
validator (§3.1), which is the price of a host language on the authoring surface
and has been this design's most defect-prone component — §3.1.3 therefore makes
its soundness a shipped deliverable rather than an assumption. Grade
authorization (§6) is separable and can ship later.

**Decisions taken.** Two forks that earlier drafts left open are now closed:

| Decision | Choice | Where |
|---|---|---|
| Authoring surface | **Freeform JavaScript** with static validation; a data-only document is the designed-out fallback | §3, §13.2, Appendix A |
| Justification under the repo razor | **Extension** — three named cases (§12 graded acceptance, §6.5 per-audience, §4.3.1 per-finality), each adding an orthogonal axis a priority list multiplies | §13.1 |

**Decision still needed from readers.** Whether §13.1's three named extensions
justify the scope. The weaker alternative — grade capability plus one acceptance
rule against the existing 24 — delivers extension 1 only, and is designed out in
§13.1 rather than merely named.

**Blast radius.** Consensus runs for every chain and vendor, and §1 leaves
exactly one runtime decision path, so there is no runtime kill-switch back to
the legacy rules. §9.1 specifies the rollout: shadow-compare in production
before cutover, and revert by binary redeploy. Treat legacy-path deletion as a
one-way door.

## 1. Goals & non-negotiable constraints

- **Simplify to base primitives.** Today's decision is 24 priority-ordered
  Go rules plus 3 short-circuit rules ([consensus/rules.go](../../consensus/rules.go)),
  entangled with four behaviour enums and five preference knobs. Replace
  that with a handful of orthogonal primitives that compose.
- **Freeform policy.** An operator expresses acceptance and preference
  logic in whatever shape their deployment needs, rather than selecting
  from `disputeBehavior` / `lowParticipantsBehavior` enum values.
- **Realtime.** The decision is made **per request, re-evaluated on every
  response arrival** — `determineWinner` is called inside the collection
  loop ([consensus/executor.go:546](../../consensus/executor.go)), followed
  immediately by `shouldShortCircuit` (`:547`). Consensus 3-of-5 benches at
  ~350k rounds/s, five evaluations per round, so ~1.75M decision
  evaluations/second ([specs/failsafe-perf-report.md](../failsafe-perf-report.md)).
  That figure is **machine-aggregate throughput across 256 concurrent
  goroutines**, not a per-core budget, so it sizes the problem rather than
  setting the bound. **The bound this design must meet is stated
  directly:** per-evaluation cost at or below the 24-closure walk over a
  rebuilt analysis it replaces (§8). Nothing in the design may put an
  interpreter, a compiler, or an unbounded cache on that path.
- **Backward compatible at the config layer only.** Existing configs keep
  working via a load-time translation. No legacy-aware branching survives
  past config load; there is exactly one runtime decision path.
- **Weakest hypothesis** (binding repo razor, [CLAUDE.md](../../CLAUDE.md)).
  Prefer the design committing least beyond observed cases; weaken by
  deleting structure. Concretely, this rules out adding an enum of
  activation predicates when a predicate primitive expresses the same thing
  without the enum.

## 2. Why the selection-policy execution model does not transfer

The engine this is modelled on is not a template for *execution*, only for
*authoring*. The difference is structural, not incidental:

| | `selectionPolicy` | Consensus policy |
|---|---|---|
| What the decision depends on | health metrics — change slowly | **this round's votes** — new every request |
| Cadence | ticker, default **15s** per slot ([slot.go:123-158](../../internal/policy/slot.go), [defaults.go:3003](../../common/defaults.go)) | per request, per response arrival |
| Eval budget | **100ms** ([defaults.go:3006](../../common/defaults.go)) | at or below the 24-closure walk it replaces (§8) — the round's 612µs p50 is a round-trip containing five evaluations, not a per-eval budget |
| Evals/sec | ~1 per slot per 15s | ~1.75M |
| Request path | atomic load of the last tick's ordered slice ([engine.go:354](../../internal/policy/engine.go)) | there is no precomputable *result* |

What "compiled" means there is worth stating precisely, because it is
weaker than it sounds: `CompiledProgram` is a *parsed* sobek program, and
every tick still calls `vm.RunProgram(...)`
([eval.go:636](../../internal/policy/eval.go)), rebuilds a JS array of
upstreams, sets per-tick globals, and calls the function — with stdlib
methods executing **eagerly** against real data. Parse once, execute per
tick. That is affordable at 1/15s and unaffordable at 1.75M/s.

**The consequence:** consensus cannot precompute the *decision*, so it
precomputes the *decision procedure* instead.

## 3. Design: freeform at load, finite tree at runtime

The policy is **a program that builds a policy**.

```
config load / reload                         request path
────────────────────                         ────────────
static validation ─▶ trace ─▶ finite tree ─▶ execute in Go, per response,
(runs once)                   of primitives  against this round's votes
```

At load the eval function is invoked against a **recorder** `round` object:
every primitive call returns another recorder and captures the operation,
producing a materialized tree of Go-implemented primitives. At request time
that tree is walked in Go. No JS, no marshalling, no allocation on the
steady path.

### 3.1 Tracing cannot enforce the boundary — static validation does

The tree is built by tracing, but **the guarantee that a policy contains no
runtime-dependent structure cannot come from tracing**, and the
specification must not claim it does. There are two distinct sinks a
runtime value can reach, and the validator must close both.

**Sink 1 — control flow.** JavaScript's `ToBoolean` on an object is
unconditionally `true` and invokes no coercion or proxy hook, so this
compiles silently, freezing one branch instead of failing:

```js
if (absent("type:internal")) {           // policy expression → always truthy
  return round.accept("fallback")        // other branch silently discarded
}
```

Note the spelling: `absent(...)` is a **free function** in the vocabulary
(§4.3), not a method on `round`. That is what makes the taint model below
harder than it first appears — the offending value never touches `round`.

**Sink 2 — tree-shaping arguments.** These contain no control flow at all,
so a control-flow-only rule lets them through:

```js
const p = round.partitionBy(valueAt(paths))  // recorder
round.require(atThreshold(p))                // threshold frozen to a recorder
round.accept(p.gradeName)                    // dynamic property off a recorder
```

Each is worse than a miscompile. A dynamic grade name defeats the static
grade/grant validation in §6.2 **and** makes the grade metric label
unbounded — an unvalidated string becomes a metric dimension, which is a
cardinality-explosion vector, not merely a correctness bug.

**The worst version of sink 2 is closed by construction, not by analysis.**
The hazard is at its sharpest when a *request-derived* value reaches an
argument — `accept(ctx.method)` would make an attacker-chosen JSON-RPC
method name a metric label. That is impossible here because `ctx` carries
**no runtime attributes at all** (§3.2): runtime state is reached only
through the policy vocabulary. Deleting that capability removes the sharpest
hazard, but it does **not** reduce the analysis to a single root — see below.

#### What must be tainted: policy expressions, not one parameter

The tainted set is **every policy expression**, and getting this list wrong is
the most likely way to ship a validator that passes its own tests and still
miscompiles. Three distinct sources, only one of which is the `round`
parameter:

| Source | Example | Why it is runtime-valued |
|---|---|---|
| **Recorder parameters** — `round` **and every nested callback receiver** | `round`, and `r` in `.when(g, r => r.rank(...))` | `r` is supplied by the tracer, not derived from `round` by any expression an analyser can follow. §12 and §4.1.1 both use it |
| **Bare vocabulary constructors** | `absent("x")`, `atThreshold(2)`, `callerMayReceive("fallback")`, `mostCommon`, `highestAt(paths)`, `all(...)` | These are **free functions**, not `round` methods (§4.2, §4.3, §12). Nothing links their results to `round` |
| Anything derived from either | assignment, destructuring, property access, call results, array/object membership | ordinary propagation |

The second row is the trap. A root-only-`round` model accepts this, and it
freezes a branch exactly as sink 1 does:

```js
const p = absent("type:internal")   // free function → not derived from `round`
if (p) { ... }                      // recorder object → always truthy
```

So the rule is not "taint the parameter and propagate". It is: **the DSL's
result type is a symbolic policy expression, and every value of that type is
tainted wherever it came from.** Track it as a type, not as a reaching
definition. A policy expression is then *legal* in the parameter positions
that declare they take one and illegal everywhere else — including every
control-flow test. The two-kind parameter split below is what makes that
precise; "reject it in every argument" is not the rule and cannot be, since
the vocabulary is built out of nesting policy expressions.

**The guarantee is provided by static validation of the function's AST at
load**, before tracing:

- Conservatively taint **every policy expression** per the table above —
  recorder parameters including nested callback receivers, bare vocabulary
  constructor results, and anything derived from either. `ctx` is not among
  them, because it is config-derived only (§3.2).
- **Every primitive parameter is declared as exactly one of three kinds.** This
  is the rule that makes the rest decidable, and stating it as "reject a
  policy expression in *any* argument" — as an earlier draft did — is
  self-contradictory: it would reject `all(absent("x"), …)` and `.when(g, …)`,
  which is the entire vocabulary.

  | Parameter kind | Accepts | Examples |
  |---|---|---|
  | **policy expression, of a declared *subtype*** | a policy expression whose subtype matches the parameter, and nothing else | `.when`'s guard, `require`'s terms, `eligible`'s predicate, `rank`'s terms, `all`/`any`'s operands, `groupCount`'s group predicate, `.earlyExit`'s predicate |
  | **Literal / config** | a compile-time constant only — literal, or a value provably derived from config | `accept`'s grade, `.earlyExit`'s reason, `atThreshold`'s `n`, `whenMethod`'s pattern, `groupCount`'s `cmp` and `n`, `valueAt`'s paths |
  | **Branch callback** | a function literal `r => …` taking a `Builder` and returning an `Outcome` | `.when`'s second argument, `first(...)`'s members |

  The third kind was omitted from an earlier draft, which left `.when`'s
  callback belonging to neither — it is not a policy expression and not a
  literal. It is declared here because the tracer supplies its parameter
  (§3.1's taint table) and because its *return* must be an `Outcome`, which is
  what makes a branch that forgets to terminate a load-time error rather than a
  tree with a dangling tail.

  Reject a policy expression in a literal/config position, and in any position
  a primitive has not declared. There are no runtime-expression parameters
  beyond the declared set in v1; adding one is a typed change to a primitive's
  signature, never an inference.

  **One `PolicyExpr` type is not enough — it must be a family.** A single
  symbolic type distinguishes expressions from literals but not expressions
  from *each other*, so every one of these satisfies the rule as stated while
  being meaningless:

  ```js
  .when(mostCommon(), ...)              // a rank term used as a guard
  .rank(absent("x"))                    // a round guard used as a rank term
  all(mostCommon(), absent("x"))        // operands from different domains
  .require(nonEmptyFirst())             // a rank term used as a requirement
  .partitionBy(atThreshold(2))          // a predicate used as a partition key
  ```

  So the vocabulary declares subtypes and each parameter names the one it
  accepts:

  | Subtype | Members | Appears in |
  |---|---|---|
  | **`GroupPred`** | `atThreshold(n)`, `requireTag(t, n)`, `notTie()`/`isTie()`, `responseTypeIs(t)`, `extractableAt(paths)`, `valid`, `not(GroupPred)` | `eligible`, `require`, `groupCount`, `exists` |
  | **`Guard`** | `always`, `absent(t)`, `whenParticipants(n)`, `participantsBelow(n)`, `whenMethod(p)`, `whenInFlight()`, `capFired()`, `callerMayReceive(g)`, `groupCount(...)`, `exists(...)`, `not(Guard)` | `.when` |
  | **`EarlyExitPred`** | every `Guard`, plus the candidate-scoped facts of §4.4.1, plus `not(EarlyExitPred)` | `.earlyExit` only |
  | **`RankTerm`** | `mostCommon()`, `nonEmptyFirst()`, `largest()`, `highestAt(paths)`, `fromLeader(opts)` | `rank` |
  | **`PartitionKey`** | `valueAt(paths)` | `partitionBy` |
  | **`Outcome`** | `accept`, `reject`, `rejectWith`, `wait` | branch tails, `first(...)` members |
  | **`Builder`** | the staged recorder chain (§4.6) | `.when`'s callback receiver |

  **`not(...)` is a primitive because host `!` is rejected** (sink 1). Without
  it the vocabulary cannot express the negations the existing shortcuts need —
  `whenInFlight() ⇒ P` is `any(not(whenInFlight()), P)` — and Phase 5 could not
  translate them at all. It is domain-preserving like `all`/`any`: `not` of a
  `Guard` is a `Guard`, `not` of a `GroupPred` is a `GroupPred`.

  `always` is a `Guard` (the constant-true one, used by §12) and `first(...)`
  takes `Outcome`s and returns an `Outcome` (§4.5's nesting). Both were used in
  examples while being absent from an earlier draft's tables — an exhaustive
  table that omits members is not exhaustive.

  **`Requirement` is deliberately *not* a subtype**, and making it one — as an
  earlier draft did — makes the vocabulary untypeable. §4.1's whole point is
  *one predicate, two positions*: `eligible(atThreshold(n))` and
  `require(atThreshold(n))` are the same `GroupPred` differing only in **what
  it is applied to**. Disjoint `GroupPred`/`Requirement` types would force
  `atThreshold` to have two, which is the modelling error §4.1 exists to
  correct, restated in the type system.

  **But the fix for that is position, not a lift into `Guard`.** A
  `candidate(pred)` lift — proposed in a later draft — is worse than the
  problem it solves, because it would make a candidate-scoped predicate
  consumable by `.when`:

  ```js
  .when(candidate(atThreshold(2)), r => r.rank(mostCommon).accept("x"))
  //    ^ the candidate is produced BY this branch's partition and rank
  ```

  That is circular: `.when` selects the branch, and the branch is what
  produces the candidate the guard is asking about. There is no evaluation
  order that makes it meaningful, and nesting makes it worse rather than
  better.

  So there is exactly **one** lift out of `GroupPred`, and candidate scope is
  reached only through builder positions that are *already* past ranking:

  - `exists(pred) → Guard` — holds for **some** group; the sole lift. Sugar for
    `groupCount(pred, ">=", 1)` (§4.3).
  - `require(pred...)` — a **`Builder` operation**, not a lift and not a
    `Guard`. It is only reachable after `partitionBy`/`eligible`/`rank` have
    run in the same chain, so "the candidate" is well defined by construction,
    and a failing `require` falls through to the next alternative.
  - `.earlyExit(reason, pred)` — likewise a builder position, attached to an
    already-selected `Outcome`, so its predicate may reference candidate-scoped
    facts (lead, threshold) that no `.when` guard can name.

  **A candidate-scoped predicate is therefore unreachable from `.when` by
  construction, not by rule.** The builder chain's ordering is what enforces
  it, which is why no extra type is needed to stop the circular case.

  `all`/`any` are **domain-uniform**: operands of one subtype, returning that
  subtype. That makes `all(valid, extractableAt(paths))` a `GroupPred` and
  `all(absent(t), callerMayReceive(g))` a `Guard` without the two ever mixing,
  and it is why §4.1.1's composition and §12's guard are both well-typed.

  Enforce it at both ends: the AST check rejects what it can see statically,
  and the tracer **structurally type-checks the built tree** before it is
  installed, so a subtype violation that survives the AST pass — reached
  through a config-derived helper the analyser could not see into — still fails
  the reload rather than compiling into a nonsensical tree. Cross-domain
  rejection tests are required per pair, not one representative case.
- **Reject a policy expression reaching a control-flow test**: `if`, `while`,
  `for`, `switch`, `?:`, `&&`, `||`, `!`, or truthiness in a return
  position. Note `all`/`any` are *primitives* in `PolicyExpr` positions, not
  the host `&&`/`||` — that distinction is the whole reason boolean
  composition over runtime state is expressible at all.
- **Grade names and early-exit reasons must be static string literals**
  drawn from finite sets the policy declares. Both become metric labels, so
  both are what make §6.2's cross-validation and bounded cardinality
  possible at all — a dynamic `reason` is the same attacker-controlled-label
  hazard as a dynamic grade.
- **Reject dynamic code generation** — `eval`, `new Function`, dynamic
  `import()`, `with` — which would defeat AST analysis wholesale.
- Reject when the analysis cannot prove a value untainted: dynamic property
  access, aliasing through a container, or a call into an opaque helper.
  Rejection is the safe direction.

```
policy rejected: policy expression reaches a control-flow test
  at evalFunc line 7 — `absent(...)` cannot be used in `if`.
  Use the guard form instead:  .when(absent("type:internal"), ...)

policy rejected: policy expression in a literal parameter
  at evalFunc line 12 — `accept(grade)` takes a literal grade name;
  got a value derived from `round.partitionBy(...)`.
```

**Accepted cost:** conservative taint analysis will occasionally reject a
policy that would in fact have been safe — typically one routing a value
through a helper the analyser cannot see into. That is a real usability
cost, and it is preferred over a silent miscompile or an unbounded metric.
If the rejection rate proves impractical in review, the fallback is a
data-only builder DSL (no host control flow at all), which removes the
analysis but also removes loops and helpers over config. §13.

The freeform that matters is unaffected, because it operates on *config*:

```js
// still fine — runs once, no runtime value in a test or an argument
const tiers = ["type:internal", "type:external"]
const quota = tiers.map(t => requireTag(t, 1))
```

#### 3.1.2 Trust boundary: operator-supplied code, executed at load

**A policy is JavaScript that this process executes at config load. That is
arbitrary code execution inside eRPC, and it is only acceptable because policy
authors are operators.** Stating it plainly, because every argument in §3.1 and
§3.3 rests on it:

- The eval function is supplied exactly the way `selectionPolicy.evalFunc` is
  today — project-level YAML/TS in the eRPC config
  ([config.go:2711](../../common/config.go)). Anyone who can author a policy can
  already author the whole configuration, including upstream credentials.
- **This is therefore not a new trust boundary**, it is the existing one. What
  is new is a second place it is exercised, on a hotter path.
- No request input, header, or authenticated claim may introduce or modify
  policy source. Callers influence the decision only through the runtime
  attributes primitives read (§3.2), never through the function.
- If a deployment ever wants less-trusted parties to supply policy, **that is a
  different feature** and this design does not support it. The static
  validation in §3.1 bounds *shape*, not *effect* — it is not a sandbox.

#### 3.1.3 The validator is a security boundary, so its soundness is a deliverable

§3.1's analysis is not a lint. A hole in it produces one of two failures, and
the second is a vulnerability rather than a bug:

1. a policy that **silently miscompiles** — a frozen branch, a threshold pinned
   to whatever the recorder saw;
2. an unvalidated string reaching a **metric label**, which is a
   cardinality-explosion vector reachable from attacker-controlled input.

The design's own history is the reason this needs a stated plan rather than
confidence: five review passes each found a fresh hole in the validator's type
system (Appendix B), every time in a validator that passed the tests written for
it. Per-pair rejection tests confirm *known* bad shapes are rejected; they say
nothing about the ones nobody thought of.

**So validator soundness is a shipped artefact, not an aspiration:**

- **A property test over generated policies.** Generate eval functions from a
  grammar that includes the hazardous shapes — runtime values in tests and
  arguments, aliasing through containers and helpers, nested callback receivers,
  dynamic property access — and assert: *every accepted policy materializes a
  tree whose structure is independent of runtime state, and every grade and
  reason in it is a literal from the declared sets.*
- **A differential oracle.** For accepted policies, trace the same function
  twice against recorders seeded with different fake runtime state; the two
  trees must be structurally identical. A policy where they differ is a
  validator hole by definition, and this catches the class directly rather than
  by enumerating shapes.
- **Fuzz the analyser, not just the policy.** Malformed and adversarial sources
  must fail closed — rejection is always safe, acceptance never is by default.
- **Coverage as a gate:** every taint source, sink class and subtype in §3.1 has
  at least one accepted-and-one-rejected case, asserted structurally so a new
  primitive cannot be added without both.

This is the cost of the authoring surface, and it is accepted knowingly: the
alternative design (Appendix A) removes the analyser entirely, and was rejected
for ergonomic reasons rather than because this cost is small.

### 3.2 Compile keys are config-derived only

A compile key is anything the tree may be specialized on. **Request-derived
values must never be compile keys**, and nothing may be compiled on the
request path.

| Kind | Examples | Rule |
|---|---|---|
| **Compile key** | the policy's own config, the network it is attached to | config-derived, enumerable at load; every tree is built during load/reload |
| **Runtime attribute** | `method`, caller's granted grades, responses in flight, response tags | reached **only** through primitives; one tree serves all values |

**`ctx` is config-derived only, and its fields are enumerated here rather than
left open.** In `(round, ctx) => …`, `ctx` carries exactly: the network id the
policy is attached to, the policy's own resolved config (thresholds,
participant counts, declared grade set), and nothing else. It carries **no**
runtime attributes — no `ctx.method`, no caller identity, no response state.
Every one of those is reached through a `round` primitive
(`whenMethod(pattern)`, `callerMayReceive(grade)`, `whenInFlight()`), which is
what keeps one tree serving all values.

This is a deliberate deletion, not an oversight. A runtime `ctx` field would be
unusable anyway — every use is either a control-flow test or a tree-shaping
argument, both rejected by §3.1 — while doubling the taint analysis's roots, and
`ctx.method` specifically is the attacker-controlled value whose leak into a
metric label §3.1 calls worse than a miscompile. New `ctx` fields must be
config-derived; a runtime one requires reopening §3.1.

`method` is specifically **not** a compile key. JSON-RPC method names are an
attacker-controlled open set: specializing per method would mean a
first-seen method triggers compilation *on the request path* (violating §1),
a policy could fail to compile after a reload was already accepted, and the
tree cache would grow without bound under a method flood. Method selection
is therefore a runtime matcher — `whenMethod(pattern)` with a **literal**
pattern (§3.1) — evaluated in Go against the compiled tree.

The rule generalizes: **all compilation happens during config load or
reload, for every tree, eagerly.** A compile failure fails the reload and
leaves the running configuration untouched. There is no lazy path, so there
is no class of "compiles at request time" bug to reason about.

### 3.3 Load-time execution must be bounded and interruptible

Moving all cost to load does not make it free, and §3.1 deliberately permits
unrestricted host control flow over config — that is the whole point of the
freeform layer. So the trace phase can diverge on a policy that passes every
static check:

```js
while (true) {}                                  // no tainted value in the test
const alts = a.flatMap(x => b.map(y => ...))     // config-derived, quadratic
```

Neither is rejectable by taint analysis, and neither should be — the first is
a bug and the second may be legitimate. **The trace must therefore run under
a wall-clock budget and an interrupt**, and a policy that exceeds it fails the
reload like any other compile failure. Without this, a bad config does not
produce a bad policy; it hangs startup, or hangs a reload before any finite
tree exists to fall back to.

**Do not copy the selection-policy timeout verbatim — it is soft.** `Slot`
races the eval against `EvalTimeout`
([slot.go:232-237](../../internal/policy/slot.go)), but on expiry it records
the error and then still blocks on `<-done`, with the comment *"sobek doesn't
support interrupt mid-call cleanly"*. A runaway eval there wedges one
background tick every 15s, which is survivable. A wedged **config reload** is
not: it holds the load path open with no timeout of its own.

Sobek does support the primitive that makes a hard cutoff possible —
`Runtime.Interrupt(v)` unwinds the running program and returns
`*InterruptedError`, with `ClearInterrupt()` to reset the runtime
(`github.com/grafana/sobek`, `runtime.go:1517-1533`) — so the requirement is:
interrupt
on budget expiry, discard that runtime rather than returning it to the pool,
fail the reload with the elapsed time and the limit. Because compilation is
eager and per-tree (§3.2), the budget applies per tree with a ceiling on the
aggregate, so a config with many networks cannot multiply its way past it.

---

## 4. Base primitives

Every one of the 24 rules is a composition of four axes plus one control
primitive. This section is the factoring; §11 is its verification.

### 4.1 Partition + Rank — *which groups exist, which are eligible, in what order*

The mechanical layer delivers responses grouped by hash. A policy may
**re-partition** them before ranking, and this is not an exotic extension —
today's `preferHighestValueFor` already does exactly that (§11, rule 2):
it re-groups individual results by extracted numeric value, so `0x5` and
`0x05` agree despite hashing differently.

| Primitive | Meaning |
|---|---|
| `partitionBy(valueAt(paths))` | re-partition results into **decision groups** keyed by an extracted value, replacing hash identity for this decision. Semantics in §4.1.1 |
| `eligible(pred)` | **filter which groups may be chosen**, before ranking. Composes; `eligible(valid)` and `eligible(all)` replace what was a fixed scope enum |
| `mostCommon()` | by vote count |
| `nonEmptyFirst()` | response-type ordering: non-empty ▸ empty ▸ consensus error |
| `largest()` | by response size |
| `highestAt(paths)` | by numeric value at JSON paths |
| `fromLeader({includeInfra})` | restrict to the block-head leader's group. `includeInfra` distinguishes rules 3 and 4, which differ only there |

**Partition is the unit everything downstream counts over.** Once a policy
declares one, `atThreshold` and `requireTag` (§4.2) count over the decision
group, not the hash group. Rule 2's own threshold is the parity case:
`vg.count < threshold` is evaluated per *value* partition
([rules.go:149](../../consensus/rules.go)), not per hash group, so `0x5` and
`0x05` must already count together for the threshold filter to reproduce.

The mechanical composition gate independently counts the same way —
`agreeingResults` spreads `minAgreement` quotas across the value partition
([executor.go:983](../../consensus/executor.go)): *"the same value with a
different encoding (0x5 vs 0x05) hashes into a different group, and its
upstream must still count toward the composition quota."* That is
**corroboration, not the requirement**: composition stays mechanical (§7.1),
so the gate keeps doing its own counting and `requireTag` is not what
reproduces it.

**`eligible` and `require` are not the same check**, and conflating them was
a modelling error an early draft made: `eligible` filters *which groups may
win* (before ranking); `require` validates *the group that won* (after). The
difference is observable — ranking by value and then requiring a threshold
picks the highest-value group and fails if it is thin, whereas filtering to
threshold-meeting groups and then ranking picks the highest value *among
those that qualify*. Rule 2 needs the second, which is why the scope
selector had to generalize from a `valid`/`all` enum into an arbitrary
predicate. One predicate, two positions.

Rank terms compose into a comparator chain; only the top candidate is
needed, so evaluation is O(G) with no sort. Each term declares a **stability
class** (§5), which is what makes finality decidable.

Hashing itself stays mechanical (§7): identity is computed once, before the
decision. Partitioning is a *decision-time* view over that identity, not a
redefinition of it.

#### 4.1.1 `partitionBy(valueAt(paths))` — exact semantics

Parity with `preferHighestValueFor` depends on five behaviours in today's
code ([rules.go:84-171](../../consensus/rules.go)). Only two of them belong
to the partition primitive; folding the other three into it would make
`partitionBy` a bespoke re-implementation of the rule, which §11's gate
forbids. They are distributed as follows:

| Behaviour | Where it belongs |
|---|---|
| Results whose paths do not extract are **excluded**, not pooled into a null partition | `partitionBy` |
| The partition's **representative response is the largest by size** — a partition spans hash groups, so "which body is served" needs a rule | `partitionBy` |
| **Activation:** applies only when ≥1 group yields an extractable value, else fall through | `exists(extractableAt(paths))` guard (§4.3) |
| **Threshold before extremum:** partitions below threshold are discarded *first*, then the highest value among survivors wins | `eligible(atThreshold(n))` (§4.1) |
| **A distinct dispute message** when no partition qualifies — error identity is part of parity | `reject(kind, message)` (§4.4) |
| **Scope:** both activation and partitioning read `getValidGroups()` ([rules.go:91](../../consensus/rules.go), [:117](../../consensus/rules.go)) — infrastructure errors never enter the partition | `eligible(valid)`, applied *before* `partitionBy` |

Composed, with no bespoke primitive:

```js
.when(all(whenMethod("eth_gasPrice"),
          exists(all(valid, extractableAt(paths)))), r => r
  .eligible(valid)                   // partition over VALID groups only
  .partitionBy(valueAt(paths))
  .eligible(atThreshold(2))          // discard thin partitions FIRST
  .rank(highestAt(paths))            // then take the highest survivor
  .accept("standard")
  .orReject(DISPUTE, "no value met agreement threshold for highest-value comparison"))
```

The `eligible(valid)` term is not decoration: without it the composition
partitions over *all* groups and pulls infrastructure errors into the value
partition, which today's rule never does. It also demonstrates the §4.1
claim that `eligible` composes — the same predicate position carries both
the scope filter and the threshold filter, in order.

That this composes is the evidence the factoring holds for rule 2. Had it
not, the honest outcome would have been to report rule 2 as irreducible
(§11), not to widen one primitive until it swallowed the rule.

### 4.2 Require — *is the candidate good enough*

These are the **same `GroupPred`s** §4.1's `eligible` filters with, applied to
the group that won instead of to every group. `require` is a **builder
operation** (§3.1), reachable only after `partitionBy`/`eligible`/`rank` have
run in the same chain — which is what makes "the candidate" well defined
without a candidate-scoped guard type, and what keeps such a predicate out of
`.when`, where it would be circular. "Require" names a *position*, not a
separate kind of predicate; that is what makes "one predicate, two positions"
(§4.1) true in the type system as well as in the prose.

| Primitive | Meaning |
|---|---|
| `atThreshold(n)` | candidate has ≥ n agreeing votes **in the current partition** |
| `requireTag(tag, n)` | ≥ n **distinct** upstreams carrying `tag` are in the candidate **partition**. Fails by falling through to the next alternative — which is why it is *not* the translation target for `requiredParticipants[].minAgreement`, whose failure disputes instead (§7.1) |
| `notTie()` / `isTie()` | candidate is (not) tied with another group |
| `responseTypeIs(type)` | exact response type — `nonEmpty`, `empty`, `consensusError`, `infrastructureError` |
| `extractableAt(paths)` | the group's **representative response** yields a value at `paths`. Deliberately not "any member extracts": today's rule-2 activation tests `group.LargestResult` alone ([rules.go:92](../../consensus/rules.go)) while the partition step that follows extracts from *every* member ([rules.go:122](../../consensus/rules.go)). The two domains differ on a group whose representative does not extract but whose members do, so the predicate must name which one it means |

**Response-type checks must be exact, not `isError`/`isNonError`.** The
existing rules branch on the precise type in both directions — e.g. "the
leading group meets threshold and is **empty or consensus-error**, while any
**non-empty** exists" ([rules.go:308](../../consensus/rules.go)) — and the
shortcut predicates in §4.4 require *non-empty* specifically, which a
non-error test does not give (an empty success is not an error). A coarse
error/non-error pair cannot express either.

### 4.3 Guard — *round-level preconditions*

| Primitive | Meaning |
|---|---|
| `whenParticipants(n)` / `participantsBelow(n)` | valid participant count |
| `absent(tag)` | **zero** round participants carry `tag` |
| `whenMethod(pattern)` | runtime method matcher, literal pattern (§3.2) |
| `whenFinality(bucket)` | the request's finality bucket — `realtime`, `unfinalized`, `finalized`, `unknown`. Literal bucket; see §4.3.1 |
| `groupCount(pred, cmp, n)` | **how many groups satisfy a group predicate**, compared against a literal `n` with a literal `cmp` (`==`, `>=`, `>`, `<=`, `<`) |
| `exists(pred)` | sugar for `groupCount(pred, ">=", 1)` |
| `whenInFlight()` | responses still outstanding |
| `capFired()` | a wait cap has fired for this round (§5.5, §7) |
| `callerMayReceive(grade)` | the caller may receive `grade` — output authorization, fail-fast (§6.4) |
| `callerHasRole(role)` | the caller carries `role` — **input routing**, never authorization (§6.5) |

**Cardinality must be a comparison, not just existence.** `exists` answers
"at least one", which is not what several rules ask:

| Rule | Condition | Expression |
|---|---|---|
| 11 | **exactly one** non-empty group, and at least one empty | `all(groupCount(responseTypeIs(nonEmpty), "==", 1), exists(responseTypeIs(empty)))` |
| 14 | **more than one** valid group at threshold — *"could have different counts"* ([rules.go:578](../../consensus/rules.go)) | `groupCount(all(valid, atThreshold(n)), ">", 1)` |

Neither is a tie: rule 14 explicitly admits unequal counts, so `isTie()` is
the wrong predicate for it, and rule 11 needs an upper bound `exists` cannot
give. One comparison primitive covers both and makes `exists` sugar rather
than a special case.

> **Translation note.** Rule 11's comment says "at least one non-empty group"
> while its code returns `hasEmpty && nonEmptyGroups == 1`
> ([rules.go:403](../../consensus/rules.go)). The code is authoritative for
> parity; the translated policy must use `== 1`.

`groupCount` and `exists` also remove one-off guards: rule 2's activation is
`exists(extractableAt(paths))`. A specialized `hasExtractableValue()` guard
is expressible with the general combinator, so no specialized guard is
introduced — one predicate language, usable at group level or lifted to the
round (Appendix B).

#### 4.3.1 `whenFinality` — a guard, not a compile key

Consensus already computes the request's finality: `finality
common.DataFinalityState` sits on the round's metrics labels
([executor.go:43](../../consensus/executor.go)), so nothing new is plumbed.

**It is a runtime matcher rather than a compile key**, even though — unlike
`method` — it *could* safely be one. Finality is a four-value closed enum, so
specialising a tree per bucket would not risk the unbounded-cache and
compile-on-request-path hazards that disqualify `method` (§3.2), and
`selectionPolicy` already offers exactly that specialisation
(`EvalScopeNetworkMethodFinality`, [config.go:2666](../../common/config.go)).
The razor decides it: one tree serving all buckets commits to less than four
trees, and a guard is deletable structure where a compile-key axis is not. If a
deployment later shows per-bucket trees measurably matter, the precedent to
follow already exists.

**Finality bucket is not the same question as block availability**, and only the
first is in v1. "Serve a different policy when upstreams can only cover blocks
N..M" is a different predicate over different data — the block-availability lane
set ([engine.go:368](../../internal/policy/engine.go)) plus the project-level
`blockAvailability` config — none of which is in round state today. §13.3 carries
it as an open question rather than a guessed primitive: the weakest thing that
handles the observed case is a bucket guard, and a range predicate should wait
for a request that actually needs ranges.

`absent(tag)` is a predicate over **participation**, never over agreement.
An upstream that answered and lost is present. This is the distinction the
whole graded-acceptance use case turns on (§12).

**`callerHasRole` and `callerMayReceive` are not two spellings of the same
thing**, and an earlier draft refused the former on the grounds that grades gave
"no capability grades lack". That premise was wrong — routing by *audience* is a
capability grades cannot express cleanly (§6.5):

| | Asks about | Enforced by the engine? | Emitted as a metric label? |
|---|---|---|---|
| `callerMayReceive(grade)` | the **answer** the round produced | **yes** — a grade the caller may not receive is withheld as a dispute (§6.4) | yes, bounded by the declared grade set |
| `callerHasRole(role)` | the **caller** | no — it selects an alternative, nothing more | **no** (§6.5) |

Using a grade as a routing key conflates the two: it makes "which policy applies
to this caller" indistinguishable from "which acceptance level this answer
reached", and it borrows output *enforcement* semantics for an input decision —
so a caller outside the intended audience gets a **dispute** rather than the
policy meant for them. `callerHasRole` keeps the two separate.

### 4.4 Outcome — *what the round returns*

| Primitive | Meaning |
|---|---|
| `accept(grade)` | serve the candidate under a named grade |
| `reject(kind, message?)` | fail with a **synthesized** error (`DISPUTE`, `LOW_PARTICIPANTS`, `COMPOSITION_DISPUTE`), optionally carrying a literal message. Several existing rules synthesize rule-specific text, and error identity is part of parity |
| `rejectWith()` | fail by **passing through the candidate group's own error** — distinct from `reject`, and required by rules 3, 4, 18 and 21 |
| `wait()` | not final; some legal completion could still change this (§5) |

**Finality tolerance is orthogonal to outcome type, and carries its own
complete predicate.** `.earlyExit(reason, predicate)` is a modifier on *any*
terminating outcome — `accept`, `reject`, `rejectWith` alike — not a field
on `accept`, and not a shared bound with per-rule exceptions.

`reason` is a **static literal** (§3.1), emitted as the short-circuit
telemetry label. It is part of the node rather than compiler-assigned
because the existing reasons are operator-visible strings that must survive
translation verbatim; making it a literal keeps the label set bounded by the
same rule that bounds grade names.

Both halves of that matter, because the three existing shortcuts share
neither an outcome type nor a condition
([rules.go:858-956](../../consensus/rules.go)):

| Shortcut | Outcome | Complete positive predicate |
|---|---|---|
| `sendrawtx_first_success` | accept | tx-broadcast method **and** any group with a non-empty response — no threshold, no lead, exits on the **first** response |
| `consensus_error_threshold` | **pass-through error** | best-by-count is a consensus-error group at threshold — **no lead requirement at all** |
| `unassailable_lead` | accept | non-empty best at threshold **and** `lead >= remaining` |

Each also carries its own suppressions, and **they do not all have the same
scope**:

| Shortcut | Suppression | Scope |
|---|---|---|
| `consensus_error_threshold` | `preferHighestValueFor` configured for the method; accept-most-common with either preference | **unconditional** ([rules.go:893](../../consensus/rules.go)) |
| `unassailable_lead` | `preferHighestValueFor`; `preferLargerResponses`; `preferNonEmpty` with an empty leader | **only while `hasRemaining()`** ([rules.go:918](../../consensus/rules.go)) |

That second row is a live parity trap. Once no responses remain, all three
of `unassailable_lead`'s suppressions are skipped and `remaining <= 0` makes
`leadOverSecond >= remaining` trivially true — so the rule **fires on the
final response even under `preferLargerResponses`**. The round was ending
anyway, but `shortCircuited` becomes true, which is an operator-visible
metric label and selects a different `markWinningParticipants` branch. The
translated predicate must therefore read
`whenInFlight() ⇒ (not preferLargerResponses ∧ …)`, not a bare conjunction.

And each carries its `reason` — `sendrawtx_first_success`,
`consensus_error_threshold`, `unassailable_lead` — which is surfaced in
telemetry and is part of observability parity.

So an early exit is *not* "the sound rule minus one unit of lead". It is a
rule-specific predicate that happens to coincide with that for exactly one
of the three.

The outcome vocabulary is otherwise closed: a candidate, a grade or an
error. It carries no timer directive and no punishment target — see §7 for
why both stay mechanical.

#### 4.4.1 `EarlyExitPred` — the context the three shortcuts actually need

`.earlyExit`'s predicate cannot be an ordinary `Guard`, and leaving it
unspecified would have made **Phase 5 unable to translate any of the three
existing shortcuts** — the translation table would name a form the vocabulary
cannot express. The three predicates (§4.4) between them need round-level
facts, candidate-scoped facts, and one quantity with no primitive at all.

`EarlyExitPred` is therefore its own context, legal **only** in `.earlyExit`,
where the builder is already past ranking so the candidate is defined (§4.6):

| Fact | Kind | Needed by |
|---|---|---|
| every `Guard` | round | all three (`whenMethod` for tx-broadcast, `whenInFlight` for the suppressions) |
| `candidateIs(GroupPred)` | candidate | `unassailable_lead` (non-empty at threshold), `consensus_error_threshold` (consensus-error at threshold) |
| `leadOver(cmp, "remaining")` | candidate + round | `unassailable_lead`'s `lead >= remaining` — **the quantity that had no primitive**. `cmp` is a literal; the right operand names a declared round quantity, not an expression |
| `preferenceActive(name)` | config | each shortcut's suppressions, which read config knobs the tree otherwise never sees |
| `not(...)`, `all(...)`, `any(...)` | — | composition, including `whenInFlight() ⇒ P` as `any(not(whenInFlight()), P)` |

> **Accepted debt, named as such.** `leadOver` reads the *known-wrong* legacy
> counter. §5.3 shows `maxParticipants - collectedResponses` over-counts live
> slots, because nil-returning slots consume a slot without appending. Wiring
> early exit to it is deliberate — it is what keeps short-circuit timing
> byte-identical on upgrade — but it means the migration carries a known defect
> across as a permanent compatibility contract. It is debt, not a footnote:
> retiring it is a behaviour change that needs its own decision, and until then
> two counters with different semantics coexist by design.

`leadOver` deliberately names **`remaining`** — the raw legacy quantity of
§5.3, not `legalOutstanding`. Early exit is bug-compatible by design, so its
lead bound must read the same counter rule 27 reads
([rules.go:951](../../consensus/rules.go)); wiring it to the corrected counter
would silently change short-circuit timing on every existing deployment, which
is precisely what §9 promises not to do.

With this context the three translate directly, and the translation is
checkable rather than aspirational:

```js
// unassailable_lead
.earlyExit("unassailable_lead", all(
  candidateIs(all(responseTypeIs(nonEmpty), atThreshold(n))),
  leadOver(">=", "remaining"),
  any(not(whenInFlight()), all(
    not(preferenceActive("preferHighestValueFor")),
    not(preferenceActive("preferLargerResponses")),
    not(all(preferenceActive("preferNonEmpty"),
            candidateIs(responseTypeIs(empty))))))))
```

The `any(not(whenInFlight()), ...)` wrapper is §4.4's `hasRemaining()`-scoped
suppression stated in the vocabulary — which is why `not` has to exist as a
primitive at all.

**If this context proves larger than the three shortcuts justify, the weaker
alternative is to keep the legacy shortcuts mechanical** — a fourth row in §7's
boundary table, translated configs keeping today's hard-coded short-circuit
rules — and let `.earlyExit` serve only hand-written policies with a
`Guard`-only predicate. That trades expressiveness for a smaller surface and
should be reconsidered if §4.4.1 grows.

### 4.5 Control — *ordered alternatives, first match wins*

`.when(guard, branch)` / `.orElse(...)` / `.orReject(kind)`, and these must
**nest**: `first(...)` inside a guard. Several existing rules are internal
cascades (rule 18 is four outcomes under one condition); without nesting
they transcribe as N alternatives repeating the same guard.

### 4.6 The builder is **staged** — ordering is a type, not a convention

Calling `Builder` a single type (§3.1) is not enough to make §3.1's claim — that
candidate scope is unreachable from `.when` *by construction* — actually true.
With one undifferentiated type, nothing rejects `r.require(atThreshold(2))`
before any `rank`, `rejectWith()` with no candidate to pass through, or
`.earlyExit(...)` attached to nothing. "By construction" has to mean something
structural, so the builder is a **staged grammar** in which each stage exposes
only the methods legal at that point:

| Stage | Methods available | Next stage |
|---|---|---|
| **Scoped** (branch entry) | `eligible(GroupPred)`, `partitionBy(PartitionKey)`, `rank(RankTerm...)` | `eligible`/`partitionBy` stay Scoped; `rank` → Ranked |
| **Ranked** (a candidate now exists) | `require(GroupPred...)`, and every terminal | `require` stays Ranked; terminals → Terminated |
| **Terminated** (an outcome was chosen) | `.earlyExit(reason, EarlyExitPred)` | Terminated (idempotent — at most one) |

Three properties follow that no amount of prose could enforce:

- **`require` and `rejectWith` are unreachable before `rank`**, because the
  Scoped stage does not expose them. That, not a rule in a document, is what
  makes "the candidate is defined" true wherever candidate scope appears.
- **`.earlyExit` is unreachable before an outcome**, because only Terminated
  exposes it — which is also why §4.4 can say it attaches to *any* outcome
  without that being a special case.
- **A branch callback that does not reach Terminated is a load-time error**,
  since its declared return kind (§3.1) is `Outcome`. A branch that filters and
  ranks but never terminates cannot compile.

`partitionBy` before `rank` is likewise enforced by stage rather than by
convention — §4.1's "threshold before extremum" ordering is a property of the
grammar, not a thing an operator must remember.

**This machinery is a cost of the authoring surface.** Under the rejected
data-only alternative (Appendix A) the same three properties are required keys
in a schema. That is the clearest single place the two forks differ in price,
and it is worth re-reading if §3.1.3's soundness obligation ever starts to bite.

## 5. Finality: when is a decision safe to serve early

Because the policy is evaluated on every response arrival, "is this final?"
is a first-class question — and it decides whether the round may
short-circuit and cancel outstanding work.

**In one paragraph, before the detail.** Every evaluation returns one of three
things: an outcome that is safe to serve now, an outcome that is *not yet* safe,
or `wait()`. It is tri-state because the round is still arriving: a decision
that looks right after three responses can be wrong after five, so "serve it"
and "it is correct so far" are different claims. A decision is **sound** when it
would be the same under every way the round could still finish — and this
section is mostly about being precise on three things that make that hard to
check: what a still-arriving response is allowed to do (§5.1), how each
predicate and rank term can move (§5.2), and what "the same" means when two
upstreams agree but return different bytes (§5's equality). Operators who only
want today's behaviour can stop after §5.4: the translator emits early-exit
forms that preserve current timing exactly.

**Definition.** A decision is **sound** iff it is identical under every legal
completion of the round. Per-primitive boolean stability is *not* sufficient:
a pending response can change which candidate `mostCommon` selects while
`atThreshold` stays true, and an *earlier* alternative can become eligible
and preempt the one currently firing.

**"Identical" is equality of the *decision*, up to the equivalence the
operator declared — not byte-equality of the served payload.** This has to be
spelled out, because both stronger readings are wrong in a way that is easy to
adopt by accident.

Group identity alone is too weak: a later response with the **same hash**
replaces the group's representative when it is larger
([analysis.go:126-129](../../consensus/analysis.go)), so the candidate group
can be fixed while the served bytes change. `ignoreFields` makes that ordinary
— responses differing only on ignored fields hash together by construction.

But **byte-stability of the payload is too strong, and adopting it deletes
sound early acceptance entirely.** Any pending slot may join the candidate's
own group with a larger same-hash response, so the representative is not
provable until `legalOutstanding == 0` — for *every* candidate, under *every*
rank term. Payload-stable therefore collapses to wait-all universally, which
would falsify §5.4's claim that sound `unassailable_lead` differs from the
legacy shortcut by exactly `>` versus `>=`. Synthesized rejects are worse
still: they carry `a.participants()`, which collects across every group
([analysis.go](../../consensus/analysis.go)) and so grows on **every** arrival
— a dispute's payload is never stable, so payload-stability would make even
`reject` unservable early.

So equality is defined field by field, and the list is the specification:

| Must be invariant | Explicitly need not be |
|---|---|
| which partition wins (its identity — hash, or partition key under `partitionBy`) | which member of that partition supplies the served body |
| the outcome kind — `accept` / `reject` / `rejectWith` | response size, and any field covered by `ignoreFields` |
| the grade, for `accept` | the participant list carried in a synthesized error |
| the `kind` and literal message, for `reject` | |
| the **error identity** of the passed-through error, for `rejectWith` | that error's message, cause and provider detail — see below |

The concession is real and stated rather than buried: **the body served early
may differ, within the declared equivalence class, from the body the completed
round would have served.** That is exactly the licence `ignoreFields` and
canonical hashing already grant — the operator declared those differences
immaterial to agreement — and consensus is a claim about agreement, not about
which agreeing upstream's bytes are relayed.

**Error identity is the normalized grouping hash, not the error object.**
`errorToConsensusHash` reduces an error to `jsonrpc:<NormalizedCode>`, else the
`StandardError` base code, else `error:generic`
([analysis.go:442-457](../../consensus/analysis.go)) — so message, cause and
provider detail are *already* outside identity for grouping purposes, and
equality here means the same thing it means there. Defining it as the whole
error object would restore wait-all through the back door, since two upstreams
returning the same normalized code with different message text would look like
a changed decision. Named regression: **same normalized code, differing
messages → still sound.**

**Coarse partitions are the exception, and they may not exit early.** The
concession above is bounded by hash identity: members of a hash group differ
only in `ignoreFields` and size. A `partitionBy(valueAt(paths))` partition is
**coarser** — it spans hash groups, so its members can differ in **any field
that is not the extracted key** (§4.1.1). "Which member supplies the body" is
then a materially larger licence than the exclusions above describe, and a
`partitionBy(valueAt(paths)).rank(mostCommon())` policy would expose exactly
that during an early exit.

So: **when the winning partition's key is coarser than hash identity, the
decision is not sound until `legalOutstanding == 0`.** This is not a new
restriction invented for the policy engine — it is precisely what today's code
does, since rule 27 refuses to short-circuit whenever `preferHighestValueFor`
is configured for the method ([rules.go:893](../../consensus/rules.go)). Parity
and safety agree here, which is the good case. A policy that wants the early
exit anyway declares it per-outcome via `.earlyExit` (§5.4), where the
divergence is explicit.

**There is no payload-stability opt-in**, and the razor is why: its semantics are
provably wait-all for every policy, no observed case asks for it, and an
unexercised knob is itself a commitment. An operator who wants every response
before deciding is asking for a *round* that waits, not a *finality rule* that
never fires — and if that need ever appears it belongs in participant
collection, not here. Deleted rather than deferred.

### 5.1 What a legal completion may do

- A pending slot may return **any** response type and hash — joining a
  group, or forming a new one.
- A pending slot's **upstream is not known in advance.** Each slot runs its
  own retry loop and selects per attempt
  ([executor.go:244](../../consensus/executor.go),
  [network_executor.go:249](../../erpc/network_executor.go)), so tag-aware
  finality must use a **possible-tag-set** per pending slot — the union of
  tags over upstreams that slot could still select — not a fixed roster.

  **How that set is obtained is part of the contract, and the right anchor is
  narrower than "the selector".** Slots do not select from the whole upstream
  registry: selection has already run, and consensus installs a **fixed,
  quota-reordered list on the request** before any slot spawns —
  `originalReq.SetUpstreams(reordered)`, explicitly *"before any participant
  slot consumes an upstream (req.UpstreamIdx is still 0)"*
  ([executor.go:139-142](../../consensus/executor.go)). Every slot's
  `NextUpstream()` then draws only from that list
  ([request.go:1312](../../common/request.go)), gated by directives, the
  consumed set, and per-attempt error state.

  So the snapshot is **the post-reorder request list**, not a re-derivation of
  selection:

  - **The list is already frozen for the round**, so the earlier justification
    — "cordons and health changes only remove candidates" — was the wrong
    argument for the right conclusion. It is not that the candidate set shrinks
    monotonically; it is that `upstreamList` does not change after the reorder
    at all. What varies per attempt are the *gates*, which only ever exclude.
  - **Filter conservatively, by immutable directives only.** A directive like
    `UseUpstream` is fixed for the request and can be applied to the snapshot;
    consumed-state and error-state gates must **not** be, since they narrow as
    the round proceeds and folding them in would make the set shrink under the
    fold's feet.
  - **Over-approximation remains the sound direction** — a superset means more
    waiting, never less — but it is now over-approximating *the gates*, not the
    membership.
  - The set is per **round**, not per slot: all slots draw from one list, so one
    tag bitset serves them all. That is both the weaker commitment and the
    accurate one.
  - **`Upstreams()` allocates a copy on every call**
    ([request.go:1157-1168](../../common/request.go)), so the snapshot must be
    taken **once** through a no-copy iteration API added for this purpose.
    Calling `Upstreams()` per evaluation is the exact C4 violation this
    contract exists to prevent.
  - Membership tests are then bitset ops against a set fixed for the round's
    lifetime — O(1), allocation-free, and unaffected by how many evaluations
    the round performs.
- A pending response may **replace an existing vote**, not only add one.
  Dedupe keeps one response per upstream, best rank winning and last arrival
  winning at equal rank ([analysis.go:189](../../consensus/analysis.go)).
  Replacement is reachable because an upstream is freed for reselection when
  it returned no response, a retryable error, or an **empty result** —
  `canReUse` in [MarkUpstreamCompleted](../../common/request.go). A
  **non-empty successful** response is never freed, so votes of that kind
  are immutable for the rest of the round (§5.3).

### 5.2 The stability test

Soundness folds conservatively over the tree in O(tree). The two clauses are
mirror images of one question — *could this alternative's contribution to the
outcome change?* — and both must be stated in full; incompleteness in either
direction is silently unsound (Appendix B):

- **For every alternative earlier than the one currently firing:** could it
  *produce an outcome* under some completion? That means its guard becoming
  true **and** its `require` terms becoming satisfied — **not the guard
  alone.** §12's first alternative is guarded `always`, so its guard can
  never change; what changes is `requireTag("type:internal", 1)` starting to
  pass once the slow first-party vote lands. A guard-only check finds nothing
  to wait for and serves `fallback`.
- **For the firing alternative:** could it *stop producing this outcome*
  under some completion? That means its guard becoming false, **or** its
  `require` terms failing, **or** the decision changing under §5's equality —
  the winning partition's identity, the outcome kind, the grade, the error
  identity; *not* the representative payload — **not the require terms alone.**
  Round-level guards are falsifiable by
  responses that never touch the candidate: `absent(tag)` flips when a
  pending slot returns carrying `tag`, `participantsBelow(n)` flips when the
  n-th valid participant arrives, and `groupCount(pred, "==", n)` flips in
  either direction as groups form. A require-only check declares such a
  policy sound while the guard that selected it is still in flux.

Both halves are load-bearing for §12, which is the design's motivating case:
its firing guard is `absent("type:internal")` (second clause) and the
alternative it must lose to is an `always`-guarded one whose `require` has
not yet passed (first clause). A fold missing either half serves the relaxed
grade — the exact failure the feature exists to prevent.

Rank terms differ sharply in how much they permit:

#### Every predicate declares `mayBecomeTrue` / `mayBecomeFalse`

The fold asks "could this guard become true" and "could this `require` fail",
but those questions are only answerable if **every predicate** — not just every
rank term — declares how it can move. Stability classes for rank terms alone
leave the fold's two central questions with no defined way to answer them.

So each `Guard` and `GroupPred` declares two functions of round state, and
neither may be assumed from the predicate's shape:

| Predicate | `mayBecomeTrue` | `mayBecomeFalse` | Note |
|---|---|---|---|
| `atThreshold(n)` | count can still rise | **yes** — vote replacement can *remove* a vote (§5.1) | not monotonic, despite looking it |
| `requireTag(t, n)` | a pending slot's tag-set contains `t` | yes — replacement can drop a tagged vote | uses the §5.1 snapshot |
| `absent(t)` | never (participation is monotonic) | a pending slot's tag-set contains `t` | the one genuinely monotonic case |
| `participantsBelow(n)` | never | a further valid participant can arrive | mirror of `whenParticipants` |
| `groupCount(pred, cmp, n)` | yes | yes | `==` moves in **both** directions as groups form and empty |
| `responseTypeIs(t)` | a group's type is fixed once formed | only via replacement emptying the group | |
| `extractableAt(paths)` | yes | **yes** — representative replacement can change what extracts (§5) | easy to mistake for fixed |
| `notTie()`/`isTie()` | yes | yes | |
| `whenInFlight()` | never | yes, monotonically | |
| `capFired()` | yes, once | never | |
| `callerMayReceive(g)`, `callerHasRole(r)` | never | never | fixed for the round |
| `whenMethod(p)`, `whenFinality(b)`, `always` | never | never | constant for the round |
| `not(P)` | `P.mayBecomeFalse` | `P.mayBecomeTrue` | dual |
| `all(P…)` | all can be true together | any can become false | |
| `any(P…)` | any can become true | all can become false together | |
| `exists(P)` | `P.mayBecomeTrue` for some group, or a new group | `P.mayBecomeFalse` for every satisfying group | |

**Vote replacement is what makes this table non-obvious.** Because a later
response can *replace* an earlier vote from the same upstream (§5.1),
`atThreshold` is not monotonic and neither is `extractableAt` — both would be
if the round were append-only, and both are the kind of predicate an
implementer would naturally hard-code as "can only improve".

The coverage requirement mirrors the rank terms': **a predicate without both
functions declared fails to compile**, so a new primitive cannot be added
without stating how it moves.

**Every rank term declares a stability class — the table must be exhaustive,
because a term absent from it has no soundness model at all:**

| Rank term | Stability |
|---|---|
| `mostCommon()` | provable — lead over the runner-up must exceed the worst-case swing (§5.3) |
| `nonEmptyFirst()` | provable — a pending response can only add a *better* type, so a non-empty leader is stable |
| `largest()`, `highestAt(paths)` | **never provable while any completion remains** — an unseen response may be larger, or carry a higher value |
| `fromLeader({includeInfra})` | **never provable while any completion remains** — its input is outside the round entirely; see below |

The `largest()`/`highestAt()` row is what today's code already does: rule 27
refuses to short-circuit when `preferHighestValueFor` or
`preferLargerResponses` is in play, and when `preferNonEmpty` is set with an
empty leader ([rules.go:911-956](../../consensus/rules.go)).

#### `fromLeader` reads mutable state outside the round

`fromLeader` is the one rank term whose input is not the round's votes.
`newConsensusAnalysis` recomputes the leader from **live** cluster state on
every response arrival — `analysis.leaderUpstream = common.EvmLeaderUpstream(net, ctx)`
([analysis.go:82](../../consensus/analysis.go)) — so it can change between two
prefixes of the *same* response sequence, driven by block-head movement that
has nothing to do with this round. Under §5's definition that makes any
`fromLeader` decision unsound by default: a later evaluation can select a
different group with no new vote at all.

**Resolution: leave the live-leader read alone, and declare `fromLeader`
unstable while any completion remains.** Soundness is then achieved by refusing
to exit early, exactly as `largest()` and `highestAt()` already do — not by
redefining what the leader is.

Snapshotting the leader per round was the earlier proposal and it is the wrong
trade. It **changes winner selection** for every existing leader-based config
whenever the live leader moves mid-round, which contradicts §9's promise that
no deployment changes behaviour on upgrade. Buying finality for one rank term
with a silent behaviour change to three existing rules (3, 4, 5) is not a
bargain the razor permits: the unstable declaration costs those rules their
early exit and nothing else, and translated policies emit `.earlyExit` anyway
(§5.4), so **today's deployments keep both today's leader semantics and today's
timing.**

What the snapshot *was* also solving is real but belongs in the test harness,
not in production: replaying a fixed response sequence against live leader state
is not a deterministic comparison, so **Phase 1's per-prefix differential
injects a fixed leader schedule** — a seam, not a behaviour change. That also
makes leader *movement* testable on purpose, which a snapshot would have hidden.

### 5.3 The swing bound is precondition-dependent

Rule 27 short-circuits when `leadOverSecond >= remaining`, assuming each
pending response swings the margin by **1**, while §5.1 permits a swing of
**2**. That is not a defect, and the reason is what the new model must
inherit — and must not over-generalize.

Rule 27 requires `best.ResponseType == ResponseTypeNonEmpty`, and a
non-empty success is never freed for reselection. Under its own precondition
the candidate's votes are **immutable**, which bounds how far a rival can
close the gap — but immutability alone does not make `>= remaining` sound.

**The comparison must be strict.** At `lead == remaining` every pending
response can pile onto the runner-up and produce a *tie*, and a tie at or
above threshold resolves as a dispute, not as the candidate. §5.4 walks the
concrete case. So:

```
sound        lead >  remaining + replaceable(candidate)
early exit   lead >= remaining                            (legacy race, §5.4)
```

where `replaceable(candidate)` is zero exactly when the candidate holds only
non-empty successes — rule 27's precondition — and otherwise counts the
votes a pending slot could take away, bounded conservatively by `remaining`.

#### `remaining` means two different things, and conflating them hangs a round

Today's `remaining` is `maxParticipants - collectedResponses` against the
**raw** collected count ([rules.go:951](../../consensus/rules.go), matching
`hasRemaining()` at [analysis.go:227](../../consensus/analysis.go), which
documents why it is raw: a deduplicated duplicate already consumed a slot).

**That quantity is not the live-slot count, and must not be used for
soundness.** `collectedResponses` is `len(responses)`
([analysis.go:96](../../consensus/analysis.go)), and `responses` only grows
for **non-nil** results — the collector `continue`s on nil
([executor.go:528](../../consensus/executor.go)) while the loop index still
advances. Two paths send nil: cancellation before execution
([executor.go:776](../../consensus/executor.go)) and a slot returning neither
response nor error ([executor.go:793](../../consensus/executor.go)). Each
consumes a slot and leaves `collectedResponses` behind, so
`maxParticipants - collectedResponses` **overstates** live slots and can be
`> 0` after every participant has finished.

For rule 27 that over-count is merely conservative: a larger `remaining`
makes `lead >= remaining` harder, so a short-circuit is missed, never
wrongly taken. **For the fold it is a liveness bug.** The fold reads
`remaining > 0` as "a completion may still arrive", returns `wait()`, and
nothing ever arrives — an unresolvable round at normal exhaustion, with no
cap to rescue it (§5.5 only backstops the cap path; see the generalization
there).

So the uses are split into **three** distinct quantities. Collapsing any two
of them breaks something, and "physical work" versus "legal completion" is the
split most easily missed, because they coincide on every path except the one
that matters:

| Quantity | Meaning | Read by |
|---|---|---|
| `physicalOutstanding` | slots spawned minus slots that have reported, nil or not | the **drain** (§5.5 step 7) — it must keep reading until every participant has sent, and under `fireAndForget` that continues *after* the round resolved |
| `legalOutstanding` (equivalently, a `resolutionClosed` flag) | completions that may still **affect the outcome** | the **soundness fold** and the possible-tag-set union (§5.1, §5.2) |
| raw `maxParticipants - collectedResponses` | today's formula, phantom slots and all | **translated `.earlyExit` only** (§5.4) — bug-compatible on purpose, so no deployment's short-circuit timing shifts |

**`physicalOutstanding` and `legalOutstanding` diverge at exactly one point,
and it is not a corner case.** Cap fire zeroes `legalOutstanding` — that is the
whole mechanism by which §5.5 makes `wait()` unreachable — while under
`fireAndForget` the slots keep running, so `physicalOutstanding` stays
positive, deliberately. A design that keeps one counter must therefore either
lie to the drain or lie to the fold. Concretely, the invariant in §5.6 must be
stated over `legalOutstanding`; asserting it over physical work would be
**false at every capped `fireAndForget` round**, which is a supported and
intentional configuration, not an edge.

`legalOutstanding` is also what §5.1's possible-tag-set union is taken over:
the union covers upstreams that a slot which can still *affect the outcome*
might select. Taken over physical work it keeps tags in the set after the
round has closed; taken over the raw formula it keeps them there forever.

Rule 27's `>=` is therefore **not** the sound bound; it is the deliberate
early-exit race, and it is only available to a policy that opts in. A
stability class that inherits the constant without the strictness, or the
strictness without the precondition, is unsafe in opposite directions.

### 5.4 Sound finality vs. permitted early exit — a behaviour decision

Today's short-circuit is **deliberately not** completion-invariant. Rule 27
comments it explicitly: *"allow potential ties to short-circuit once
threshold is met and the lead is unassailable"*
([rules.go:949](../../consensus/rules.go)). With A=2, B=1, threshold 2 and
one response pending, `lead(1) >= remaining(1)` fires and A is served — but
if that pending vote joins B the completed round is a 2–2 tie, which the
tie rules resolve as a **dispute**. Result served early, dispute at
completion.

So "reproduce today's short-circuit points" and "enforce sound finality" are
**mutually exclusive**, and the spec must choose rather than assert both.

The resolution is to name both notions and let the policy declare which it
wants, rather than hiding the choice in a global mode:

| Notion | Condition | Meaning |
|---|---|---|
| **Sound** (default for hand-written policies) | `lead > remaining + replaceable`, plus the §5.2 fold | serve only when provably invariant under every completion |
| **Permitted early exit** (`.earlyExit(reason, pred)` on any outcome) | the rule's own predicate — see the table in §4.4 | serve when that predicate holds, accepting that some completions would have resolved differently |

For the `unassailable_lead` rule specifically, the two differ by exactly one
unit of `lead` (`>` versus `>=`), and that unit is the tie race. The other
two shortcuts are not lead-based at all, so the difference there is not a
bound but a whole predicate — which is why tolerance carries a predicate
rather than a relaxation of a shared one.

**The translator emits the early-exit form for every existing config**, so
no deployment changes behaviour or latency on upgrade. New hand-written
policies are sound by default, because silently serving a result where the
completed round disputes is the failure mode consensus exists to prevent —
but it is an opt-out, not an imposition.

Consequence for testing: short-circuit parity is asserted **against the
translated early-exit form**, and the sound form is asserted separately to
diverge per shortcut. Only `unassailable_lead` diverges "at the tie race";
the other two differ by a whole predicate, so each of the three needs its own
sound-versus-early pair (plan.md Phase 3).

### 5.5 Cap-fire lifecycle

`capFired()` (§4.3) is a guard, so it is only useful if the tree is
*evaluated* after the cap fires — and today it is not. On cap fire the
collection loop sets `waitCapped`, cancels the outstanding slots, and
`break`s ([executor.go:509-527](../../consensus/executor.go)); the outcome
sent is whichever winner the last *response arrival* produced. No decision
runs with knowledge that the cap fired.

**One exception, and the implementation must not double-evaluate through
it.** If the cap fires before *any* response arrives, `analysis` is still
nil, so the post-loop block does run `newConsensusAnalysis` +
`determineWinner` over the empty response slice
([executor.go:577-580](../../consensus/executor.go)). Step 4 below is
therefore already reached on that one path — the lifecycle generalizes it to
every path rather than introducing it.

That leaves a soundness trap: if outstanding slots still count as legal
completions after the cap, the §5.2 fold reports "could still change", the
tree returns `wait()`, and there is nothing left to wait for. The transition
must therefore be specified exactly, not left to the implementation:

1. **Cap fires.** Set `capFired` on the round state.
2. **Outstanding slots stop being legal completions.** `legalOutstanding`
   (§5.3) and every possible-tag-set drop to empty. `physicalOutstanding` is
   **untouched** — under `fireAndForget` those slots keep running and step 7
   still has to drain them. This step closes *resolution*, not execution.
3. **Cancel** the outstanding slots, unless `fireAndForget` — unchanged from
   today.
4. **Evaluate the tree exactly once**, with `capFired()` true and
   `whenInFlight()` false. Exactly once on *every* path: the no-response
   case already evaluates post-loop today, so this step replaces that
   evaluation rather than adding a second one.
5. **That evaluation must terminate**, and the backstop is *not* specific to
   the cap — see §5.6. With the legal-completion set empty the fold reports
   every outcome sound, so a well-formed policy terminates naturally. If a
   tree nonetheless yields `wait()`, the engine synthesizes the round's
   default failure rather than hanging.
6. **Send the outcome.** `sendOutcomeOnce` fires *here*, before any draining
   — this ordering is load-bearing, see below.
7. **Then keep draining the response channel, releasing late bodies**,
   without touching the frozen analysis. Every participant is guaranteed to
   send exactly once ([executor.go:506](../../consensus/executor.go)), and
   cancellation is best-effort — under `fireAndForget` the attempts continue
   deliberately. This mirrors what the short-circuit path already does:
   *"Analysis is frozen at the short-circuit moment. Any response that
   arrives after is NOT in analysis.groups, so releaseNonWinningResponses
   won't cover it. Release it here."*
   ([executor.go:532](../../consensus/executor.go))

**Step 6 before step 7 is the difference between a wait cap and no wait cap
at all.** Today the outcome is sent only *after* the collection loop
([executor.go:586](../../consensus/executor.go)), and the cap path reaches
that by `break`ing out. If the `break` is replaced by an in-loop drain — the
obvious way to implement step 7 — the caller no longer receives its outcome
at cap-fire time but at *last-participant* time, which is exactly the latency
the cap exists to bound. Under `fireAndForget`, where outstanding attempts
are deliberately not cancelled, that wait is bounded only by the slot and
request timeouts. So the drain must run **after** the outcome is published,
off the caller's path.

Step 7 is otherwise not a new requirement introduced by this design — it is a
gap the design must not inherit. The cap path `break`s out of collection
([executor.go:526](../../consensus/executor.go)), `responseChan` is read
nowhere else, and `releaseNonWinningResponses` iterates only the `responses`
slice, which stopped growing at the break. Late bodies on a wait-capped round
therefore appear never to be released today, where the short-circuit path
releases them explicitly.

### 5.6 Termination is a property of an empty completion set, not of the cap

§5.5 is where the cap makes `wait()` unreachable, but framing it as *"the one
place `wait()` must be impossible"* is too narrow, and the narrow version
leaves a reachable hang. `wait()` is a first-class outcome the policy may
return at any evaluation, including the one that follows the **final ordinary
slot** on a round with no cap configured at all. Nothing in §5.5 covers that
path, and §5.3's `remaining` over-count makes it reachable rather than
theoretical: a phantom live slot keeps the fold from proving soundness, the
tree returns `wait()`, and there is nothing left to wait for.

So the backstop is stated once, over the condition rather than the cause:

> **Whenever the legal-completion set is empty, `wait()` is not a permitted
> outcome.** If the tree yields one, the engine synthesizes the round's
> default failure, emits a metric, and logs the alternative that asked to
> wait. This holds identically for cap fire, normal exhaustion, and any
> future path that empties the set.

Two invariants follow, and both are worth asserting directly rather than
inferring from policy discipline: the completion set is empty **iff
`legalOutstanding` is zero** (§5.3 — *not* `physicalOutstanding`, which stays
positive at a capped `fireAndForget` round by design), and no round can be
published with a non-final verdict. The metric is a bug signal — a well-formed
tree never trips it — so it should be zero in production and non-zero only
when a policy or the fold is wrong.

## 6. Grades and caller authorization

`accept(grade)` names the acceptance level a round was served under. Grade
names are static literals (§3.1) from a finite set the policy declares —
which is what makes everything in this section decidable at load.

### 6.1 There is no grant plumbing today — this feature must build it

[`common.User`](../../common/user.go) carries `Id`, `RateLimitBudget` and
`AllowClientDirectives` — no roles, no grades. Introducing **either** therefore
requires defining, for **every** auth strategy (`secret`, `database`, `jwt`,
`siwe`, `network`), where a caller's grant comes from. Grades and roles share
that plumbing and the same invariant; they differ only in what the engine does
with them (§6.5).

**The claim-to-value mapping §6.2 asks for already exists in JWT, and should
be copied rather than invented.** The strategy extracts `sub` as the id, and
then reads a **configurable named claim** into a capability field
([auth/strategy_jwt.go:123-132](../../auth/strategy_jwt.go)):

```go
claimName := s.cfg.RateLimitBudgetClaimName   // default "rlm"
if v, exists := claims[claimName]; exists { user.RateLimitBudget = ... }
```

That is the same shape a grade grant needs — operator names the claim in
config, the strategy reads that one claim and nothing else. So for JWT this
is closer to wiring than to a new subsystem; the genuinely new work is the
grade-set type, the startup cross-validation (§6.2), and the four other
strategies.

The pattern already exists and should be followed verbatim.
`User.AllowClientDirectives` is a capability field with this invariant:

> Capability fields are populated ONLY by auth strategies; the trusted-header
> path (`NormalizedRequest.SetUserFromTrustedHeader`) sets `Id` alone, so an
> unvalidated header can never grant a capability.

Modelling grade grants the same way answers the trusted-header question by
construction: `trustUserIdHeader` cannot widen a grant because it never
populates capabilities.

### 6.2 Grants must be a finite, declared mapping

Grants cannot be read from arbitrary claim values, because then no static
validation is possible — an unbounded claim space cannot be checked against
a finite grade set. Each strategy therefore declares a **finite mapping**
from a named claim/column value to a grade set, enumerable at load.

Two things follow, both checked at startup:

- a mapping naming a grade no policy can produce → error;
- a **non-default** policy grade that no mapping covers → error (it could
  never be served). The `default` grade (§6.3) is explicitly exempt: it is
  the fallback for callers with no matching grant, so requiring a mapping
  for it would reject every valid multi-grade config.

### 6.3 The default grade — no inferred ordering

Grades are arbitrary names with **no strictness ordering**; "the strictest
grade" is not a well-defined concept and must not be inferred from
alternative order (an earlier alternative is not necessarily stricter). A
policy declaring more than one grade must therefore mark exactly one as the
**default** — the grade served to callers with no matching grant, including
unauthenticated ones. Omitting it is a startup error.

| Situation | Grant |
|---|---|
| Policy produces exactly one grade (every config today, and every translated legacy config) | every caller may receive it; no declaration needed |
| Policy declares more than one grade | a `default` grade is mandatory; explicit mappings are mandatory for the **non-default** grades only |

Existing deployments have one grade, so nothing changes for them.

### 6.4 Two enforcement layers

1. **Engine-enforced (mechanism).** The decision carries a grade; the engine
   independently checks it against the caller's grant before serving. A
   round resolving at a grade the caller may not receive is **withheld as a
   dispute, never silently downgraded**. This cannot be forgotten, because
   it is not in the policy.
2. **Policy-visible (policy).** `callerMayReceive(grade)` is available as a
   guard so a policy may additionally narrow — it can never widen.

The second layer is a latency fix, not belt-and-braces: with enforcement
alone, a strict-only caller waits out the whole round, the round resolves at
a relaxed grade, and only then is the caller refused — having paid full
latency for an answer it was never allowed to receive.

### 6.5 Roles: input routing, deliberately weaker than grades

A **role** answers "who is calling", and is used only to select which
alternative applies. It is the weaker of the two caller capabilities, and the
weakness is the point.

**Grants follow grades' plumbing exactly** (§6.1, §6.2) — a capability field on
[`common.User`](../../common/user.go) populated **only** by auth strategies, so
`trustUserIdHeader` can never grant a role. Per strategy, mirroring each one's
existing `RateLimitBudget` shape: a static `roles` list on `secret` / `network` /
`siwe`; a named claim on `jwt` (`rolesClaimName`, the
`RateLimitBudgetClaimName` pattern); a config-declared column mapping on
`database`, failing closed to no roles.

Four rules keep roles from becoming a second authorization system:

1. **The engine never enforces a role.** Unlike a grade (§6.4), no outcome is
   withheld because of one. A policy whose role-guarded alternatives all miss
   falls through like any other unmatched guard.
2. **`callerHasRole(role)` takes a literal** (§3.1), so the roles a *policy*
   tests are a finite declared set even though the granted set is open. That is
   what makes the guard checkable at load without constraining operators'
   role vocabularies.
3. **Roles are never emitted as a metric label**, and this is a hard rule rather
   than an oversight: role values come from claim data, so labelling by role
   would reintroduce exactly the unbounded-cardinality hazard §3.1 closes for
   grades. The *decision record* (§10) may carry the role for a sampled round;
   the metrics pipeline may not.
4. **No coverage cross-validation.** §6.2 errors when a non-default grade has no
   mapping, because an unproducible grade is dead policy. Roles get no such
   check: a policy may legitimately test a role no configured strategy grants
   yet — staged rollout, or a role granted by an operator's IdP outside this
   config. Unmatched is not an error, it is a fallthrough.

Grades remain the only engine-enforced capability. Adding roles costs one more
capability field per strategy and buys per-audience routing that grades could
only express by abusing output enforcement (§4.3).

## 7. Engine boundary

| Knob | Decision | Trade-off accepted |
|---|---|---|
| **24 winner rules + 3 short-circuit rules** | **policy** | The point of the feature. Cost: the default policy must reproduce all 24 exactly (§11). |
| **Decision-time partitioning** (`partitionBy`) | **policy** | Forced by parity: `preferHighestValueFor` already re-partitions by value and spreads quotas across the partition (§4.1). Cost: "which responses agree" is now two-layered — hash identity, then an optional decision view over it. |
| **Hash identity** (`ignoreFields`, canonical hashing) | **mechanical** | Computed once, before the decision, off the policy's hot path. Cost: a policy cannot define per-caller or per-grade *identity*; it can only re-partition what identity produced. |
| **Wait caps — timer, arming, and the arming gate** | **mechanical, in full** | The policy is evaluated on response arrivals, so it cannot wake a round when nothing arrives; the timer is the event source that breaks the collection `select` ([executor.go:425](../../consensus/executor.go), [:511](../../consensus/executor.go)). Critically, **arming is quota-aware** — it is withheld until collected responses cover every quota tag with distinct upstreams ([executor.go:473](../../consensus/executor.go)) — and that gate is load-bearing, not a workaround: without it the countdown starts off a response set that cannot yet satisfy the composition quota, "converting every such round into a retryable composition dispute". A guard evaluated *after* the timer fires cannot reconstruct an interval that must *begin* when quotas become satisfied. Also mechanical for the same reason: the `isNoAttemptResult` suppression, and the split between `maxWaitOnEmpty` arming and `maxWaitOnResult` tightening. |
| **Wait-cap *resolution*** | **policy**, via `capFired()` | The policy decides what a capped round resolves to — the one part that is a decision rather than a clock. It changes nothing about when the cap fires. Requires the §5.5 lifecycle: the engine must evaluate the tree once *after* the cap fires (today it does not), with outstanding slots removed from the completion space so `wait()` is unreachable. |
| **`punishMisbehavior` targeting** | **mechanical, grade-aware** | The goal — judging dissenters against the grade that served rather than a bare winner — is met by passing the served grade and candidate to the existing differ. A targeting *output* would expand the closed outcome vocabulary (§4.4) for no capability the mechanical form lacks. |
| **Winner-composition quotas** (`requiredParticipants[].minAgreement`) | **mechanical** | See §7.1. A single post-decision gate applied to whatever the tree returns, unchanged from today. Cost: composition failure cannot be a policy-visible branch, so a policy cannot say "if the quota fails, try this other acceptance instead". Nothing observed asks for that. |
| **Participant selection** (`maxParticipants`, `minParticipants`, [quota.go](../../consensus/quota.go)) | **mechanical** | Choosing *who to ask* overlaps `selectionPolicy`'s job. Cost: the composition quota is split — `minParticipants` (pre-round) and the §7.1 gate (post-round) live in different places. |

### 7.1 Winner composition stays mechanical — and is not one of the 24 rules

The decision surface is **not** exhausted by the 24 winner rules and the 3
short-circuit rules. Two pieces of decision logic sit outside both lists, and
a design that accounts only for the 27 will pass its own parity gate while
diverging:

1. **`enforceWinnerComposition`** ([executor.go:924](../../consensus/executor.go))
   — a post-decision gate every rule's output flows through, documented as
   *"the single enforcement point: every rule's output flows through here, so
   no individual rule needs to be composition-aware."*
2. **The provisional-dispute suppression in `shouldShortCircuit`**
   ([executor.go:829-833](../../consensus/executor.go)) — while a composition
   dispute is outstanding and responses may still arrive, **all three**
   shortcuts are blocked, because a later response can still complete the
   quota.

**`require` is the wrong shape for this, not merely an under-specified one.**
A failing `require` inside an alternative *falls through to the next
alternative* — that is exactly what §12's graded example depends on, and what
§11 relies on for rule 5. Today a failing quota does the opposite: it
**converts the winner into `ErrConsensusCompositionDispute`, with no
re-decision**. Translating `minAgreement` into `require(requireTag(...))`
would therefore let a rule-2 winner that misses the quota fall through to
rule 3+ and serve a different result, where today it disputes. The two are
not the same operator, and §4.5's vocabulary has no third one: `.orReject`
fires only when *no* alternative matched, so there is no way to say "apply
this check to whatever the whole subtree produced".

So the gate stays mechanical, in full, including three pass-throughs that
have no expression in the vocabulary either:

| Pass-through | Code |
|---|---|
| tx-broadcast methods exempt entirely | [executor.go:928](../../consensus/executor.go) |
| synthesized error winners pass — `groupOf` returns nil for a freshly constructed dispute error | [executor.go:932](../../consensus/executor.go) |
| infrastructure-error group winners pass | [executor.go:932](../../consensus/executor.go) |

and its suppression is expressed as an engine-level `wait()` while responses
remain, matching today's provisional semantics.

`requireTag` (§4.2) therefore exists **only for the new graded-acceptance use
case** (§12), where fallthrough *is* the wanted behaviour. It is not the
translation target for `minAgreement`. Nothing in today's data forces
composition into policy; folding it in would cost a new tree-level operator,
three transcribed pass-throughs, and a class of parity risk, for no observed
capability. Weakest hypothesis: delete the commitment.

## 8. Realtime constraints on the stdlib

| # | Constraint | Why |
|---|---|---|
| C1 | **No JS, and no compilation, on the request path.** | §1's 1.75M evals/sec; §3.2's eager-compilation rule. |
| C2 | **Primitives are declarative descriptions, not eager operations.** | Required for the load-time trace (§3). |
| C3 | **Round state is incremental, and reversible.** | Today `newConsensusAnalysis` rebuilds from the full slice on every arrival ([analysis.go](../../consensus/analysis.go), [executor.go:545](../../consensus/executor.go)) — O(N²). State is **not append-only**: a later response from an already-voted upstream replaces its vote (§5.1), so it must support removing a vote, deleting an emptied group, and recomputing extrema, ties and partitions. |
| C4 | **Every primitive is Go, O(G) worst case, allocation-free on the steady path.** | Marshalling per evaluation is fatal here. |
| C5 | **Tri-state decision with a stability test over all legal completions.** | §5. |
| C6 | **Vocabulary is votes, partitions and groups**, not upstream lists. | §4. |
| C7 | **Side effects are declared outputs, not in-pipeline actions.** | Keeps the tree pure: traceable, testable without a live tracker, safe to evaluate N times per round. |
| C8 | **Per-evaluation cost is a static property of the compiled tree, and is bounded by an enforced ceiling.** | An operator should see what a policy costs before production — and a policy that exceeds the cost budget must fail the reload, not merely report a large number. Config-derived loops make an unbounded tree reachable from a valid policy. |

Net cost per evaluation is **O(A × G)** — A alternatives, G groups
(≤ participants) — with no allocation.

**`A ≈ 5–25` is an assumption, so it is enforced rather than asserted.** The
freeform layer exists to run loops and helpers over config (§3.1), so
`tiers.flatMap(...)` over a large config list can emit thousands of
alternatives from a policy that is entirely valid under §3.1 — every argument
a literal, no runtime value in a test. Nothing in the tracer bounds the
output, and the resulting tree is walked on **every response arrival**, so
the cost claim above would silently become false in production while the
policy stayed "valid".

Compilation therefore **rejects an oversized tree at load**, against ceilings
on alternative count, nesting depth, and total static cost. A reload that
exceeds a ceiling fails and leaves the running configuration untouched (§3.2),
with an error naming the measured value and the limit.

**The maximum is immutable; configuration may only lower it.** A fully
configurable ceiling would not enforce §1's bound at all — it would relocate
it, letting an operator admit a tree arbitrarily beyond the cost the design
promises and turning "at or below the 24-closure walk" back into an
aspiration. So there are two numbers: a **hard maximum**, calibrated by
benchmark against the walk it replaces and not settable from config, and an
optional **operator ceiling** that may be set anywhere below it. Raising the
hard maximum is a code change with a benchmark to justify it, which is the
point — it makes the cost promise falsifiable in review rather than
per-deployment. Defaults are chosen so today's 24-rule default tree and every
translated legacy config sit well inside both.

## 9. Backward compatibility — config layer only

Existing configs keep working through a **load-time translation**, the same
architecture as `selectionPolicy`'s legacy handling
([common/legacy/translate.go](../../common/legacy/translate.go),
[eval_synthesis.go](../../common/legacy/eval_synthesis.go)): control flow
becomes synthesized policy, tunable scalars stay config.

| Today | After translation |
|---|---|
| `disputeBehavior` (4 values) | which branch the fallthrough takes |
| `lowParticipantsBehavior` (4 values) | a `participantsBelow()` guard and its branch |
| `preferNonEmpty`, `preferLargerResponses` | terms in the rank chain |
| `preferHighestValueFor` | the full §4.1.1 composition: `exists(all(valid, extractableAt(paths)))` guard + `eligible(valid)` + `partitionBy(valueAt(paths))` + `eligible(atThreshold(n))` + `rank(highestAt(paths))` + `reject(kind, message)` — the partition is what makes the rule's own threshold count across encodings, the leading `eligible(valid)` is what keeps infrastructure errors out of it, and the second `eligible` stage is what puts threshold before extremum (§4.1) |
| `requiredParticipants[].minAgreement` | **unchanged — mechanical** (§7.1). Not translated into `require(requireTag(...))`: `require` falls through to the next alternative, where the existing gate converts the winner into a composition dispute |
| 24 ordered rules + 3 short-circuit rules | one nestable ordered-alternatives tree; short-circuit is `wait()` |
| current short-circuit timing | `.earlyExit(reason, pred)` on the corresponding outcome — including the **pass-through-error** shortcut, which is not an `accept` — preserving each rule's own suppression conditions and each shortcut's own predicate and telemetry reason (§4.4, §5.4) |

- A config setting none of these gets the **default policy** — today's 24
  rules in the vocabulary — behaviourally identical.
- No deprecation warning: the old knobs are a **supported shape**, unlike
  `selectionPolicy`'s `evalFunction`.
- Setting both the old knobs and an explicit policy on the same block is a
  startup validation error.

### 9.1 Rollout, rollback, and the one-way door

§1 leaves **exactly one runtime decision path** — no legacy-aware branching
survives config load. That is right for the code and creates an operational
problem the design must own: **there is no runtime kill-switch back to the
legacy rules.** A parity bug found in production is reverted by redeploying the
previous binary, not by flipping a flag.

Consensus runs for every chain and vendor, so an offline differential (§11) is
necessary but not sufficient for a hot-path swap of this blast radius.

| Stage | What runs | Revert |
|---|---|---|
| **1. Build-flag gated** | new engine compiled in, disabled; legacy rules serve | flag |
| **2. Shadow compare (prod)** | both run per round; **legacy's outcome is served**; the engine's is computed and compared; divergences recorded with the round's full decision record (§10) and alerted on | flag |
| **3. Canary** | engine serves on a small set of networks/projects; legacy still computed and compared | flag |
| **4. Cutover** | engine serves; legacy code deleted | **binary redeploy** |
| **5. Legacy deletion** | the 24 rules and 3 shortcuts removed | — |

**Stage 2 is the load-bearing one** and it is the strongest parity evidence
available — real traffic, real vendor quirks, real timing, on shapes no
generated corpus will produce. Exit criterion: zero unexplained divergences over
a stated traffic volume and window, with every divergence either fixed or
explicitly accepted as an intended difference. This is also the only stage that
can catch the §7.1 surfaces, whose whole hazard is that a green rule-level
differential says nothing about them.

Shadow-mode cost is one extra tree walk per round, which §8 bounds; it does not
double upstream traffic, because both decisions read the *same* collected
responses. Mirroring machinery already exists in the codebase for a different
purpose (`probeExcluded` shadow traffic, `ShadowUpstreamConfig`), but this is a
decision-level compare, not a request mirror — do not reuse that path.

**Mark stage 4 as a one-way door.** After it, reversibility is redeploy-only
and the translation becomes a permanent backward-compatibility contract —
including `.earlyExit`'s deliberate bug-compatibility with the raw `remaining`
counter (§4.4.1). Deleting legacy (stage 5) should lag cutover by at least one
release so the redeploy path stays real rather than theoretical.

## 10. Observability

- **Served grade** on the response and as a metric label — bounded because
  grade names are static literals from a declared set (§3.1).
- **Decision records** (candidate, partition, grade, alternative fired,
  finality verdict and *why* it was not sound) — **sampled, not per round, and
  the selection-policy model is the wrong one to copy wholesale.**
  [internal/policy/decision.go](../../internal/policy/decision.go) is a
  reasonable *shape*, but it is built once per 15s background tick and says so
  (*"Built fresh each tick; never persisted"*), carrying maps and slices per
  record. Producing one of those per consensus round is ~350k allocations/s on
  the request path, which contradicts C4 outright.

  So: a **preallocated ring** of fixed-size records, written under a sampling
  rate that defaults low, with unconditional capture only for rounds that are
  *already* exceptional — the §5.6 backstop firing, a grade withheld by §6.4,
  a ceiling rejection. Full-rate capture is a debug-only mode with the cost
  documented, like `Engine.SetStepLogEnabled`. Steady-state allocation stays
  zero and the record path is off the hot path by construction, not by
  discipline.

  Exposure reuses the **existing admin RPC registry** (`erpc_*` methods in
  [erpc/admin.go](../../erpc/admin.go)) rather than adding a surface — the
  §14 non-goal stands (Appendix B).
- **Static cost report** for the compiled tree, plus the enforced ceilings it
  was checked against and the headroom remaining (C8, §8).
- Existing `punishMisbehavior` metrics unchanged; the differ now receives
  the served grade (§7).

## 11. Validation: the 24 rules

The factoring is a claim about existing code, so it is verified against the
rules most likely to resist. **All six factor** — five chosen for cascading
actions, crossed enums or non-standard scopes, and one (rule 2) that a
review surfaced as the rule which actually broke the original grouping
boundary:

| Rule | Resists because | Resolution |
|---|---|---|
| 2 — `preferHighestValueFor` | **re-partitions individual results by extracted value** ([rules.go:117](../../consensus/rules.go)), `agreeingResults` spreads composition quotas across that partition ([executor.go:983](../../consensus/executor.go)), and it filters by threshold *before* ranking — so it is neither a rank over hash groups nor a rank-then-require | `exists(all(valid, extractableAt(paths)))` guard + `eligible(valid)` + `partitionBy` + `eligible(atThreshold(n))` + `rank(highestAt)` + `reject(kind, message)` — the full composition in §4.1.1. This rule drove decision-time partitioning into policy scope **and** forced the scope selector to generalize from an enum into a predicate |
| 3 — `onlyBlockHeadLeader` on dispute | 3-way cascade ending in *the leader's own error, including infra* | `rejectWith()` + `fromLeader({includeInfra: true})` |
| 4 — `onlyBlockHeadLeader` on low participants | same shape, excludes infra errors | same primitive, `includeInfra: false` |
| 5 — `preferBlockHeadLeader` | condition ORs two enums and requires "leader non-error exists" | two alternatives; the existence check becomes a `require` that falls through |
| 18 — low participants + accept-most-common | four outcomes cascading under one condition, tie→dispute branch | nestable control (§4.5) + `notTie()`/`isTie()` + `rejectWith()` |
| 21 — identical infra errors at threshold | operates on **infrastructure-error groups**, excluded from `getValidGroups()` | `eligible(responseTypeIs(infrastructureError))` — inexpressible while the group scope was a fixed `valid`/`all` enum |

Evidence the tangle is real rather than incidental: **several rules carry
explicit negative conditions purely to avoid overlapping a later rule** —
rules 3 and 5 both re-derive "no valid group meets threshold" so as not to
steal the threshold case from rule 19. Under free ordering those vanish; the
priority list is ordered but *not re-orderable*.

**Scope of this gate — read with §7.1.** The 24 rules are not the whole
decision. `enforceWinnerComposition` and the provisional-dispute suppression
in `shouldShortCircuit` are decision logic **outside** both rule lists, so a
green differential over the 24 says nothing about them. They stay mechanical
and unchanged, and are asserted **separately**, against their own behaviour
rather than against a rewritten equivalent — including the three
pass-throughs (§7.1) and the "quota may still complete" hold.

### 11.1 Three surfaces the parity gate does not cover

The differential proves the *decision* matches. Three high-risk surfaces need
their own stated approach, because a green parity run says nothing about them:

- **Validator soundness — property test and a differential oracle.** The full
  approach is §3.1.3, and it is a deliverable rather than a test-suite nicety:
  generate eval functions including the hazardous shapes and assert every
  accepted policy yields a runtime-independent tree with literal labels; and
  trace each accepted policy twice against recorders seeded with different fake
  runtime state, requiring structurally identical trees. Per-pair rejection
  tests confirm known bad shapes only, and this validator has had a fresh hole
  found in it on every review pass (Appendix B).
- **Load-time interrupt and divergence (§3.3).** A runaway trace — `while(true)`,
  or a quadratic config `flatMap` — must be interrupted within budget, the
  runtime discarded rather than pooled, the reload failed with elapsed and
  limit, and the running config left untouched. Assert the per-tree budget and
  the aggregate ceiling both hold. Copying `Slot`'s soft timeout
  ([slot.go:232-237](../../internal/policy/slot.go)) fails this test by design.
- **A standing performance gate in CI.** §1's bound is "per-evaluation cost at
  or below the 24-closure walk it replaces" and §8's hard maximum is calibrated
  against that walk — but a bound nothing measures on every change is an
  aspiration. Gate per-evaluation ns/op **and** allocs/op (C4 requires zero on
  the steady path) against a recorded baseline, as a merge blocker alongside
  the parity gate.
- **Reversible-state differential fuzz.** The incremental round state (C3) —
  vote replacement, group emptying, extrema/tie/partition recompute — is the
  classic place for a subtle bug. Fuzz random arrival *and replacement*
  sequences against the current `newConsensusAnalysis` full rebuild, comparing
  at every prefix. §11's "permutations and every prefix" implies it; name
  replacement explicitly, since it is the case that makes the state non-append-only.

**Gate:** all 24 rules rewritten in the vocabulary, with equivalence proven
over **response permutations and every prefix** — not merely completed round
shapes — before any engine work merges (plan.md Phase 1).

## 12. Worked example: graded acceptance with a relaxed grade

```js
(round, ctx) => round
  .when(always, r => r
    .rank(mostCommon)
    .require(atThreshold(2),
             requireTag("type:internal", 1),
             requireTag("type:external", 1))
    .accept("standard"))

  .when(all(absent("type:internal"), callerMayReceive("fallback")), r => r
    .rank(mostCommon)
    .require(atThreshold(2),
             requireTag("type:external", 2))
    .accept("fallback"))

  .orReject(COMPOSITION_DISPUTE)
```

**No `activation:` / `absentGroups:` schema fields.** The guard is a
predicate; a future `groupBelow(tag, n)` is another predicate, not a new
schema field.

`rank` precedes `require` because the staged grammar (§4.6) does not expose
`require` until a candidate exists, and `"standard"` / `"fallback"` are
checkable against the declared grade set at load (§6.2) because §3.1 requires
them to be literals.

| Situation | `absent("type:internal")` | Outcome |
|---|---|---|
| all tiers agree | false | `standard` |
| first-party up, **dissenting** | false — it participated | falls through → `COMPOSITION_DISPUTE` |
| first-party down / cordoned | true | `fallback` (same predicate; the cordon path needs no special case) |
| first-party merely **slower**, still in flight | provisionally true | **`wait()`** |

### The failure this design prevents

Two third parties agree at t=50ms while the first-party node is simply
slower and still in flight. `absent` is *currently* true, so a naive
implementation serves a relaxed answer to a round that would have been
`standard` 20ms later — precisely the failure the feature exists to prevent.
The guard alone does not stop it; the stability test does: while any pending
slot's possible-tag-set (§5.1) contains `type:internal`, the alternative is
not sound and the outcome is `wait()`.

**This case exercises both halves of §5.2's fold, and either half alone
serves `fallback`.** The firing alternative's *guard* — `absent("type:internal")`
— is falsifiable, which a require-only check misses; and the earlier
alternative is guarded `always` with a *require* that has not yet passed,
which a guard-only check misses. Any implementation that treats the fold as
"check earlier guards, then check my requires" fails this test while looking
correct, so it is the named regression for the fold itself, not only for
tag-aware finality.

Note this policy is written **sound**, not `earlyExit` (§5.4) — a relaxed
grade is exactly where a tie race must not be tolerated.

Named test case: *two third parties agree while a slower first-party
upstream is in flight → the round waits and serves `standard`, never
`fallback`.*

## 13. Decisions taken, and what remains open

### 13.1 Why the engine exists: three extensions, then maintainability

The repo's razor makes this a question the document has to answer, not assume.
"The 24 rules are tangled" is an argument about **form**, and the razor states
that short/tidy is *"provably neither necessary nor sufficient"*. The argument
that carries is **extension** — unseen-but-plausible policies the engine
decides correctly that a 25th rule cannot.

**On extension, three cases are now named**, where an earlier draft could name
only one and fell back on maintainability alone:

| # | Policy the engine decides correctly | Why a 25th rule cannot |
|---|---|---|
| 1 | **Graded acceptance** — `standard` when the first party participates, `fallback` when it is *absent*, never when it merely dissents or is in flight (§12) | Needs a predicate over *participation* plus the §5 fold; no enum setting expresses it |
| 2 | **Per-audience policy** — a caller's role selects which acceptance shape applies (§6.5) | The rule list has no caller dimension at all. Bolting one on means a config surface per role × the existing enums |
| 3 | **Per-finality policy** — different acceptance for `realtime` vs `finalized` (§4.3.1) | Every rule's condition would need a finality test *and* a config surface keyed by finality — the four-enum × five-knob cross-product multiplied by a fourth axis |

Cases 2 and 3 share the shape that makes them extensions rather than
conveniences: each adds an **orthogonal axis** to the decision. In a priority
list, a new axis multiplies the rules; in a composed vocabulary it is one guard.
That is a difference in *extension* — unseen-but-plausible policies decided
correctly — not in form, which is exactly what the razor asks for.

**Maintainability is then a secondary argument, not the load-bearing one.** It
still holds — rules 3 and 5 duplicate a negative condition solely to avoid
stealing rule 19's case; the behaviour surface is a cross-product nothing
enumerates; §7.1 found two decision paths outside both rule lists — but it is a
cost the *team* pays rather than a capability an operator gains, so it is offered
as reinforcement and the extensions above carry the razor.

**The weaker alternative, designed out rather than named.** Add the grade
capability (§6) plus one acceptance-tier rule to the existing 24 — Phase 0 +
Phase 4 only. Concretely: a 25th rule, ordered before rule 19, whose condition
is `lowParticipantsBehavior == acceptTierFallback && absent(tag) &&
callerMayReceive(grade)` and whose action is today's accept-most-common
restricted to the tier. It delivers extension 1 and neither of the others. What it also does not
deliver: the finality model (§5) still would not exist, so the tier rule would serve `fallback` in
the §12 race — the one failure the feature exists to prevent — unless the
soundness fold is built anyway, which is most of Phase 3. **That is the real
comparison**: the weaker option is Phase 0 + Phase 3 + Phase 4, not Phase 0 +
Phase 4, and what it saves is the vocabulary, the authoring surface and the
translator. It remains a defensible choice; it is simply a smaller saving than
it first appears.

### 13.2 Authoring surface: freeform JavaScript (decided)

**Decided in favour of freeform JS with static validation; the data-only
document is a rejected alternative (Appendix A).** The decisive factor is
authoring ergonomics over config: a policy expresses its structure with the host
language's loops and helpers —

```js
const tiers = ["type:internal", "type:external", "type:archive"]
const quota = tiers.map(t => requireTag(t, 1))
```

— which a data document cannot do. It writes the list, and deployments with many
tiers either repeat structure or generate the document from something upstream,
which relocates the problem rather than solving it. Continuity matters too:
`selectionPolicy` already takes a JavaScript `evalFunction`, so operators author
both policy surfaces the same way rather than learning a second, weaker one.

**The cost is real and is not being minimised.** It is carried in three places,
all of which exist *only* because the authoring surface is a host language:

| Cost | Where |
|---|---|
| A soundness-critical static analysis over the eval function's AST | §3.1 |
| A bounded, interruptible load-time execution phase | §3.3 |
| A staged builder type system to make ordering structural | §4.6 |

**And the honest record: this analysis has been the design's most defect-prone
component.** Five review passes each found a fresh hole in it — taint roots,
argument positions, a `Requirement` subtype that made the vocabulary untypeable,
a circular `candidate` lift, a missing branch-callback parameter kind, absent
`always`/`first`/`not`, an unstaged builder (Appendix B). Every one existed only
to police freeform JS; none is reachable in a document.

That history is why **§3.1.3 makes validator soundness a shipped deliverable**
— a property test over generated policies plus a two-trace differential oracle —
rather than something assumed from passing the tests written for it. If that
obligation proves unsustainable in review or in maintenance, Appendix A is the
designed-out fallback and switching does not change the runtime, the primitive
vocabulary, or the tree: both forks target the identical Go tree, and the fork
is only about how it is authored.

### 13.3 Still open

- **Block-availability predicates (§4.3.1).** `whenFinality(bucket)` covers the
  finality axis. A policy keyed on the *available block range* — "upstreams can
  only serve N..M, so accept differently" — needs the lane set
  ([engine.go:368](../../internal/policy/engine.go)) and project-level
  `blockAvailability` plumbed into round state, and a predicate shape nothing
  has yet forced. Deliberately not guessed.
- **Partition depth (§4.1).** Only value-partitioning is required today. Is
  a general `partitionBy(key)` warranted, or should it stay narrow until a
  second use appears?
- **Primitive naming.** §4's spellings are illustrative; the axes are the
  commitment.
- **Cold start on an invalid policy.** §3.2 specifies that a failed *reload*
  leaves the running config untouched, but first boot has no running config:
  `LoadConfig` failure returns up through `main`
  ([main.go:344-347](../../cmd/erpc/main.go)) and the process does not start.
  That is the correct default — refusing to serve beats serving with an
  unknown decision procedure — but it means a bad policy is a crashloop under
  an orchestrator, so the failure must name the offending policy and limit in
  the boot log. Confirm this is the wanted behaviour rather than last-good
  fallback.
- **Per-arrival re-walk vs. dirty-tracking.** §8 commits to walking the tree on
  every arrival at O(A × G). Most arrivals cannot flip the decision, so
  predicate-level invalidation could skip most of that work. Out of scope for
  v1 — the cost is already bounded and measured — but it is the obvious next
  optimisation if the ceilings ever bind.

## 14. Non-goals

- **No new endpoint or routing surface.** Decision-record and cost-report
  exposure are methods on the existing admin RPC registry
  ([erpc/admin.go](../../erpc/admin.go)), not a new endpoint (§10).
- **No policy-defined hash identity** — partitioning is a decision-time view
  over mechanical hashing, not a replacement for it (§7).
- **No policy-owned winner composition** — `minAgreement` stays a mechanical
  post-decision gate; `requireTag` serves the graded use case only (§7.1).
- **No sandbox.** §3.1's validation bounds a policy's *shape*, not its
  *effect*. Policy authors are trusted operators (§3.1.2); this design does not
  support policy from less-trusted parties.
- **No runtime attributes on `ctx`** — runtime state is reached only through
  `round` primitives (§3.2).
- **No interpreter and no compilation on the request path** (§3.2).
- **No enum of activation predicates.** Predicates are primitives (§12).
- **Roles are not authorization** — `callerHasRole` selects an alternative and
  nothing else; grades remain the only engine-enforced capability (§6.5).
- **Not a port of the selection stdlib.** Different nouns, different
  execution model, different constraints (§8).

---

## Appendix A — Rejected alternative: a declarative policy document

**Status: rejected (§13.2).** Kept in full because it is the designed-out
fallback: if §3.1.3's validator-soundness obligation proves unsustainable, this
is what to switch to, and switching changes neither the runtime, the primitive
vocabulary, nor the compiled tree.

The shape was: the policy is **data**, not a program. An operator writes a
document; at load it is validated against a schema and materialized into the
same finite Go tree §3 produces. Identical runtime properties.

**Why it was rejected:** authoring ergonomics over config (§13.2). A document
cannot express `tiers.map(t => requireTag(t, 1))`; it writes the list, and
deployments with many tiers repeat structure or generate the document upstream.
`selectionPolicy` also already takes JavaScript, so a document would mean two
authoring models for two policy surfaces.

**What it would have bought** — worth keeping in view, because these are the
costs §3 now carries instead:

| Guarantee | Under freeform JS (chosen) | Under a document |
|---|---|---|
| Tree is finite and request-independent | static taint analysis over two sink classes, soundness a deliverable (§3.1, §3.1.3) | true by construction — data has no branches |
| Argument positions correctly typed | inferred subtypes + positional rules (§3.1) | schema types |
| Ordering (partition → rank → require → outcome) | staged builder types (§4.6) | required keys |
| Load terminates | wall-clock budget + `Runtime.Interrupt` (§3.3) | a document cannot diverge |
| Labels stay bounded | prove no runtime value reaches a label | enumerated values in the schema |

Every row in the right-hand column is free; every row in the left is code this
repo maintains. The specification as it stood follows.

### A.1 The design

The policy is **data**, not a program. An operator writes a document; at config
load it is validated against a schema and materialized into a finite tree of
Go-implemented primitives. At request time that tree is walked in Go.

```
config load / reload                          request path
────────────────────                          ────────────
schema validation ─▶ materialize ─▶ finite ─▶ walk in Go, per response,
(runs once)                         tree      against this round's votes
```

No host language, no interpreter, no marshalling, and no allocation on the
steady path.

### A.2 Why data rather than freeform JavaScript

`selectionPolicy` takes a JavaScript `evalFunction`, so the obvious shape for
this feature was freeform JS traced through a recorder at load. **That was the
design through several drafts and it is now a rejected alternative** — the full
version, and the reasoning that killed it, are in Appendix A.

The short form: a host language on the authoring surface has to be *policed*.
Because the tree must be finite and identical for every request, a JS policy
may contain no runtime-dependent structure — which requires a static analysis
over the eval function proving that no runtime value reaches a control-flow
test or a tree-shaping argument. That analysis is a small type system, it is
soundness-critical (a hole is both a miscompile and an unbounded-metric-label
vector), and successive review passes kept finding holes in it: the taint
roots, the argument-position rule, a `Requirement` subtype that made the
vocabulary untypeable, a circular `candidate` lift, a missing branch-callback
parameter kind, absent `always`/`first`/`not` constructors, an unstaged
builder. Every one of those existed **only** to police freeform JS.

A document has no control flow to analyse. The same guarantees become
structural:

| Guarantee | Under freeform JS | Under a document |
|---|---|---|
| Tree is finite and request-independent | static taint analysis over two sink classes | true by construction — data has no branches |
| Argument positions are correctly typed | inferred subtypes + positional rules | schema types |
| Ordering (partition → rank → require → outcome) | staged builder types | required keys and nesting |
| Load terminates | wall-clock budget + `Runtime.Interrupt` | a document cannot diverge |
| Labels stay bounded | prove no runtime value reaches a label | enumerated values in the schema |

**What is given up:** loops and helpers over config. A policy that wanted
`tiers.map(t => requireTag(t, 1))` writes the list. That is the whole cost, and
it is paid in verbosity by the config author rather than in a soundness-critical
analyser maintained forever by this repo.

**What is not given up:** expressiveness of the *decision*. Both forks target
the identical Go tree and the identical primitive vocabulary (§4); the fork was
only ever about how the tree is authored. Operators who want generated policies
generate the document upstream, where a bug is a config diff rather than a
miscompile.

#### A.2.1 The schema is typed — the vocabulary is not flat

Validation is schema validation, but the schema is not "a bag of keys". Each
slot in the document accepts one declared type, so a rank term cannot appear
where a guard belongs:

| Subtype | Members | Appears in |
|---|---|---|
| **`GroupPred`** | `atThreshold(n)`, `requireTag(t, n)`, `notTie()`/`isTie()`, `responseTypeIs(t)`, `extractableAt(paths)`, `valid`, `not(GroupPred)` | `eligible`, `require`, `groupCount`, `exists` |
| **`Guard`** | `always`, `absent(t)`, `whenParticipants(n)`, `participantsBelow(n)`, `whenMethod(p)`, `whenInFlight()`, `capFired()`, `callerMayReceive(g)`, `groupCount(...)`, `exists(...)`, `not(Guard)` | `when` |
| **`EarlyExitPred`** | every `Guard`, plus the candidate-scoped facts of §4.4.1, plus `not(EarlyExitPred)` | `earlyExit` only |
| **`RankTerm`** | `mostCommon()`, `nonEmptyFirst()`, `largest()`, `highestAt(paths)`, `fromLeader(opts)` | `rank` |
| **`PartitionKey`** | `valueAt(paths)` | `partitionBy` |
| **`Outcome`** | `accept`, `reject`, `rejectWith`, `wait` | branch tails, `first(...)` members |
| **`Builder`** | *(not a schema type — ordering is required keys, §4.6)* | — |

`not(...)` is a primitive rather than a host operator: the document has no
operators at all, and the negations the existing shortcuts need are real —
`whenInFlight() ⇒ P` is `any(not(whenInFlight()), P)`, without which Phase 5
could not translate them. It is domain-preserving like `all`/`any`: `not` of a
`Guard` is a `Guard`, `not` of a `GroupPred` is a `GroupPred`.

`always` is the constant-true `Guard` (used by §12); `first(...)` takes
`Outcome`s and returns an `Outcome` (§4.5's nesting). `all`/`any` are
**domain-uniform** — operands of one subtype, returning that subtype — which
makes `all(valid, extractableAt(paths))` a `GroupPred` and
`all(absent(t), callerMayReceive(g))` a `Guard` without the two ever mixing.

**There is no `Requirement` subtype, and there is no `candidate(...)` lift.**
`eligible: atThreshold(n)` and `require: atThreshold(n)` are the *same*
`GroupPred` in two document slots — §4.1's "one predicate, two positions" —
and giving them disjoint types would force `atThreshold` to have both. Scope
comes from the slot: `eligible` applies its predicate to every group,
`require` to the group that won. `exists(GroupPred) → Guard` is the sole lift
between domains, and it exists because a *round-level* guard sometimes needs to
ask a group-level question (§4.3).

A candidate-scoped predicate can never appear in a `when` guard, because
`require` and `earlyExit` are keys **inside** a branch that already declares
`rank`, while `when` is the branch's own selector. The document's nesting is
what makes that true; under the rejected fork it needed a staged type system to
say the same thing (Appendix A).

Ordering under this fork is required keys rather than a staged grammar — a
branch is `{when, eligible?, partitionBy?, rank, require?, outcome, earlyExit?}`,
with `rank` required before `require` can mean anything and `outcome` mandatory
so a branch cannot fail to terminate. Compare §4.6.

## Appendix B — Draft provenance

Several sections in the main body previously carried inline notes of the form
"an earlier draft did X, which was wrong because Y". Those are validation
provenance, not design, and they are collected here so the main body reads as a
specification rather than a changelog. They are retained rather than deleted
because each records a *reachable* modelling error that a reimplementation could
repeat.

| Area | The error | Why it was wrong |
|---|---|---|
| §4.1 eligible/require | treated as one check | ranking then requiring a threshold picks a different winner than filtering to threshold-meeting groups then ranking; rule 2 needs the second |
| §4.1.1 rule 2 | five semantics folded into `partitionBy` | would have rebuilt the bespoke rule the factoring exists to eliminate; the sixth (valid-group scope) was missing entirely |
| §4.3 | a specialized `hasExtractableValue()` guard | `groupCount`/`exists` generalize it; one predicate language, not a one-off |
| §4.4 | `earlyExit` as a field on `accept` | one of the three shortcuts terminates on a pass-through error, not an accept |
| §5 equality | group identity only, then byte-stability | too weak (representative can change), then too strong (collapses every policy to wait-all) |
| §5.2 fold | earlier-guard + firing-require only | mirror-image incompleteness; §12 needs earlier-*require* and firing-*guard* |
| §5.2 | stability classes for rank terms only | the fold also asks how guards and group predicates move; `fromLeader` had no class at all |
| §5.3 | one `remaining` | three distinct quantities; conflating physical with legal breaks capped `fireAndForget` |
| §5.5 | drain before publishing the outcome | converts a wait cap into wait-all |
| §5.6 | backstop scoped to cap fire | `wait()` after normal exhaustion is equally reachable |
| §7.1 | `minAgreement` translated to `require` | `require` falls through; the existing gate disputes |
| §9 | "no deployment changes behaviour" alongside a leader snapshot | the snapshot *was* a behaviour change |
| §3.1 | a data-only builder replacing freeform JS | Appendix A — considered and rejected on authoring ergonomics (§13.2) |


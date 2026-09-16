# Custom Consensus Policies Engine — Implementation Plan

**Status**: Implemented — Phases 1–5 shipped (MVP), v1.1 empty-waiver shipped as
a follow-on; Phase 6 (decision cache) deferred, Phase 7 (docs/E2E) shipped
**Last revised**: 2026-09-16
**Branch**: implementation on `feat/techops-29773-consensus-policy-selector-engine`
(`crcl-main/erpc` PR [#136](https://github.com/crcl-main/erpc/pull/136), HEAD
`70e16699`); spec on `feat/custom-consensus-policies-spec`
**Spec**: [feature.md](./feature.md) — single source of truth for behavior ·
[feature-details.md](./feature-details.md) — implementation edge cases
**Issue**: [#1088](https://github.com/erpc/erpc/issues/1088)

Companion to [feature.md](./feature.md). Phased so each step ships value
independently; the selector engine and MissingData waiver are the headline
correctness pieces. Phase checkboxes below reflect the **shipped** state.

---

## Locked decisions

| Decision | Choice |
|----------|--------|
| When JS runs | **Pre-round** only — selects which policy to run |
| What JS returns | Policy **name** only; no inline object in v1 |
| Auth / roles | Inside `customPolicy.evalFunction` via `ctx.user` — no separate claim→policy allowlist |
| Round grading | Stays in declarative `ConsensusPolicyConfig` / executor |
| Mid-round switch | **Never** — fail-visible under the selected policy |
| Historical safety net | Role-gated: `standard` (no waiver), `standard-waive-missing` (waiver), `historical` (external-only for known-old blocks) |
| Empty/null pruning | **Shipped** as v1.1 (`waiveAgreementOnEmptyOutsideRetention` + `retentionBlocks` + `blockEvidenceFields`); block proof by avail lower bound then chain-head depth |
| `blockAvailability` | Existing config + `EvmAssertBlockAvailability` — no new dynamic model |
| Fallback policy shape | Plain `{ maxParticipants, agreementThreshold }` — no tag quotas |
| Decision cache | **Not in v1** — add only if measured eval cost forces it |
| Sitout state | Moved to `health.Tracker` (per-class cordon flags + rate limiter) so selector reads it without importing `consensus/`; exporter stays per-policy |
| Header + metric | Reimplemented here (`X-eRPC-Consensus-Policy`, `consensus_policy` label); not dependent on #1041 |
| Default policy | Must be the strictest; fail-closed target |

---

## Phase 0 — Spec (this PR)

- [x] Write `specs/custom-consensus-policies/feature.md`
- [x] Write this plan
- [x] Add `feature-details.md` — implementation edge cases synced to the shipped code
- [ ] Land on `andreclaro/erpc` for review; link from #1088

**Acceptance**: Spec reviewed and accepted as the implementation contract.
**Status**: Spec now describes shipped behavior (`crcl-main/erpc` PR #136 @ `70e16699`).

---

## Phase 1 — Standalone selector engine

Package: `internal/consensus/policy/` — no imports from `consensus/` executor.

1. **Sobek pool** — compile once at startup (`sobek.Program`); pre-warm 8 VMs;
   borrow per eval; `evalTimeout` hard cap. Pool exhaustion fails closed to
   default policy + `consensus_policy_eval_bypassed_total{reason="pool_exhausted"}`.
2. **`EvalContext`** types — request, user, upstream refs (id, tags, health,
   blockAvailability), network.
3. **Stdlib v1** — `withTag`, `healthy`, `anyPunished`, `anyOperatorCordon`,
   `allUnavailable` (fail-closed availability whitelist; **false on an empty
   set**), `canServeBlock` (wraps `EvmAssertBlockAvailability`), `hasRole`.
4. **API** — `Compile(js) (*Policy, error)`, `Evaluate(ctx) (name string, err error)`.
   Empty / null → `""` (default). Unknown-name resolution is the caller's job.
5. **Micro-benchmark** — `go test -bench` in the selector package, results
   posted in the PR. Cases:
   - **Steady-state eval** of the §2 reference function on a pre-warmed VM,
     at 5 / 20 / 50 upstreams — the number compared against upstream RTT.
   - **Ctx binding** alone (building + binding `EvalContext` to the VM) —
     isolates per-request fixed cost from eval logic.
   - **Cold vs warm** — first eval on a fresh VM vs pooled VM, to size the
     pre-warm pool.
   - **Concurrent throughput** — parallel evals across the 8-VM pool;
     verifies pool-exhaustion fail-closed fires instead of queueing.
   - **Timeout path** — a looping eval must hit `evalTimeout`, fail closed,
     and the poisoned VM is discarded.
   Decision rule: p99 steady-state ≪ 1ms → decision cache stays
   unbuilt; materially worse → re-open Phase 6 before continuing.
6. **Unit tests first on fallthrough** — nil user, empty upstreams, empty
   eval, timeout, throw → error (caller fails closed). Then happy-path name
   returns and each stdlib helper.

**Acceptance**: `go test ./internal/consensus/policy/...` green; package has
zero imports from `consensus/` executor; benchmark result posted.
**Status — SHIPPED.** `policy.go` / `selector.go` / `pool.go` / `stdlib.js` with
`selector_test.go`, `stdlib_test.go`, `bench_test.go`. The runtime is
**sandboxed** (`newSandboxRuntime`: no `env`/`process.env`/`console`) and the
one-time expression evaluation at pool build is `evalTimeout`-bounded. Benchmark
confirmed µs-order eval → decision cache stayed unbuilt.

---

## Phase 2 — Config schema

1. Extend consensus config with `policies map[string]*ConsensusPolicyConfig`
   and `customPolicy { evalFunction, evalTimeout }`.
2. Add `waiveAgreementOnMissingData bool` on `ConsensusRequiredParticipant`
   (default false, opt-in).
3. Validation: named policies validate as today's
   `ConsensusPolicyConfig.Validate()`; `customPolicy.evalFunction` smoke-compiles;
   unknown cross-refs rejected at load where statically knowable.
4. Defaults: inline `consensus:` with no `policies` → anonymous default policy.
   `evalTimeout` default `50ms`.
5. Tygo regen for TypeScript config types.

**Acceptance**: existing configs load unchanged; a `policies` + `customPolicy`
fixture validates and compiles.
**Status — SHIPPED.** `ConsensusCustomPolicyConfig`, `ConsensusPolicyConfig.Policies`,
`BlockEvidenceFields`, and the waiver flags on `ConsensusRequiredParticipant` are
in `common/config.go`; `evalTimeout` default `50ms` in `common/defaults.go`.
Validation additionally rejects the reserved name `default`, nested
`customPolicy`/`policies` on a named policy, waiver configs without a
never-waivable floor, and empty-waiver configs missing `retentionBlocks` /
`blockEvidenceFields`.

---

## Phase 3 — MissingData waiver

1. **Waiver** in `enforceWinnerComposition` (`missingDataWaivedQuotas`): if
   `waiveAgreementOnMissingData` and **≥ `minAgreement`** distinct tag-matching
   participants returned `ErrEndpointMissingData` **and none returned data**,
   skip that quota (sibling transport errors do not block; a matching data vote
   holds it). Emit
   `erpc_consensus_composition_waived_total{project,network,tag,reason="missing_data"}`
   + log. The waiver is round-complete: it only evaluates once the round is
   sealed/terminated. Because composition disputes do **not** short-circuit
   while `hasRemaining`, the wait-cap must **seal** the collection
   (`sealCollection` + re-run `determineWinner`) so a still-unanswered required
   slot doesn't defer the waiver forever; `fireAndForget` does not seal.
2. **Load-time validation** — reject a policy unless at least one
   `requiredParticipants` entry is both never-waivable **and** carries
   `minAgreement > 0` (R9). The quota condition is not redundant: both
   `anyAgreementQuota` and `resultsSatisfyAgreementQuotas` skip entries with
   `minAgreement <= 0` (`consensus/quota.go`), so a never-waivable entry
   without a quota passes a flag-only check while enforcing nothing.
   Also reject two policies sharing an S3 `misbehaviorsDestination.path`
   unless each `filePattern` contains `{timestampMs}` (R10) — the exporter
   is per-policy, and without the placeholder two exporters resolve the same
   key and each S3 PUT overwrites the previous archive.
3. **Characterization tests** — one per edge-matrix row in feature.md §7.2.
   Existing consensus tests run unchanged against the default policy (zero
   regression).

**Acceptance**: UC3 (role-gated historical) passes: unauthorized on `standard`
dispute on MissingData; `standard-waive-missing` serves via waiver; `historical`
serves external-only for known-old blocks. Mixed MissingData group winning is
a composition dispute (the waiver does not fire when any matching participant
returned a value); a config whose only never-waivable entry has
`minAgreement: 0` is rejected at load time.
**Status — SHIPPED.** `missingDataWaivedQuotas`, `recordCompositionWaivers`, and
the seal path in `consensus/executor.go`; characterization tests in
`consensus/composition_test.go` and `consensus/wait_cap_test.go` (fires on ≥
`minAgreement` abstentions, holds on a sibling data vote, holds below
`minAgreement`, defers while `hasRemaining`, fires when sealed short of
`maxParticipants`, `fireAndForget` does not seal, opt-in only).

---

## Phase 4 — Auth + health plumbing

1. Expose JWT roles/claims on `ctx.user` (**new plumbing** — `common.User`
   gains `Roles`; JWT strategy populates from a configurable claim; other
   strategies leave it empty).
2. Add a required `cordonClass` parameter to the cordon API. This is wider
   than one function: `Cordon` / `Uncordon` on both interfaces in
   `common/upstream.go`, `Upstream.Cordon` / `Uncordon`
   (`upstream/upstream.go`), `Tracker.Cordon` / `Uncordon`
   (`health/tracker.go`), and both fakes in `common/upstream_fake.go`, plus
   the existing call sites and their tests. Classes: consensus executor →
   `punishment`; EVM state poller (chain identity) and SVM state poller
   (lag/unhealthy) → `availability`; admin API → `operator`.
3. Replace `TrackedMetrics.Cordoned atomic.Bool` with a per-class bitmask on
   the same `(upstream, method)` entry. `IsCordoned` is "any bit set";
   `Uncordon(class)` clears one bit; `CordonedAtMs` is stamped on the
   empty→non-empty transition and cleared on the return to empty, so
   cordon-duration accounting is unchanged. This is the correctness step: with
   one shared flag, a poller cordoning an already-punished upstream overwrites
   `punishment` with `availability`, and either source's `Uncordon` clears the
   other's cordon outright (§4.5).
4. Move consensus sitout state to `health.Tracker`, and move the misbehavior
   rate limiter there too (keyed by upstream ID) so reaching a sitout is
   global across policies. The misbehavior exporter stays per-policy.
5. Selector reads the class set — never the reason string. `health.state` is
   `"cordoned"` whenever the set is non-empty, so a failing health check on a
   punished upstream cannot report `"unhealthy"`. Reference eval checks
   `anyPunished()` and `anyOperatorCordon()` over the full upstream set first
   (either → `standard`), then `allUnavailable()` on internals for the
   fallback decision.
6. `blockNumber` extraction for eval ctx (numeric request params).

**Acceptance**: punished internals never select `fallback`; a punished
**external** with internals down also keeps the network on `standard`; an
**admin-cordoned** upstream (any method scope relevant to the request) keeps
the network on `standard` via `anyOperatorCordon()`; unauthorized callers
never get `fallback` / `generous-dev`; availability-cordoned (chain-mismatch,
SVM lag) internals allow `fallback` for authorized roles. Order-independence
is tested directly: punish an upstream, then availability-cordon it, and
`allUnavailable()` is still false; uncordon the availability class and the
punishment cordon survives. `allUnavailable()` on an internal tag matching
zero upstreams returns false and the round stays on `standard`.
**Status — SHIPPED.** `common.User.Roles` + JWT `rolesClaimName` (default
`roles`, `auth/strategy_jwt.go`); typed `cordonClass` on the `Cordon`/`Uncordon`
API with per-class bitmask state in `health/tracker.go`; the selector's
`anyPunished`/`anyOperatorCordon` scan a `PunishedOrOperator` list built from the
**network** registry, so nodes selection already dropped (`removeCordoned`) still
pin the request (`consensus/selector.go` `buildEvalContext`).
**Trial caveat:** role-gating was exercised **ungated** on staging (health /
block-range / cordon predicates), not live role-gated grades — code-complete,
not yet battle-tested end-to-end.

---

## Phase 5 — Executor wiring

1. **Pre-round resolution** in the consensus path: build `EvalContext` from
   auth + upstream registry + request; evaluate; resolve name → config; run
   existing executor under that config.
2. **Fail closed** — eval error / timeout / unknown name / pool exhaustion →
   default policy + log.
3. **Header + metric** — `X-eRPC-Consensus-Policy`, `consensus_policy` label,
   `consensus_policy_eval_duration_seconds`,
   `consensus_policy_eval_failed_total{reason}`.
4. **Load benchmark** — follow the `erpc/failsafe_load_test.go` pattern
   (`BenchmarkLoad_Consensus_3of5_256w`): HTTP-layer concurrent load,
   `runLatencyLoadTest`-style harness reporting `req/s`, p50/p95/p99/p999,
   max, err%, heap delta. Run the same 3-of-5 consensus fixture twice —
   selector disabled (baseline) vs `customPolicy` enabled with the §2
   reference eval — same concurrency and wall-clock. The p99 delta between
   the two runs is the true per-request cost of the selector under realistic
   pressure; post both runs in the PR.

**Acceptance**: UC1 (standard mixed-node), UC2 (role-gated fallback), UC3
(role-gated historical: `standard` / `standard-waive-missing` / `historical`)
pass as config-level fixtures; no mid-round switch behavior exists; the load
benchmark shows the selector's p99 delta over the no-selector baseline within
noise (≪ 1ms) at 256-way concurrency.
**Status — SHIPPED.** Pre-round resolution in `consensus/selector.go`
(`resolvePolicy` → `buildEvalContext` → `Evaluate` → resolve name → `c.policy`
fail-closed). Header `X-ERPC-Consensus-Policy` is **output-only**, written in
`erpc/http_server.go` from the `ExecState` snapshot and emitted only when a
selector is configured. Metrics: `erpc_consensus_policy_selected_total`,
`_eval_failed_total{timeout|throw|unknown_name}`,
`_eval_bypassed_total{pool_exhausted}`, `_eval_duration_seconds`.

---

## Phase 6 — Decision cache (optional, future)

Only if the Phase 1 benchmark shows eval cost matters. Design is in
feature.md §5; implementation is a follow-on PR.

1. Tracking-ctx first eval per (network, method) to derive key schema.
2. Health-tracker generation counter invalidation.
3. `blockNumber` bucketing by `blockAvailability` boundaries + cardinality
   guard.
4. Uncacheable detection (time/random).
5. Metrics: hits / misses / bypassed{reason}.
6. Fallback to operator-declared `cacheKeys` only if getter tracking proves
   impractical (STOP and report — do not invent a third key model).

**Acceptance**: role-only evals hit cache across requests; health state
transitions invalidate; flapping upstream tradeoff documented in metric
bypass/miss rates.
**Status — DEFERRED / NOT BUILT.** The Phase 1 benchmark showed µs-order eval,
so the cache was not forced. Still future work.

---

## Phase 7 — Docs + E2E

1. Docs page under `docs/pages/config/` for `consensus.policies` /
   `customPolicy` / waiver fields (agent-first: Config schema table, Edge
   cases, Observability — match `failsafe/hedge.mdx` exemplar).
2. E2E fixtures for UC1–UC3 + matrix rows.
3. Update #1088 with implementation status.

**Acceptance**: docs build; E2E green; issue updated.
**Status — SHIPPED.** Operator docs at
`docs/pages/config/failsafe/consensus-policies.mdx` (agent-first: config schema,
edge cases, observability, source-code entry points) plus updates to
`consensus.mdx` / `hedge.mdx`; E2E in `consensus/composition_waiver_e2e_test.go`
and `erpc/networks_hedge_cancel_test.go`.

---

## Phase 8 — v1.1 empty-outside-retention waiver (follow-on, SHIPPED)

Shipped after the MVP once dispute traces showed the null-shaped prune case
(`eth_getTransactionByHash` on pruned history). Not a pre-planned MVP phase — the
original plan listed it as out of scope.

1. **Config** — `waiveAgreementOnEmptyOutsideRetention` + `retentionBlocks` on
   `ConsensusRequiredParticipant`, `blockEvidenceFields` on the policy
   (`common/config.go`); validation requires `retentionBlocks > 0` +
   non-empty `blockEvidenceFields` when opted in.
2. **Waiver** — `emptyOutsideRetentionWaivedQuotas` mirrors the MissingData
   waiver (≥ `minAgreement` empties, no matching non-empty, round-complete) plus
   a **block proof**: prefer each empty upstream's finite `blockAvailability`
   lower bound (`winnerBlock < lower`), else chain-head depth
   (`head − winnerBlock > retentionBlocks`). `head` is the majority network
   served tip only under `evm.servedTip.enabledFor: latest`, else the min of ≥ 2
   corroborated participant poller latests; default max-mode served tip is not
   trusted.
3. **`promoteAbstentionWinner`** — rescues the synthesized default mixed-node tie
   (lone non-empty archive group vs equally-sized empty internal group) that has
   no backing group and so never reaches the waiver path, under the same guards.
4. **Hedge consensus-slot empty-keep** — `runHedge` keeps an emptyish result as
   the slot's vote when `consensusSlot` is set (`erpc/network_executor.go`);
   without it the empty is replaced by another node or `n/a` and the waiver never
   sees the internal empty. Regressed in `6ac0f798`, restored in `70e16699`.
5. **PreferNonEmpty tie fix** — gate the `accept-most-common + prefer-non-empty`
   rule on counts (any empty/error ≥ threshold + any non-empty, only while
   empty/error is at least tied with the leading non-empty), not on
   `getBestByCount()`, to kill a Go map-order flake without overriding
   PreferLarger.
6. **Observability** — `empty_outside_retention` reason on
   `erpc_consensus_composition_waived_total`; new
   `erpc_consensus_composition_waiver_unproven_total{reason}` for fail-closed
   holds (`no_winner_block` / `no_avail_bound` / `no_head`); mid-round deferral
   is **debug-only**.

**Acceptance — MET.** `consensus/composition_waiver_e2e_test.go`,
`consensus/composition_test.go` (`TestEmptyOutsideRetention*`),
`consensus/wait_cap_test.go`, and served-tip invariants under `erpc/`. Full
mechanics in [feature-details.md](./feature-details.md) §2–§5.

---

## Out of scope for v1 (explicit)

- ~~`waiveAgreementOnEmptyOutsideRetention` + `blockEvidenceFields`~~ → **SHIPPED
  as v1.1** once dispute traces showed the null shape (Phase 8 above).
- Decision cache (Phase 6 remains a future optimization; the benchmark did not
  force it).
- Inline policy object return from eval (name only in v1).
- Mid-round policy switching / post-round JS grading.
- #1069 bounded-deviation value logic.
- Separate claim→policy allowlist outside JS.

---

## Risks / watch-items

### Resolved in the trial (lessons)

- **mp=4 + one dead internal never sealed.** Composition disputes don't
  short-circuit while `hasRemaining`, so a round with an unanswered required slot
  deferred its waiver forever and shipped a dispute. Fix: the wait-cap now
  **seals** the collection (`sealCollection` + re-run `determineWinner`) so
  waivers evaluate with cancelled slots; `fireAndForget` deliberately does not
  seal (`TestWaitCap_FireAndForgetDoesNotSealWaivers`). Details §3.
- **Hedge replaced the internal empty vote.** For lookup methods the hedge
  rejects an emptyish leg to keep racing, which on a consensus slot replaced the
  internal `null` with another node or `n/a` — so the empty-waiver never saw the
  internal empty. Fix: keep the empty as the slot vote when `consensusSlot` is
  set. Regressed in `6ac0f798`, restored in `70e16699`. Watch this on any future
  hedge/consensus refactor.
- **PreferNonEmpty map-order flake.** Gating the `accept-most-common +
  prefer-non-empty` rule on `getBestByCount()` flaked on tied empty/non-empty
  groups because Go map iteration order picked the "best" nondeterministically.
  Fix: gate on counts (fire only while empty/error ≥ threshold is at least tied
  with the leading non-empty) so it neither flakes nor overrides PreferLarger.
- **Waiver threshold was unanimous in the spec.** Shipped as **≥ `minAgreement`**
  distinct tag-matching abstentions with "matching data holds" — the symmetric,
  correct floor. A single value vote (or non-empty) still holds the quota.

### Still live

- **Punished vs unhealthy indistinguishability** — held: cordons carry a typed
  class, the tracker holds one flag per class, and `anyPunished`/`anyOperatorCordon`
  scan the **network** set (including nodes selection dropped) so a punished node
  can't be masked into a downgrade. Keep the class whitelist fail-closed for any
  future class.
- **Role-gating not battle-tested** — the trial ran selectors **ungated**; the
  `hasRole` path is code-complete but hasn't served live role-gated grades. Prove
  it before relying on JWT roles as a correctness control.
- **Empty-waiver proof trust** — the head fallback trusts the network tip only
  under majority served-tip mode; default max-mode is not trusted (single
  inflator). Two colluders reporting the same inflated latest can still raise the
  min-of-two participant fallback — residual, documented in code.
- **Decision-cache key explosion** — if Phase 6 is ever built: freeform JS over
  `blockNumber` without bucketing; cardinality guard must disable caching, not
  OOM. Sobek getter tracking: if impractical, STOP and fall back to declared
  `cacheKeys`.
- **Scope creep into post-round grading** — any temptation to expose
  `valueGroups` / `agreeing` to JS is a different product; reject and point
  at #1069 / executor config.

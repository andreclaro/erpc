# Custom Consensus Policies Engine — Implementation Plan

**Status**: Draft — for review
**Last revised**: 2026-09-10
**Branch**: `feat/custom-consensus-policies-spec` (spec PR); implementation on a
follow-on branch
**Spec**: [feature.md](./feature.md) — single source of truth for behavior
**Issue**: [#1088](https://github.com/erpc/erpc/issues/1088)

Companion to [feature.md](./feature.md). Phased so each step ships value
independently; the selector engine and MissingData waiver are the headline
correctness pieces.

---

## Locked decisions

| Decision | Choice |
|----------|--------|
| When JS runs | **Pre-round** only — selects which policy to run |
| What JS returns | Policy **name** only; no inline object in v1 |
| Auth / roles | Inside `customPolicy.evalFunction` via `ctx.user` — no separate claim→policy allowlist |
| Round grading | Stays in declarative `ConsensusPolicyConfig` / executor |
| Mid-round switch | **Never** — fail-visible under the selected policy |
| Historical safety net | `waiveAgreementOnMissingData` on `requiredParticipants[]` |
| Empty/null pruning | Spec for v1.1 (`waiveAgreementOnEmptyOutsideRetention`); **not** in v1 milestones |
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
- [ ] Land on `andreclaro/erpc` for review; link from #1088

**Acceptance**: Spec reviewed and accepted as the implementation contract.

---

## Phase 1 — Standalone selector engine

Package: `internal/consensus/policy/` — no imports from `consensus/` executor.

1. **Sobek pool** — compile once at startup (`sobek.Program`); pre-warm 8 VMs;
   borrow per eval; `evalTimeout` hard cap. Pool exhaustion fails closed to
   default policy + `consensus_policy_eval_bypassed_total{reason="pool_exhausted"}`.
2. **`EvalContext`** types — request, user, upstream refs (id, tags, health,
   blockAvailability), network.
3. **Stdlib v1** — `withTag`, `healthy`, `anyPunished`, `allUnavailable`
   (fail-closed fallback predicate over typed cordon classes; **false on an
   empty set**), `canServeBlock` (wraps `EvmAssertBlockAvailability`),
   `hasRole`.
4. **API** — `Compile(js) (*Policy, error)`, `Evaluate(ctx) (name string, err error)`.
   Empty / null → `""` (default). Unknown-name resolution is the caller's job.
5. **Benchmark** — measure eval latency on a pre-warmed VM; report result in
   the PR (gates the future decision-cache phase).
6. **Unit tests first on fallthrough** — nil user, empty upstreams, empty
   eval, timeout, throw → error (caller fails closed). Then happy-path name
   returns and each stdlib helper.

**Acceptance**: `go test ./internal/consensus/policy/...` green; package has
zero imports from `consensus/` executor; benchmark result posted.

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

---

## Phase 3 — MissingData waiver

1. **Waiver** in `enforceWinnerComposition`: if
   `waiveAgreementOnMissingData` and every tag-matching participant returned
   `ErrEndpointMissingData`, skip that quota. Emit
   `consensus_composition_waived_total{tag,reason="missing_data"}` + log.
   The waiver is round-complete: it only evaluates after all participants
   have responded or the round has otherwise terminated. The wait-cap arming
   gate at `executor.go:490` is unchanged — it still holds arming until
   every quota tag is covered by distinct upstreams.
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
3. **Characterization tests** — one per edge-matrix row in feature.md §7.2
   (v1 rows only; null-shape stays dispute). Existing consensus tests run
   unchanged against the default policy (zero regression).

**Acceptance**: UC3 (historical via waiver) passes; mixed MissingData group
winning is a composition dispute (the waiver does not fire when any matching
participant returned a value); a config whose only never-waivable entry has
`minAgreement: 0` is rejected at load time.

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
   punished upstream cannot report `"unhealthy"`. Reference fallback predicate
   is `allUnavailable()`.
6. `blockNumber` extraction for eval ctx (numeric request params).

**Acceptance**: punished internals never select `fallback`; unauthorized
callers never get `fallback` / `generous-dev`; operator-cordoned internals
block `fallback`; availability-cordoned (chain-mismatch, SVM lag) internals
allow `fallback` for authorized roles. Order-independence is tested directly:
punish an upstream, then availability-cordon it, and `allUnavailable()` is
still false; uncordon the availability class and the punishment cordon
survives. `allUnavailable()` on an internal tag matching zero upstreams
returns false and the round stays on `standard`.

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

**Acceptance**: UC1 (standard mixed-node), UC2 (role-gated fallback), UC3
(historical via waiver) pass as config-level fixtures; no mid-round switch
behavior exists.

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

---

## Phase 7 — Docs + E2E

1. Docs page under `docs/pages/config/` for `consensus.policies` /
   `customPolicy` / waiver fields (agent-first: Config schema table, Edge
   cases, Observability — match `failsafe/hedge.mdx` exemplar).
2. E2E fixtures for UC1–UC3 + matrix rows.
3. Update #1088 with implementation status.

**Acceptance**: docs build; E2E green; issue updated.

---

## Out of scope for v1 (explicit)

- `waiveAgreementOnEmptyOutsideRetention` + `blockEvidenceFields` (v1.1 —
  build when dispute traces show the null shape).
- Decision cache (Phase 6 is a future optimization, gated on measured eval
  cost).
- Inline policy object return from eval (name only in v1).
- Mid-round policy switching / post-round JS grading.
- #1069 bounded-deviation value logic.
- Separate claim→policy allowlist outside JS.

---

## Risks / watch-items

- **Punished vs unhealthy indistinguishability** — if health refs collapse
  sitout into "unhealthy", fallback becomes an attacker-forced downgrade. R7
  is load-bearing; block Phase 5 on a distinct signal.
- **Decision-cache key explosion** — freeform JS over `blockNumber` without
  bucketing. Cardinality guard must disable caching, not OOM.
- **Sobek getter tracking** — if impractical, STOP and fall back to declared
  `cacheKeys`; do not ship a half-broken auto-key.
- **Waiver over-breadth** — waive only when *all* matching participants
  returned MissingData; a single value vote holds the quota (security).
- **Scope creep into post-round grading** — any temptation to expose
  `valueGroups` / `agreeing` to JS is a different product; reject and point
  at #1069 / executor config.
